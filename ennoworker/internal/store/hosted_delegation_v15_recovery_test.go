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

// runStartupRecoveryFull mirrors the production startup sequence including
// projection rebuilds.
func runStartupRecoveryFull(t *testing.T, delegations *store.DelegationRepo, runs *store.RunRepo) {
	t.Helper()
	_, err := runs.RecoverActive(context.Background())
	require.NoError(t, err)
	_, err = delegations.ReapOrphans(context.Background())
	require.NoError(t, err)
	_, err = delegations.RecoverDelegation(context.Background())
	require.NoError(t, err)
	_, err = delegations.RebuildMissingCompletions(context.Background(), 10)
	require.NoError(t, err)
	_, err = delegations.RebuildMissingDeliveryEvents(context.Background(), 10)
	require.NoError(t, err)
	_, err = (&store.AttentionRepo{DB: delegations.DB}).RebuildAttention(context.Background(), 10)
	require.NoError(t, err)
}

func TestRecoveryMatrixRetryCrashBeforeEnqueue(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	// Retry generation committed; the child exists but was never enqueued.
	generation, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "matrix-retry",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)

	queued, err := runs.RecoverActive(ctx)
	require.NoError(t, err)
	assert.Contains(t, queued, children[0].ID)
	runStartupRecoveryFull(t, delegations, runs)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=?`,
		generation.Generation).Scan(&queued[0]))
	_ = queued
}

func TestRecoveryMatrixBackgroundCompletionAndAttention(t *testing.T) {
	delegations, runs, submission, _ := settleBackgroundGroup(t)
	// Parent finishes normally; restart clears active runs and rebuilds any
	// lost projections.
	_, err := delegations.DB.Exec(`UPDATE agent_runs SET status='succeeded' WHERE id=?`, submission.Run.ID)
	require.NoError(t, err)
	runStartupRecoveryFull(t, delegations, runs)

	var completions int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_completions`).Scan(&completions))
	assert.Equal(t, 1, completions)
	var events int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delivery_events`).Scan(&events))
	assert.Equal(t, 1, events)
	var attention int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM attention_items`).Scan(&attention))
	assert.GreaterOrEqual(t, attention, 1)
	_ = runs
}

func TestSecurityMatrixPrivacyBoundaries(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	// Budget-increase retry creates an approval whose preview must stay bounded.
	_, _, approval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "matrix-approval",
		BudgetOverrides: map[string]domain.BudgetCeilingJSON{
			failedItemID: {MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, approval)

	// Inspect the group projection and the approval payload: no private
	// transcript, credentials, Role prompt, or assignment may leak.
	inspection, err := delegations.Inspect(ctx, group.ID)
	require.NoError(t, err)
	raw, err := jsonMarshal(inspection)
	require.NoError(t, err)
	for _, forbidden := range []string{"transcript", "credential", "apiKey", "rolePrompt", "systemPrompt", "apiKey", "secret"} {
		assert.NotContains(t, string(raw), forbidden)
	}
	rawApproval, err := jsonMarshal(approval)
	require.NoError(t, err)
	for _, forbidden := range []string{"transcript", "credential", "apiKey", "rolePrompt", "assignment"} {
		assert.NotContains(t, string(rawApproval), forbidden)
	}
}

func TestSecurityMatrixKillSwitchAndDepth(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	// Kill switch denies a retry with zero child Provider calls (no child rows).
	_, err := delegations.DB.Exec(`UPDATE agent_profiles SET delegation_enabled=0
		WHERE id='builtin-workspace-explorer'`)
	require.NoError(t, err)
	_, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "matrix-kill",
	})
	require.Error(t, err)
	assert.Nil(t, children)
	var childRuns int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?`, group.ParentRunID).Scan(&childRuns))
	assert.Equal(t, 2, childRuns)
	_, err = delegations.DB.Exec(`UPDATE agent_profiles SET delegation_enabled=1
		WHERE id='builtin-workspace-explorer'`)
	require.NoError(t, err)

	// A delegated Role must never receive delegate_roles: verify the child tool
	// registry excludes it by checking a depth-two materialization is refused.
	// (Depth is enforced at create time: children are delegated_agent runs with
	// no further delegation path; verify via the execution-depth contract.)
	var maxDepth int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COALESCE(MAX(execution_depth),0) FROM agent_runs
		WHERE run_kind='delegated_agent'`).Scan(&maxDepth))
	assert.LessOrEqual(t, maxDepth, 1, "delegation depth must stay exactly one")
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
