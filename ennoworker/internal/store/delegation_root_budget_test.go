package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRootBudgetParent creates a running top-level Host Run with a frozen
// effective config, which must carry the DelegationPolicySnapshot and create
// the root budget ledger row.
func setupRootBudgetParent(t *testing.T, requestID string) (*store.DelegationRepo, *store.RunRepo, *domain.TurnSubmission) {
	t.Helper()
	repo, submission := setupSubmittedRun(t, requestID)
	ctx := context.Background()
	provider, err := (&store.ProviderRepo{DB: repo.DB}).Create(ctx, store.CreateProviderInput{
		Name: "provider", ProviderType: domain.ProviderOpenAICompatible, BaseURL: "https://provider.test",
		CredentialRef: "env:PROVIDER_KEY",
	})
	require.NoError(t, err)
	_, err = (&store.ModelRepo{DB: repo.DB}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "m", ContextWindow: 32000, MaxOutputTokens: 2048,
		SupportsThinking: true, ThinkingDialect: domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: repo.DB}
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	parentRun, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, parentRun)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Delegation, "top-level Host Run must freeze a delegation policy snapshot")
	return &store.DelegationRepo{DB: repo.DB}, runs, submission
}

func readRootLedger(t *testing.T, db *sql.DB, rootRunID string) map[string]any {
	t.Helper()
	row := db.QueryRow(`SELECT max_model_calls,max_tool_calls,max_total_tokens,max_output_tokens,max_cost_usd_micros,
		reserved_model_calls,reserved_tool_calls,reserved_total_tokens,reserved_output_tokens,reserved_cost_usd_micros,
		consumed_model_calls,consumed_tool_calls,consumed_total_tokens,consumed_output_tokens,consumed_cost_usd_micros,
		active_children,version FROM delegation_root_budgets WHERE root_run_id=?`, rootRunID)
	values := make(map[string]any)
	var fields [17]int64
	require.NoError(t, row.Scan(&fields[0], &fields[1], &fields[2], &fields[3], &fields[4],
		&fields[5], &fields[6], &fields[7], &fields[8], &fields[9],
		&fields[10], &fields[11], &fields[12], &fields[13], &fields[14], &fields[15], &fields[16]))
	for index, name := range []string{
		"max_model_calls", "max_tool_calls", "max_total_tokens", "max_output_tokens", "max_cost_usd_micros",
		"reserved_model_calls", "reserved_tool_calls", "reserved_total_tokens", "reserved_output_tokens", "reserved_cost_usd_micros",
		"consumed_model_calls", "consumed_tool_calls", "consumed_total_tokens", "consumed_output_tokens", "consumed_cost_usd_micros",
		"active_children", "version",
	} {
		values[name] = fields[index]
	}
	return values
}

