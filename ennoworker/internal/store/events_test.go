package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	stores "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/require"
)

func TestSessionEventsAfterWhitelistOrderAndVisibility(t *testing.T) {
	db, _, session := newSessionDB(t)
	ctx := context.Background()
	events := &stores.EventRepo{DB: db}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	pubRun := uuid.NewString()
	msgID := uuid.NewString()
	_, err := db.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, role, status, speaker_kind, speaker_snapshot_json, visibility, created_at, seq)
		 VALUES (?,?, 'user', 'complete', 'user', '{"kind":"user"}', 'public', ?, 0)`, msgID, session.ID, now)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO agent_runs (id, session_id, run_kind, base_message_id, publish_mode, created_at) VALUES (?,?,'context_compaction',?,'public_final',?)`,
		pubRun, session.ID, msgID, now)
	require.NoError(t, err)
	childRun := uuid.NewString()
	_, err = db.ExecContext(ctx,
		`INSERT INTO agent_runs (id, session_id, run_kind, parent_run_id, publish_mode, created_at) VALUES (?,?,'delegated_agent',?,'private_to_parent',?)`,
		childRun, session.ID, pubRun, now)
	require.NoError(t, err)

	insert := func(runID string, seq int64, eventType string) {
		_, err := db.ExecContext(ctx,
			`INSERT INTO run_events (run_id, seq, event_type, payload_json, created_at) VALUES (?,?,?,'{}',?)`,
			runID, seq, eventType, now)
		require.NoError(t, err)
	}
	insert(pubRun, 1, "run_queued")               // not whitelisted
	insert(pubRun, 2, "message_committed")        // whitelisted
	insert(pubRun, 3, "run_transcript_committed") // not whitelisted
	insert(pubRun, 4, "run_succeeded")            // whitelisted
	insert(childRun, 1, "message_committed")      // whitelisted type but private_to_parent

	got, err := events.SessionEventsAfter(ctx, session.ID, 0, 100)
	require.NoError(t, err)

	var types []string
	for _, event := range got {
		types = append(types, event.EventType)
	}
	require.Equal(t, []string{"message_committed", "run_succeeded"}, types)

	// Cursor resume: after the first event, only the second remains.
	after, err := events.SessionEventsAfter(ctx, session.ID, got[0].EventID, 100)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, "run_succeeded", after[0].EventType)
}
