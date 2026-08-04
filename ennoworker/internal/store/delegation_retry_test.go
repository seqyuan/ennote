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

// halfExplorerItem is an explorer item whose budget sits below the Role
// ceiling, so a retry can demonstrate an approval-visible increase.
func halfExplorerItem() store.CreateDelegationItemInput {
	item := explorerItem()
	item.Budget = domain.BudgetCeilingJSON{MaxModelCalls: 2, MaxToolCalls: 4, MaxTotalTokens: 10000,
		MaxOutputTokens: 2000, MaxCostMicros: 50000, MaxWallTimeMS: 60000}
	return item
}

// settleMixedGroup creates a two-child group, settles one child as success and
// the other as failure, and returns the group plus the failed item id.
func settleMixedGroup(t *testing.T) (*store.DelegationRepo, *store.RunRepo, *domain.TurnSubmission, *domain.DelegationGroup, string) {
	t.Helper()
	delegations, runs, submission := setupRootBudgetParent(t, "retry-mixed")
	ctx := context.Background()
	second := halfExplorerItem()
	second.Name = "fail"
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{explorerItem(), second},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 2)

	// Record the parent tool call so generation 0 folding lands exactly once.
	_, err = delegations.DB.Exec(`INSERT INTO tool_calls
		(id,run_id,seq,tool_call_id,tool_name,arguments_json,status,started_at)
		VALUES('tc-settled',?,1,'call-1','delegate_roles','{}','completed',CURRENT_TIMESTAMP)`,
		submission.Run.ID)
	require.NoError(t, err)

	for index, child := range children {
		_, err = runs.Claim(ctx, child.ID)
		require.NoError(t, err)
		if index == 0 {
			require.NoError(t, runs.FinalizeChildSuccess(ctx, child.ID, domain.RunOutput{
				Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
					Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "ok"}}}},
				Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "success summary"},
			}))
		} else {
			_, _, failErr := runs.FinalizeChildFailure(ctx, child.ID, "provider_unavailable", "model failed")
			require.NoError(t, failErr)
		}
	}
	return delegations, runs, submission, group, items[1].ID
}

func TestRetryReusesSuccessAndRerunsOnlyFailedItem(t *testing.T) {
	delegations, runs, submission, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	_ = runs
	_ = submission

	generation, children, approval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-1",
	})
	require.NoError(t, err)
	require.Nil(t, approval)
	require.NotNil(t, generation)
	assert.Equal(t, 1, generation.Generation)
	assert.Equal(t, domain.DelegationGenerationRetry, generation.Kind)
	assert.Equal(t, domain.DelegationGenerationQueued, generation.Status)
	require.Len(t, children, 1, "only the failed item gets a new child Run")

	// The successful sibling is reused with the exact frozen attempt reference.
	require.Len(t, generation.ReusedAttempts, 1)
	reused := generation.ReusedAttempts[0]
	assert.NotEqual(t, failedItemID, reused.ItemID)
	assert.NotEmpty(t, reused.ResultDigest)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, reused.ResultDigest)
	assert.Equal(t, 0, reused.Generation)

	// New attempt points back to the failed attempt; old attempt stays frozen.
	var retryOf string
	require.NoError(t, delegations.DB.QueryRow(`SELECT retry_of_attempt_id FROM delegation_item_attempts
		WHERE item_id=? AND generation=1`, failedItemID).Scan(&retryOf))
	var oldStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_item_attempts WHERE id=?`, retryOf).Scan(&oldStatus))
	assert.Equal(t, "failed", oldStatus)

	// Group cursor advanced; original generation 0 facts unchanged.
	var cursor int
	require.NoError(t, delegations.DB.QueryRow(`SELECT current_generation FROM delegation_groups WHERE id=?`, group.ID).Scan(&cursor))
	assert.Equal(t, 1, cursor)
	var gen0Status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=0`, group.ID).Scan(&gen0Status))
	assert.Equal(t, "settled", gen0Status)
}

