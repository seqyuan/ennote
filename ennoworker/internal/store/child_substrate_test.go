package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChildRunCoexistsWithRunningParent verifies migration 21 replaced the
// session-active unique index with a parent-aware one: a private child Run can
// be created while the top-level Host Run is queued/running, and a second
// top-level Run is still rejected.
func TestChildRunCoexistsWithRunningParent(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "child-coexist")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	// Second top-level Run must still be rejected by the parent-aware index.
	_, err = repo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: submission.Run.SessionID, ClientRequestID: "second-top", Text: "nope",
	})
	require.Error(t, err)

	// A child Run (delegated_agent, parent_run_id set) may coexist.
	now := "2026-08-03T00:00:00Z"
	childID := "child-coexist-1"
	_, err = repo.DB.Exec(`INSERT INTO agent_runs
		(id,turn_id,session_id,run_kind,base_message_id,attempt,status,requested_config_json,
		 effective_config_json,speaker_snapshot_json,root_run_id,parent_run_id,execution_depth,publish_mode,
		 commit_format_version,context_snapshot_json,created_at)
		VALUES(?,NULL,?,'delegated_agent',?,1,'queued','{}','{}',
		 '{"kind":"role","displayName":"Workspace Explorer"}',?,'`+submission.Run.ID+`',1,'private_to_parent',
		 2,'{}',?)`,
		childID, submission.Run.SessionID, submission.UserMessageID, submission.Run.ID, now)
	require.NoError(t, err, "child Run must coexist with a running parent under migration 21")

	var status string
	require.NoError(t, repo.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, childID).Scan(&status))
	assert.Equal(t, "queued", status)

	// Child must not participate in the top-level busy fence.
	var topCount int
	require.NoError(t, repo.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE session_id=? AND parent_run_id IS NULL
		AND status IN ('queued','running')`, submission.Run.SessionID).Scan(&topCount))
	assert.Equal(t, 1, topCount)
}

func TestDelegationTablesExistWithWorkspaceExplorerBuiltin(t *testing.T) {
	db := store.SetupDB(t)
	ctx := context.Background()
	for _, table := range []string{"delegation_groups", "delegation_items", "run_budgets"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count))
		assert.Equal(t, 1, count, table)
	}
	var roleCount, versionCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM agent_profiles WHERE id='builtin-workspace-explorer'`).Scan(&roleCount))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM agent_profile_versions WHERE id='builtin-workspace-explorer-v1'`).Scan(&versionCount))
	assert.Equal(t, 1, roleCount)
	assert.Equal(t, 1, versionCount)
	var currentVersion string
	require.NoError(t, db.QueryRow(`SELECT current_version_id FROM agent_profiles WHERE id='builtin-workspace-explorer'`).Scan(&currentVersion))
	assert.Equal(t, "builtin-workspace-explorer-v3", currentVersion)

	// The builtin definition must pass RoleRepo validation (known tools, fixed
	// or inherit binding accepted, read-only without mutation tools).
	repo := &store.RoleRepo{DB: db, KnownTools: map[string]bool{
		"read": true, "ls": true, "grep": true, "find": true, "git_readonly": true,
	}, KnownSkills: map[string]bool{}}
	version, err := repo.GetVersion(ctx, "builtin-workspace-explorer", "builtin-workspace-explorer-v1")
	require.NoError(t, err)
	assert.Equal(t, domain.RoleAuthorityReadOnly, version.Definition.Authority)
	for _, tool := range version.Definition.AllowedTools {
		assert.NotEqual(t, "bash", tool, "read-only Explorer must not allow mutation tools")
		assert.NotEqual(t, "write", tool)
		assert.NotEqual(t, "exec", tool)
	}
	var enabled string
	require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key='workspace_explorer_enabled'`).Scan(&enabled))
	assert.Equal(t, "1", enabled)
}

var _ = json.RawMessage{}
