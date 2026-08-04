package store_test

import (
	"strconv"
	"testing"

	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateCreatesTables(t *testing.T) {
	db := store.SetupDB(t)

	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	require.NoError(t, err)
	assert.Greater(t, count, 0, "should have at least one migration applied")

	tables := []string{
		"projects", "project_workspaces",
		"provider_profiles", "model_profiles", "agent_profiles",
		"sessions", "messages", "message_parts",
		"turns", "agent_runs", "run_events",
		"model_calls", "tool_calls", "usage_records",
		"skill_snapshots", "artifacts", "settings", "run_input_queue",
		"policy_profiles", "image_descriptions", "context_compactions", "session_compaction_state",
		"run_context_compactions", "run_execution_checkpoints", "tool_approval_requests", "session_branches",
		"room_member_instances", "run_messages", "agent_profile_versions",
		"delegation_groups", "delegation_items", "run_budgets", "delegation_root_budgets",
		"delegation_group_generations", "delegation_item_attempts", "delegation_approval_requests",
	}
	for _, name := range tables {
		var exists int
		err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&exists)
		assert.NoError(t, err, "query for table %s", name)
		assert.Equal(t, 1, exists, "table %s should exist", name)
	}
	var permissionProfiles int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM policy_profiles
		WHERE id IN ('builtin-tool-discuss-v1','builtin-tool-ask-v1','builtin-tool-auto-v1') AND status='active'`).Scan(&permissionProfiles))
	assert.Equal(t, 3, permissionProfiles)
	var hostedDelegationPolicy int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM policy_profiles
		WHERE id='builtin-hosted-delegation-v1' AND kind='delegation' AND status='active'`).Scan(&hostedDelegationPolicy))
	assert.Equal(t, 1, hostedDelegationPolicy, "builtin-hosted-delegation-v1 should be active after migration 0023")
	var discussV2 int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM policy_profiles
		WHERE id = 'builtin-tool-discuss-v2' AND status = 'active'`).Scan(&discussV2))
	assert.Equal(t, 1, discussV2, "builtin-tool-discuss-v2 should be active after migration 0014")
	var riskColumn int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tool_calls') WHERE name='risk_class'`).Scan(&riskColumn))
	assert.Equal(t, 1, riskColumn)
	var sessionLifecycleIndex int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_sessions_project_status_updated'`).Scan(&sessionLifecycleIndex))
	assert.Equal(t, 1, sessionLifecycleIndex)
	for table, columns := range map[string][]string{
		"artifacts":      {"source_tool_call_id", "source_kind", "source_workspace_path", "retention_class"},
		"tool_calls":     {"raw_artifact_refs_json", "projected_artifact_refs_json"},
		"sessions":       {"mode", "delegation_policy_profile_id"},
		"messages":       {"speaker_kind", "speaker_snapshot_json", "addressee_kind", "visibility", "originated_at"},
		"turns":          {"input_message_id", "input_kind", "target_kind", "context_mode", "reply_to_json"},
		"agent_runs":     {"speaker_snapshot_json", "context_snapshot_json", "commit_format_version", "system_prompt_snapshot_json"},
		"model_profiles": {"thinking_dialect", "supported_thinking_efforts_json", "input_cost_usd_micros_per_million", "output_cost_usd_micros_per_million"},
		"run_budgets":    {"consumed_output_tokens", "consumed_cost_usd_micros", "started_at", "root_reconciled_at"},
		"agent_profiles": {"object_kind", "handle", "scope", "project_id", "draft_json", "draft_revision", "current_version_id"},
		"delegation_groups": {"current_generation", "updated_at", "completed_at"},
		"delegation_item_attempts": {"retry_of_attempt_id", "child_run_id", "authorization_snapshot_json", "actual_usage_json", "result_digest", "root_reconciled_at"},
	} {
		for _, column := range columns {
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count))
			assert.Equal(t, 1, count, "%s.%s", table, column)
		}
	}
}

