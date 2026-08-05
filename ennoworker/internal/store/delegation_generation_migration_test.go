package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMigration22DB applies migrations 1..22 to a fresh in-memory database.
func newMigration22DB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = OFF")
	require.NoError(t, err)
	for _, migration := range migrations.Sorted() {
		if migration.Version > 22 {
			break
		}
		_, err = db.Exec(migration.SQL)
		require.NoError(t, err)
	}
	return db
}

// runMigration24 applies migration 24 and its Go backfill exactly as the
// production Migrate flow does.
func runMigration24(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, migration := range migrations.Sorted() {
		if migration.Version != 24 {
			continue
		}
		_, err := db.Exec(migration.SQL)
		require.NoError(t, err)
	}
	tx, err := db.Begin()
	require.NoError(t, err)
	require.NoError(t, store.BackfillDelegationGenerations(context.Background(), tx))
	require.NoError(t, tx.Commit())
}

// seedMigration22DelegationDB applies migrations 1..22 and seeds delegation
// history: a settled two-child group (one success, one failure), a waiting
// group, and a cancelled group, with consumed child budget rows.
func seedMigration22DelegationDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newMigration22DB(t)
	exec := func(sql string) {
		t.Helper()
		_, err := db.Exec(sql)
		require.NoError(t, err)
	}
	exec(`INSERT INTO projects (id,name,created_at,updated_at) VALUES ('p1','p',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO sessions (id,project_id,title,created_at,updated_at) VALUES ('s1','p1','s',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO session_branches (id,session_id,label,created_at,updated_at)
		VALUES ('b1','s1','main',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	exec(`UPDATE sessions SET active_branch_id='b1' WHERE id='s1'`)
	exec(`INSERT INTO messages (id,session_id,role,status,created_at) VALUES ('m1','s1','user','complete',CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO turns (id,session_id,client_request_id,user_message_id,status,created_at,updated_at)
		VALUES ('t1','s1','req','m1','pending',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO agent_runs
		(id,turn_id,session_id,run_kind,status,publish_mode,commit_format_version,created_at)
		VALUES ('parent','t1','s1','agent','running','public_final',2,CURRENT_TIMESTAMP)`)
	for _, child := range []struct{ id, status string }{
		{"child-ok", "succeeded"}, {"child-fail", "failed"}, {"child-wait", "queued"}, {"child-cancel", "cancelled"},
	} {
		exec(`INSERT INTO agent_runs
			(id,session_id,run_kind,status,parent_run_id,publish_mode,commit_format_version,created_at)
			VALUES ('` + child.id + `','s1','delegated_agent','` + child.status + `','parent','private_to_parent',2,CURRENT_TIMESTAMP)`)
	}
	exec(`INSERT INTO delegation_groups (id,parent_run_id,parent_tool_call_id,strategy,status,created_at)
		VALUES ('g-settled','parent','call-1','parallel','settled',CURRENT_TIMESTAMP),
		       ('g-waiting','parent','call-2','single','waiting_children',CURRENT_TIMESTAMP),
		       ('g-cancelled','parent','call-3','single','cancelled',CURRENT_TIMESTAMP)`)
	budget := `'{"maxModelCalls":4,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}'`
	exec(`INSERT INTO delegation_items
		(id,group_id,child_run_id,name,role_version_id,assignment_json,output_contract,budget_json,result_json,status,ordinal,created_at)
		VALUES
		 ('i-ok','g-settled','child-ok','ok','builtin-workspace-explorer-v3','{}','text-v1',` + budget + `,
		  '{"status":"completed","summary":"found README"}','succeeded',0,CURRENT_TIMESTAMP),
		 ('i-fail','g-settled','child-fail','fail','builtin-workspace-explorer-v3','{}','text-v1',` + budget + `,
		  '{"status":"blocked","summary":"exploration failed"}','failed',1,CURRENT_TIMESTAMP),
		 ('i-wait','g-waiting','child-wait','wait','builtin-workspace-explorer-v3','{}','text-v1',` + budget + `,
		  NULL,'pending',0,CURRENT_TIMESTAMP),
		 ('i-cancel','g-cancelled','child-cancel','cancel','builtin-workspace-explorer-v3','{}','text-v1',` + budget + `,
		  NULL,'cancelled',0,CURRENT_TIMESTAMP)`)
	exec(`INSERT INTO run_budgets
		(run_id,max_model_calls,max_tool_calls,max_total_tokens,max_output_tokens,max_cost_usd_micros,max_wall_time_ms,
		 consumed_model_calls,consumed_tool_calls,consumed_tokens,reserved_at)
		VALUES
		 ('child-ok',4,8,20000,4000,100000,120000,2,3,1000,CURRENT_TIMESTAMP),
		 ('child-fail',4,8,20000,4000,100000,120000,1,2,500,CURRENT_TIMESTAMP),
		 ('child-wait',4,8,20000,4000,100000,120000,0,0,0,CURRENT_TIMESTAMP),
		 ('child-cancel',4,8,20000,4000,100000,120000,0,0,0,CURRENT_TIMESTAMP)`)
	return db
}

func TestMigration24GenerationBackfillPreservesDelegationHistory(t *testing.T) {
	db := seedMigration22DelegationDB(t)
	runMigration24(t, db)

	// Every group gets exactly one generation 0 with the right status.
	var generations, currentGeneration int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_group_generations`).Scan(&generations))
	assert.Equal(t, 3, generations)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_group_generations WHERE generation=0`).Scan(&currentGeneration))
	assert.Equal(t, 3, currentGeneration)
	statusByGroup := map[string]string{}
	rows, err := db.Query(`SELECT group_id,status FROM delegation_group_generations`)
	require.NoError(t, err)
	for rows.Next() {
		var groupID, status string
		require.NoError(t, rows.Scan(&groupID, &status))
		statusByGroup[groupID] = status
	}
	require.NoError(t, rows.Close())
	assert.Equal(t, "settled", statusByGroup["g-settled"])
	assert.Equal(t, "running", statusByGroup["g-waiting"])
	assert.Equal(t, "cancelled", statusByGroup["g-cancelled"])

	// Group cursors point at generation 0.
	require.NoError(t, db.QueryRow(`SELECT current_generation FROM delegation_groups WHERE id='g-settled'`).Scan(&currentGeneration))
	assert.Equal(t, 0, currentGeneration)

	// One attempt per child Run, preserving exact status and result.
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts`).Scan(&attempts))
	assert.Equal(t, 4, attempts)
	attemptsByItem := map[string]struct {
		status, childRunID, result string
	}{}
	attemptRows, err := db.Query(`SELECT item_id,status,child_run_id,COALESCE(result_json,'') FROM delegation_item_attempts`)
	require.NoError(t, err)
	for attemptRows.Next() {
		var itemID, status, childRunID, result string
		require.NoError(t, attemptRows.Scan(&itemID, &status, &childRunID, &result))
		attemptsByItem[itemID] = struct {
			status, childRunID, result string
		}{status, childRunID, result}
	}
	require.NoError(t, attemptRows.Close())
	assert.Equal(t, "succeeded", attemptsByItem["i-ok"].status)
	assert.Equal(t, "child-ok", attemptsByItem["i-ok"].childRunID)
	assert.JSONEq(t, `{"status":"completed","summary":"found README"}`, attemptsByItem["i-ok"].result)
	assert.Equal(t, "failed", attemptsByItem["i-fail"].status)
	assert.Equal(t, "child-fail", attemptsByItem["i-fail"].childRunID)
	assert.Equal(t, "cancelled", attemptsByItem["i-cancel"].status)
	assert.Equal(t, "queued", attemptsByItem["i-wait"].status)

	// Usage and digests are preserved and canonical.
	var usageJSON string
	require.NoError(t, db.QueryRow(`SELECT actual_usage_json FROM delegation_item_attempts WHERE item_id='i-ok'`).Scan(&usageJSON))
	var usage map[string]int64
	require.NoError(t, json.Unmarshal([]byte(usageJSON), &usage))
	assert.EqualValues(t, 2, usage["modelCalls"])
	assert.EqualValues(t, 3, usage["toolCalls"])
	assert.EqualValues(t, 1000, usage["tokens"])

	var resultDigest, authDigest string
	require.NoError(t, db.QueryRow(`SELECT result_digest,authorization_snapshot_digest FROM delegation_item_attempts WHERE item_id='i-ok'`).
		Scan(&resultDigest, &authDigest))
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, resultDigest)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, authDigest)

	// Ordering is preserved from delegation_items.ordinal.
	var ordered []string
	orderRows, err := db.Query(`SELECT a.item_id FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id WHERE i.group_id='g-settled' ORDER BY i.ordinal`)
	require.NoError(t, err)
	for orderRows.Next() {
		var itemID string
		require.NoError(t, orderRows.Scan(&itemID))
		ordered = append(ordered, itemID)
	}
	require.NoError(t, orderRows.Close())
	assert.Equal(t, []string{"i-ok", "i-fail"}, ordered)

	// Idempotency: running the backfill again creates nothing new.
	tx, err := db.Begin()
	require.NoError(t, err)
	require.NoError(t, store.BackfillDelegationGenerations(context.Background(), tx))
	require.NoError(t, tx.Commit())
	var after int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts`).Scan(&after))
	assert.Equal(t, 4, after)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_group_generations`).Scan(&after))
	assert.Equal(t, 3, after)
}

func TestAttemptImmutabilityTriggers(t *testing.T) {
	db := seedMigration22DelegationDB(t)
	runMigration24(t, db)

	// Identity/snapshot columns are immutable.
	_, err := db.Exec(`UPDATE delegation_item_attempts SET authorization_snapshot_json='{}' WHERE item_id='i-ok'`)
	require.Error(t, err)
	// A terminal attempt cannot reopen.
	_, err = db.Exec(`UPDATE delegation_item_attempts SET status='queued' WHERE item_id='i-ok'`)
	require.Error(t, err)
	// Terminal metadata (result/status) can transition exactly once.
	_, err = db.Exec(`UPDATE delegation_item_attempts SET status='failed',error_code='x' WHERE item_id='i-ok'`)
	require.Error(t, err, "terminal attempts cannot change status at all")
}