func TestRootBudgetFreezesWithEffectiveConfig(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "root-freeze")
	ctx := context.Background()
	assert.Equal(t, "builtin-hosted-delegation-v1", func() string {
		var stored string
		require.NoError(t, delegations.DB.QueryRow(`SELECT effective_config_json FROM agent_runs WHERE id=?`,
			submission.Run.ID).Scan(&stored))
		var effective domain.EffectiveRunConfig
		require.NoError(t, json.Unmarshal([]byte(stored), &effective))
		require.NotNil(t, effective.Delegation)
		assert.NotEmpty(t, effective.Delegation.Digest)
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, effective.Delegation.Digest)
		return effective.Delegation.ID
	}())

	var snapshotDigest string
	require.NoError(t, delegations.DB.QueryRow(`SELECT policy_snapshot_digest FROM delegation_root_budgets
		WHERE root_run_id=?`, submission.Run.ID).Scan(&snapshotDigest))
	assert.NotEmpty(t, snapshotDigest)

	// A second freeze is idempotent: no duplicate ledger rows.
	parentRun, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = runs.ResolveAndFreezeConfig(ctx, parentRun)
	require.NoError(t, err)
	var count int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_root_budgets WHERE root_run_id=?`,
		submission.Run.ID).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestRootBudgetRejectsOversizedGroupAtomically(t *testing.T) {
	delegations, _, submission := setupRootBudgetParent(t, "root-oversized")
	ctx := context.Background()
	_, err := delegations.DB.Exec(`UPDATE delegation_root_budgets SET max_model_calls=3,max_tool_calls=8,
		max_total_tokens=20000,max_output_tokens=4000,max_cost_usd_micros=100000 WHERE root_run_id=?`,
		submission.Run.ID)
	require.NoError(t, err)

	// explorerItem() ceiling is 4 model calls; the root envelope admits 3.
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDelegationBudgetExceeded)
	assert.Nil(t, group)
	assert.Nil(t, items)
	assert.Nil(t, children)

	// The losing transaction created nothing: no group, item, child, or budget row.
	var groups, itemsCount, childrenCount, budgets int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_groups WHERE parent_run_id=?`,
		submission.Run.ID).Scan(&groups))
	assert.Zero(t, groups)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_items`).Scan(&itemsCount))
	assert.Zero(t, itemsCount)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?`,
		submission.Run.ID).Scan(&childrenCount))
	assert.Zero(t, childrenCount)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM run_budgets`).Scan(&budgets))
	assert.Zero(t, budgets)
}

func TestConcurrentGroupsContendOnRootLedger(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "root-contention")
	ctx := context.Background()
	// The root envelope admits exactly one explorer item (4 model calls).
	_, err := delegations.DB.Exec(`UPDATE delegation_root_budgets SET max_model_calls=4,max_tool_calls=16,
		max_total_tokens=40000,max_output_tokens=8000,max_cost_usd_micros=200000 WHERE root_run_id=?`,
		submission.Run.ID)
	require.NoError(t, err)

	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-a", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Len(t, children, 1)

	// Simulate the parent becoming runnable again before the first group
	// settles: the second materialization must contend on the same ledger and
	// lose without creating any rows.
	_, err = delegations.DB.Exec(`UPDATE agent_runs SET status='running' WHERE id=?`, submission.Run.ID)
	require.NoError(t, err)
	second, secondItems, secondChildren, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-b", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDelegationBudgetExceeded)
	assert.Nil(t, second)
	assert.Nil(t, secondItems)
	assert.Nil(t, secondChildren)

	ledger := readRootLedger(t, delegations.DB, submission.Run.ID)
	assert.EqualValues(t, 4, ledger["reserved_model_calls"])
	assert.EqualValues(t, 1, ledger["active_children"])

	// The losing group produced no rows.
	var groups, itemCount, childrenCount, budgets int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_groups WHERE parent_run_id=?`,
		submission.Run.ID).Scan(&groups))
	assert.Equal(t, 1, groups)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_items`).Scan(&itemCount))
	assert.Equal(t, 1, itemCount)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?`,
		submission.Run.ID).Scan(&childrenCount))
	assert.Equal(t, 1, childrenCount)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM run_budgets`).Scan(&budgets))
	assert.Equal(t, 1, budgets)

	// Settling the first group releases its reservation, so a later
	// materialization fits again.
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "ok"},
	}))
	_, err = delegations.DB.Exec(`UPDATE agent_runs SET status='running' WHERE id=?`, submission.Run.ID)
	require.NoError(t, err)
	third, _, thirdChildren, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-c", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, third)
	require.Len(t, thirdChildren, 1)
	ledger = readRootLedger(t, delegations.DB, submission.Run.ID)
	assert.EqualValues(t, 4, ledger["reserved_model_calls"], "released reservation must be reusable")
	assert.EqualValues(t, 0, ledger["consumed_model_calls"], "no actual child usage was recorded")
}

func TestRootBudgetReconcilesOnceOnChildSuccess(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "root-reconcile")
	ctx := context.Background()
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	_ = group
	_ = items

	// Simulate actual child usage before terminalization.
	require.NoError(t, delegations.RecordBudgetUsage(ctx, children[0].ID, 2, 3, 1000, 500, 50))

	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "ok"},
	}))

	ledger := readRootLedger(t, delegations.DB, submission.Run.ID)
	assert.EqualValues(t, 0, ledger["reserved_model_calls"], "reservation must be released")
	assert.EqualValues(t, 2, ledger["consumed_model_calls"])
	assert.EqualValues(t, 3, ledger["consumed_tool_calls"])
	assert.EqualValues(t, 1000, ledger["consumed_total_tokens"])
	assert.EqualValues(t, 500, ledger["consumed_output_tokens"])
	assert.EqualValues(t, 50, ledger["consumed_cost_usd_micros"])
	assert.EqualValues(t, 0, ledger["active_children"])

	// Replaying the finalizer and the orphan sweep must not double-charge.
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "ok"},
	}))
	_, err = delegations.ReapOrphans(ctx)
	require.NoError(t, err)
	after := readRootLedger(t, delegations.DB, submission.Run.ID)
	assert.EqualValues(t, 2, after["consumed_model_calls"])
	assert.EqualValues(t, 1000, after["consumed_total_tokens"])
	assert.EqualValues(t, 0, after["reserved_model_calls"])
}

func TestRootBudgetReconcilesOnCancel(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "root-cancel")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Len(t, children, 1)

	require.NoError(t, runs.Cancel(ctx, submission.Run.ID))
	ledger := readRootLedger(t, delegations.DB, submission.Run.ID)
	assert.EqualValues(t, 0, ledger["reserved_model_calls"], "cancel must release the child reservation")
	assert.EqualValues(t, 0, ledger["active_children"])
	assert.EqualValues(t, 0, ledger["consumed_model_calls"])

	childStatus, err := runs.Get(ctx, children[0].ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, childStatus.Status)
}

func TestRootBudgetReconcilesInterruptedChildViaOrphanSweep(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "root-interrupted")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Len(t, children, 1)

	// Worker restart interrupts the child; the orphan sweep must reconcile.
	require.NoError(t, runs.Interrupt(ctx, children[0].ID, "worker restarted"))
	_, err = delegations.ReapOrphans(ctx)
	require.NoError(t, err)

	ledger := readRootLedger(t, delegations.DB, submission.Run.ID)
	assert.EqualValues(t, 0, ledger["reserved_model_calls"])
	assert.EqualValues(t, 0, ledger["active_children"])
}

func TestMigration23UpgradesPreserveDelegationHistory(t *testing.T) {
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

	// Seed a migration-22 dataset: one settled group with one child and one
	// consumed child budget row, plus one pending group.
	_, err = db.Exec(`INSERT INTO projects (id,name,created_at,updated_at) VALUES ('p1','p',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions (id,project_id,title,created_at,updated_at) VALUES ('s1','p1','s',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO messages (id,session_id,role,status,created_at) VALUES ('m1','s1','user','complete',CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO turns (id,session_id,client_request_id,user_message_id,status,created_at,updated_at)
		VALUES ('t1','s1','req','m1','pending',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs
		(id,turn_id,session_id,run_kind,status,publish_mode,commit_format_version,created_at)
		VALUES ('parent','t1','s1','agent','running','public_final',2,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs
		(id,session_id,run_kind,status,parent_run_id,publish_mode,commit_format_version,created_at)
		VALUES ('child','s1','delegated_agent','succeeded','parent','private_to_parent',2,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO delegation_groups (id,parent_run_id,parent_tool_call_id,strategy,status,created_at)
		VALUES ('g1','parent','call-1','single','settled',CURRENT_TIMESTAMP),
		       ('g2','parent','call-2','single','waiting_children',CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO delegation_items
		(id,group_id,child_run_id,name,role_version_id,assignment_json,output_contract,budget_json,result_json,status,ordinal,created_at)
		VALUES ('i1','g1','child','explore','builtin-workspace-explorer-v2','{}','text-v1',
		        '{"maxModelCalls":4,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}',
		        '{"status":"completed","summary":"ok"}','succeeded',0,CURRENT_TIMESTAMP),
		       ('i2','g2',NULL,'explore2','builtin-workspace-explorer-v2','{}','text-v1',
		        '{"maxModelCalls":4,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}',
		        NULL,'pending',0,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO run_budgets
		(run_id,max_model_calls,max_tool_calls,max_total_tokens,max_output_tokens,max_cost_usd_micros,max_wall_time_ms,
		 consumed_model_calls,consumed_tool_calls,consumed_tokens,reserved_at)
		VALUES ('child',4,8,20000,4000,100000,120000,2,3,1000,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)

	// Apply migration 23.
	for _, migration := range migrations.Sorted() {
		if migration.Version != 23 {
			continue
		}
		_, err = db.Exec(migration.SQL)
		require.NoError(t, err)
	}

	var groups, items, budgets int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_groups`).Scan(&groups))
	assert.Equal(t, 2, groups)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_items`).Scan(&items))
	assert.Equal(t, 2, items)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_budgets WHERE run_id='child'`).Scan(&budgets))
	assert.Equal(t, 1, budgets)
	var consumedTokens int64
	require.NoError(t, db.QueryRow(`SELECT consumed_tokens FROM run_budgets WHERE run_id='child'`).Scan(&consumedTokens))
	assert.EqualValues(t, 1000, consumedTokens)

	var builtin int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM policy_profiles WHERE id='builtin-hosted-delegation-v1'
		AND kind='delegation' AND status='active'`).Scan(&builtin))
	assert.Equal(t, 1, builtin)
	var sessionPolicy string
	require.NoError(t, db.QueryRow(`SELECT delegation_policy_profile_id FROM sessions WHERE id='s1'`).Scan(&sessionPolicy))
	assert.Equal(t, "builtin-hosted-delegation-v1", sessionPolicy)

	// Schema surfaces exist.
	for table, column := range map[string]string{
		"delegation_root_budgets": "",
		"run_budgets":             "root_reconciled_at",
	} {
		if column == "" {
			var exists int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists))
			assert.Equal(t, 1, exists, "table %s", table)
			continue
		}
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count))
		assert.Equal(t, 1, count, "%s.%s", table, column)
	}

	rows, err := db.Query("PRAGMA foreign_key_check")
	require.NoError(t, err)
	defer rows.Close()
	assert.False(t, rows.Next(), "migration 23 must leave no broken foreign keys")
}
