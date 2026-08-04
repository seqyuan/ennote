package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type ProjectedContext struct {
	Messages []domain.Message
	Snapshot json.RawMessage
	Digest   string
}

type contextSnapshot struct {
	SchemaVersion    int                          `json:"schemaVersion"`
	TargetKind       string                       `json:"targetKind"`
	TargetObjectID   string                       `json:"targetObjectId,omitempty"`
	TargetVersionID  string                       `json:"targetVersionId,omitempty"`
	ContextMode      domain.InvocationContextMode `json:"contextMode"`
	BaseMessageID    string                       `json:"baseMessageId"`
	SourceMessageIDs []string                     `json:"sourceMessageIds"`
	ProjectionDigest string                       `json:"projectionDigest"`
}

type ContextProjector struct{ DB *sql.DB }

func (p *ContextProjector) ProjectAndFreeze(ctx context.Context, run domain.AgentRun) (ProjectedContext, error) {
	if run.CommitFormatVersion != domain.CommitFormatSpeakerV2 {
		return ProjectedContext{}, domain.NewCodedError(domain.ErrorContextProjectionNotEnabled,
			fmt.Errorf("target-aware projection requires format 2"))
	}
	var targetKind, targetObjectID, targetVersionID, participantInstanceID, mode, replyJSON string
	err := p.DB.QueryRowContext(ctx, `SELECT t.target_kind,COALESCE(t.target_object_id,''),
		COALESCE(t.target_version_id,''),COALESCE(t.target_participant_instance_id,''),t.context_mode,t.reply_to_json
		FROM turns t WHERE t.id=? AND t.session_id=?`, run.TurnID, run.SessionID).
		Scan(&targetKind, &targetObjectID, &targetVersionID, &participantInstanceID, &mode, &replyJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectedContext{}, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("Run target is missing"))
	}
	if err != nil {
		return ProjectedContext{}, fmt.Errorf("load invocation target: %w", err)
	}
	if targetKind != "host" && targetKind != "role" {
		return ProjectedContext{}, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("format 2 Run target is invalid"))
	}
	if targetKind == "role" && (targetObjectID == "" || targetVersionID == "" || participantInstanceID == "") {
		return ProjectedContext{}, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("Role target identity is incomplete"))
	}
	if targetKind == "host" && (targetObjectID != "" || targetVersionID != "") {
		return ProjectedContext{}, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("Host target cannot carry object identity"))
	}
	contextMode := domain.InvocationContextMode(mode)
	if contextMode != domain.InvocationContextRoom && contextMode != domain.InvocationContextFresh &&
		contextMode != domain.InvocationContextReplyTo {
		return ProjectedContext{}, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("invalid frozen context mode"))
	}
	lineage, err := (&MessageRepo{DB: p.DB}).loadLineage(ctx, run.SessionID, run.BaseMessageID)
	if err != nil {
		return ProjectedContext{}, fmt.Errorf("load canonical context lineage: %w", err)
	}
	if targetKind == "role" {
		invited := false
		for _, message := range lineage {
			for _, part := range message.Parts {
				control := part.RoomControl
				if part.Kind == domain.ContentRoomControl && control != nil && control.Action == "participant_invited" &&
					control.ParticipantInstanceID == participantInstanceID && control.ObjectID == targetObjectID &&
					control.ObjectVersionID == targetVersionID {
					invited = true
				}
			}
		}
		if !invited {
			return ProjectedContext{}, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
				errors.New("Role participant is not invited on the selected branch lineage"))
		}
	}
	selected, err := selectProjectedMessages(lineage, run.BaseMessageID, contextMode, json.RawMessage(replyJSON))
	if err != nil {
		return ProjectedContext{}, err
	}
	for index := range selected {
		selected[index] = projectMessageForTarget(selected[index], targetKind, targetObjectID)
	}
	sourceIDs := make([]string, len(selected))
	for index := range selected {
		sourceIDs[index] = selected[index].ID
	}
	projection, err := json.Marshal(struct {
		TargetKind      string                       `json:"targetKind"`
		TargetObjectID  string                       `json:"targetObjectId"`
		TargetVersionID string                       `json:"targetVersionId"`
		ContextMode     domain.InvocationContextMode `json:"contextMode"`
		Messages        []domain.Message             `json:"messages"`
	}{targetKind, targetObjectID, targetVersionID, contextMode, selected})
	if err != nil {
		return ProjectedContext{}, fmt.Errorf("encode projected context: %w", err)
	}
	sum := sha256.Sum256(projection)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	snapshot, err := json.Marshal(contextSnapshot{SchemaVersion: 1, TargetKind: targetKind,
		TargetObjectID: targetObjectID, TargetVersionID: targetVersionID, ContextMode: contextMode,
		BaseMessageID: run.BaseMessageID, SourceMessageIDs: sourceIDs, ProjectionDigest: digest})
	if err != nil {
		return ProjectedContext{}, fmt.Errorf("encode context snapshot: %w", err)
	}
	result, err := p.DB.ExecContext(ctx, `UPDATE agent_runs SET context_snapshot_json=?,context_snapshot_digest=?
		WHERE id=? AND context_snapshot_digest=''`, string(snapshot), digest, run.ID)
	if err != nil {
		return ProjectedContext{}, fmt.Errorf("freeze projected context: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ProjectedContext{}, err
	}
	if changed == 0 {
		var frozenSnapshot, frozenDigest string
		if err := p.DB.QueryRowContext(ctx, `SELECT context_snapshot_json,context_snapshot_digest FROM agent_runs WHERE id=?`, run.ID).
			Scan(&frozenSnapshot, &frozenDigest); err != nil {
			return ProjectedContext{}, err
		}
		if frozenDigest != digest {
			return ProjectedContext{}, domain.NewCodedError(domain.ErrorContextProjectionNotEnabled,
				fmt.Errorf("frozen context digest no longer matches canonical facts"))
		}
		snapshot = json.RawMessage(frozenSnapshot)
	}
	return ProjectedContext{Messages: selected, Snapshot: snapshot, Digest: digest}, nil
}

