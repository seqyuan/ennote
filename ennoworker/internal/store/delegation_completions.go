package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// createCompletionTx inserts exactly one logical completion for a settled
// handle/generation plus its durable delivery event, in the caller's
// transaction. Blocking generation 0 completions are consumed by the folded
// parent tool result and never produce a background notification; every other
// completion starts pending. The completion insert and delivery event are
// idempotent via unique keys.
func createCompletionTx(ctx context.Context, tx *sql.Tx, groupID string, generation int) (*domain.DelegationCompletion, error) {
	var handleID, sessionID, executionMode string
	var autoResume int
	if err := tx.QueryRowContext(ctx, `SELECT h.id,h.session_id,h.execution_mode,h.auto_resume
		FROM delegation_handles h JOIN delegation_groups g ON g.id=h.group_id
		WHERE h.group_id=?`, groupID).Scan(&handleID, &sessionID, &executionMode, &autoResume); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDelegationGroupNotFound
		}
		return nil, err
	}
	kind := "completed"
	var groupStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM delegation_groups WHERE id=?`, groupID).Scan(&groupStatus); err != nil {
		return nil, err
	}
	if groupStatus == "cancelled" {
		kind = "cancelled"
	}
	resultJSON, err := foldGenerationResult(ctx, tx, groupID, generation)
	if err != nil {
		return nil, err
	}
	digest, err := digestJSON(json.RawMessage(resultJSON))
	if err != nil {
		return nil, err
	}
	sequence, err := nextDeliverySequenceTx(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	completionID := uuid.NewString()

	deliveryStatus := "pending"
	if generation == 0 && executionMode == string(domain.DelegationExecutionBlocking) {
		deliveryStatus = "consumed_by_parent"
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delegation_completions
		(id,handle_id,session_id,generation,kind,result_json,result_digest,sequence,delivery_status,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		completionID, handleID, sessionID, generation, kind, resultJSON, digest, sequence, deliveryStatus, now); err != nil {
		return nil, fmt.Errorf("create delegation completion: %w", err)
	}
	eventType := "delegation_completion"
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delivery_events
		(session_id,source_kind,source_id,source_generation,event_type,payload_json,created_at)
		VALUES(?, 'delegation_completion',?,?,?,?,?)`,
		sessionID, handleID, generation, eventType,
		mustMarshalJSON(map[string]any{"completionId": completionID, "handleId": handleID,
			"generation": generation, "kind": kind, "deliveryStatus": deliveryStatus}),
		now); err != nil {
		return nil, fmt.Errorf("create delivery event: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return nil, err
	}
	// Project the cross-session notification (bounded, never the private
	// transcript or credentials).
	var projectID string
	_ = tx.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id=?`, sessionID).Scan(&projectID)
	attentionKind := domain.AttentionDelegationCompleted
	if kind == "cancelled" || kind == "failed" {
		attentionKind = domain.AttentionDelegationFailed
	}
	_ = ProjectAttentionTx(ctx, tx, projectID, sessionID,
		domain.AttentionSourceDelegationCompletion, handleID, generation,
		attentionKind, false,
		map[string]any{"kind": kind, "generation": generation,
			"summary": boundedAttentionSummary(resultJSON)},
		&domain.AttentionAction{Kind: "none"})
	return &domain.DelegationCompletion{
		ID: completionID, HandleID: handleID, SessionID: sessionID, Generation: generation,
		Kind: kind, ResultJSON: resultJSON, ResultDigest: digest, Sequence: sequence,
		DeliveryStatus: deliveryStatus, CreatedAt: parsed,
	}, nil
}

// foldGenerationResult aggregates the explicit generation selection in ordinal
// order — never by timestamp — into the completion result payload.
func foldGenerationResult(ctx context.Context, tx *sql.Tx, groupID string, generation int) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT i.name,COALESCE(a.status,''),COALESCE(a.result_json,'')
		FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		WHERE i.group_id=? AND a.generation=? ORDER BY i.ordinal`, groupID, generation)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type childResult struct {
		Name   string          `json:"name"`
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
	}
	children := make([]childResult, 0)
	for rows.Next() {
		var name, status string
		var result sql.NullString
		if err := rows.Scan(&name, &status, &result); err != nil {
			return "", err
		}
		res := json.RawMessage("null")
		if result.Valid && result.String != "" {
			res = json.RawMessage(result.String)
		}
		children = append(children, childResult{Name: name, Status: status, Result: res})
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	payload := map[string]any{"status": "settled", "children": children}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// RebuildMissingCompletions reconstructs completions for settled generations
// that lost their delivery insert (crash between settlement and completion).
// It never reruns children; terminal attempt facts are the source of truth.
func (r *DelegationRepo) RebuildMissingCompletions(ctx context.Context, limit int) ([]domain.DelegationCompletion, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT g.id,g.status FROM delegation_groups g
		WHERE g.status IN ('settled','cancelled')
		  AND NOT EXISTS (SELECT 1 FROM delegation_completions c
			JOIN delegation_handles h ON h.id=c.handle_id WHERE h.group_id=g.id)
		ORDER BY g.created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	type pending struct{ groupID, status string }
	var pendingGroups []pending
	for rows.Next() {
		var entry pending
		if err := rows.Scan(&entry.groupID, &entry.status); err != nil {
			rows.Close()
			return nil, err
		}
		pendingGroups = append(pendingGroups, entry)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	completions := make([]domain.DelegationCompletion, 0, len(pendingGroups))
	for _, group := range pendingGroups {
		var generation int
		if err := tx.QueryRowContext(ctx, `SELECT current_generation FROM delegation_groups WHERE id=?`,
			group.groupID).Scan(&generation); err != nil {
			return nil, err
		}
		completion, err := createCompletionTx(ctx, tx, group.groupID, generation)
		if err != nil {
			return nil, err
		}
		completions = append(completions, *completion)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return completions, nil
}

// RebuildMissingDeliveryEvents replays durable delivery events for completions
// whose event insert was lost, using the unique source key as idempotency.
func (r *DelegationRepo) RebuildMissingDeliveryEvents(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.handle_id,c.session_id,c.generation,c.kind,c.delivery_status
		FROM delegation_completions c
		WHERE NOT EXISTS (SELECT 1 FROM delivery_events e
			WHERE e.source_kind='delegation_completion' AND e.source_id=c.handle_id
			  AND e.source_generation=c.generation AND e.event_type='delegation_completion')
		ORDER BY c.sequence LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type completionRow struct {
		id, handleID, sessionID, kind, deliveryStatus string
		generation                                    int
	}
	var pending []completionRow
	for rows.Next() {
		var entry completionRow
		if err := rows.Scan(&entry.id, &entry.handleID, &entry.sessionID, &entry.generation,
			&entry.kind, &entry.deliveryStatus); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, entry)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, completion := range pending {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delivery_events
			(session_id,source_kind,source_id,source_generation,event_type,payload_json,created_at)
			VALUES(?, 'delegation_completion',?,?, 'delegation_completion',?,?)`,
			completion.sessionID, completion.handleID, completion.generation,
			mustMarshalJSON(map[string]any{"completionId": completion.id, "handleId": completion.handleID,
				"generation": completion.generation, "kind": completion.kind,
				"deliveryStatus": completion.deliveryStatus}),
			now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(pending), nil
}
