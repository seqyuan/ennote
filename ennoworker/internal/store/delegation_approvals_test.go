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

// requestBudgetApproval creates a settled mixed group and issues a retry whose
// budget increase is authorization-required, returning the approval.
func requestBudgetApproval(t *testing.T) (*store.DelegationRepo, *store.DelegationApprovalRepo, *domain.DelegationGroup, string, *domain.DelegationApprovalRequest) {
	t.Helper()
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	generation, children, approval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "approval-request-1",
		BudgetOverrides: map[string]domain.BudgetCeilingJSON{
			failedItemID: {MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		},
	})
	require.NoError(t, err)
	require.Nil(t, children)
	require.NotNil(t, approval)
	require.NotNil(t, generation)
	assert.Equal(t, domain.DelegationGenerationAwaitingAuthorization, generation.Status)
	return delegations, &store.DelegationApprovalRepo{DB: delegations.DB}, group, failedItemID, approval
}

func TestApprovalRequestPreviewIsBounded(t *testing.T) {
	_, _, _, _, approval := requestBudgetApproval(t)
	// The approval preview carries only bounded facts: no Role prompt,
	// credentials, transcript, or full assignment.
	raw, err := json.Marshal(approval)
	require.NoError(t, err)
	text := string(raw)
	for _, forbidden := range []string{"rolePrompt", "systemPrompt", "apiKey", "credential", "transcript", "assignment"} {
		assert.NotContains(t, text, forbidden, "approval preview leaks %s", forbidden)
	}
	var items []map[string]any
	require.NoError(t, json.Unmarshal(approval.ItemsJSON, &items))
	require.Len(t, items, 1)
	assert.NotEmpty(t, items[0]["itemId"])
	assert.NotEmpty(t, items[0]["retryOfAttemptId"])
	assert.NotEmpty(t, items[0]["budget"])
}

func TestApprovalCreatesChildrenFromFrozenSnapshot(t *testing.T) {
	delegations, approvals, group, failedItemID, approval := requestBudgetApproval(t)
	ctx := context.Background()

	decided, children, err := approvals.Decide(ctx, approval.ID, domain.DecisionApproved, "decision-1")
	require.NoError(t, err)
	assert.Equal(t, "approved", decided.Status)
	require.Len(t, children, 1)

	// The new attempt references the frozen retry-of attempt and keeps the
	// approval-visible budget.
	var attemptID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT id FROM delegation_item_attempts
		WHERE item_id=? AND generation=1`, failedItemID).Scan(&attemptID))
	var retryOf, reservedBudget string
	require.NoError(t, delegations.DB.QueryRow(`SELECT retry_of_attempt_id,reserved_budget_json
		FROM delegation_item_attempts WHERE id=?`, attemptID).Scan(&retryOf, &reservedBudget))
	assert.NotEmpty(t, retryOf)
	assert.JSONEq(t, `{"maxModelCalls":4,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}`, reservedBudget)

	// Generation advanced to running; the frozen item budget was not rewritten.
	var generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=1`, group.ID).Scan(&generationStatus))
	assert.Equal(t, "running", generationStatus)
	var itemBudget string
	require.NoError(t, delegations.DB.QueryRow(`SELECT budget_json FROM delegation_items WHERE id=?`, failedItemID).Scan(&itemBudget))
	assert.JSONEq(t, `{"maxModelCalls":2,"maxToolCalls":4,"maxTotalTokens":10000,"maxOutputTokens":2000,"maxCostUsdMicros":50000,"maxWallTimeMs":60000}`, itemBudget)
}

func TestApprovalRejectionRewindsWithoutTouchingPreviousGeneration(t *testing.T) {
	delegations, approvals, group, failedItemID, approval := requestBudgetApproval(t)
	ctx := context.Background()

	decided, children, err := approvals.Decide(ctx, approval.ID, domain.DecisionRejected, "decision-reject")
	require.NoError(t, err)
	assert.Equal(t, "rejected", decided.Status)
	assert.Nil(t, children)

	// Generation terminal; cursor rewound to generation 0.
	var generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=1`, group.ID).Scan(&generationStatus))
	assert.Equal(t, "failed", generationStatus)
	var cursor int
	require.NoError(t, delegations.DB.QueryRow(`SELECT current_generation FROM delegation_groups WHERE id=?`, group.ID).Scan(&cursor))
	assert.Equal(t, 0, cursor)

	// Previous selected generation facts are untouched and still retryable.
	var gen0Status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=0`, group.ID).Scan(&gen0Status))
	assert.Equal(t, "settled", gen0Status)
	_, _, _, err = delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "retry-after-reject",
	})
	require.NoError(t, err, "a rejected retry must allow a fresh retry")
}

func TestApprovalDecisionFirstWriterWinsAndConflicts(t *testing.T) {
	delegations, approvals, group, _, approval := requestBudgetApproval(t)
	ctx := context.Background()

	// First decision wins.
	decided, children, err := approvals.Decide(ctx, approval.ID, domain.DecisionApproved, "decision-win")
	require.NoError(t, err)
	assert.Equal(t, "approved", decided.Status)
	require.Len(t, children, 1)

	// Same decision + same request id is idempotent.
	replayed, replayChildren, err := approvals.Decide(ctx, approval.ID, domain.DecisionApproved, "decision-win")
	require.NoError(t, err)
	assert.Equal(t, "approved", replayed.Status)
	require.Len(t, replayChildren, 1)
	assert.Equal(t, children[0].ID, replayChildren[0].ID)

	// Opposite decision conflicts and creates nothing.
	_, _, err = approvals.Decide(ctx, approval.ID, domain.DecisionRejected, "decision-lose")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDelegationApprovalConflict)
	var attempts int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attempts))
	assert.Equal(t, 1, attempts)
	var cursor int
	require.NoError(t, delegations.DB.QueryRow(`SELECT current_generation FROM delegation_groups WHERE id=?`, group.ID).Scan(&cursor))
	assert.Equal(t, 1, cursor)
}

func TestApprovalFailsClosedOnRoleKillSwitch(t *testing.T) {
	delegations, approvals, _, _, approval := requestBudgetApproval(t)
	ctx := context.Background()

	// V2 kill switch: the frozen Role meta is the only authority. Simulate a
	// revoked identity by clearing the item's frozen meta; Decide must fail
	// closed and materialize nothing.
	_, err := delegations.DB.Exec(`UPDATE delegation_items SET role_meta_json='{}'
		WHERE group_id=(SELECT group_id FROM delegation_approval_requests WHERE id=?)`,
		approval.ID)
	require.NoError(t, err)
	_, children, err := approvals.Decide(ctx, approval.ID, domain.DecisionApproved, "decision-kill")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
	assert.Nil(t, children)
	// Approval stays pending; nothing was materialized.
	var status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_approval_requests WHERE id=?`,
		approval.ID).Scan(&status))
	assert.Equal(t, "pending", status)
	var attempts int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attempts))
	assert.Zero(t, attempts)
}

func TestApprovalCannotExceedRootBudget(t *testing.T) {
	delegations, approvals, _, _, approval := requestBudgetApproval(t)
	ctx := context.Background()

	// Shrink the root envelope so materialization cannot fit.
	_, err := delegations.DB.Exec(`UPDATE delegation_root_budgets SET max_model_calls=0`)
	require.NoError(t, err)
	_, children, err := approvals.Decide(ctx, approval.ID, domain.DecisionApproved, "decision-nobudget")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDelegationBudgetExceeded)
	assert.Nil(t, children)
	var status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_approval_requests WHERE id=?`,
		approval.ID).Scan(&status))
	assert.Equal(t, "pending", status)
}