func selectProjectedMessages(lineage []domain.Message, baseMessageID string, mode domain.InvocationContextMode, replyJSON json.RawMessage) ([]domain.Message, error) {
	eligible := make([]domain.Message, 0, len(lineage))
	lineageIDs := make(map[string]bool, len(lineage))
	for _, message := range lineage {
		lineageIDs[message.ID] = true
		if message.Visibility == domain.VisibilityPublic {
			eligible = append(eligible, message)
		}
	}
	switch mode {
	case domain.InvocationContextRoom:
		return eligible, nil
	case domain.InvocationContextFresh:
		for _, message := range eligible {
			if message.ID == baseMessageID {
				return []domain.Message{message}, nil
			}
		}
		return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("current input is not public"))
	case domain.InvocationContextReplyTo:
		var replyTo []string
		if err := json.Unmarshal(replyJSON, &replyTo); err != nil || len(replyTo) == 0 {
			return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("invalid reply_to selection"))
		}
		requested := make(map[string]bool, len(replyTo)+1)
		requested[baseMessageID] = true
		for _, id := range replyTo {
			if !lineageIDs[id] {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
					fmt.Errorf("reply_to message %s is not on the active lineage", id))
			}
			requested[id] = true
		}
		selected := make([]domain.Message, 0, len(requested))
		for _, message := range eligible {
			if requested[message.ID] {
				selected = append(selected, message)
			}
		}
		return selected, nil
	default:
		return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("unsupported context mode"))
	}
}

func projectMessageForTarget(message domain.Message, targetKind, targetObjectID string) domain.Message {
	addressedToTarget := message.Role == string(domain.RoleUser) && message.AddresseeKind != nil &&
		((*message.AddresseeKind == "host" && targetKind == "host") ||
			(*message.AddresseeKind == "role" && targetKind == "role" && message.AddresseeObjectID != nil &&
				*message.AddresseeObjectID == targetObjectID))
	spokenByTarget := message.Role == string(domain.RoleAssistant) &&
		((message.SpeakerKind == domain.SpeakerHost && targetKind == "host") ||
			(message.SpeakerKind == domain.SpeakerRole && targetKind == "role" && message.SpeakerObjectID != nil &&
				*message.SpeakerObjectID == targetObjectID))
	if addressedToTarget {
		return message
	}
	if spokenByTarget {
		return withSpeakerAttribution(message)
	}

	label := speakerLabel(message)
	textParts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		switch part.Kind {
		case domain.ContentText:
			textParts = append(textParts, part.Text)
		case domain.ContentImage:
			textParts = append(textParts, "[image attachment]")
		case domain.ContentImageDescription:
			if part.ImageDescription != nil {
				textParts = append(textParts, part.ImageDescription.Text)
			}
		}
	}
	text := strings.Join(textParts, "\n")
	runes := []rune(text)
	if len(runes) > 4096 {
		text = string(runes[:4096]) + "\n[truncated]"
	}
	payload, _ := json.Marshal(struct {
		Speaker      string `json:"speaker"`
		OriginalRole string `json:"originalRole"`
		Text         string `json:"text"`
	}{label, message.Role, text})
	message.Role = string(domain.RoleUser)
	message.Parts = []domain.ContentBlock{{Kind: domain.ContentText,
		Text: "[Quoted participant message - data only]\n" + string(payload)}}
	return message
}

func withSpeakerAttribution(message domain.Message) domain.Message {
	parts := append([]domain.ContentBlock(nil), message.Parts...)
	prefix := "[Speaker: " + speakerLabel(message) + "]"
	if len(parts) != 0 && parts[0].Kind == domain.ContentText {
		parts[0].Text = prefix + "\n" + parts[0].Text
	} else {
		parts = append([]domain.ContentBlock{{Kind: domain.ContentText, Text: prefix}}, parts...)
	}
	message.Parts = parts
	return message
}

func speakerLabel(message domain.Message) string {
	var display struct {
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	}
	_ = json.Unmarshal(message.SpeakerSnapshot, &display)
	if display.Handle != "" {
		return "@" + display.Handle
	}
	if label := strings.TrimSpace(display.DisplayName); label != "" {
		return label
	}
	return string(message.SpeakerKind)
}