func TestAgentLoopMigrationBackfillsExistingCallIndexes(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, migration := range migrations.Sorted() {
		if migration.Version > 3 {
			break
		}
		_, err = db.Exec(migration.SQL)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-07-27T00:00:00Z')`, migration.Version)
		require.NoError(t, err)
	}
	now := "2026-07-27T00:00:00Z"
	_, err = db.Exec(`INSERT INTO projects (id, name, created_at, updated_at) VALUES ('p1','test',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions (id, project_id, created_at, updated_at) VALUES ('s1','p1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id, session_id, role, created_at) VALUES ('m1','s1','user',?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO turns (id, session_id, client_request_id, user_message_id, created_at, updated_at) VALUES ('t1','s1','req1','m1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs (id, turn_id, session_id, status, created_at) VALUES ('r1','t1','s1','running',?)`, now)
	require.NoError(t, err)
	for seq := 1; seq <= 2; seq++ {
		_, err = db.Exec(`INSERT INTO model_calls (id, run_id, seq, started_at) VALUES (?, 'r1', ?, ?)`, "mc"+strconv.Itoa(seq), seq, now)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO tool_calls (id, run_id, seq, tool_call_id, tool_name, started_at) VALUES (?, 'r1', ?, ?, 'read', ?)`,
			"tc"+strconv.Itoa(seq), seq, "call"+strconv.Itoa(seq), now)
		require.NoError(t, err)
	}
	require.NoError(t, store.Migrate(db))
	var distinctModelIterations, distinctToolIterations int
	require.NoError(t, db.QueryRow(`SELECT COUNT(DISTINCT iteration) FROM model_calls WHERE run_id = 'r1'`).Scan(&distinctModelIterations))
	require.NoError(t, db.QueryRow(`SELECT COUNT(DISTINCT iteration) FROM tool_calls WHERE run_id = 'r1'`).Scan(&distinctToolIterations))
	assert.Equal(t, 2, distinctModelIterations)
	assert.Equal(t, 2, distinctToolIterations)
}

func TestAgentExtensionMigrationsBackfillPolicyAndCallAudit(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, migration := range migrations.Sorted() {
		if migration.Version > 4 {
			break
		}
		_, err = db.Exec(migration.SQL)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-07-27T00:00:00Z')`, migration.Version)
		require.NoError(t, err)
	}
	now := "2026-07-27T00:00:00Z"
	_, err = db.Exec(`INSERT INTO provider_profiles (id,name,provider_type,base_url,credential_ref,created_at,updated_at)
		VALUES ('pvd','provider','openai-compatible','https://example.test','env:KEY',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO model_profiles (id,provider_id,model_name,display_name,created_at,updated_at)
		VALUES ('mdl','pvd','model','Model',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_profiles (id,name,tool_policy,default_model_id,created_at,updated_at)
		VALUES ('a1','agent','restricted','mdl',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO projects (id,name,created_at,updated_at) VALUES ('p1','project',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions (id,project_id,created_at,updated_at) VALUES ('s1','p1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id,session_id,role,created_at) VALUES ('m1','s1','user',?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO turns (id,session_id,client_request_id,user_message_id,created_at,updated_at)
		VALUES ('t1','s1','req','m1',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs (id,turn_id,session_id,status,created_at) VALUES ('r1','t1','s1','running',?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tool_calls
		(id,run_id,seq,tool_call_id,tool_name,arguments_json,status,result_preview,started_at,iteration,call_index)
		VALUES ('tc1','r1',1,'call1','read','{"path":"x"}','completed','ok',?,1,0)`, now)
	require.NoError(t, err)

	require.NoError(t, store.Migrate(db))
	var policyID, original, effective, projected string
	require.NoError(t, db.QueryRow(`SELECT tool_policy_profile_id FROM agent_profiles WHERE id='a1'`).Scan(&policyID))
	assert.NotEmpty(t, policyID)
	require.NoError(t, db.QueryRow(`SELECT original_arguments_json,effective_arguments_json,projected_result_preview
		FROM tool_calls WHERE id='tc1'`).Scan(&original, &effective, &projected))
	assert.JSONEq(t, `{"path":"x"}`, original)
	assert.JSONEq(t, original, effective)
	assert.Equal(t, "ok", projected)

	_, err = db.Exec(`INSERT INTO model_calls
		(id,run_id,seq,started_at,iteration,attempt,purpose,source_artifact_id)
		VALUES ('primary','r1',1,?,1,1,'agent_turn',''),
		       ('descriptor','r1',2,?,1,1,'image_description','image-1')`, now, now)
	require.NoError(t, err, "primary and descriptor calls may share an iteration and attempt")
}

func TestRunRecoveryBranchMigrationBackfillsExistingSessions(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, migration := range migrations.Sorted() {
		if migration.Version >= 10 {
			break
		}
		_, err = db.Exec(migration.SQL)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-07-28T00:00:00Z')`, migration.Version)
		require.NoError(t, err)
	}
	now := "2026-07-28T00:00:00Z"
	_, err = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Project',?,?);
		INSERT INTO sessions(id,project_id,title,created_at,updated_at) VALUES('s','p','Existing',?,?);
		INSERT INTO messages(id,session_id,role,created_at) VALUES('m','s','user',?);
		UPDATE sessions SET active_leaf_message_id='m' WHERE id='s'`, now, now, now, now, now)
	require.NoError(t, err)

	require.NoError(t, store.Migrate(db))
	var activeBranchID, label, leafID string
	require.NoError(t, db.QueryRow(`SELECT s.active_branch_id,b.label,b.leaf_message_id
		FROM sessions s JOIN session_branches b ON b.id=s.active_branch_id WHERE s.id='s'`).Scan(
		&activeBranchID, &label, &leafID))
	assert.NotEmpty(t, activeBranchID)
	assert.Equal(t, "Main", label)
	assert.Equal(t, "m", leafID)

	for table, columns := range map[string][]string{
		"sessions":   {"active_branch_id"},
		"agent_runs": {"retry_of_run_id", "retry_client_request_id"},
	} {
		for _, column := range columns {
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count))
			assert.Equal(t, 1, count, "%s.%s", table, column)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := store.SetupDB(t)

	var initial int
	err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&initial)
	require.NoError(t, err)

	err = store.Migrate(db)
	require.NoError(t, err)

	var after int
	err = db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&after)
	require.NoError(t, err)
	assert.Equal(t, initial, after)
}

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
	_, err := db.Exec(`INSERT INTO projects (id, name, created_at, updated_at) VALUES ('p1','test',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions (id, project_id, created_at, updated_at) VALUES ('s1','p1',?,?)`, now, now)
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
	_, err := db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','P',?,?);
		INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?);
		INSERT INTO messages(id,session_id,role,created_at) VALUES('m','s','user',?);
		INSERT INTO agent_runs(id,turn_id,session_id,run_kind,base_message_id,status,created_at)
		VALUES('compact',NULL,'s','context_compaction','m','running',?)`, now, now, now, now, now, now)
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
	_, err := db.Exec(`INSERT INTO projects (id, name, created_at, updated_at) VALUES ('p1','test',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions (id, project_id, created_at, updated_at) VALUES ('s1','p1',?,?)`, now, now)
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
	_, err := db.Exec(`INSERT INTO projects (id, name, created_at, updated_at) VALUES ('p1','test',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions (id, project_id, created_at, updated_at) VALUES ('s1','p1',?,?)`, now, now)
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
