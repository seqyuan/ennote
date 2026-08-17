package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
)

var (
	ErrSessionNotFound  = errors.New("session not found")
	ErrSessionRunActive = errors.New("session already has an active run")
	ErrRunNotFound      = errors.New("agent run not found")
	ErrInvalidRunState  = errors.New("invalid agent run state transition")
)

type EventPublisher interface {
	Publish(...domain.RunEvent)
}

type RunRepo struct {
	DB          *sql.DB
	Publisher   EventPublisher
	Providers   *ProviderRepo
	Models      *ModelRepo
	Policies    *fileconfig.PolicyStore
	RoleSources *globalsource.Store
}

func (r *RunRepo) SubmitTurn(ctx context.Context, input domain.SubmitTurnInput) (*domain.TurnSubmission, error) {
	return r.submitTurn(ctx, input, nil)
}

func (r *RunRepo) SubmitInvocation(ctx context.Context, input domain.SubmitInvocationInput) (*domain.TurnSubmission, error) {
	if input.Target.Kind != domain.InvocationTargetRole || strings.TrimSpace(input.Target.ObjectID) == "" ||
		strings.TrimSpace(input.Target.VersionID) == "" {
		return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("a Role object and version are required"))
	}
	if strings.TrimSpace(string(input.RequestedConfig)) != "" && string(input.RequestedConfig) != "null" && string(input.RequestedConfig) != "{}" {
		return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
			fmt.Errorf("direct Role invocation does not allow runtime model, permission, or loop config overrides"))
	}
	if input.Target.ContextMode != domain.InvocationContextRoom && input.Target.ContextMode != domain.InvocationContextFresh &&
		input.Target.ContextMode != domain.InvocationContextReplyTo {
		return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("unsupported context mode %q", input.Target.ContextMode))
	}
	if input.Target.ContextMode == domain.InvocationContextReplyTo && len(input.Target.ReplyTo) == 0 {
		return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("reply_to context requires message ids"))
	}
	return r.submitTurn(ctx, domain.SubmitTurnInput{
		SessionID: input.SessionID, ClientRequestID: input.ClientRequestID, BaseMessageID: input.BaseMessageID,
		Text: input.Text, Parts: input.Parts, RequestedConfig: input.RequestedConfig,
	}, &input.Target)
}

