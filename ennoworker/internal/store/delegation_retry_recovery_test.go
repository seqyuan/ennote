package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runStartupRecovery mimics the production startup sequence.
func runStartupRecovery(t *testing.T, delegations *store.DelegationRepo, runs *store.RunRepo) []string {
	t.Helper()
	queued, err := runs.RecoverActive(context.Background())
	require.NoError(t, err)
	_, err = delegations.ReapOrphans(context.Background())
	require.NoError(t, err)
	_, err = delegations.RecoverDelegation(context.Background())
	require.NoError(t, err)
	return queued
}

func TestRecoveryKeepsActiveRetryGenerationRunning(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	_, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "recovery-active",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)

	queued := runStartupRecovery(t, delegations, runs)
	// The queued retry child must remain enqueueable.
	assert.Contains(t, queued, children[0].ID)

	// The generation stays running because its attempt is still queued.
	var generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=1`, group.ID).Scan(&generationStatus))
	assert.Equal(t, "running", generationStatus)

	// A second recovery pass does not duplicate or rewrite anything.
	runStartupRecovery(t, delegations, runs)
	var attempts int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attempts))
	assert.Equal(t, 1, attempts)
}

func TestRecoverySettlesGenerationAfterAllAttemptsTerminal(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	generation, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "recovery-settle",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)

	// Crash after the retry child succeeded but before generation settlement.
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "retried ok"},
	}))

	runStartupRecovery(t, delegations, runs)
	var generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=1`, group.ID).Scan(&generationStatus))
	assert.Equal(t, "settled", generationStatus)
	_ = generation
}

func TestRecoveryInterruptedRetryChildReconcilesAndSettles(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	_, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "recovery-interrupt",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)

	// Worker restart interrupts the running retry child.
	require.NoError(t, runs.Interrupt(ctx, children[0].ID, "worker restarted"))
	runStartupRecovery(t, delegations, runs)

	var attemptStatus, generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_item_attempts WHERE child_run_id=?`,
		children[0].ID).Scan(&attemptStatus))
	assert.Equal(t, "interrupted", attemptStatus)
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=1`, group.ID).Scan(&generationStatus))
	assert.Equal(t, "settled", generationStatus)

	// Root reservation was released.
	ledger := readRootLedger(t, delegations.DB, group.ParentRunID)
	assert.EqualValues(t, 0, ledger["active_children"])
}

func TestRecoveryNeverRewritesGenerationZero(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	var originalResult string
	require.NoError(t, delegations.DB.QueryRow(`SELECT result_json FROM delegation_items WHERE id=?`,
		failedItemID).Scan(&originalResult))

	_, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "recovery-no-overwrite",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)

	runStartupRecovery(t, delegations, runs)

	// Generation 0's folded result and item status stay byte-for-byte.
	var after string
	require.NoError(t, delegations.DB.QueryRow(`SELECT result_json FROM delegation_items WHERE id=?`,
		failedItemID).Scan(&after))
	assert.Equal(t, originalResult, after)
	var gen0Status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=0`, group.ID).Scan(&gen0Status))
	assert.Equal(t, "settled", gen0Status)
}

func TestRecoveryApprovedGenerationWithoutChildrenFailsClosed(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	approvals := &store.DelegationApprovalRepo{DB: delegations.DB}

	generation, _, approval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "recovery-approval",
		BudgetOverrides: map[string]domain.BudgetCeilingJSON{
			failedItemID: {MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, approval)
	_ = generation

	// Reject the approval, then crash before the cursor rewind is observed:
	// recovery must terminalize the awaiting generation and rewind.
	_, _, err = approvals.Decide(ctx, approval.ID, domain.DecisionRejected, "recovery-reject")
	require.NoError(t, err)
	runStartupRecovery(t, delegations, runs)

	var generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=1`, group.ID).Scan(&generationStatus))
	assert.Equal(t, "failed", generationStatus)
	var cursor int
	require.NoError(t, delegations.DB.QueryRow(`SELECT current_generation FROM delegation_groups WHERE id=?`,
		group.ID).Scan(&cursor))
	assert.Equal(t, 0, cursor)
}
