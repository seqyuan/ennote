package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type TranscriptSource string

const (
	TranscriptSourceShadow        TranscriptSource = "shadow"
	TranscriptSourceLegacy        TranscriptSource = "legacy"
	TranscriptSourceSpeakerLedger TranscriptSource = "speaker_ledger"
)

type CompatibleTranscript struct {
	RunID         string
	FormatVersion domain.CommitFormatVersion
	Source        TranscriptSource
	Messages      []domain.RunMessage
	Digest        string
}

func LoadRunTranscript(ctx context.Context, db *sql.DB, runID string) (CompatibleTranscript, error) {
	var format domain.CommitFormatVersion
	var assistantID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT commit_format_version,assistant_message_id FROM agent_runs WHERE id=?`, runID).
		Scan(&format, &assistantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompatibleTranscript{}, ErrRunNotFound
		}
		return CompatibleTranscript{}, err
	}
	if format == domain.CommitFormatSpeakerV2 {
		transcript, err := (&RunMessageRepo{DB: db}).List(ctx, runID)
		if err != nil {
			return CompatibleTranscript{}, err
		}
		if !assistantID.Valid || assistantID.String == "" {
			return CompatibleTranscript{}, domain.NewCodedError(domain.ErrorTranscriptCorrupt,
				fmt.Errorf("format 2 run has no public assistant message"))
		}
		var role, visibility, ownerRunID string
		if err := db.QueryRowContext(ctx, `SELECT role,visibility,COALESCE(run_id,'') FROM messages WHERE id=?`, assistantID.String).
			Scan(&role, &visibility, &ownerRunID); err != nil {
			return CompatibleTranscript{}, domain.NewCodedError(domain.ErrorTranscriptCorrupt, err)
		}
		if role != string(domain.RoleAssistant) || visibility != string(domain.VisibilityPublic) || ownerRunID != runID {
			return CompatibleTranscript{}, domain.NewCodedError(domain.ErrorTranscriptCorrupt,
				errors.New("format 2 public message attribution is invalid"))
		}
		parts, err := loadMessageParts(ctx, db, assistantID.String)
		if err != nil {
			return CompatibleTranscript{}, err
		}
		lastAssistant := -1
		for index := range transcript.Messages {
			if transcript.Messages[index].Role == domain.RoleAssistant {
				lastAssistant = index
			}
		}
		if lastAssistant < 0 {
			return CompatibleTranscript{}, domain.NewCodedError(domain.ErrorTranscriptCorrupt,
				errors.New("format 2 transcript has no assistant output"))
		}
		canonicalPayload, _ := json.Marshal(parts)
		privatePayload, _ := json.Marshal(transcript.Messages[lastAssistant].Content)
		if string(canonicalPayload) != string(privatePayload) {
			return CompatibleTranscript{}, domain.NewCodedError(domain.ErrorTranscriptShadowMismatch,
				errors.New("format 2 public answer differs from final private assistant output"))
		}
		return CompatibleTranscript{RunID: runID, FormatVersion: format, Source: TranscriptSourceSpeakerLedger,
			Messages: transcript.Messages, Digest: transcript.Digest}, nil
	}
	if format != domain.CommitFormatLegacyV1 {
		return CompatibleTranscript{}, domain.NewCodedError(domain.ErrorContextProjectionNotEnabled,
			fmt.Errorf("transcript projection for format %d is not enabled", format))
	}
	canonical, err := loadLegacyRunTranscript(ctx, db, runID, assistantID)
	if err != nil {
		return CompatibleTranscript{}, err
	}
	shadow, shadowErr := (&RunMessageRepo{DB: db}).List(ctx, runID)
	if shadowErr != nil {
		if domain.ErrorCodeOf(shadowErr) != domain.ErrorTranscriptShadowMissing {
			return CompatibleTranscript{}, shadowErr
		}
		digest, err := transcriptDigest(format, canonical)
		if err != nil {
			return CompatibleTranscript{}, err
		}
		return CompatibleTranscript{RunID: runID, FormatVersion: format, Source: TranscriptSourceLegacy,
			Messages: canonical, Digest: digest}, nil
	}
	if err := compareTranscripts(canonical, shadow.Messages); err != nil {
		return CompatibleTranscript{}, domain.NewCodedError(domain.ErrorTranscriptShadowMismatch, err)
	}
	return CompatibleTranscript{RunID: runID, FormatVersion: format, Source: TranscriptSourceShadow,
		Messages: shadow.Messages, Digest: shadow.Digest}, nil
}

func loadLegacyRunTranscript(ctx context.Context, db *sql.DB, runID string, assistantID sql.NullString) ([]domain.RunMessage, error) {
	if !assistantID.Valid || assistantID.String == "" {
		return nil, domain.NewCodedError(domain.ErrorTranscriptShadowMissing,
			fmt.Errorf("run %s has no committed transcript", runID))
	}
	current := assistantID.String
	reversed := make([]domain.RunMessage, 0)
	for current != "" {
		var messageID, ownerRunID, role, createdAt string
		var parent sql.NullString
		err := db.QueryRowContext(ctx, `SELECT id,parent_message_id,COALESCE(run_id,''),role,created_at
			FROM messages WHERE id=?`, current).Scan(&messageID, &parent, &ownerRunID, &role, &createdAt)
		if err != nil {
			return nil, domain.NewCodedError(domain.ErrorTranscriptCorrupt, err)
		}
		if ownerRunID != runID {
			break
		}
		content, err := loadMessageParts(ctx, db, messageID)
		if err != nil {
			return nil, err
		}
		created, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, domain.NewCodedError(domain.ErrorTranscriptCorrupt, err)
		}
		reversed = append(reversed, domain.RunMessage{ID: messageID, RunID: runID, Role: domain.Role(role),
			Content: content, Visibility: domain.VisibilityPrivate, CreatedAt: created})
		if !parent.Valid {
			break
		}
		current = parent.String
	}
	if len(reversed) == 0 {
		return nil, domain.NewCodedError(domain.ErrorTranscriptCorrupt,
			fmt.Errorf("run %s has no run-owned canonical messages", runID))
	}
	messages := make([]domain.RunMessage, len(reversed))
	for index := range reversed {
		message := reversed[len(reversed)-1-index]
		message.Ordinal = index
		messages[index] = message
	}
	return messages, nil
}

func compareTranscripts(canonical, shadow []domain.RunMessage) error {
	if len(canonical) != len(shadow) {
		return fmt.Errorf("transcript row count differs: canonical=%d shadow=%d", len(canonical), len(shadow))
	}
	for index := range canonical {
		if canonical[index].Role != shadow[index].Role || canonical[index].Ordinal != shadow[index].Ordinal {
			return fmt.Errorf("transcript row %d role or ordinal differs", index)
		}
		left, err := json.Marshal(canonical[index].Content)
		if err != nil {
			return err
		}
		right, err := json.Marshal(shadow[index].Content)
		if err != nil {
			return err
		}
		if string(left) != string(right) {
			return fmt.Errorf("transcript row %d payload differs", index)
		}
	}
	return nil
}