func (r *RunRepo) submitTurn(ctx context.Context, input domain.SubmitTurnInput, target *domain.RoleInvocationTarget) (*domain.TurnSubmission, error) {
	if strings.TrimSpace(input.ClientRequestID) == "" {
		return nil, fmt.Errorf("client request id is required")
	}
	if strings.TrimSpace(input.Text) == "" && len(input.Parts) == 0 {
		return nil, fmt.Errorf("turn content is required")
	}
	requestedConfig := input.RequestedConfig
	if len(requestedConfig) == 0 {
		requestedConfig = json.RawMessage(`{}`)
	}
	if !json.Valid(requestedConfig) {
		return nil, fmt.Errorf("requested config is not valid JSON")
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin submit turn: %w", err)
	}
	defer tx.Rollback()

	if existing, err := findSubmissionTx(ctx, tx, input.SessionID, input.ClientRequestID); err != nil {
		return nil, err
	} else if existing != nil {
		existing.Existing = true
		return existing, nil
	}

	var activeLeaf, activeBranch sql.NullString
	var sessionProjectID string
	if err := tx.QueryRowContext(ctx,
		`SELECT active_leaf_message_id,active_branch_id,project_id FROM sessions WHERE id = ? AND status = 'active'`, input.SessionID,
	).Scan(&activeLeaf, &activeBranch, &sessionProjectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("load session: %w", err)
	}

	var activeRunKind sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT run_kind FROM agent_runs WHERE session_id = ? AND status IN ('queued', 'running', 'waiting_for_approval', 'waiting_delegation_admission', 'waiting_children') AND parent_run_id IS NULL LIMIT 1`,
		input.SessionID,
	).Scan(&activeRunKind); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check active run: %w", err)
	}
	if activeRunKind.Valid {
		if activeRunKind.String == string(domain.RunKindContextCompaction) {
			return nil, ErrSessionCompacting
		}
		return nil, ErrSessionRunActive
	}
	activeBranchID, err := ensureActiveBranchTx(ctx, tx, input.SessionID, activeLeaf, activeBranch)
	if err != nil {
		return nil, err
	}

	commitFormat := domain.CommitFormatLegacyV1
	targetKind, targetObjectID, targetVersionID := "host", "", ""
	contextMode := domain.InvocationContextRoom
	replyTo := json.RawMessage(`[]`)
	speakerSnapshot := json.RawMessage(`{"kind":"host","displayName":"Host"}`)
	participantInstanceID := ""
	createParticipant := false
	inviteParticipant := false
	if target != nil {
		var handle, name, icon, color, positioning, configDigest string
		var definition domain.RoleDefinition
		if r.RoleSources != nil {
			resolved, err := r.resolveFileRole(ctx, target.ObjectID, target.VersionID)
			if err != nil {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, err)
			}
			handle, name = resolved.Document.Handle, resolved.Document.Name
			icon, color, positioning = resolved.Document.Icon, resolved.Document.Color, resolved.Document.Positioning
			configDigest, definition = resolved.Revision.Digest, resolved.Definition
		} else {
			var writerSetting string
			if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='hosted_commit_format_version'`).Scan(&writerSetting); err != nil || writerSetting != "2" {
				return nil, domain.NewCodedError(domain.ErrorCommitFormatNotEnabled, fmt.Errorf("format 2 writer is not enabled"))
			}
			var definitionJSON string
			err := tx.QueryRowContext(ctx, `SELECT p.handle,p.name,p.icon,p.color,p.positioning,v.config_digest,v.definition_json
				FROM agent_profiles p JOIN agent_profile_versions v ON v.id=p.current_version_id
				WHERE p.id=? AND v.id=? AND p.object_kind='role' AND p.status='active' AND v.status='published'
				AND (p.project_id IS NULL OR p.project_id=?)`, target.ObjectID, target.VersionID, sessionProjectID).
				Scan(&handle, &name, &icon, &color, &positioning, &configDigest, &definitionJSON)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("Role target is unavailable or not published"))
			}
			if err != nil {
				return nil, fmt.Errorf("resolve Role target: %w", err)
			}
			if err := json.Unmarshal([]byte(definitionJSON), &definition); err != nil {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("decode Role target: %w", err))
			}
		}
		contextAllowed := false
		for _, allowed := range definition.ContextPolicy.AllowedModes {
			contextAllowed = contextAllowed || string(allowed) == string(target.ContextMode) ||
				(allowed == domain.RoleContextReply && target.ContextMode == domain.InvocationContextReplyTo)
		}
		if !contextAllowed {
			return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
				fmt.Errorf("Role does not allow %s context", target.ContextMode))
		}
		speakerSnapshot, err = json.Marshal(map[string]string{
			"kind": "role", "objectId": target.ObjectID, "versionId": target.VersionID, "handle": handle,
			"displayName": name, "icon": icon, "color": color, "positioning": positioning, "configDigest": configDigest,
		})
		if err != nil {
			return nil, fmt.Errorf("encode Role speaker snapshot: %w", err)
		}
		if len(target.ReplyTo) != 0 {
			replyTo, err = json.Marshal(target.ReplyTo)
			if err != nil {
				return nil, fmt.Errorf("encode reply targets: %w", err)
			}
		}
		commitFormat = domain.CommitFormatSpeakerV2
		targetKind, targetObjectID, targetVersionID = "role", target.ObjectID, target.VersionID
		contextMode = target.ContextMode
		err = tx.QueryRowContext(ctx, `SELECT id FROM room_member_instances
			WHERE session_id=? AND role_id=? AND role_version_id=?`, input.SessionID, target.ObjectID, target.VersionID).
			Scan(&participantInstanceID)
		if errors.Is(err, sql.ErrNoRows) {
			participantInstanceID, createParticipant, inviteParticipant = uuid.NewString(), true, true
		} else if err != nil {
			return nil, fmt.Errorf("resolve Role participant: %w", err)
		}
	}

	baseMessageID := input.BaseMessageID
	if baseMessageID == "" && activeLeaf.Valid {
		baseMessageID = activeLeaf.String
	}
	if input.BaseMessageID != "" && (!activeLeaf.Valid || input.BaseMessageID != activeLeaf.String) {
		return nil, ErrBranchPointNotActive
	}
	if baseMessageID != "" {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM messages WHERE id = ? AND session_id = ?`,
			baseMessageID, input.SessionID,
		).Scan(&exists); err != nil {
			return nil, fmt.Errorf("validate base message: %w", err)
		}
		if exists != 1 {
			return nil, fmt.Errorf("base message does not belong to session: %s", baseMessageID)
		}
	}
	if target == nil && baseMessageID != "" {
		// Host-only sessions stay format 1 so the Conversation Surface tool
		// timeline survives reload. Once the active lineage already contains a
		// format-2 (Role) contribution, the room runs on the speaker-ledger
		// model and this Host turn must project other Speakers safely.
		var formatTwoAncestors int
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE chain(id,parent_message_id,run_id) AS (
			SELECT id,parent_message_id,run_id FROM messages WHERE id=? AND session_id=?
			UNION ALL SELECT m.id,m.parent_message_id,m.run_id FROM messages m JOIN chain c
			  ON m.id=c.parent_message_id WHERE m.session_id=?
		) SELECT COUNT(*) FROM chain WHERE run_id IN (SELECT id FROM agent_runs WHERE commit_format_version=2)`,
			baseMessageID, input.SessionID, input.SessionID).Scan(&formatTwoAncestors); err != nil {
			return nil, fmt.Errorf("inspect lineage format: %w", err)
		}
		if formatTwoAncestors != 0 {
			commitFormat = domain.CommitFormatSpeakerV2
		}
	}
	if target != nil && !inviteParticipant {
		var invitedOnLineage int
		if baseMessageID != "" {
			err := tx.QueryRowContext(ctx, `WITH RECURSIVE chain(id,parent_message_id) AS (
				SELECT id,parent_message_id FROM messages WHERE id=? AND session_id=?
				UNION ALL SELECT m.id,m.parent_message_id FROM messages m JOIN chain c ON m.id=c.parent_message_id
				WHERE m.session_id=?
			) SELECT COUNT(*) FROM chain c JOIN message_parts p ON p.message_id=c.id
			WHERE p.block_kind='room_control'
			AND json_extract(p.payload_json,'$.action')='participant_invited'
			AND json_extract(p.payload_json,'$.participantInstanceId')=?`, baseMessageID, input.SessionID,
				input.SessionID, participantInstanceID).Scan(&invitedOnLineage)
			if err != nil {
				return nil, fmt.Errorf("fold Role participant invitation: %w", err)
			}
		}
		inviteParticipant = invitedOnLineage == 0
	}

	timestamp := time.Now().UTC()
	messageID := uuid.NewString()
	turnID := uuid.NewString()
	runID := uuid.NewString()
	parts, imageArtifactIDs, err := prepareUserPartsTx(ctx, tx, input.SessionID, input.Text, input.Parts)
	if err != nil {
		return nil, err
	}

	userParentMessageID := baseMessageID
	if createParticipant {
		if _, err := tx.ExecContext(ctx, `INSERT INTO room_member_instances
			(id,session_id,role_id,role_version_id,created_at) VALUES(?,?,?,?,?)`, participantInstanceID,
			input.SessionID, targetObjectID, targetVersionID, timestamp.Format(time.RFC3339Nano)); err != nil {
			return nil, fmt.Errorf("create Role participant: %w", err)
		}
	}
	if inviteParticipant {
		inviteMessageID := uuid.NewString()
		inviteSeq, err := nextMessageSeq(ctx, tx, input.SessionID)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages
			(id,session_id,parent_message_id,role,status,speaker_kind,speaker_snapshot_json,
			 addressee_kind,visibility,originated_at,created_at,seq)
			 VALUES(?,?,?,'system','complete','system','{"kind":"system","displayName":"System"}',
			 'room','room_control',?,?,?)`, inviteMessageID, input.SessionID, nullableStr(baseMessageID),
			timestamp.Format(time.RFC3339Nano), timestamp.Format(time.RFC3339Nano), inviteSeq); err != nil {
			return nil, fmt.Errorf("insert participant invite: %w", err)
		}
		control := domain.ContentBlock{Kind: domain.ContentRoomControl, RoomControl: &domain.RoomControl{
			Action: "participant_invited", ParticipantInstanceID: participantInstanceID, ObjectID: targetObjectID,
			ObjectVersionID: targetVersionID, DisplaySnapshot: speakerSnapshot,
		}}
		if err := insertMessageParts(ctx, tx, inviteMessageID, []domain.ContentBlock{control}); err != nil {
			return nil, fmt.Errorf("insert participant invite content: %w", err)
		}
		userParentMessageID = inviteMessageID
	}
	messageSeq, err := nextMessageSeq(ctx, tx, input.SessionID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, parent_message_id, role, status,
		 speaker_kind, speaker_snapshot_json, addressee_kind, addressee_object_id, addressee_version_id,
		 visibility, originated_at, created_at, seq)
		 VALUES (?, ?, ?, 'user', 'complete', 'user', '{"kind":"user","displayName":"You"}',
		 ?, ?, ?, 'public', ?, ?, ?)`,
		messageID, input.SessionID, nullableStr(userParentMessageID), targetKind, nullableStr(targetObjectID), nullableStr(targetVersionID),
		timestamp.Format(time.RFC3339Nano), timestamp.Format(time.RFC3339Nano), messageSeq,
	); err != nil {
		return nil, fmt.Errorf("insert user message: %w", err)
	}
	if err := insertMessageParts(ctx, tx, messageID, parts); err != nil {
		return nil, fmt.Errorf("insert user message parts: %w", err)
	}
	for _, artifactID := range imageArtifactIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET message_id=? WHERE id=? AND message_id IS NULL`, messageID, artifactID); err != nil {
			return nil, fmt.Errorf("link image artifact: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO turns
		 (id, session_id, client_request_id, user_message_id, base_message_id, status,
		 input_message_id, input_kind, target_kind, target_object_id, target_version_id,
		 target_participant_instance_id, context_mode, reply_to_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, 'user_message', ?, ?, ?, ?, ?, ?, ?, ?)`,
		turnID, input.SessionID, input.ClientRequestID, messageID, nullableStr(baseMessageID), messageID,
		targetKind, nullableStr(targetObjectID), nullableStr(targetVersionID), nullableStr(participantInstanceID), contextMode,
		string(replyTo), timestamp.Format(time.RFC3339Nano), timestamp.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("insert turn: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_runs
		 (id, turn_id, session_id, run_kind, base_message_id, attempt, status, requested_config_json,
		 effective_config_json, speaker_snapshot_json, root_run_id, execution_depth, publish_mode,
		 commit_format_version, context_snapshot_json, created_at)
		 VALUES (?, ?, ?, 'agent', ?, 1, 'queued', ?, '{}', ?, ?, 0, 'public_final', ?, '{}', ?)`,
		runID, turnID, input.SessionID, messageID, string(requestedConfig), string(speakerSnapshot), runID,
		commitFormat, timestamp.Format(time.RFC3339Nano),
	); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrSessionRunActive
		}
		return nil, fmt.Errorf("insert agent run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session_branches SET leaf_message_id=?,updated_at=?
		WHERE id=? AND session_id=?`, messageID, timestamp.Format(time.RFC3339Nano), activeBranchID, input.SessionID); err != nil {
		return nil, fmt.Errorf("update branch leaf: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET active_leaf_message_id = ?, updated_at = ? WHERE id = ? AND active_branch_id=?`,
		messageID, timestamp.Format(time.RFC3339Nano), input.SessionID, activeBranchID,
	); err != nil {
		return nil, fmt.Errorf("update session leaf: %w", err)
	}
	queuedPayload, _ := json.Marshal(map[string]string{"turnId": turnID, "userMessageId": messageID, "targetKind": targetKind})
	committedEvents, err := appendEventsTx(ctx, tx, runID, domain.PendingEvent{EventType: "run_queued", Payload: queuedPayload})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit submit turn: %w", err)
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committedEvents...)
	}

	return &domain.TurnSubmission{
		TurnID: turnID, UserMessageID: messageID,
		Run: domain.AgentRun{
			ID: runID, TurnID: turnID, SessionID: input.SessionID, RunKind: domain.RunKindAgent, BaseMessageID: messageID, Attempt: 1,
			Status: domain.RunQueued, CommitFormatVersion: commitFormat, RootRunID: runID,
			ExecutionDepth: 0, PublishMode: domain.PublishPublicFinal,
			SpeakerSnapshot: speakerSnapshot, ContextSnapshot: json.RawMessage(`{}`),
			RequestedConfig: requestedConfig, EffectiveConfig: json.RawMessage(`{}`), CreatedAt: timestamp,
		},
	}, nil
}

