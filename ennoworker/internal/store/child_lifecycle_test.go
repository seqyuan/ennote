package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
)

// TestChildLifecycleParentWake verifies the full single-child substrate flow:
// parent running -> group+child created -> child claimed -> child finalized
// with submit_result -> item folded -> group settled -> parent woken to queued.
func TestChildLifecycleParentWake(t *testing.T) {
	fixture := newFileRunFixture(t, "child-lifecycle")
	ctx := context.Background()
	runs := fixture.Runs
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: fixture.SessionID, ClientRequestID: "child-lifecycle", Text: "run",
	})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	parentRun, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	parentResolved, err := runs.ResolveAndFreezeConfig(ctx, parentRun)
	require.NoError(t, err)
	require.NotEmpty(t, parentResolved.Effective.ModelProfileID)

	delegations := fixture.Delegations()
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "explore-workspace", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)

	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: submission.Run.ID, ItemID: items[0].ID, SessionID: submission.Run.SessionID,
	})
	require.NoError(t, err)

	// Child executes and terminalizes with submit_result.
	_, err = runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	output := domain.RunOutput{
		Messages: []domain.ChatMessage{
			{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "inspect"}}},
			{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "found README"}}},
		},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "found README.md"},
	}
	require.NoError(t, runs.FinalizeChildSuccess(ctx, child.ID, output))

	// Item folded + group settled.
	items, err = delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationItemTerminal, items[0].Status)
	assert.Contains(t, string(items[0].ResultJSON), "found README.md")
	storedGroup, err := delegations.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationGroupSettled, storedGroup.Status)

	// Child is succeeded with private transcript only (no canonical message).
	childStored, err := runs.Get(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, childStored.Status)
	var canonical, shadow int
	require.NoError(t, fixture.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE run_id=?`, child.ID).Scan(&canonical))
	require.NoError(t, fixture.DB.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, child.ID).Scan(&shadow))
	assert.Zero(t, canonical, "children must not publish canonical messages")
	assert.Equal(t, 2, shadow)

	// Parent woken to queued for Coordinator re-enqueue.
	parentAfter, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, parentAfter.Status)
}

func TestFinalizeChildRejectsMissingTerminalContract(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "child-no-terminal")
	ctx := context.Background()
	delegations := &store.DelegationRepo{DB: repo.DB, Policies: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}
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
	_, err = runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	err = runs.FinalizeChildSuccess(ctx, child.ID, domain.RunOutput{Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "no submit"}},
	}}})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorIncompleteTerminalContract, domain.ErrorCodeOf(err))

	var _ = json.RawMessage{}
	_ = group
}