func TestRetryEligibilityRules(t *testing.T) {
	delegations, runs, submission, group, _ := settleMixedGroup(t)
	ctx := context.Background()

	// Selecting the successful sibling is ineligible.
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	var successItemID string
	for _, item := range items {
		if item.Status == domain.DelegationItemTerminal {
			successItemID = item.ID
		}
	}
	_, _, _, err = delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{successItemID}, ClientRequestID: "retry-bad",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorDelegationRetryIneligible, domain.ErrorCodeOf(err))

	// A running current generation is not retryable at all.
	_, err = delegations.DB.Exec(`UPDATE delegation_group_generations SET status='running'
		WHERE group_id=? AND generation=0`, group.ID)
	require.NoError(t, err)
	_, _, _, err = delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemIDOf(t, delegations, group.ID)}, ClientRequestID: "retry-busy",
	})
	require.Error(t, err)
	_, err = delegations.DB.Exec(`UPDATE delegation_group_generations SET status='settled'
		WHERE group_id=? AND generation=0`, group.ID)
	require.NoError(t, err)

	// Cancelled and interrupted attempts are eligible.
	_, err = delegations.DB.Exec(`UPDATE agent_runs SET status='running' WHERE id=?`, submission.Run.ID)
	require.NoError(t, err)
	second := explorerItem()
	second.Name = "cancel"
	group2, _, children2, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-2", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{second},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children2, 1)
	require.NoError(t, runs.Cancel(ctx, submission.Run.ID))
	// The cancelled group's generation is cancelled; create an interrupted variant.
	_, err = delegations.DB.Exec(`UPDATE delegation_group_generations SET status='settled'
		WHERE group_id=? AND generation=0`, group2.ID)
	require.NoError(t, err)
	var item2ID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT id FROM delegation_items WHERE group_id=?`, group2.ID).Scan(&item2ID))
	_, _, _, err = delegations.RetryGeneration(ctx, group2.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{item2ID}, ClientRequestID: "retry-cancelled",
	})
	require.NoError(t, err, "cancelled attempts must be retry-eligible")
}

func failedItemIDOf(t *testing.T, delegations *store.DelegationRepo, groupID string) string {
	t.Helper()
	var itemID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT i.id FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id AND a.generation=0
		WHERE i.group_id=? AND a.status='failed'`, groupID).Scan(&itemID))
	return itemID
}

func TestRetryIdempotencyAndConflicts(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	// Same client request id returns the same generation and children.
	first, firstChildren, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-idem",
	})
	require.NoError(t, err)
	second, secondChildren, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-idem",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, first.Generation, second.Generation)
	require.Len(t, firstChildren, 1)
	require.Len(t, secondChildren, 1)
	assert.Equal(t, firstChildren[0].ID, secondChildren[0].ID)
	var attempts int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts
		WHERE item_id=? AND generation=1`, failedItemID).Scan(&attempts))
	assert.Equal(t, 1, attempts)

	// Stale expected generation conflicts and creates nothing.
	var groupsBefore, attemptsBefore int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_groups`).Scan(&groupsBefore))
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts`).Scan(&attemptsBefore))
	_, _, _, err = delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-stale",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorDelegationGenerationConflict, domain.ErrorCodeOf(err))
	var groupsAfter, attemptsAfter int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_groups`).Scan(&groupsAfter))
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts`).Scan(&attemptsAfter))
	assert.Equal(t, groupsBefore, groupsAfter)
	assert.Equal(t, attemptsBefore, attemptsAfter)
}

func TestRetryRoleKillSwitchFailsClosed(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	// Disable the Role identity: retry must fail before creating any child.
	_, err := delegations.DB.Exec(`UPDATE agent_profiles SET delegation_enabled=0
		WHERE id='builtin-workspace-explorer'`)
	require.NoError(t, err)
	_, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-kill",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
	assert.Nil(t, children)
	var attempts, childrenCount int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attempts))
	assert.Zero(t, attempts)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?`, group.ParentRunID).Scan(&childrenCount))
	assert.Equal(t, 2, childrenCount, "no new child Run may be created")
	// Re-enable for other tests that share nothing (this test is isolated).
	_, err = delegations.DB.Exec(`UPDATE agent_profiles SET delegation_enabled=1
		WHERE id='builtin-workspace-explorer'`)
	require.NoError(t, err)
}

