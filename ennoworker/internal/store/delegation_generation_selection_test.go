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

func finalizeRetrySuccess(t *testing.T, runs *store.RunRepo, child *domain.AgentRun, summary string) {
	t.Helper()
	ctx := context.Background()
	_, err := runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, child.ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: summary}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: summary},
	}))
}

func TestSelectiveRetryCompletionIncludesReusedSibling(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	_, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "logical-completion",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)
	finalizeRetrySuccess(t, runs, children[0], "retry succeeded")

	var resultJSON string
	require.NoError(t, delegations.DB.QueryRow(`SELECT result_json FROM delegation_completions
		WHERE generation=1`).Scan(&resultJSON))
	var result struct {
		Children []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"children"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultJSON), &result))
	require.Len(t, result.Children, 2)
	assert.Equal(t, "succeeded", result.Children[0].Status)
	assert.Equal(t, "succeeded", result.Children[1].Status)
}

func TestSecondSelectiveRetryResolvesEarlierReuse(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	_, firstChildren, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "logical-retry-1",
	})
	require.NoError(t, err)
	require.Len(t, firstChildren, 1)
	_, err = runs.Claim(ctx, firstChildren[0].ID)
	require.NoError(t, err)
	_, _, err = runs.FinalizeChildFailure(ctx, firstChildren[0].ID, "provider_unavailable", "still failed")
	require.NoError(t, err)

	generation, secondChildren, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 1, ItemIDs: []string{failedItemID}, ClientRequestID: "logical-retry-2",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, generation.Generation)
	require.Len(t, generation.ReusedAttempts, 1)
	require.Len(t, secondChildren, 1)
}

func TestFollowUpReusedSiblingAndLaterChildAssignment(t *testing.T) {
	delegations, runs, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	generation, retryChildren, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "logical-follow-retry",
	})
	require.NoError(t, err)
	finalizeRetrySuccess(t, runs, retryChildren[0], "retry ok")
	require.Len(t, generation.ReusedAttempts, 1)
	reused := generation.ReusedAttempts[0]

	followGeneration, child, err := delegations.FollowUp(ctx, reused.ItemID, domain.DelegationInputCommand{
		SourceAttemptID: reused.AttemptID, ExpectedGeneration: 1,
		Text: "expand the successful result", ClientRequestID: "logical-follow",
	})
	require.NoError(t, err)
	require.NotNil(t, child)
	assert.Equal(t, 2, followGeneration.Generation)
	assignment, err := delegations.AssignmentForChild(ctx, child.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"objective":"inspect the workspace"}`, string(assignment))
	claimed, err := runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Equal(t, "builtin-workspace-explorer-v3", resolved.Effective.Role.VersionID)
}

func TestReapOrphansPreservesLaterGenerationChildOfTerminalParent(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	_, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "later-parent-terminal",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = delegations.DB.Exec(`UPDATE agent_runs SET status='succeeded',finished_at=CURRENT_TIMESTAMP
		WHERE id=?`, group.ParentRunID)
	require.NoError(t, err)
	reaped, err := delegations.ReapOrphans(ctx)
	require.NoError(t, err)
	assert.NotContains(t, reaped, children[0].ID)
	var status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, children[0].ID).Scan(&status))
	assert.Equal(t, "queued", status)
}

func TestContinuationBudgetIncreaseAwaitsApproval(t *testing.T) {
	delegations, _, _, itemID := settleNeedsInputGroup(t)
	ctx := context.Background()
	sourceAttemptID := attemptIDForItemGeneration(t, delegations, itemID, 0)
	generation, child, err := delegations.ContinueNeedsInput(ctx, itemID, domain.DelegationInputCommand{
		SourceAttemptID: sourceAttemptID, ExpectedGeneration: 0, Text: "inspect src",
		Budget: &domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
			MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		ClientRequestID: "continuation-budget",
	})
	require.NoError(t, err)
	assert.Nil(t, child)
	assert.Equal(t, domain.DelegationGenerationAwaitingAuthorization, generation.Status)

	var approvalID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT id FROM delegation_approval_requests
		WHERE group_id=? AND generation=1 AND status='pending'`, generation.GroupID).Scan(&approvalID))
	approval, children, err := (&store.DelegationApprovalRepo{DB: delegations.DB}).Decide(
		ctx, approvalID, domain.DecisionApproved, "continuation-budget-approve")
	require.NoError(t, err)
	assert.Equal(t, "approved", approval.Status)
	require.Len(t, children, 1)
	var continuationCount int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_attempt_continuations
		WHERE source_attempt_id=?`, sourceAttemptID).Scan(&continuationCount))
	assert.Equal(t, 1, continuationCount)
}

func TestRejectedContinuationBudgetRestoresNeedsInputAttention(t *testing.T) {
	delegations, _, _, itemID := settleNeedsInputGroup(t)
	ctx := context.Background()
	sourceAttemptID := attemptIDForItemGeneration(t, delegations, itemID, 0)
	generation, child, err := delegations.ContinueNeedsInput(ctx, itemID, domain.DelegationInputCommand{
		SourceAttemptID: sourceAttemptID, ExpectedGeneration: 0, Text: "inspect src",
		Budget: &domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
			MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		ClientRequestID: "continuation-budget-reject",
	})
	require.NoError(t, err)
	assert.Nil(t, child)
	var approvalID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT id FROM delegation_approval_requests
		WHERE group_id=? AND generation=?`, generation.GroupID, generation.Generation).Scan(&approvalID))
	_, _, err = (&store.DelegationApprovalRepo{DB: delegations.DB}).Decide(
		ctx, approvalID, domain.DecisionRejected, "continuation-reject")
	require.NoError(t, err)
	var pending int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM attention_items
		WHERE source_kind='delegation_item' AND source_id=? AND source_generation=0 AND status='pending'`,
		itemID).Scan(&pending))
	assert.Equal(t, 1, pending)
}

func TestGenerationIdempotencyKeyRejectsDifferentPayload(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()
	_, _, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "digest-retry",
	})
	require.NoError(t, err)
	_, _, _, err = delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{"different-item"}, ClientRequestID: "digest-retry",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorDelegationGenerationConflict, domain.ErrorCodeOf(err))

	needs, _, _, needsItemID := settleNeedsInputGroup(t)
	sourceAttemptID := attemptIDForItemGeneration(t, needs, needsItemID, 0)
	_, _, err = needs.ContinueNeedsInput(ctx, needsItemID, domain.DelegationInputCommand{
		SourceAttemptID: sourceAttemptID, ExpectedGeneration: 0,
		Text: "first", ClientRequestID: "digest-continuation",
	})
	require.NoError(t, err)
	_, _, err = needs.ContinueNeedsInput(ctx, needsItemID, domain.DelegationInputCommand{
		SourceAttemptID: sourceAttemptID, ExpectedGeneration: 0,
		Text: "different", ClientRequestID: "digest-continuation",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorDelegationGenerationConflict, domain.ErrorCodeOf(err))
}