func prepareUserPartsTx(ctx context.Context, tx *sql.Tx, sessionID, text string, requested []domain.ContentBlock) ([]domain.ContentBlock, []string, error) {
	parts := make([]domain.ContentBlock, 0, len(requested)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, domain.ContentBlock{Kind: domain.ContentText, Text: text})
	}
	parts = append(parts, requested...)
	artifactIDs := make([]string, 0)
	for index := range parts {
		switch parts[index].Kind {
		case domain.ContentText:
			if strings.TrimSpace(parts[index].Text) == "" {
				return nil, nil, fmt.Errorf("text content part cannot be empty")
			}
		case domain.ContentImage:
			if parts[index].Image == nil || strings.TrimSpace(parts[index].Image.ArtifactID) == "" {
				return nil, nil, fmt.Errorf("image content part requires artifactId")
			}
			var mime, sha, metadata string
			err := tx.QueryRowContext(ctx, `SELECT a.mime_type,a.sha256,a.metadata_json FROM artifacts a
				JOIN sessions s ON s.id=? WHERE a.id=? AND a.kind='image_attachment'
				AND a.project_id=s.project_id AND (a.session_id IS NULL OR a.session_id=s.id)`,
				sessionID, parts[index].Image.ArtifactID).Scan(&mime, &sha, &metadata)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil, domain.NewCodedError(domain.ErrorImageInvalid, fmt.Errorf("image artifact does not belong to session"))
			}
			if err != nil {
				return nil, nil, err
			}
			var dimensions struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			}
			_ = json.Unmarshal([]byte(metadata), &dimensions)
			parts[index].Image = &domain.ImageRef{ArtifactID: parts[index].Image.ArtifactID,
				MIMEType: mime, SHA256: sha, Width: dimensions.Width, Height: dimensions.Height}
			artifactIDs = append(artifactIDs, parts[index].Image.ArtifactID)
		default:
			return nil, nil, fmt.Errorf("unsupported user content kind: %s", parts[index].Kind)
		}
	}
	return parts, artifactIDs, nil
}

// ParentOfRun returns the parent_run_id of a Run, or "" for a top-level Run.
func (r *RunRepo) ParentOfRun(ctx context.Context, runID string) (string, error) {
	var parent sql.NullString
	if err := r.DB.QueryRowContext(ctx, `SELECT parent_run_id FROM agent_runs WHERE id=?`, runID).Scan(&parent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrRunNotFound
		}
		return "", err
	}
	return parent.String, nil
}

// OwnedChildIDs returns non-terminal direct children before a parent cancel
// transition changes their durable status. Coordinator uses this snapshot to
// cancel the corresponding runtime contexts as part of the same user action.
func (r *RunRepo) OwnedChildIDs(ctx context.Context, parentRunID string) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id FROM agent_runs WHERE parent_run_id=?
		AND status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children')
		ORDER BY created_at,id`, parentRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *RunRepo) Get(ctx context.Context, runID string) (*domain.AgentRun, error) {
	row := r.DB.QueryRowContext(ctx, runSelect+` WHERE id = ?`, runID)
	run, err := scanAgentRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *RunRepo) IsWaitingChildren(ctx context.Context, runID string) bool {
	var status string
	err := r.DB.QueryRowContext(ctx, `SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status)
	if err != nil {
		return false
	}
	return status == string(domain.RunWaitingChildren)
}

