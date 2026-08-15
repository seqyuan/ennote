package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAttemptImmutabilityTriggers validates the delegation_item_attempts
// trigger invariants that are part of the Session schema.
func TestAttemptImmutabilityTriggers(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "attempt-immutable")
	db := repo.DB
	now := "2026-08-03T00:00:00Z"
	_, err := db.Exec(`INSERT INTO delegation_groups(id,parent_run_id,parent_tool_call_id,strategy,status,created_at)
		VALUES('g',?,'tc','single','running',?)`,
		submission.Run.ID, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO delegation_items(id,group_id,name,role_version_id,assignment_json,status,ordinal,created_at)
		VALUES('i-ok','g','explore','builtin-workspace-explorer-v3','{}','running',0,?)`, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO delegation_item_attempts(id,item_id,generation,retry_of_attempt_id,child_run_id,
		authorization_snapshot_json,authorization_snapshot_digest,reserved_budget_json,actual_usage_json,
		status,created_at)
		VALUES('a-ok','i-ok',0,NULL,?,'{"v":1}','sha256:0000000000000000000000000000000000000000000000000000000000000000','{}','{}','running',?)`,
		submission.Run.ID, now)
	require.NoError(t, err)
	// Identity/snapshot columns are immutable.
	_, err = db.Exec(`UPDATE delegation_item_attempts SET authorization_snapshot_json='{"v":2}' WHERE item_id='i-ok'`)
	require.Error(t, err)
	// A running attempt may terminalize exactly once...
	_, err = db.Exec(`UPDATE delegation_item_attempts SET status='succeeded' WHERE item_id='i-ok'`)
	require.NoError(t, err)
	// ...but a terminal attempt cannot reopen or change status.
	_, err = db.Exec(`UPDATE delegation_item_attempts SET status='queued' WHERE item_id='i-ok'`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE delegation_item_attempts SET status='failed',error_code='x' WHERE item_id='i-ok'`)
	require.Error(t, err, "terminal attempts cannot change status at all")
}
