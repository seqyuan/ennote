package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var ErrRunTranscriptShadowMissing = errors.New("run transcript shadow missing")

type RunMessageRepo struct{ DB *sql.DB }

type RunTranscript struct {
	RunID         string
	FormatVersion domain.CommitFormatVersion
	Messages      []domain.RunMessage
	Digest        string
}

func (r *RunMessageRepo) List(ctx context.Context, runID string) (RunTranscript, error) {
	if runID == "" {
		return RunTranscript{}, fmt.Errorf("run id is required")
	}
	var format domain.CommitFormatVersion
	if err := r.DB.QueryRowContext(ctx, `SELECT commit_format_version FROM agent_runs WHERE id=?`, runID).Scan(&format); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunTranscript{}, ErrRunNotFound
		}
		return RunTranscript{}, err
	}
	messages, err := loadRunMessages(ctx, r.DB, runID)
	if err != nil {
		return RunTranscript{}, err
	}
	if len(messages) == 0 {
		return RunTranscript{RunID: runID, FormatVersion: format, Messages: []domain.RunMessage{}},
			domain.NewCodedError(domain.ErrorTranscriptShadowMissing, ErrRunTranscriptShadowMissing)
	}
	digest, err := transcriptDigest(format, messages)
	if err != nil {
		return RunTranscript{}, err
	}
	return RunTranscript{RunID: runID, FormatVersion: format, Messages: messages, Digest: digest}, nil
}

// ResumeMessages projects a Waiting run's private transcript back into model
// history. Folded tool results are joined by the provider-visible tool_call_id;
// tool_calls.id is an internal record UUID and must never be sent to a provider.
func (r *RunMessageRepo) ResumeMessages(ctx context.Context, runID string) ([]domain.ChatMessage, error) {
	stored, err := loadRunMessages(ctx, r.DB, runID)
	if err != nil {
		return nil, err
	}
	if len(stored) == 0 {
		return nil, nil
	}

	// loadRunMessages closes its rows before this second query. The worker uses
	// one SQLite connection, so nesting this query inside transcript iteration
	// would deadlock.
	rows, err := r.DB.QueryContext(ctx, `SELECT tool_call_id,result_preview
		FROM tool_calls WHERE run_id=? AND status='completed'`, runID)
	if err != nil {
		return nil, err
	}
	folded := make(map[string]string)
	for rows.Next() {
		var toolCallID, result string
		if err := rows.Scan(&toolCallID, &result); err != nil {
			_ = rows.Close()
			return nil, err
		}
		folded[toolCallID] = result
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	messages := make([]domain.ChatMessage, 0, len(stored))
	for _, storedMessage := range stored {
		blocks := storedMessage.Content
		for index := range blocks {
			if blocks[index].ToolResult == nil {
				continue
			}
			if result, ok := folded[blocks[index].ToolResult.ToolCallID]; ok && result != "" {
				toolResult := *blocks[index].ToolResult
				toolResult.Content = result
				blocks[index].ToolResult = &toolResult
			}
		}
		messages = append(messages, domain.ChatMessage{Role: storedMessage.Role, Content: blocks})
	}
	return messages, nil
}

type runMessageReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadRunMessages(ctx context.Context, reader runMessageReader, runID string) ([]domain.RunMessage, error) {
	rows, err := reader.QueryContext(ctx, `SELECT id,run_id,ordinal,role,payload_json,visibility,created_at
		FROM run_messages WHERE run_id=? ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]domain.RunMessage, 0)
	for rows.Next() {
		var message domain.RunMessage
		var payload, createdAt string
		if err := rows.Scan(&message.ID, &message.RunID, &message.Ordinal, &message.Role,
			&payload, &message.Visibility, &createdAt); err != nil {
			return nil, err
		}
		if message.Ordinal != len(messages) {
			return nil, domain.NewCodedError(domain.ErrorTranscriptCorrupt,
				fmt.Errorf("run transcript ordinal %d, expected %d", message.Ordinal, len(messages)))
		}
		if !validRunMessageRole(message.Role) || !json.Valid([]byte(payload)) {
			return nil, domain.NewCodedError(domain.ErrorTranscriptCorrupt,
				fmt.Errorf("run transcript row %d is invalid", message.Ordinal))
		}
		if err := json.Unmarshal([]byte(payload), &message.Content); err != nil {
			return nil, domain.NewCodedError(domain.ErrorTranscriptCorrupt,
				fmt.Errorf("decode run transcript row %d: %w", message.Ordinal, err))
		}
		message.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, domain.NewCodedError(domain.ErrorTranscriptCorrupt, err)
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func AppendRunMessagesTx(ctx context.Context, tx *sql.Tx, runID string, format domain.CommitFormatVersion,
	generated []domain.ChatMessage, timestamp time.Time) ([]domain.RunMessage, string, error) {
	if runID == "" {
		return nil, "", fmt.Errorf("run id is required")
	}
	if len(generated) == 0 {
		return nil, "", fmt.Errorf("run transcript cannot be empty")
	}
	existing, err := loadRunMessages(ctx, tx, runID)
	if err != nil {
		return nil, "", err
	}
	if len(generated) < len(existing) {
		return nil, "", fmt.Errorf("run transcript shrank from %d to %d messages", len(existing), len(generated))
	}

	messages := make([]domain.RunMessage, 0, len(generated))
	for ordinal, generatedMessage := range generated {
		if !validRunMessageRole(generatedMessage.Role) {
			return nil, "", fmt.Errorf("unsupported run transcript role %q", generatedMessage.Role)
		}
		payload, err := json.Marshal(generatedMessage.Content)
		if err != nil {
			return nil, "", fmt.Errorf("encode run transcript row %d: %w", ordinal, err)
		}
		if !json.Valid(payload) {
			return nil, "", fmt.Errorf("encode run transcript row %d: invalid JSON", ordinal)
		}

		if ordinal < len(existing) {
			message := existing[ordinal]
			if message.Role != generatedMessage.Role ||
				!compatibleRunMessageContentUpdate(message.Content, generatedMessage.Content) {
				return nil, "", domain.NewCodedError(domain.ErrorTranscriptCorrupt,
					fmt.Errorf("run transcript row %d changed outside folded tool result content", ordinal))
			}
			if _, err := tx.ExecContext(ctx, `UPDATE run_messages SET payload_json=? WHERE id=?`,
				string(payload), message.ID); err != nil {
				return nil, "", fmt.Errorf("update run transcript row %d: %w", ordinal, err)
			}
			message.Content = generatedMessage.Content
			messages = append(messages, message)
			continue
		}

		message := domain.RunMessage{ID: uuid.NewString(), RunID: runID, Ordinal: ordinal,
			Role: generatedMessage.Role, Content: generatedMessage.Content,
			Visibility: domain.VisibilityPrivate, CreatedAt: timestamp}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_messages
			(id,run_id,ordinal,role,payload_json,visibility,created_at) VALUES(?,?,?,?,?,'private',?)`,
			message.ID, runID, ordinal, message.Role, string(payload), timestamp.Format(time.RFC3339Nano)); err != nil {
			return nil, "", fmt.Errorf("append run transcript row %d: %w", ordinal, err)
		}
		messages = append(messages, message)
	}
	digest, err := transcriptDigest(format, messages)
	if err != nil {
		return nil, "", err
	}
	return messages, digest, nil
}

