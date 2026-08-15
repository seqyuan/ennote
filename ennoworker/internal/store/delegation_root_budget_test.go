package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRootBudgetParent creates a running top-level Host Run with a frozen
// effective config (file-native V2 stack), which must carry the
// DelegationPolicySnapshot and create the root budget ledger row.
func setupRootBudgetParent(t *testing.T, requestID string) (*store.DelegationRepo, *store.RunRepo, *domain.TurnSubmission) {
	t.Helper()
	fixture := newFileRunFixture(t, requestID)
	ctx := context.Background()
	runs := fixture.Runs
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: fixture.SessionID, ClientRequestID: requestID, Text: "run",
	})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	parentRun, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, parentRun)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Delegation, "top-level Host Run must freeze a delegation policy snapshot")
	return fixture.Delegations(), runs, submission
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