func (r *RunRepo) FindActiveBySession(ctx context.Context, sessionID string) (*domain.AgentRun, error) {
	run, err := scanAgentRun(r.DB.QueryRowContext(ctx, runSelect+
		` WHERE session_id=? AND status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children') AND parent_run_id IS NULL ORDER BY created_at DESC LIMIT 1`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *RunRepo) Claim(ctx context.Context, runID string) (*domain.AgentRun, error) {
	if err := r.transition(ctx, runID, domain.RunRunning, "run_started", nil, nil); err != nil {
		return nil, err
	}
	return r.Get(ctx, runID)
}

func (r *RunRepo) Succeed(ctx context.Context, runID string) error {
	return r.transition(ctx, runID, domain.RunSucceeded, "run_succeeded", nil, nil)
}

func (r *RunRepo) FinalizeSuccess(ctx context.Context, runID string, output domain.RunOutput) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin successful run finalization: %w", err)
	}
	defer tx.Rollback()

	var current domain.RunStatus
	var commitFormat domain.CommitFormatVersion
	var turnID, sessionID, parentMessageID, targetKind, speakerSnapshot string
	var targetObjectID, targetVersionID, participantInstanceID sql.NullString
	var activeLeaf, activeBranch sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ar.status,ar.commit_format_version,ar.turn_id,ar.session_id,t.user_message_id,
		t.target_kind,t.target_object_id,t.target_version_id,t.target_participant_instance_id,ar.speaker_snapshot_json,
		s.active_leaf_message_id,s.active_branch_id FROM agent_runs ar
		JOIN turns t ON t.id=ar.turn_id JOIN sessions s ON s.id=ar.session_id WHERE ar.id=?`, runID).
		Scan(&current, &commitFormat, &turnID, &sessionID, &parentMessageID, &targetKind, &targetObjectID,
			&targetVersionID, &participantInstanceID, &speakerSnapshot, &activeLeaf, &activeBranch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("load run for finalization: %w", err)
	}
	if current == domain.RunSucceeded {
		return nil
	}
	if commitFormat != domain.CommitFormatLegacyV1 && commitFormat != domain.CommitFormatSpeakerV2 {
		return domain.NewCodedError(domain.ErrorCommitFormatNotEnabled,
			fmt.Errorf("run %s uses unsupported commit format %d", runID, commitFormat))
	}
	if commitFormat == domain.CommitFormatSpeakerV2 && r.RoleSources == nil {
		var writerSetting string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='hosted_commit_format_version'`).Scan(&writerSetting); err != nil || writerSetting != "2" {
			return domain.NewCodedError(domain.ErrorCommitFormatNotEnabled, fmt.Errorf("format 2 writer is not enabled"))
		}
	}
	if !domain.CanTransitionRun(current, domain.RunSucceeded) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidRunState, current, domain.RunSucceeded)
	}
	if err := requireNoOwnedChildrenTx(ctx, tx, runID); err != nil {
		return fmt.Errorf("parent cannot finalize with owned children: %w", err)
	}
	if !activeLeaf.Valid || activeLeaf.String != parentMessageID || !activeBranch.Valid {
		return fmt.Errorf("%w: active branch moved before run finalization", ErrBranchPointNotActive)
	}
	activeBranchID := activeBranch.String

	timestamp := time.Now().UTC()
	var activeCalls int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM model_calls WHERE run_id = ? AND status = 'started') +
		(SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND status = 'started')`, runID, runID).Scan(&activeCalls); err != nil {
		return fmt.Errorf("check active calls before success: %w", err)
	}
	if activeCalls != 0 {
		return fmt.Errorf("successful run still has %d active calls", activeCalls)
	}
	shadowMessages, transcriptDigest, err := AppendRunMessagesTx(ctx, tx, runID, commitFormat, output.Messages, timestamp)
	if err != nil {
		return err
	}
	messageIDs := make([]string, 0, len(output.Messages))
	assistantMessageID := ""
	finalAssistantIndex := -1
	for index := range output.Messages {
		if output.Messages[index].Role == domain.RoleAssistant {
			finalAssistantIndex = index
		}
	}
	if finalAssistantIndex < 0 {
		return fmt.Errorf("successful run has no complete assistant message")
	}
	if commitFormat == domain.CommitFormatSpeakerV2 {
		message := output.Messages[finalAssistantIndex]
		messageID := uuid.NewString()
		seq, err := nextMessageSeq(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		speakerKind := targetKind
		if speakerKind != string(domain.SpeakerRole) {
			speakerKind = string(domain.SpeakerHost)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages
			(id,session_id,parent_message_id,role,status,run_id,speaker_kind,speaker_object_id,
			 speaker_version_id,participant_instance_id,speaker_snapshot_json,addressee_kind,
			 visibility,originated_at,created_at,seq)
			VALUES(?,?,?,'assistant','complete',?,?,?,?,?,?,'room','public',?,?,?)`,
			messageID, sessionID, parentMessageID, runID, speakerKind, nullableNullString(targetObjectID),
			nullableNullString(targetVersionID), nullableNullString(participantInstanceID), speakerSnapshot,
			timestamp.Format(time.RFC3339Nano), timestamp.Format(time.RFC3339Nano), seq); err != nil {
			return fmt.Errorf("insert speaker-ledger message: %w", err)
		}
		if err := insertMessageParts(ctx, tx, messageID, message.Content); err != nil {
			return err
		}
		if err := linkToolResultArtifactsTx(ctx, tx, messageID, sessionID, runID, message.Content); err != nil {
			return err
		}
		parentMessageID, assistantMessageID = messageID, messageID
		messageIDs = append(messageIDs, messageID)
	} else {
		for index, message := range output.Messages {
			if message.Role != domain.RoleUser && message.Role != domain.RoleAssistant && message.Role != domain.RoleTool {
				return fmt.Errorf("unsupported projected message role: %s", message.Role)
			}
			messageID := uuid.NewString()
			visibility := domain.VisibilityLegacyExecution
			if index == finalAssistantIndex {
				visibility = domain.VisibilityPublic
			}
			seq, err := nextMessageSeq(ctx, tx, sessionID)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO messages
				(id, session_id, parent_message_id, role, status, run_id, speaker_kind,
				 speaker_snapshot_json, visibility, originated_at, created_at, seq)
				VALUES (?, ?, ?, ?, 'complete', ?, 'host', '{"kind":"host","displayName":"Host"}', ?, ?, ?, ?)`,
				messageID, sessionID, parentMessageID, message.Role, runID, visibility,
				timestamp.Format(time.RFC3339Nano), timestamp.Format(time.RFC3339Nano), seq); err != nil {
				return fmt.Errorf("insert projected message: %w", err)
			}
			if err := insertMessageParts(ctx, tx, messageID, message.Content); err != nil {
				return err
			}
			if err := linkToolResultArtifactsTx(ctx, tx, messageID, sessionID, runID, message.Content); err != nil {
				return err
			}
			parentMessageID = messageID
			messageIDs = append(messageIDs, messageID)
			if message.Role == domain.RoleAssistant {
				assistantMessageID = messageID
			}
		}
	}

	finishedAt := timestamp.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = 'succeeded',
		assistant_message_id = ?, finished_at = ?, error_code = NULL, error_message = NULL WHERE id = ?`,
		assistantMessageID, finishedAt, runID); err != nil {
		return fmt.Errorf("complete run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET status = 'succeeded', updated_at = ? WHERE id = ?`,
		finishedAt, turnID); err != nil {
		return fmt.Errorf("complete turn: %w", err)
	}
	branchResult, err := tx.ExecContext(ctx, `UPDATE session_branches SET leaf_message_id=?,updated_at=?
		WHERE id=? AND session_id=? AND leaf_message_id=?`, parentMessageID, finishedAt,
		activeBranchID, sessionID, activeLeaf.String)
	if err != nil {
		return fmt.Errorf("advance branch leaf: %w", err)
	}
	if changed, _ := branchResult.RowsAffected(); changed != 1 {
		return fmt.Errorf("%w: active branch leaf changed", ErrBranchPointNotActive)
	}
	sessionResult, err := tx.ExecContext(ctx, `UPDATE sessions SET active_leaf_message_id=?,updated_at=?
		WHERE id=? AND active_branch_id=? AND active_leaf_message_id=?`, parentMessageID, finishedAt,
		sessionID, activeBranchID, activeLeaf.String)
	if err != nil {
		return fmt.Errorf("activate assistant message: %w", err)
	}
	if changed, _ := sessionResult.RowsAffected(); changed != 1 {
		return fmt.Errorf("%w: active session branch changed", ErrBranchPointNotActive)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_input_queue SET status = 'cancelled', cancelled_at = ?
		WHERE run_id = ? AND status = 'queued'`, finishedAt, runID); err != nil {
		return fmt.Errorf("cancel pending run inputs: %w", err)
	}
	messagePayload, _ := json.Marshal(map[string]any{
		"assistantMessageId": assistantMessageID, "messageIds": messageIDs,
	})
	transcriptPayload, _ := json.Marshal(map[string]any{
		"count": len(shadowMessages), "digest": transcriptDigest,
		"format": commitFormat, "shadow": commitFormat == domain.CommitFormatLegacyV1,
	})
	runPayload, _ := json.Marshal(map[string]any{"status": domain.RunSucceeded})
	telemetry, telemetryErr := r.buildRunTelemetryTx(ctx, tx, runID, finishedAt)
	if telemetryErr != nil {
		telemetry = domain.RunTelemetryPayload{Partial: true}
	}
	telemetryPayload, _ := json.Marshal(telemetry)
	committedEvents, err := appendEventsTx(ctx, tx, runID,
		domain.PendingEvent{EventType: "message_committed", Payload: messagePayload},
		domain.PendingEvent{EventType: "run_transcript_committed", Payload: transcriptPayload},
		domain.PendingEvent{EventType: "run_telemetry", Payload: telemetryPayload},
		domain.PendingEvent{EventType: "run_succeeded", Payload: runPayload},
	)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit successful run finalization: %w", err)
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committedEvents...)
	}
	return nil
}

func linkToolResultArtifactsTx(ctx context.Context, tx *sql.Tx, messageID, sessionID, runID string,
	blocks []domain.ContentBlock) error {
	linked := make(map[string]struct{})
	for _, block := range blocks {
		if block.Kind != domain.ContentToolResult || block.ToolResult == nil {
			continue
		}
		for _, reference := range block.ToolResult.Artifacts {
			if _, duplicate := linked[reference.ArtifactID]; duplicate {
				return fmt.Errorf("artifact %s is referenced more than once in projected message", reference.ArtifactID)
			}
			linked[reference.ArtifactID] = struct{}{}
			var stored domain.ArtifactReference
			var artifactRunID, sourceToolCallID, metadataJSON string
			var currentMessageID sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT a.id,a.name,a.kind,a.mime_type,a.size_bytes,a.sha256,
				COALESCE(a.run_id,''),a.source_tool_call_id,a.metadata_json,a.message_id
				FROM artifacts a JOIN sessions s ON s.id=?
				WHERE a.id=? AND a.session_id=s.id AND a.project_id=s.project_id`, sessionID, reference.ArtifactID).Scan(
				&stored.ArtifactID, &stored.Name, &stored.Kind, &stored.MIMEType, &stored.SizeBytes, &stored.SHA256,
				&artifactRunID, &sourceToolCallID, &metadataJSON, &currentMessageID)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("artifact %s does not belong to projected session", reference.ArtifactID)
			}
			if err != nil {
				return fmt.Errorf("load projected artifact %s: %w", reference.ArtifactID, err)
			}
			var dimensions struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			}
			_ = json.Unmarshal([]byte(metadataJSON), &dimensions)
			stored.Width, stored.Height = dimensions.Width, dimensions.Height
			if artifactRunID != runID || sourceToolCallID != block.ToolResult.ToolCallID || stored != reference {
				return fmt.Errorf("artifact %s provenance or metadata does not match projected reference", reference.ArtifactID)
			}
			if currentMessageID.Valid && currentMessageID.String != messageID {
				return fmt.Errorf("artifact %s is already linked to another message", reference.ArtifactID)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET message_id=? WHERE id=? AND (message_id IS NULL OR message_id=?)`,
				messageID, reference.ArtifactID, messageID); err != nil {
				return fmt.Errorf("link artifact %s: %w", reference.ArtifactID, err)
			}
		}
	}
	return nil
}

// requireNoOwnedChildrenTx fails when a Run still owns non-terminal children.
// Parents cannot terminalize while children are queued/running/waiting.
func requireNoOwnedChildrenTx(ctx context.Context, tx *sql.Tx, runID string) error {
	var children int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?
		AND status IN ('queued','running','waiting_for_approval')`, runID).Scan(&children); err != nil {
		return err
	}
	if children != 0 {
		return fmt.Errorf("parent Run owns %d non-terminal children", children)
	}
	return nil
}

