// This file defines the durable outbox for observer hooks. Observer hooks
// (RunEnd, PreCompact, ApprovalRequested, Notification) must survive worker
// restarts, so their delivery is recorded in a SQLite outbox table and
// processed by a background worker with at-least-once semantics.
//
// The outbox model: one row per event (event_id UNIQUE). The background
// worker loads the run's frozen hook set at delivery time and fans out to
// each matching hook, generating a stable sub-delivery key.
package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

const (
	OutboxStatusPending    = "pending"
	OutboxStatusDelivering = "delivering"
	OutboxStatusDelivered  = "delivered"
	OutboxStatusDead       = "dead"
	maxDeliveryAttempts     = 5
	outboxPollInterval      = 5 * time.Second
)

// OutboxEntry is a single pending observer hook delivery.
type OutboxEntry struct {
	DeliveryID   string
	EventID      int64
	RunID        string
	SessionID    string
	EventType    string
	PayloadJSON  string
	WorkspaceID  string
	WorkspaceRoot string
	Status       string
	Attempts     int
	NextAttemptAt *time.Time
	CreatedAt    time.Time
}

// OutboxWriter is the interface for inserting outbox rows in a transaction.
type OutboxWriter interface {
	InsertOutbox(ctx context.Context, tx *sql.Tx, entry OutboxEntry) error
}

// OutboxStore reads/writes the hook_event_outbox table.
type OutboxStore struct {
	DB *sql.DB
}

// InitOutbox creates the outbox table if it doesn't exist.
func InitOutbox(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS hook_event_outbox (
		delivery_id   TEXT PRIMARY KEY,
		event_id      INTEGER NOT NULL UNIQUE,
		run_id        TEXT NOT NULL,
		session_id    TEXT NOT NULL DEFAULT '',
		event_type    TEXT NOT NULL,
		payload_json  TEXT NOT NULL DEFAULT '{}',
		workspace_id  TEXT NOT NULL DEFAULT '',
		workspace_root TEXT NOT NULL DEFAULT '',
		status        TEXT NOT NULL CHECK(status IN ('pending','delivering','delivered','dead')),
		attempts      INTEGER NOT NULL DEFAULT 0,
		next_attempt_at TEXT,
		last_error    TEXT,
		created_at    TEXT NOT NULL,
		delivered_at  TEXT
	)`)
	return err
}

// InsertOutbox inserts an outbox row within an existing transaction. The
// delivery_id is typically "{event_id}_{event_type}" to ensure one row per
// durable event.
func (s *OutboxStore) InsertOutbox(ctx context.Context, tx *sql.Tx, entry OutboxEntry) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO hook_event_outbox
		(delivery_id, event_id, run_id, session_id, event_type, payload_json,
		 workspace_id, workspace_root, status, attempts, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.DeliveryID, entry.EventID, entry.RunID, entry.SessionID, entry.EventType,
		entry.PayloadJSON, entry.WorkspaceID, entry.WorkspaceRoot,
		OutboxStatusPending, 0, time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// FetchPending returns up to limit pending/delivering entries whose
// next_attempt_at is nil or in the past.
func (s *OutboxStore) FetchPending(ctx context.Context, limit int) ([]OutboxEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT delivery_id, event_id, run_id, session_id, event_type,
		payload_json, workspace_id, workspace_root, status, attempts, next_attempt_at, created_at
		FROM hook_event_outbox
		WHERE status IN ('pending','delivering')
		AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY event_id ASC LIMIT ?`, time.Now().UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []OutboxEntry
	for rows.Next() {
		var entry OutboxEntry
		var nextAttempt sql.NullString
		var createdAt string
		if err := rows.Scan(&entry.DeliveryID, &entry.EventID, &entry.RunID, &entry.SessionID,
			&entry.EventType, &entry.PayloadJSON, &entry.WorkspaceID, &entry.WorkspaceRoot,
			&entry.Status, &entry.Attempts, &nextAttempt, &createdAt); err != nil {
			return nil, err
		}
		if nextAttempt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, nextAttempt.String)
			entry.NextAttemptAt = &t
		}
		entry.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// MarkDelivering sets an entry's status to 'delivering', increments attempts,
// and returns the new attempt count.
func (s *OutboxStore) MarkDelivering(ctx context.Context, deliveryID string) (int, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE hook_event_outbox
		SET status = ?, attempts = attempts + 1, next_attempt_at = NULL
		WHERE delivery_id = ?`, OutboxStatusDelivering, deliveryID)
	if err != nil {
		return 0, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return 0, sql.ErrNoRows
	}
	return entryAttempts(s.DB, ctx, deliveryID), nil
}

// MarkDelivered sets an entry's status to 'delivered'.
func (s *OutboxStore) MarkDelivered(ctx context.Context, deliveryID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hook_event_outbox
		SET status = ?, delivered_at = ? WHERE delivery_id = ?`,
		OutboxStatusDelivered, time.Now().UTC().Format(time.RFC3339Nano), deliveryID)
	return err
}