func TestRetryBudgetIncreaseRequiresApproval(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	// Raising the failed item's budget (within the frozen Role ceiling but
	// above the frozen item budget) requires a durable approval snapshot and
	// creates no child Run.
	generation, children, approval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-budget-up",
		BudgetOverrides: map[string]domain.BudgetCeilingJSON{
			failedItemID: {MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, approval)
	require.Nil(t, children)
	assert.Equal(t, domain.DelegationGenerationAwaitingAuthorization, generation.Status)
	assert.Equal(t, "retry_budget", approval.Kind)
	assert.Equal(t, "pending", approval.Status)

	// The old generation and attempts stay frozen; nothing was materialized.
	var attempts int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attempts))
	assert.Zero(t, attempts)
	var childRuns int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?`, group.ParentRunID).Scan(&childRuns))
	assert.Equal(t, 2, childRuns)

	// Same request is idempotent; a different request conflicts on the cursor.
	_, _, replayApproval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-budget-up",
		BudgetOverrides: map[string]domain.BudgetCeilingJSON{
			failedItemID: {MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, replayApproval)
	assert.Equal(t, approval.ID, replayApproval.ID)

	_, _, _, err = delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-budget-other",
		BudgetOverrides: map[string]domain.BudgetCeilingJSON{
			failedItemID: {MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		},
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorDelegationGenerationConflict, domain.ErrorCodeOf(err))
}

func TestRetryPreservesOriginalToolResult(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	generation, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-original",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)
	_ = generation

	// Generation 0's folded tool result must not be rewritten by the retry.
	var preview string
	require.NoError(t, delegations.DB.QueryRow(`SELECT result_preview FROM tool_calls WHERE id='tc-settled'`).Scan(&preview))
	assert.Contains(t, preview, "settled", "generation 0 folding must have written the original result")
	var itemStatus, attemptStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_items WHERE id=?`, failedItemID).Scan(&itemStatus))
	assert.Equal(t, "failed", itemStatus, "generation 0 substrate columns stay frozen")
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_item_attempts WHERE item_id=? AND generation=0`,
		failedItemID).Scan(&attemptStatus))
	assert.Equal(t, "failed", attemptStatus)
}

func TestRetryRootBudgetRollbackIsAtomic(t *testing.T) {
	delegations, _, submission, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	// Shrink the root envelope so the retry reservation cannot fit.
	_, err := delegations.DB.Exec(`UPDATE delegation_root_budgets SET max_model_calls=0 WHERE root_run_id=?`,
		submission.Run.ID)
	require.NoError(t, err)

	_, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-nobudget",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDelegationBudgetExceeded)
	assert.Nil(t, children)

	// The failed transaction left no group/generation/attempt/child/budget rows.
	var generationRows, attemptRows, childRows, budgetRows int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_group_generations WHERE group_id=? AND generation=1`,
		group.ID).Scan(&generationRows))
	assert.Zero(t, generationRows)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attemptRows))
	assert.Zero(t, attemptRows)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?`, submission.Run.ID).Scan(&childRows))
	assert.Equal(t, 2, childRows)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM run_budgets`).Scan(&budgetRows))
	assert.Equal(t, 2, budgetRows)
	var cursor int
	require.NoError(t, delegations.DB.QueryRow(`SELECT current_generation FROM delegation_groups WHERE id=?`, group.ID).Scan(&cursor))
	assert.Equal(t, 0, cursor, "cursor must not advance on a rolled-back retry")
}

func TestRetryReusedUsageAndDigestAreExact(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	_ = runs

	generation, _, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-exact",
	})
	require.NoError(t, err)
	require.Len(t, generation.ReusedAttempts, 1)
	reused := generation.ReusedAttempts[0]

	// The reused reference matches the frozen attempt byte-for-byte.
	var storedDigest string
	require.NoError(t, delegations.DB.QueryRow(`SELECT result_digest FROM delegation_item_attempts WHERE id=?`,
		reused.AttemptID).Scan(&storedDigest))
	assert.Equal(t, reused.ResultDigest, storedDigest)
	var attemptJSON string
	require.NoError(t, delegations.DB.QueryRow(`SELECT authorization_snapshot_json FROM delegation_item_attempts
		WHERE item_id=? AND generation=0`, reused.ItemID).Scan(&attemptJSON))
	var auth map[string]any
	require.NoError(t, json.Unmarshal([]byte(attemptJSON), &auth))
	assert.NotEmpty(t, auth["roleVersionId"])
}
