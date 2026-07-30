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
)

var ErrRunNotActive = errors.New("agent run is not active")

type QueueRepo struct{ DB *sql.DB }

func (r *QueueRepo) Enqueue(ctx context.Context, runID, clientRequestID string, kind domain.QueuedInputKind, text string) (*domain.QueuedInput, error) {
	if kind != domain.QueuedInputSteer && kind != domain.QueuedInputFollowUp {
		return nil, fmt.Errorf("unsupported queued input kind: %s", kind)
	}
	if strings.TrimSpace(clientRequestID) == "" {
		return nil, fmt.Errorf("client request id is required")
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("queued input text is required")
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin queue transaction: %w", err)
	}
	defer tx.Rollback()

	if existing, err := findQueuedInputTx(ctx, tx, runID, clientRequestID); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	var sessionID string
	var status domain.RunStatus
	if err := tx.QueryRowContext(ctx,
		`SELECT session_id, status FROM agent_runs WHERE id = ?`, runID,
	).Scan(&sessionID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("load run for queued input: %w", err)
	}
	if status != domain.RunQueued && status != domain.RunRunning && status != domain.RunWaitingForApproval {
		return nil, ErrRunNotActive
	}

	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM run_input_queue WHERE run_id = ?`, runID,
	).Scan(&seq); err != nil {
		return nil, fmt.Errorf("allocate queued input sequence: %w", err)
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	timestamp := time.Now().UTC()
	item := &domain.QueuedInput{
		ID: uuid.NewString(), RunID: runID, SessionID: sessionID,
		ClientRequestID: clientRequestID, Seq: seq, Kind: kind,
		Text: text, Status: "queued", CreatedAt: timestamp,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO run_input_queue
		 (id, run_id, session_id, client_request_id, seq, kind, content_json, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?)`,
		item.ID, item.RunID, item.SessionID, item.ClientRequestID, item.Seq,
		item.Kind, string(content), timestamp.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("insert queued input: %w", err)
	}
	eventPayload, _ := json.Marshal(map[string]any{
		"queueItemId": item.ID, "kind": item.Kind, "seq": item.Seq,
	})
	if _, err := appendEventsTx(ctx, tx, runID, domain.PendingEvent{
		EventType: "input_queued", Payload: eventPayload,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit queued input: %w", err)
	}
	return item, nil
}

func (r *QueueRepo) Drain(ctx context.Context, runID string, kind domain.QueuedInputKind, mode domain.QueueMode) ([]domain.QueuedInput, error) {
	limit := 1
	if mode == domain.QueueAll {
		limit = 1000
	} else if mode != domain.QueueOneAtATime {
		return nil, fmt.Errorf("unsupported queue mode: %s", mode)
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin queue drain: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT id, run_id, session_id, client_request_id, seq, kind, content_json, status, created_at
		 FROM run_input_queue
		 WHERE run_id = ? AND kind = ? AND status = 'queued'
		 ORDER BY seq LIMIT ?`, runID, kind, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query queued inputs: %w", err)
	}
	var items []domain.QueuedInput
	for rows.Next() {
		var item domain.QueuedInput
		var contentJSON, createdAt string
		if err := rows.Scan(
			&item.ID, &item.RunID, &item.SessionID, &item.ClientRequestID,
			&item.Seq, &item.Kind, &contentJSON, &item.Status, &createdAt,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan queued input: %w", err)
		}
		var content struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(contentJSON), &content); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode queued input: %w", err)
		}
		item.Text = content.Text
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("parse queued input timestamp: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	injectedAt := time.Now().UTC()
	for index := range items {
		item := &items[index]
		if _, err := tx.ExecContext(ctx,
			`UPDATE run_input_queue SET status = 'injected', injected_at = ?
			 WHERE id = ? AND status = 'queued'`,
			injectedAt.Format(time.RFC3339Nano), item.ID,
		); err != nil {
			return nil, fmt.Errorf("mark queued input injected: %w", err)
		}
		item.Status = "injected"
		item.InjectedAt = &injectedAt
		payload, _ := json.Marshal(map[string]any{
			"queueItemId": item.ID, "kind": item.Kind, "seq": item.Seq,
		})
		if _, err := appendEventsTx(ctx, tx, runID, domain.PendingEvent{
			EventType: "input_injected", Payload: payload,
		}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit queue drain: %w", err)
	}
	return items, nil
}

func (r *QueueRepo) CancelPending(ctx context.Context, runID string) (int64, error) {
	result, err := r.DB.ExecContext(ctx,
		`UPDATE run_input_queue SET status = 'cancelled', cancelled_at = ?
		 WHERE run_id = ? AND status = 'queued'`,
		time.Now().UTC().Format(time.RFC3339Nano), runID,
	)
	if err != nil {
		return 0, fmt.Errorf("cancel pending inputs: %w", err)
	}
	return result.RowsAffected()
}

func findQueuedInputTx(ctx context.Context, tx *sql.Tx, runID, clientRequestID string) (*domain.QueuedInput, error) {
	var item domain.QueuedInput
	var contentJSON, createdAt string
	err := tx.QueryRowContext(ctx,
		`SELECT id, run_id, session_id, client_request_id, seq, kind, content_json, status, created_at
		 FROM run_input_queue WHERE run_id = ? AND client_request_id = ?`,
		runID, clientRequestID,
	).Scan(
		&item.ID, &item.RunID, &item.SessionID, &item.ClientRequestID,
		&item.Seq, &item.Kind, &contentJSON, &item.Status, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find queued input: %w", err)
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(contentJSON), &content); err != nil {
		return nil, err
	}
	item.Text = content.Text
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	return &item, nil
}