func (r *RunRepo) Fail(ctx context.Context, runID, code, message string) error {
	return r.transition(ctx, runID, domain.RunFailed, "run_failed", &code, &message)
}

func (r *RunRepo) Cancel(ctx context.Context, runID string) error {
	// Structured concurrency: propagate cancel to children, items, and the
	// group before the parent itself terminalizes. Child budget reservations
	// are reconciled in the same transaction; over-cancellation is safe.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM agent_runs WHERE parent_run_id=?
		AND status IN ('queued','running','waiting_for_approval')`, runID)
	if err != nil {
		return err
	}
	childIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		childIDs = append(childIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, childID := range childIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='cancelled', finished_at=?
			WHERE id=? AND status IN ('queued','running','waiting_for_approval')`, now, childID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_item_attempts SET status='cancelled',finished_at=?,error_code='run_cancelled'
			WHERE child_run_id=? AND status IN ('queued','running')`, now, childID); err != nil {
			return err
		}
		if err := reconcileRootBudgetTx(ctx, tx, childID); err != nil {
			return err
		}
	}
	// Settle the cancelled Run's own delegation attempt when it is itself a
	// delegated child (direct pre-dispatch child cancellation). Without this, a
	// queued attempt remains and the sibling's settlement never settles the
	// generation, so no logical completion is created. Non-delegated runs have
	// no attempt row and this is a no-op.
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_item_attempts SET status='cancelled',finished_at=?,error_code='run_cancelled'
		WHERE child_run_id=? AND status IN ('queued','running')`, now, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status='cancelled'
		WHERE child_run_id=? AND status IN ('pending','running')`, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status='cancelled'
		WHERE child_run_id IN (SELECT id FROM agent_runs WHERE parent_run_id=? AND status='cancelled')`, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET status='cancelled'
		WHERE parent_run_id=? AND status IN ('pending','waiting_children')`, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_group_generations SET status='cancelled',completed_at=?
		WHERE group_id IN (SELECT id FROM delegation_groups WHERE parent_run_id=? AND status='cancelled')
		  AND status IN ('queued','running','awaiting_authorization')`, now, runID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return r.transition(ctx, runID, domain.RunCancelled, "run_cancelled", nil, nil)
}

