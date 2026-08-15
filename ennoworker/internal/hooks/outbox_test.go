package hooks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupOutboxDB(t *testing.T) (*sql.DB, *OutboxStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "outbox.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, InitOutbox(context.Background(), db))
	return db, &OutboxStore{DB: db}
}

func TestOutboxInsertAndFetchPending(t *testing.T) {
	_, s := setupOutboxDB(t)

	tx, err := s.DB.Begin()
	require.NoError(t, err)
	entry := OutboxEntry{
		DeliveryID:    "runend_run-1",
		EventID:       100,
		RunID:         "run-1",
		SessionID:     "session-1",
		EventType:     "RunEnd",
		PayloadJSON:   `{"status":"succeeded"}`,
		WorkspaceID:   "ws-1",
		WorkspaceRoot: "/data/project",
		Status:        OutboxStatusPending,
		CreatedAt:     time.Now(),
	}
	require.NoError(t, s.InsertOutbox(context.Background(), tx, entry))
	require.NoError(t, tx.Commit())

	entries, err := s.FetchPending(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "runend_run-1", entries[0].DeliveryID)
	assert.Equal(t, "RunEnd", entries[0].EventType)
}

func TestOutboxInsertIdempotent(t *testing.T) {
	_, s := setupOutboxDB(t)

	tx, _ := s.DB.Begin()
	entry := OutboxEntry{
		DeliveryID: "runend_run-1", EventID: 100, RunID: "run-1",
		EventType: "RunEnd", PayloadJSON: `{}`, Status: OutboxStatusPending, CreatedAt: time.Now(),
	}
	require.NoError(t, s.InsertOutbox(context.Background(), tx, entry))
	require.NoError(t, tx.Commit())

	// Insert the same event again → INSERT OR IGNORE, no error, still 1 row.
	tx2, _ := s.DB.Begin()
	require.NoError(t, s.InsertOutbox(context.Background(), tx2, entry))
	require.NoError(t, tx2.Commit())

	entries, err := s.FetchPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestOutboxWorkerDeliversRunEndHook(t *testing.T) {
	db, s := setupOutboxDB(t)
	dir := t.TempDir()
	hookScript := filepath.Join(dir, "runend.sh")
	// The hook writes its delivery_id to a file so we can assert delivery.
	outputFile := filepath.Join(dir, "delivered.txt")
	require.NoError(t, os.WriteFile(hookScript, []byte(
		"#!/bin/sh\ncat > "+outputFile+"\n"), 0o700))

	// Build a frozen hook set that matches RunEnd.
	set := HookSet{
		"RunEnd": {
			Matchers: []HookMatcherConfig{
				{ID: "m1", Hooks: []HookConfig{
					{ID: "runend-hook", Command: hookScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}

	// Seed an outbox row.
	tx, _ := db.Begin()
	entry := OutboxEntry{
		DeliveryID:    "runend_run-1",
		EventID:       1,
		RunID:         "run-1",
		EventType:     "RunEnd",
		PayloadJSON:   `{"status":"succeeded"}`,
		WorkspaceID:   "ws-1",
		WorkspaceRoot: dir,
		Status:        OutboxStatusPending,
		CreatedAt:     time.Now(),
	}
	require.NoError(t, s.InsertOutbox(context.Background(), tx, entry))
	require.NoError(t, tx.Commit())

	// Run the worker once (processBatch) via a short-lived Start.
	worker := &OutboxWorker{
		Store: s,
		Resolver: func(ctx context.Context, runID string) (HookSet, string, string, error) {
			return set, "ws-1", dir, nil
		},
		BatchSize: 10,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go worker.Start(ctx)
	time.Sleep(300 * time.Millisecond) // let the first poll fire
	cancel()
	// Process immediately to avoid waiting for the ticker.
	worker.processBatch(context.Background())

	// The hook should have run and written the delivery input.
	data, err := os.ReadFile(outputFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"event_type":"RunEnd"`)
	assert.Contains(t, string(data), `"run_id":"run-1"`)

	// The row should be delivered.
	entries, err := s.FetchPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, entries, "no pending entries after successful delivery")
}

func TestOutboxWorkerMarksFailedHookAsDead(t *testing.T) {
	db, s := setupOutboxDB(t)
	dir := t.TempDir()
	// Hook always fails (exit 3).
	failScript := filepath.Join(dir, "fail.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/sh\nexit 3\n"), 0o700))

	set := HookSet{
		"RunEnd": {
			Matchers: []HookMatcherConfig{
				{ID: "m1", Hooks: []HookConfig{
					{ID: "fail-hook", Command: failScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}

	// Seed an outbox row with attempts already near the cap.
	tx, _ := db.Begin()
	entry := OutboxEntry{
		DeliveryID:    "runend_run-2",
		EventID:       2,
		RunID:         "run-2",
		EventType:     "RunEnd",
		PayloadJSON:   `{}`,
		WorkspaceID:   "ws-1",
		WorkspaceRoot: dir,
		Status:        OutboxStatusPending,
		CreatedAt:     time.Now(),
	}
	require.NoError(t, s.InsertOutbox(context.Background(), tx, entry))
	require.NoError(t, tx.Commit())

	worker := &OutboxWorker{
		Store: s,
		Resolver: func(ctx context.Context, runID string) (HookSet, string, string, error) {
			return set, "ws-1", dir, nil
		},
	}
	// Simulate deliveries. MarkFailed schedules a future next_attempt_at, so
	// manually clear it between rounds to force immediate re-fetch (this is
	// what the worker restart scanner does after the backoff elapses).
	for i := 0; i <= maxDeliveryAttempts; i++ {
		_, _ = db.Exec(`UPDATE hook_event_outbox SET next_attempt_at = NULL WHERE delivery_id = ?`, "runend_run-2")
		entries, _ := s.FetchPending(context.Background(), 10)
		if len(entries) == 0 {
			break
		}
		worker.processBatch(context.Background())
	}

	// After maxDeliveryAttempts (5) failed deliveries, the row must be dead.
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM hook_event_outbox WHERE delivery_id = ?`, "runend_run-2").Scan(&status))
	assert.Equal(t, OutboxStatusDead, status)

	// Dead entries are not returned by FetchPending.
	entries, err := s.FetchPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, entries, "dead entries are not pending")
}
