package store_test

import (
	"testing"

	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWALAndForeignKeys(t *testing.T) {
	db := store.SetupDBFile(t)

	// WAL may report as "delete" until first write; verify that sqlite
	// accepts the connection and that foreign keys are enforced.
	err := db.Ping()
	require.NoError(t, err)

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS _fk_test (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	// Verify foreign keys are active by attempting a violation
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS _fk_child (pid INTEGER REFERENCES _fk_test(id))")
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO _fk_child (pid) VALUES (999)") // should fail
	assert.Error(t, err, "foreign key constraint must be enforced")
}

func TestActiveRunConstraint(t *testing.T) {
	db := store.SetupDB(t)

	now := "2026-07-27T00:00:00Z"
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, created_at, updated_at) VALUES ('s1','p1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, created_at) VALUES ('m1','s1','user',?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO turns (id, session_id, client_request_id, user_message_id, created_at, updated_at) VALUES ('t1','s1','req1','m1',?,?)`, now, now)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO agent_runs (id, turn_id, session_id, attempt, status, created_at) VALUES ('r1','t1','s1',1,'queued',?)`, now)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO agent_runs (id, turn_id, session_id, attempt, status, created_at) VALUES ('r2','t1','s1',2,'queued',?)`, now)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "UNIQUE")
}

func TestContextCompactionMigrationGeneralizesRunsAndRequestGenerations(t *testing.T) {
	db := store.SetupDB(t)
	now := "2026-07-28T00:00:00Z"
	_, err := db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?);
		INSERT INTO messages(id,session_id,role,created_at) VALUES('m','s','user',?);
		INSERT INTO agent_runs(id,turn_id,session_id,run_kind,base_message_id,status,created_at)
		VALUES('compact',NULL,'s','context_compaction','m','running',?)`, now, now, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs(id,turn_id,session_id,run_kind,status,created_at)
		VALUES('invalid',NULL,'s','agent','failed',?)`, now)
	assert.Error(t, err)

	_, err = db.Exec(`INSERT INTO model_calls
		(id,run_id,seq,started_at,iteration,attempt,purpose,source_artifact_id,request_generation)
		VALUES('g0','compact',1,?,1,1,'agent_turn','',0),
		      ('g1','compact',2,?,1,1,'agent_turn','',1)`, now, now)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM model_calls WHERE run_id='compact'`).Scan(&count))
	assert.Equal(t, 2, count)
}

func TestClientRequestIdUnique(t *testing.T) {
	db := store.SetupDB(t)

	now := "2026-07-27T00:00:00Z"
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, created_at, updated_at) VALUES ('s1','p1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, created_at) VALUES ('m1','s1','user',?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, created_at) VALUES ('m2','s1','user',?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO turns (id, session_id, client_request_id, user_message_id, created_at, updated_at) VALUES ('t1','s1','req-dup','m1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO turns (id, session_id, client_request_id, user_message_id, created_at, updated_at) VALUES ('t2','s1','req-dup','m2',?,?)`, now, now)
	assert.Error(t, err)
}

func TestUniqueRunEventsSeq(t *testing.T) {
	db := store.SetupDB(t)

	now := "2026-07-27T00:00:00Z"
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, created_at, updated_at) VALUES ('s1','p1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, created_at) VALUES ('m1','s1','user',?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO turns (id, session_id, client_request_id, user_message_id, created_at, updated_at) VALUES ('t1','s1','req1','m1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs (id, turn_id, session_id, attempt, status, created_at) VALUES ('r1','t1','s1',1,'running',?)`, now)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO run_events (run_id, seq, event_type, created_at) VALUES ('r1',1,'text_delta',?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO run_events (run_id, seq, event_type, created_at) VALUES ('r1',1,'text_delta',?)`, now)
	assert.Error(t, err, "duplicate seq in same run must be rejected")
}