func (r *RunRepo) Interrupt(ctx context.Context, runID, message string) error {
	code := "worker_restarted"
	return r.transition(ctx, runID, domain.RunInterrupted, "run_interrupted", &code, &message)
}

func (r *RunRepo) RecoverActive(ctx context.Context) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id,status FROM agent_runs WHERE status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children') ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list recoverable runs: %w", err)
	}
	var queued, running []string
	for rows.Next() {
		var id string
		var status domain.RunStatus
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return nil, err
		}
		if status == domain.RunQueued {
			queued = append(queued, id)
		} else {
			running = append(running, id)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range running {
		if err := r.Interrupt(ctx, id, "worker restarted during run execution"); err != nil {
			return nil, err
		}
	}
	return queued, nil
}

// buildRunTelemetryTx aggregates run statistics within the terminal-transition
// transaction. Best-effort: an error returns a partial payload rather than
// failing the terminal event.
func (r *RunRepo) buildRunTelemetryTx(ctx context.Context, tx *sql.Tx, runID, timestamp string) (domain.RunTelemetryPayload, error) {
	telemetry := domain.RunTelemetryPayload{
		ToolTimings: make(map[string]domain.ToolTiming),
	}

	// Iterations = max completed iteration in tool_calls / model_calls.
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(iteration), 0) FROM model_calls WHERE run_id = ?`, runID).Scan(&telemetry.Iterations)

	// Model call + usage aggregation.
	if err := tx.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COALESCE(SUM(uncached_input_tokens + cache_read_tokens + cache_write_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0)
		FROM model_calls WHERE run_id = ?`, runID).Scan(
		&telemetry.ModelCalls, &telemetry.InputTokens, &telemetry.OutputTokens, &telemetry.CachedTokens); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return telemetry, err
	}

	// Per-tool aggregation: count, error count, total duration.
	rows, err := tx.QueryContext(ctx, `SELECT tool_name,
		COUNT(*),
		COALESCE(SUM(CASE WHEN is_error THEN 1 ELSE 0 END), 0),
		COALESCE(SUM((julianday(finished_at) - julianday(started_at)) * 86400000), 0)
		FROM tool_calls WHERE run_id = ? GROUP BY tool_name`, runID)
	if err != nil {
		return telemetry, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var timing domain.ToolTiming
		if err := rows.Scan(&name, &timing.Count, &timing.ErrorCount, &timing.TotalMS); err != nil {
			return telemetry, err
		}
		telemetry.ToolTimings[name] = timing
	}
	if err := rows.Err(); err != nil {
		return telemetry, err
	}

	// Max context utilization: parse the frozen effective config's context
	// window and use peak billed input / window. Best-effort; 0 when unknown.
	var windowText string
	if err := tx.QueryRowContext(ctx, `SELECT effective_config_json FROM agent_runs WHERE id = ?`, runID).Scan(&windowText); err == nil && windowText != "" {
		var effective domain.EffectiveRunConfig
		if jsonErr := json.Unmarshal([]byte(windowText), &effective); jsonErr == nil {
			window := effective.ContextTokens
			if window > 0 {
				var peak int
				_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(uncached_input_tokens + cache_read_tokens + cache_write_tokens), 0) FROM model_calls WHERE run_id = ?`, runID).Scan(&peak)
				telemetry.MaxContextUtilization = float64(peak) / float64(window)
			}
		}
	}

	return telemetry, nil
}

func (r *RunRepo) transition(ctx context.Context, runID string, target domain.RunStatus, eventType string, errorCode, errorMessage *string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var current domain.RunStatus
	var turnID sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT status, turn_id FROM agent_runs WHERE id = ?`, runID,
	).Scan(&current, &turnID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("load run state: %w", err)
	}
	if current == target {
		return nil
	}
	if !domain.CanTransitionRun(current, target) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidRunState, current, target)
	}
	if target == domain.RunSucceeded {
		if err := requireNoOwnedChildrenTx(ctx, tx, runID); err != nil {
			return fmt.Errorf("parent cannot succeed with owned children: %w", err)
		}
	}

	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	var startedAt, finishedAt any
	if target == domain.RunRunning {
		startedAt = timestamp
	}
	if target.Terminal() {
		finishedAt = timestamp
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_runs
		 SET status = ?, started_at = COALESCE(?, started_at), finished_at = COALESCE(?, finished_at),
		     error_code = ?, error_message = ?
		 WHERE id = ?`,
		target, startedAt, finishedAt, errorCode, errorMessage, runID,
	); err != nil {
		return fmt.Errorf("update run state: %w", err)
	}
	if turnID.Valid {
		turnStatus := turnStatusForRun(target)
		if _, err := tx.ExecContext(ctx,
			`UPDATE turns SET status = ?, updated_at = ? WHERE id = ?`, turnStatus, timestamp, turnID.String,
		); err != nil {
			return fmt.Errorf("update turn state: %w", err)
		}
	}
	var callEvents []domain.PendingEvent
	if target.Terminal() {
		if target == domain.RunSucceeded {
			var activeCalls int
			if err := tx.QueryRowContext(ctx, `SELECT
				(SELECT COUNT(*) FROM model_calls WHERE run_id = ? AND status = 'started') +
				(SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND status = 'started')`, runID, runID).Scan(&activeCalls); err != nil {
				return fmt.Errorf("check active calls before success: %w", err)
			}
			if activeCalls != 0 {
				return fmt.Errorf("successful run still has %d active calls", activeCalls)
			}
		} else {
			callEvents, err = closeStartedCallsTx(ctx, tx, runID, target, errorCode, timestamp)
			if err != nil {
				return err
			}
			compactionEvents, closeErr := closeActiveCompactionsTx(ctx, tx, runID, target, errorCode, errorMessage, timestamp)
			if closeErr != nil {
				return closeErr
			}
			callEvents = append(callEvents, compactionEvents...)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE run_input_queue SET status = 'cancelled', cancelled_at = ?
			 WHERE run_id = ? AND status = 'queued'`, timestamp, runID,
		); err != nil {
			return fmt.Errorf("cancel pending run inputs: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_approval_requests SET status='cancelled',resolved_at=?
			WHERE run_id=? AND status='pending'`, timestamp, runID); err != nil {
			return fmt.Errorf("cancel pending approvals: %w", err)
		}
		checkpointStatus := domain.CheckpointInterrupted
		if target == domain.RunCancelled {
			checkpointStatus = domain.CheckpointCancelled
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_execution_checkpoints SET status=?,finished_at=?
			WHERE run_id=? AND status IN ('pending','executing')`, checkpointStatus, timestamp, runID); err != nil {
			return fmt.Errorf("close execution checkpoints: %w", err)
		}
	}
	payload := map[string]any{"status": target}
	if errorCode != nil {
		payload["errorCode"] = *errorCode
	}
	if errorMessage != nil {
		payload["errorMessage"] = *errorMessage
	}
	encoded, _ := json.Marshal(payload)

	// Emit run_telemetry BEFORE the terminal event so SSE consumers always
	// receive the structured summary before the stream closes (§4.5).
	var pendingEvents []domain.PendingEvent
	if target.Terminal() {
		telemetry, telemetryErr := r.buildRunTelemetryTx(ctx, tx, runID, timestamp)
		if telemetryErr != nil {
			// Telemetry is best-effort: never block the terminal transition.
			telemetry = domain.RunTelemetryPayload{Partial: true}
		}
		telemetryPayload, _ := json.Marshal(telemetry)
		pendingEvents = append(callEvents,
			domain.PendingEvent{EventType: "run_telemetry", Payload: telemetryPayload},
			domain.PendingEvent{EventType: eventType, Payload: encoded})
	} else {
		pendingEvents = append(callEvents, domain.PendingEvent{EventType: eventType, Payload: encoded})
	}
	committedEvents, err := appendEventsTx(ctx, tx, runID, pendingEvents...)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run transition: %w", err)
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committedEvents...)
	}
	return nil
}

