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

// settleBackgroundGroup creates a background group and settles its single
// child with a success result.
func settleBackgroundGroup(t *testing.T) (*store.DelegationRepo, *store.RunRepo, *domain.TurnSubmission, *domain.DelegationGroup) {
	t.Helper()
	delegations, runs, submission := setupRootBudgetParent(t, "completion-bg")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "bg-call", Strategy: domain.DelegationStrategySingle,
		ExecutionMode: domain.DelegationExecutionBackground,
		Items:         []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "background result"},
	}))
	return delegations, runs, submission, group
}

func TestBackgroundCompletionCreatedOnceWithPendingDelivery(t *testing.T) {
	delegations, _, _, _ := settleBackgroundGroup(t)
	ctx := context.Background()

	var completionCount, eventCount int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_completions`).Scan(&completionCount))
	assert.Equal(t, 1, completionCount)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delivery_events`).Scan(&eventCount))
	assert.Equal(t, 1, eventCount)

	var deliveryStatus, kind string
	var generation int
	require.NoError(t, delegations.DB.QueryRow(`SELECT delivery_status,kind,generation FROM delegation_completions`).
		Scan(&deliveryStatus, &kind, &generation))
	assert.Equal(t, "pending", deliveryStatus)
	assert.Equal(t, "completed", kind)
	assert.Equal(t, 0, generation)

	// Replaying the finalizer and recovery must not duplicate the completion.
	_, err := delegations.ReapOrphans(ctx)
	require.NoError(t, err)
	_, err = delegations.RecoverDelegation(ctx)
	require.NoError(t, err)
	_, err = delegations.RebuildMissingCompletions(ctx, 10)
	require.NoError(t, err)
	_, err = delegations.RebuildMissingDeliveryEvents(ctx, 10)
	require.NoError(t, err)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_completions`).Scan(&completionCount))
	assert.Equal(t, 1, completionCount)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delivery_events`).Scan(&eventCount))
	assert.Equal(t, 1, eventCount)
}

func TestBlockingGen0CompletionConsumedByParent(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "completion-blocking")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "blk-call", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "blocking result"},
	}))

	var deliveryStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT delivery_status FROM delegation_completions`).Scan(&deliveryStatus))
	assert.Equal(t, "consumed_by_parent", deliveryStatus, "generation 0 blocking folding consumes the completion")
	_ = group
}

func TestCompletionResultFoldsExplicitSelectionInOrdinalOrder(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "completion-ordinal")
	ctx := context.Background()
	second := explorerItem()
	second.Name = "second"
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "ord-call", Strategy: domain.DelegationStrategyParallel,
		ExecutionMode: domain.DelegationExecutionBackground,
		Items:         []store.CreateDelegationItemInput{explorerItem(), second},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 2)
	for index, child := range children {
		_, err = runs.Claim(ctx, child.ID)
		require.NoError(t, err)
		require.NoError(t, runs.FinalizeChildSuccess(ctx, child.ID, domain.RunOutput{
			Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
				Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
			Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "r" + string(rune('0'+index))},
		}))
	}
	var resultJSON string
	require.NoError(t, delegations.DB.QueryRow(`SELECT result_json FROM delegation_completions`).Scan(&resultJSON))
	var folded struct {
		Children []struct {
			Name   string `json:"name"`
			Result struct {
				Summary string `json:"summary"`
			} `json:"result"`
		} `json:"children"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultJSON), &folded))
	require.Len(t, folded.Children, 2)
	assert.Equal(t, "explore", folded.Children[0].Name)
	assert.Equal(t, "second", folded.Children[1].Name)
	assert.Equal(t, "r0", folded.Children[0].Result.Summary)
	assert.Equal(t, "r1", folded.Children[1].Result.Summary)
	_ = group
}

func TestRetryGenerationCreatesSecondCompletion(t *testing.T) {
	delegations, runs, _, group := settleBackgroundGroup(t)
	ctx := context.Background()

	// Retry the (only) item of the settled background group.
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	failedID := items[0].ID
	// Make the first attempt retry-eligible by simulating a failure path:
	// rebuild a fresh mixed scenario is easier — reuse settleMixedGroup for the
	// retry mechanics instead.
	_ = failedID
	_ = runs

	// Simpler path: settle a mixed group and retry its failed item.
	delegations2, runs2, _, group2, failedItemID := settleMixedGroup(t)
	_, children, _, err := delegations2.RetryGeneration(ctx, group2.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "completion-retry",
	})
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = runs2.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs2.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "retried"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "retry ok"},
	}))

	// Both the generation-0 and generation-1 completions exist.
	var generation0, generation1 int
	require.NoError(t, delegations2.DB.QueryRow(`SELECT COUNT(*) FROM delegation_completions WHERE generation=0`).Scan(&generation0))
	require.NoError(t, delegations2.DB.QueryRow(`SELECT COUNT(*) FROM delegation_completions WHERE generation=1`).Scan(&generation1))
	assert.Equal(t, 1, generation0)
	assert.Equal(t, 1, generation1)
	var events int
	require.NoError(t, delegations2.DB.QueryRow(`SELECT COUNT(*) FROM delivery_events`).Scan(&events))
	assert.Equal(t, 2, events)
}

func TestRebuildMissingCompletionsAndEvents(t *testing.T) {
	delegations, _, _, _ := settleBackgroundGroup(t)
	ctx := context.Background()

	// Simulate a crash that lost the delivery projection.
	_, err := delegations.DB.Exec(`DELETE FROM delivery_events`)
	require.NoError(t, err)
	_, err = delegations.DB.Exec(`DELETE FROM delegation_completions`)
	require.NoError(t, err)

	rebuilt, err := delegations.RebuildMissingCompletions(ctx, 10)
	require.NoError(t, err)
	require.Len(t, rebuilt, 1)
	assert.Equal(t, "completed", rebuilt[0].Kind)
	assert.NotEmpty(t, rebuilt[0].ResultDigest)

	// Delivery events were already restored with the completion; replay is a
	// no-op and idempotent.
	rebuiltEvents, err := delegations.RebuildMissingDeliveryEvents(ctx, 10)
	require.NoError(t, err)
	assert.Zero(t, rebuiltEvents)
	var eventCount int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delivery_events`).Scan(&eventCount))
	assert.Equal(t, 1, eventCount)
	again, err := delegations.RebuildMissingDeliveryEvents(ctx, 10)
	require.NoError(t, err)
	assert.Zero(t, again)
}
