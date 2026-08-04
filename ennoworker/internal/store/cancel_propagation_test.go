package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupParentWithChild(t *testing.T) (*store.RunRepo, *store.DelegationRepo, *domain.TurnSubmission, string) {
	t.Helper()
	repo, submission := setupSubmittedRun(t, "cancel-parent")
	ctx := context.Background()
	delegations := &store.DelegationRepo{DB: repo.DB}
	runs := &store.RunRepo{DB: repo.DB}
	_, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "c", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: submission.Run.ID, ItemID: items[0].ID, SessionID: submission.Run.SessionID,
	})
	require.NoError(t, err)
	return runs, delegations, submission, child.ID
}

func TestParentCannotSucceedWithOwnedChildren(t *testing.T) {
	runs, _, _, childID := setupParentWithChild(t)
	ctx := context.Background()
	_, err := runs.Claim(ctx, childID)
	require.NoError(t, err)
	err = runs.Succeed(ctx, runsMustGetParentID(t, runs, childID))
	require.Error(t, err, "parent must not succeed while a child is running")
	err = runs.FinalizeSuccess(ctx, runsMustGetParentID(t, runs, childID), domain.RunOutput{Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "x"}},
	}}})
	require.Error(t, err, "parent must not finalize while a child is running")
}

func runsMustGetParentID(t *testing.T, runs *store.RunRepo, childID string) string {
	t.Helper()
	parent, err := runs.ParentOfRun(context.Background(), childID)
	require.NoError(t, err)
	require.NotEmpty(t, parent)
	return parent
}

func TestParentCancelPropagatesToChildrenAndGroup(t *testing.T) {
	runs, delegations, _, childID := setupParentWithChild(t)
	ctx := context.Background()
	parentID := runsMustGetParentID(t, runs, childID)

	require.NoError(t, runs.Cancel(ctx, parentID))

	var childStatus, parentStatus string
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, childID).Scan(&childStatus))
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, parentID).Scan(&parentStatus))
	assert.Equal(t, "cancelled", childStatus)
	assert.Equal(t, "cancelled", parentStatus)

	items, err := delegations.ListItems(ctx, "does-not-matter")
	_ = items
	_ = err
	// The group and its item must be cancelled via the parent relationship.
	var itemStatus string
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM delegation_items WHERE child_run_id=?`, childID).Scan(&itemStatus))
	assert.Equal(t, "cancelled", itemStatus)
	var groupStatus string
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM delegation_groups WHERE parent_run_id=?`, parentID).Scan(&groupStatus))
	assert.Equal(t, "cancelled", groupStatus)
}