// compatibleRunMessageContentUpdate permits only the placeholder-to-folded
// result upgrade. Tool IDs, names, roles, block kinds, and every other field
// remain immutable across parent resume.
func compatibleRunMessageContentUpdate(existing, incoming []domain.ContentBlock) bool {
	if len(existing) != len(incoming) {
		return false
	}
	for index := range existing {
		left, right := existing[index], incoming[index]
		if left.ToolResult == nil || right.ToolResult == nil {
			if !reflect.DeepEqual(left, right) {
				return false
			}
			continue
		}
		leftResult, rightResult := *left.ToolResult, *right.ToolResult
		rightResult.Content = leftResult.Content
		left.ToolResult, right.ToolResult = &leftResult, &rightResult
		if !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}

func transcriptDigest(format domain.CommitFormatVersion, messages []domain.RunMessage) (string, error) {
	hash := sha256.New()
	for index, message := range messages {
		if message.Ordinal != index || !validRunMessageRole(message.Role) {
			return "", domain.NewCodedError(domain.ErrorTranscriptCorrupt,
				fmt.Errorf("invalid transcript ordinal or role at %d", index))
		}
		payload, err := json.Marshal(message.Content)
		if err != nil {
			return "", err
		}
		envelope, err := json.Marshal(struct {
			Format  domain.CommitFormatVersion `json:"format"`
			Ordinal int                        `json:"ordinal"`
			Role    domain.Role                `json:"role"`
			Payload json.RawMessage            `json:"payload"`
		}{format, message.Ordinal, message.Role, payload})
		if err != nil {
			return "", err
		}
		hash.Write(envelope)
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validRunMessageRole(role domain.Role) bool {
	switch role {
	case domain.RoleSystem, domain.RoleUser, domain.RoleAssistant, domain.RoleTool:
		return true
	default:
		return false
	}
}
