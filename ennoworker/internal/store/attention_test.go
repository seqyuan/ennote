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

func setupAttentionProject(t *testing.T) (*store.DelegationRepo, *store.AttentionRepo, *store.RunRepo, string, string) {
	t.Helper()
	delegations, runs, submission := setupRootBudgetParent(t, "attention")
	attention := &store.AttentionRepo{DB: delegations.DB}
	return delegations, attention, runs, submission.Run.SessionID, submission.Run.ID
}

func parentRunID(t *testing.T, delegations *store.DelegationRepo, sessionID string) string {
	t.Helper()
	var runID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT id FROM agent_runs WHERE session_id=? AND parent_run_id IS NULL AND status='running' LIMIT 1`,
		sessionID).Scan(&runID))
	return runID
}

func TestAttentionProjectsDelegationApprovalAndCompletion(t *testing.T) {
	delegations, attention, _, sessionID, _ := setupAttentionProject(t)
	ctx := context.Background()
	second := halfExplorerItem()
	second.Name = "fail"
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: parentRunID(t, delegations, sessionID), ParentToolCallID: "att-call",
		Strategy: domain.DelegationStrategyParallel,
		Items:    []store.CreateDelegationItemInput{explorerItem(), second},
	}, sessionID)
	require.NoError(t, err)
	require.Len(t, children, 2)
	for index, child := range children {
		_, err = (&store.RunRepo{DB: delegations.DB}).Claim(ctx, child.ID)
		require.NoError(t, err)
		if index == 0 {
			require.NoError(t, (&store.RunRepo{DB: delegations.DB}).FinalizeChildSuccess(ctx, child.ID, domain.RunOutput{
				Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
					Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "ok"}}}},
				Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "ok"},
			}))
		} else {
			_, _, failErr := (&store.RunRepo{DB: delegations.DB}).FinalizeChildFailure(ctx, child.ID, "provider_unavailable", "boom")
			require.NoError(t, failErr)
		}
	}

	// Budget-increase retry projects an approval-required attention item.
	failedItemID := items[1].ID
	_, _, approval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "attention-retry",
		BudgetOverrides: map[string]domain.BudgetCeilingJSON{
			failedItemID: {MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, approval)

	items2, err := attention.ListAttention(ctx, projectIDFor(t, delegations, sessionID), "", "pending", 10)
	require.NoError(t, err)
	var item domain.AttentionItem
	for _, entry := range items2 {
		if entry.SourceKind == domain.AttentionSourceDelegationApproval {
			item = entry
		}
	}
	assert.Equal(t, domain.AttentionApprovalRequired, item.Kind)
	assert.True(t, item.RequiresAction)
	assert.Equal(t, domain.AttentionSourceDelegationApproval, item.SourceKind)
	require.NotNil(t, item.Action)
	assert.Equal(t, "delegation_approval", item.Action.Kind)
	assert.Equal(t, approval.ID, item.Action.ApprovalID)

	// The display payload never leaks private material.
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	for _, forbidden := range []string{"rolePrompt", "credential", "apiKey", "transcript", "assignment"} {
		assert.NotContains(t, string(raw), forbidden)
	}

	// Deciding the approval resolves its attention row in the same transaction.
	_, _, err = (&store.DelegationApprovalRepo{DB: delegations.DB}).Decide(ctx, approval.ID, domain.DecisionApproved, "att-decide")
	require.NoError(t, err)
	items3, err := attention.ListAttention(ctx, projectIDFor(t, delegations, sessionID), "", "resolved", 10)
	require.NoError(t, err)
	require.Len(t, items3, 1)
	assert.Equal(t, "resolved", items3[0].Status)

	// Completion projected a notification.
	items4, err := attention.ListAttention(ctx, projectIDFor(t, delegations, sessionID), "", "pending", 10)
	require.NoError(t, err)
	var completionNotification int
	for _, entry := range items4 {
		if entry.SourceKind == domain.AttentionSourceDelegationCompletion {
			completionNotification++
		}
	}
	assert.Greater(t, completionNotification, 0)
}

func TestAttentionNeedsInputCannotBeDismissed(t *testing.T) {
	delegations, _, _, itemID := settleNeedsInputGroup(t)
	attention := &store.AttentionRepo{DB: delegations.DB}
	ctx := context.Background()
	var sessionID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT session_id FROM agent_runs
		WHERE id=(SELECT parent_run_id FROM delegation_groups LIMIT 1)`).Scan(&sessionID))
	projectID := projectIDFor(t, delegations, sessionID)

	// Finalizing needs_input projects an action-typed item in the same transaction.
	items, err := attention.ListAttention(ctx, projectID, "", "pending", 10)
	require.NoError(t, err)
	var inputItem *domain.AttentionItem
	for index := range items {
		if items[index].SourceKind == domain.AttentionSourceDelegationItem {
			inputItem = &items[index]
		}
	}
	require.NotNil(t, inputItem)
	require.NotNil(t, inputItem.Action)
	assert.Equal(t, "delegation_input", inputItem.Action.Kind)
	assert.Equal(t, itemID, inputItem.Action.ItemID)

	// Dismissing an action item fails.
	err = attention.Dismiss(ctx, inputItem.ID)
	require.Error(t, err)
}

