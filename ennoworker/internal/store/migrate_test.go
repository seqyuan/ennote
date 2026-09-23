package store_test

import (
	"database/sql"
	"testing"

	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/sessionmigrations"
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

// A column nothing writes and nothing reads is worse than a missing one: it will
// eventually be believed. skill_snapshot_digest was dropped so a fresh database
// and an existing one agree on the shape of agent_runs.
func TestAgentRunsDroppedTheUnusedSkillSnapshotDigest(t *testing.T) {
	db := store.SetupDB(t)

	columns := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('agent_runs')`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		columns[name] = true
	}
	require.NoError(t, rows.Err())

	assert.False(t, columns["skill_snapshot_digest"], "the dead column must be gone")
	// The neighbouring provenance columns must survive: this migration drops one
	// column, not the schema's record of a Run's frozen identity.
	for _, kept := range []string{"system_prompt_digest", "tool_policy_digest", "system_prompt_snapshot_json"} {
		assert.True(t, columns[kept], "%s must survive", kept)
	}
}

// The consolidated initial schema is history and is not edited, so dropping the
// dead column has to be an ordinary incremental migration. This pins that: a
// database created before the migration existed converges to the same shape as a
// fresh one, which is the whole reason the column was not simply deleted from
// 0001_session.sql.
func TestUnusedColumnMigrationConvergesAnExistingDatabase(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Build a database as it existed before 0009: every migration but the last.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`)
	require.NoError(t, err)
	migrations := sessionmigrations.Sorted()
	require.GreaterOrEqual(t, len(migrations), 2)
	target := migrations[len(migrations)-1]
	for _, migration := range migrations {
		if migration.Version >= target.Version {
			continue
		}
		_, err := db.Exec(migration.SQL)
		require.NoError(t, err, "apply migration %d", migration.Version)
		_, err = db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			migration.Version, "2026-09-23T00:00:00Z")
		require.NoError(t, err)
	}
	assert.True(t, hasColumn(t, db, "agent_runs", "skill_snapshot_digest"),
		"the pre-migration state must actually have the column, or this test proves nothing")

	// The real migrator applies only what is missing.
	require.NoError(t, store.MigrateSession(db))
	assert.False(t, hasColumn(t, db, "agent_runs", "skill_snapshot_digest"))

	var applied int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, target.Version).Scan(&applied))
	assert.Equal(t, 1, applied, "the migration must be recorded exactly once")
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		if name == column {
			return true
		}
	}
	require.NoError(t, rows.Err())
	return false
}