func closeActiveCompactionsTx(ctx context.Context, tx *sql.Tx, runID string, target domain.RunStatus,
	errorCode, errorMessage *string, timestamp string) ([]domain.PendingEvent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM context_compactions WHERE run_id=? AND status IN ('planned','running')`, runID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	status := domain.CompactionFailed
	eventType := "context_compaction_failed"
	code := string(domain.ErrorCompactionProviderFailed)
	if errorCode != nil {
		code = *errorCode
	}
	if target == domain.RunCancelled || target == domain.RunInterrupted {
		status = domain.CompactionCancelled
		eventType = "context_compaction_cancelled"
		code = string(domain.ErrorCompactionCancelled)
	}
	message := "run terminated before context compaction completed"
	if errorMessage != nil {
		message = *errorMessage
	}
	var pending []domain.PendingEvent
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE context_compactions SET status=?,error_code=?,error_message=?,
			finished_at=? WHERE id=? AND status IN ('planned','running')`, status, code, message, timestamp, id); err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(map[string]any{"compactionId": id, "errorCode": code})
		pending = append(pending, domain.PendingEvent{EventType: eventType, Payload: payload})
	}

	runRows, err := tx.QueryContext(ctx, `SELECT id FROM run_context_compactions
		WHERE run_id=? AND status IN ('planned','running')`, runID)
	if err != nil {
		return nil, err
	}
	var runIDs []string
	for runRows.Next() {
		var id string
		if err := runRows.Scan(&id); err != nil {
			runRows.Close()
			return nil, err
		}
		runIDs = append(runIDs, id)
	}
	if err := runRows.Close(); err != nil {
		return nil, err
	}
	runEventType := "run_context_compaction_failed"
	if status == domain.CompactionCancelled {
		runEventType = "run_context_compaction_cancelled"
	}
	for _, id := range runIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE run_context_compactions SET status=?,error_code=?,
			error_message=?,finished_at=? WHERE id=? AND status IN ('planned','running')`,
			status, code, message, timestamp, id); err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(map[string]any{"compactionId": id, "scope": "run", "errorCode": code})
		pending = append(pending, domain.PendingEvent{EventType: runEventType, Payload: payload})
	}
	return pending, nil
}

func closeStartedCallsTx(ctx context.Context, tx *sql.Tx, runID string, target domain.RunStatus, errorCode *string, timestamp string) ([]domain.PendingEvent, error) {
	code := string(domain.ErrorToolBatchFailed)
	if errorCode != nil {
		code = *errorCode
	} else if target == domain.RunCancelled {
		code = string(domain.ErrorRunCancelled)
	} else if target == domain.RunInterrupted {
		code = "worker_restarted"
	}
	status := "failed"
	if target == domain.RunCancelled || target == domain.RunInterrupted {
		status = "cancelled"
	}

	type modelCall struct {
		id                 string
		iteration, attempt int
	}
	modelRows, err := tx.QueryContext(ctx, `SELECT id, iteration, attempt FROM model_calls
		WHERE run_id = ? AND status = 'started' ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list active model calls: %w", err)
	}
	var models []modelCall
	for modelRows.Next() {
		var call modelCall
		if err := modelRows.Scan(&call.id, &call.iteration, &call.attempt); err != nil {
			modelRows.Close()
			return nil, err
		}
		models = append(models, call)
	}
	if err := modelRows.Close(); err != nil {
		return nil, err
	}

	type toolCall struct {
		id, toolCallID, name string
		iteration, callIndex int
	}
	toolRows, err := tx.QueryContext(ctx, `SELECT id, tool_call_id, tool_name, iteration, call_index
		FROM tool_calls WHERE run_id = ? AND status = 'started' ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list active tool calls: %w", err)
	}
	var tools []toolCall
	for toolRows.Next() {
		var call toolCall
		if err := toolRows.Scan(&call.id, &call.toolCallID, &call.name, &call.iteration, &call.callIndex); err != nil {
			toolRows.Close()
			return nil, err
		}
		tools = append(tools, call)
	}
	if err := toolRows.Close(); err != nil {
		return nil, err
	}

	var pending []domain.PendingEvent
	for _, call := range models {
		if _, err := tx.ExecContext(ctx, `UPDATE model_calls SET status = ?, error_code = ?, finished_at = ?
			WHERE id = ? AND status = 'started'`, status, code, timestamp, call.id); err != nil {
			return nil, fmt.Errorf("close active model call: %w", err)
		}
		payload, _ := json.Marshal(map[string]any{"callId": call.id, "iteration": call.iteration,
			"attempt": call.attempt, "category": code, "retryable": false})
		pending = append(pending, domain.PendingEvent{EventType: "model_call_failed", Payload: payload})
	}
	for _, call := range tools {
		content := fmt.Sprintf("Tool call ended because the run became %s.", target)
		rawArtifacts, err := loadRawToolArtifactsTx(ctx, tx, runID, call.toolCallID)
		if err != nil {
			return nil, err
		}
		encodedArtifacts, _ := json.Marshal(nonNilArtifactReferences(rawArtifacts))
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = ?, result_preview = ?,
			raw_artifact_refs_json = ?, is_error = 1, finished_at = ?
			WHERE id = ? AND status = 'started'`, status, content, string(encodedArtifacts), timestamp, call.id); err != nil {
			return nil, fmt.Errorf("close active tool call: %w", err)
		}
		payload, _ := json.Marshal(map[string]any{"recordId": call.id, "iteration": call.iteration,
			"callIndex": call.callIndex, "toolCallId": call.toolCallID, "toolName": call.name,
			"content": content, "isError": true})
		pending = append(pending, domain.PendingEvent{EventType: "tool_call_completed", Payload: payload})
	}
	return pending, nil
}