func TestAttentionProjectsAndResolvesToolApproval(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	ctx := context.Background()
	request, err := approvals.Suspend(ctx, submission.Run.ID, 1, 2, "attention-tool",
		json.RawMessage(`{"version":1}`), approvalItems(), nil)
	require.NoError(t, err)
	var projectID string
	require.NoError(t, runs.DB.QueryRow(`SELECT project_id FROM sessions WHERE id=?`,
		submission.Run.SessionID).Scan(&projectID))
	attention := &store.AttentionRepo{DB: runs.DB}
	items, err := attention.ListAttention(ctx, projectID, "", "pending", 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, domain.AttentionSourceToolApproval, items[0].SourceKind)
	require.NotNil(t, items[0].Action)
	assert.Equal(t, request.ID, items[0].Action.ApprovalID)

	_, err = approvals.Decide(ctx, request.ID, domain.DecisionRejected, "attention-tool-reject", nil)
	require.NoError(t, err)
	resolved, err := attention.ListAttention(ctx, projectID, "", "resolved", 10)
	require.NoError(t, err)
	require.Len(t, resolved, 1)
}

func TestAttentionNotificationDismissalDoesNotMutateSource(t *testing.T) {
	delegations, attention, _, sessionID, _ := setupAttentionProject(t)
	ctx := context.Background()
	projectID := projectIDFor(t, delegations, sessionID)

	require.NoError(t, attention.ProjectAttention(ctx, projectID, sessionID,
		domain.AttentionSourceDelegationCompletion, "handle-1", 0, domain.AttentionDelegationCompleted, false,
		map[string]any{"kind": "completed"}, &domain.AttentionAction{Kind: "none"}))
	items, err := attention.ListAttention(ctx, projectID, "", "pending", 10)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, attention.Dismiss(ctx, items[0].ID))
	items2, err := attention.ListAttention(ctx, projectID, "", "dismissed", 10)
	require.NoError(t, err)
	require.Len(t, items2, 1)
}

func TestAttentionDedupeAndRebuild(t *testing.T) {
	delegations, _, submission, _ := settleBackgroundGroup(t)
	attention := &store.AttentionRepo{DB: delegations.DB}
	sessionID := submission.Run.SessionID
	ctx := context.Background()
	projectID := projectIDFor(t, delegations, sessionID)
	handle, err := delegations.HandleForGroup(ctx, func() string {
		var groupID string
		require.NoError(t, delegations.DB.QueryRow(`SELECT group_id FROM delegation_handles LIMIT 1`).Scan(&groupID))
		return groupID
	}())
	require.NoError(t, err)

	// Same source key twice is idempotent.
	for i := 0; i < 2; i++ {
		require.NoError(t, attention.ProjectAttention(ctx, projectID, sessionID,
			domain.AttentionSourceDelegationCompletion, handle.ID, 0, domain.AttentionDelegationCompleted, false,
			map[string]any{"kind": "completed"}, &domain.AttentionAction{Kind: "none"}))
	}
	items, err := attention.ListAttention(ctx, projectID, "", "pending", 10)
	require.NoError(t, err)
	completions := 0
	for _, entry := range items {
		if entry.SourceID == handle.ID {
			completions++
		}
	}
	assert.Equal(t, 1, completions)

	// Rebuild reconstructs missing items from source facts.
	_, err = delegations.DB.Exec(`DELETE FROM attention_items`)
	require.NoError(t, err)
	rebuilt, err := attention.RebuildAttention(ctx, 50)
	require.NoError(t, err)
	assert.Greater(t, rebuilt, 0)
	items2, err := attention.ListAttention(ctx, projectID, "", "pending", 50)
	require.NoError(t, err)
	assert.NotEmpty(t, items2)
}

func projectIDFor(t *testing.T, delegations *store.DelegationRepo, sessionID string) string {
	t.Helper()
	var projectID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT project_id FROM sessions WHERE id=?`, sessionID).Scan(&projectID))
	return projectID
}
