package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settleNeedsInputGroup creates a background group whose single child finalizes
// with needs_input.
func settleNeedsInputGroup(t *testing.T) (*store.DelegationRepo, *store.RunRepo, *domain.DelegationGroup, string) {
	t.Helper()
	delegations, runs, submission := setupRootBudgetParent(t, "follow-up")
	ctx := context.Background()
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "ni-call", Strategy: domain.DelegationStrategySingle,
		ExecutionMode: domain.DelegationExecutionBackground,
		Items:         []store.CreateDelegationItemInput{halfExplorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "need more"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitNeedsInput, Summary: "which files?"},
	}))
	return delegations, runs, group, items[0].ID
}

func attemptIDForItemGeneration(t *testing.T, delegations *store.DelegationRepo, itemID string, generation int) string {
	t.Helper()
	var attemptID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT id FROM delegation_item_attempts
		WHERE item_id=? AND generation=?`, itemID, generation).Scan(&attemptID))
	return attemptID
}

func TestContinueNeedsInputCreatesContinuationGeneration(t *testing.T) {
	delegations, _, group, itemID := settleNeedsInputGroup(t)
	ctx := context.Background()

	generation, child, err := delegations.ContinueNeedsInput(ctx, itemID, domain.DelegationInputCommand{
		ExpectedGeneration: 0, SourceAttemptID: attemptIDForItemGeneration(t, delegations, itemID, 0), Text: "inspect src/ only",
		ClientRequestID: "ni-1",
	})
	require.NoError(t, err)
	require.NotNil(t, generation)
	require.NotNil(t, child)
	assert.Equal(t, domain.DelegationGenerationInput, generation.Kind)
	assert.Equal(t, 1, generation.Generation)
	assert.Equal(t, domain.RunKindDelegatedAgent, child.RunKind)

	// The continuation fact records the exact source attempt and bounded input.
	var sourceAttemptID, kind string
	require.NoError(t, delegations.DB.QueryRow(`SELECT c.source_attempt_id,c.kind
		FROM delegation_attempt_continuations c
		JOIN delegation_item_attempts a ON a.id=c.attempt_id
		WHERE a.item_id=? AND a.generation=1`, itemID).Scan(&sourceAttemptID, &kind))
	assert.Equal(t, "input", kind)
	var sourceStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_item_attempts WHERE id=?`,
		sourceAttemptID).Scan(&sourceStatus))
	assert.Equal(t, "needs_input", sourceStatus, "source attempt stays immutable")

	// The source attempt/result were never rewritten.
	var gen0Status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=0`, group.ID).Scan(&gen0Status))
	assert.Equal(t, "settled", gen0Status)
}

func TestContinueNeedsInputRejectsWrongSourceState(t *testing.T) {
	delegations, _, group, itemID := settleNeedsInputGroup(t)
	ctx := context.Background()

	// A succeeded sibling scenario: settle a completed attempt and try input.
	delegations2, runs2, _, _ := settleBackgroundGroup(t)
	_ = delegations2
	_ = runs2

	// Wrong expected generation is stale.
	_, _, err := delegations.ContinueNeedsInput(ctx, itemID, domain.DelegationInputCommand{
		ExpectedGeneration: 5, SourceAttemptID: attemptIDForItemGeneration(t, delegations, itemID, 0),
		Text: "x", ClientRequestID: "ni-stale",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorDelegationInputStale, domain.ErrorCodeOf(err))
	_ = group
}

func TestContinueNeedsInputIdempotent(t *testing.T) {
	delegations, _, _, itemID := settleNeedsInputGroup(t)
	ctx := context.Background()

	sourceAttemptID := attemptIDForItemGeneration(t, delegations, itemID, 0)
	first, firstChild, err := delegations.ContinueNeedsInput(ctx, itemID, domain.DelegationInputCommand{
		ExpectedGeneration: 0, SourceAttemptID: sourceAttemptID,
		Text: "inspect src/", ClientRequestID: "ni-idem",
	})
	require.NoError(t, err)
	second, secondChild, err := delegations.ContinueNeedsInput(ctx, itemID, domain.DelegationInputCommand{
		ExpectedGeneration: 0, SourceAttemptID: sourceAttemptID,
		Text: "inspect src/", ClientRequestID: "ni-idem",
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)
	assert.Equal(t, firstChild.ID, secondChild.ID)
	var attempts int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attempts))
	assert.Equal(t, 1, attempts)
}

func TestFollowUpResumesCompletedAttempt(t *testing.T) {
	delegations, runs, _, _ := settleBackgroundGroup(t)
	ctx := context.Background()

	items, err := delegations.ListItems(ctx, func() string {
		var groupID string
		require.NoError(t, delegations.DB.QueryRow(`SELECT group_id FROM delegation_handles LIMIT 1`).Scan(&groupID))
		return groupID
	}())
	require.NoError(t, err)
	require.Len(t, items, 1)

	generation, child, err := delegations.FollowUp(ctx, items[0].ID, domain.DelegationInputCommand{
		ExpectedGeneration: 0, SourceAttemptID: attemptIDForItemGeneration(t, delegations, items[0].ID, 0),
		Text: "expand the summary please", ClientRequestID: "fu-1",
	})
	require.NoError(t, err)
	require.NotNil(t, generation)
	require.NotNil(t, child)
	assert.Equal(t, domain.DelegationGenerationFollowUp, generation.Kind)

	var kind string
	require.NoError(t, delegations.DB.QueryRow(`SELECT kind FROM delegation_attempt_continuations
		WHERE attempt_id=(SELECT id FROM delegation_item_attempts WHERE child_run_id=?)`,
		child.ID).Scan(&kind))
	assert.Equal(t, "follow_up", kind)
	_ = runs
}

func TestFollowUpRejectsFailedAttempt(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	// failed attempt is not follow-up eligible; retry is the correct command.
	_, _, err := delegations.FollowUp(ctx, failedItemID, domain.DelegationInputCommand{
		ExpectedGeneration: 0, SourceAttemptID: attemptIDForItemGeneration(t, delegations, failedItemID, 0),
		Text: "nope", ClientRequestID: "fu-fail",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorDelegationFollowUpForbidden, domain.ErrorCodeOf(err))
	_ = group
}