func loadRawToolArtifactsTx(ctx context.Context, tx *sql.Tx, runID, toolCallID string) ([]domain.ArtifactReference, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,kind,mime_type,size_bytes,sha256,metadata_json
		FROM artifacts WHERE run_id=? AND source_tool_call_id=? ORDER BY created_at,id`, runID, toolCallID)
	if err != nil {
		return nil, fmt.Errorf("load raw tool artifacts: %w", err)
	}
	defer rows.Close()
	var references []domain.ArtifactReference
	for rows.Next() {
		var reference domain.ArtifactReference
		var metadataJSON string
		if err := rows.Scan(&reference.ArtifactID, &reference.Name, &reference.Kind, &reference.MIMEType,
			&reference.SizeBytes, &reference.SHA256, &metadataJSON); err != nil {
			return nil, err
		}
		var dimensions struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		_ = json.Unmarshal([]byte(metadataJSON), &dimensions)
		reference.Width, reference.Height = dimensions.Width, dimensions.Height
		references = append(references, reference)
	}
	return references, rows.Err()
}

func findSubmissionTx(ctx context.Context, tx *sql.Tx, sessionID, requestID string) (*domain.TurnSubmission, error) {
	row := tx.QueryRowContext(ctx,
		runSelect+` JOIN turns t ON t.id = agent_runs.turn_id
		 WHERE t.session_id = ? AND t.client_request_id = ? ORDER BY agent_runs.attempt DESC LIMIT 1`,
		sessionID, requestID,
	)
	run, err := scanAgentRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var messageID string
	if err := tx.QueryRowContext(ctx, `SELECT user_message_id FROM turns WHERE id = ?`, run.TurnID).Scan(&messageID); err != nil {
		return nil, err
	}
	return &domain.TurnSubmission{TurnID: run.TurnID, UserMessageID: messageID, Run: run}, nil
}

const runSelect = `SELECT agent_runs.id, agent_runs.turn_id, agent_runs.session_id, agent_runs.run_kind,
	agent_runs.base_message_id, agent_runs.attempt, agent_runs.status, agent_runs.assistant_message_id,
	agent_runs.retry_of_run_id, agent_runs.commit_format_version, agent_runs.system_prompt_snapshot_json,
	agent_runs.system_prompt_digest, agent_runs.parent_run_id, agent_runs.root_run_id,
	agent_runs.execution_depth, agent_runs.publish_mode,
	agent_runs.speaker_snapshot_json, agent_runs.context_snapshot_json, agent_runs.context_snapshot_digest,
	agent_runs.requested_config_json, agent_runs.effective_config_json, agent_runs.error_code, agent_runs.error_message,
	agent_runs.started_at, agent_runs.finished_at, agent_runs.created_at
	FROM agent_runs`

func scanAgentRun(row rowScanner) (domain.AgentRun, error) {
	var run domain.AgentRun
	var turnID, baseMessageID, assistantID, retryOfRunID, parentRunID, rootRunID, errorCode, errorMessage sql.NullString
	var startedAt, finishedAt sql.NullString
	var speakerSnapshot, contextSnapshot, requestedConfig, effectiveConfig, createdAt string
	var systemPromptSnapshot, systemPromptDigest string
	if err := row.Scan(
		&run.ID, &turnID, &run.SessionID, &run.RunKind, &baseMessageID, &run.Attempt, &run.Status,
		&assistantID, &retryOfRunID, &run.CommitFormatVersion, &systemPromptSnapshot, &systemPromptDigest,
		&parentRunID, &rootRunID, &run.ExecutionDepth, &run.PublishMode,
		&speakerSnapshot, &contextSnapshot, &run.ContextSnapshotDigest,
		&requestedConfig, &effectiveConfig, &errorCode, &errorMessage, &startedAt, &finishedAt, &createdAt,
	); err != nil {
		return run, err
	}
	if turnID.Valid {
		run.TurnID = turnID.String
	}
	if baseMessageID.Valid {
		run.BaseMessageID = baseMessageID.String
	}
	if assistantID.Valid {
		run.AssistantMessageID = &assistantID.String
	}
	if retryOfRunID.Valid {
		run.RetryOfRunID = retryOfRunID.String
	}
	if parentRunID.Valid {
		run.ParentRunID = parentRunID.String
	}
	if rootRunID.Valid {
		run.RootRunID = rootRunID.String
	}
	run.SpeakerSnapshot = json.RawMessage(speakerSnapshot)
	run.ContextSnapshot = json.RawMessage(contextSnapshot)
	if systemPromptDigest != "" {
		var snapshot domain.SystemPromptSnapshot
		if err := json.Unmarshal([]byte(systemPromptSnapshot), &snapshot); err != nil {
			return run, fmt.Errorf("decode frozen system prompt metadata: %w", err)
		}
		if snapshot.Digest != systemPromptDigest {
			return run, fmt.Errorf("frozen system prompt digest mismatch")
		}
		run.SystemPrompt = &domain.SystemPromptMetadata{
			Version: snapshot.Version, AgentProfileID: snapshot.AgentProfileID,
			PlatformVersion: snapshot.PlatformVersion, Digest: systemPromptDigest,
		}
	}
	if errorCode.Valid {
		run.ErrorCode = &errorCode.String
	}
	if errorMessage.Valid {
		run.ErrorMessage = &errorMessage.String
	}
	run.RequestedConfig = json.RawMessage(requestedConfig)
	run.EffectiveConfig = json.RawMessage(effectiveConfig)
	var err error
	run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return run, err
	}
	if startedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, startedAt.String)
		if parseErr != nil {
			return run, parseErr
		}
		run.StartedAt = &value
	}
	if finishedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, finishedAt.String)
		if parseErr != nil {
			return run, parseErr
		}
		run.FinishedAt = &value
	}
	return run, nil
}

func turnStatusForRun(status domain.RunStatus) domain.TurnStatus {
	switch status {
	case domain.RunRunning:
		return domain.TurnRunning
	case domain.RunWaitingForApproval:
		return domain.TurnWaitingForApproval
	case domain.RunSucceeded:
		return domain.TurnSucceeded
	case domain.RunFailed:
		return domain.TurnFailed
	case domain.RunCancelled:
		return domain.TurnCancelled
	case domain.RunInterrupted:
		return domain.TurnInterrupted
	default:
		return domain.TurnPending
	}
}