// MarkFailed records a failure and schedules a retry with exponential backoff.
func (s *OutboxStore) MarkFailed(ctx context.Context, deliveryID string, errMsg string) error {
	backoff := time.Duration(1<<uint(minInt(entryAttempts(s.DB, ctx, deliveryID), 5))) * time.Second
	nextAttempt := time.Now().UTC().Add(backoff).Format(time.RFC3339Nano)
	_, dbErr := s.DB.ExecContext(ctx, `UPDATE hook_event_outbox
		SET status = ?, last_error = ?, next_attempt_at = ?
		WHERE delivery_id = ?`, OutboxStatusPending, errMsg, nextAttempt, deliveryID)
	return dbErr
}

// MarkDead marks an entry as dead (max retries exceeded).
func (s *OutboxStore) MarkDead(ctx context.Context, deliveryID string, errMsg string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hook_event_outbox
		SET status = ?, last_error = ? WHERE delivery_id = ?`,
		OutboxStatusDead, errMsg, deliveryID)
	return err
}

// OutboxWorker is a background goroutine that processes observer hook deliveries.
type OutboxWorker struct {
	Store     *OutboxStore
	Resolver  func(ctx context.Context, runID string) (HookSet, string, string, error) // returns (frozenHookSet, workspaceID, workspaceRoot, error)
	Runner    *Runner
	BatchSize int
}

// Start begins processing the outbox in a loop. It should be run in a goroutine.
func (w *OutboxWorker) Start(ctx context.Context) {
	if w.BatchSize <= 0 {
		w.BatchSize = 10
	}
	ticker := time.NewTicker(outboxPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

func (w *OutboxWorker) processBatch(ctx context.Context) {
	entries, err := w.Store.FetchPending(ctx, w.BatchSize)
	if err != nil {
		slog.Warn("outbox: fetch pending failed", "error", err)
		return
	}
	for _, entry := range entries {
		w.deliverOne(ctx, entry)
	}
}

func (w *OutboxWorker) deliverOne(ctx context.Context, entry OutboxEntry) {
	attempt, err := w.Store.MarkDelivering(ctx, entry.DeliveryID)
	if err != nil {
		return
	}
	if attempt > maxDeliveryAttempts {
		_ = w.Store.MarkDead(ctx, entry.DeliveryID, "max attempts exceeded")
		return
	}

	// Load the frozen hook set from the run's effective config.
	frozenSet, wsID, wsRoot, err := w.Resolver(ctx, entry.RunID)
	if err != nil {
		slog.Warn("outbox: resolve hook set failed", "delivery_id", entry.DeliveryID, "error", err)
		_ = w.Store.MarkFailed(ctx, entry.DeliveryID, err.Error())
		return
	}

	dispatcher := NewDispatcher(frozenSet, wsRoot, nil)
	if dispatcher == nil {
		_ = w.Store.MarkDelivered(ctx, entry.DeliveryID)
		return
	}

	// Resolve each matching hook and deliver.
	matched := frozenSet.MatchHooks(entry.EventType, "", nil)
	allOk := true
	for _, h := range matched {
		subKey := fmt.Sprintf("%s_%s", entry.DeliveryID, h.ID)
		input := HookInput{
			DeliveryID:    subKey,
			EventType:     entry.EventType,
			RunID:         entry.RunID,
			SessionID:     entry.SessionID,
			WorkspaceID:   wsID,
			WorkspaceRoot: wsRoot,
		}
		// Parse the durable event payload and merge relevant fields.
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err == nil {
			if s, ok := payload["status"].(string); ok {
				input.Status = s
			}
			if s, ok := payload["error_code"].(string); ok {
				input.ErrorCode = s
			}
			if s, ok := payload["message"].(string); ok {
				input.Message = s
			}
		}

		r := w.Runner
		if r == nil {
			r = &Runner{ProjectDir: wsRoot}
		} else {
			r.ProjectDir = wsRoot
		}
		_, err := r.Run(ctx, h, input)
		if err != nil {
			slog.Warn("outbox: hook delivery failed", "delivery_id", subKey, "hook", h.ID, "error", err)
			allOk = false
		}
	}

	if allOk {
		_ = w.Store.MarkDelivered(ctx, entry.DeliveryID)
	} else {
		_ = w.Store.MarkFailed(ctx, entry.DeliveryID, "one or more hooks failed")
	}
}

func entryAttempts(db *sql.DB, ctx context.Context, deliveryID string) int {
	var a int
	_ = db.QueryRowContext(ctx, `SELECT attempts FROM hook_event_outbox WHERE delivery_id = ?`, deliveryID).Scan(&a)
	return a
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
