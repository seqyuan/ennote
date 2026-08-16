package sessionstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func setupTailDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDatabase(filepath.Join(t.TempDir(), "session.db"))
	require.NoError(t, err)
	require.NoError(t, migrate(db))
	t.Cleanup(func() { db.Close() })
	return db
}

func insertTailSession(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES(?,?,?,?)`,
		id, "project", "2026-08-10T00:00:00Z", "2026-08-10T00:00:00Z")
	require.NoError(t, err)
}

// insertTailRun inserts a minimal agent run (run_kind=agent) with the given
// status, wiring the required turn and message rows.
func insertTailRun(t *testing.T, db *sql.DB, id, sessionID, status string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO messages(id,session_id,role,created_at) VALUES(?,?,?,?)`,
		"msg-"+id, sessionID, "user", "2026-08-10T00:00:00Z")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO turns(id,session_id,client_request_id,user_message_id,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
		"turn-"+id, sessionID, "req-"+id, "msg-"+id, "complete", "2026-08-10T00:00:00Z", "2026-08-10T00:00:00Z")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs(id,session_id,run_kind,turn_id,status,publish_mode,commit_format_version,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		id, sessionID, "agent", "turn-"+id, status, "public_final", 1, "2026-08-10T00:00:00Z")
	require.NoError(t, err)
}

func TestScanTailRemovesTornRowsOfTerminalRuns(t *testing.T) {
	db := setupTailDB(t)
	ctx := context.Background()
	insertTailSession(t, db, "session")
	insertTailRun(t, db, "terminal", "session", "failed")
	insertTailRun(t, db, "active", "session", "running")

	// Terminal run: a started (half-written) model call and tool call.
	_, err := db.Exec(`INSERT INTO model_calls(id,run_id,seq,status,started_at) VALUES('mc-t','terminal',1,'started','t')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tool_calls(id,run_id,seq,tool_call_id,tool_name,status,started_at) VALUES('tc-t','terminal',1,'tcid','echo','started','t')`)
	require.NoError(t, err)

	// Active run: a started model call that must survive.
	_, err = db.Exec(`INSERT INTO model_calls(id,run_id,seq,status,started_at) VALUES('mc-a','active',1,'started','t')`)
	require.NoError(t, err)
	// Terminal run: a completed model call that must survive.
	_, err = db.Exec(`INSERT INTO model_calls(id,run_id,seq,status,started_at,finished_at,iteration) VALUES('mc-ok','terminal',2,'completed','t','u',1)`)
	require.NoError(t, err)

	require.NoError(t, scanTail(ctx, db))

	// Torn rows gone.
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM model_calls WHERE id='mc-t'`).Scan(&count))
	require.Equal(t, 0, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE id='tc-t'`).Scan(&count))
	require.Equal(t, 0, count)

	// Preserved rows.
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM model_calls WHERE id='mc-a'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM model_calls WHERE id='mc-ok'`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestScanTailIsIdempotent(t *testing.T) {
	db := setupTailDB(t)
	ctx := context.Background()
	insertTailSession(t, db, "session")
	insertTailRun(t, db, "terminal", "session", "cancelled")
	_, err := db.Exec(`INSERT INTO model_calls(id,run_id,seq,status,started_at) VALUES('mc','terminal',1,'started','t')`)
	require.NoError(t, err)

	require.NoError(t, scanTail(ctx, db))
	require.NoError(t, scanTail(ctx, db)) // second pass deletes nothing

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM model_calls WHERE id='mc'`).Scan(&count))
	require.Equal(t, 0, count)
}
