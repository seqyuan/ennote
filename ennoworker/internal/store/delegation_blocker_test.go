package store_test

// The Attention projection of a needs_input delegation item carries the typed
// blocker the child reported, so the UI can label the row without parsing the
// summary prose. A result written before the field existed keeps exactly the
// payload it had: no `blocker` key, and no guessed value.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// needsInputOutput is the terminal output of a child that stopped to ask: the
// contract plus the transcript a real child always has (AppendRunMessagesTx
// refuses an empty one).
func needsInputOutput(blocker domain.SubmitBlocker, summary string) domain.RunOutput {
	return domain.RunOutput{
		Terminal: &domain.SubmitResult{Status: domain.SubmitNeedsInput, Blocker: blocker, Summary: summary},
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: summary}}}},
	}
}

// finalizeNeedsInput settles the fixture's only child with the given blocker and
// returns the projected Attention display payload.
func finalizeNeedsInput(t *testing.T, blocker domain.SubmitBlocker) map[string]any {
	t.Helper()
	runs, delegations, _, childID := setupParentWithChild(t)
	ctx := context.Background()
	_, err := runs.Claim(ctx, childID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, childID, needsInputOutput(blocker, "which sample should I use?")))
	var displayJSON string
	require.NoError(t, delegations.DB.QueryRowContext(ctx,
		`SELECT display_json FROM attention_items WHERE source_kind=? AND kind=?`,
		string(domain.AttentionSourceDelegationItem), string(domain.AttentionNeedsInput)).Scan(&displayJSON))
	var display map[string]any
	require.NoError(t, json.Unmarshal([]byte(displayJSON), &display))
	return display
}

func TestNeedsInputAttentionCarriesTypedBlocker(t *testing.T) {
	for _, blocker := range domain.SubmitBlockers {
		display := finalizeNeedsInput(t, blocker)
		assert.Equal(t, string(blocker), display["blocker"])
		// The pre-existing keys are untouched: the blocker is additive.
		assert.Equal(t, "needs_input", display["kind"])
		assert.Contains(t, display, "generation")
		assert.Contains(t, display, "summary")
	}
}

// A needs_input result without a blocker (an attempt written before the field
// existed) projects exactly the payload it used to: the key is absent rather than
// defaulted to a plausible-sounding guess.
func TestNeedsInputAttentionWithoutBlockerHasNoKey(t *testing.T) {
	display := finalizeNeedsInput(t, "")
	_, hasBlocker := display["blocker"]
	assert.False(t, hasBlocker, "an old result must not gain a blocker: %v", display)
	assert.Equal(t, "needs_input", display["kind"])
}

// The blocker is part of the durable result, not just of the projection: the whole
// submit_result lands in delegation_items.result_json, so a reader that re-decodes
// the stored row gets the same typed reason.
func TestNeedsInputStoredResultKeepsBlocker(t *testing.T) {
	runs, delegations, _, childID := setupParentWithChild(t)
	ctx := context.Background()
	_, err := runs.Claim(ctx, childID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, childID,
		needsInputOutput(domain.BlockerNeedsUserInput, "which sample?")))
	var resultJSON string
	require.NoError(t, delegations.DB.QueryRowContext(ctx,
		`SELECT result_json FROM delegation_items WHERE child_run_id=?`, childID).Scan(&resultJSON))
	var stored domain.SubmitResult
	require.NoError(t, json.Unmarshal([]byte(resultJSON), &stored))
	assert.Equal(t, domain.SubmitNeedsInput, stored.Status)
	assert.Equal(t, domain.BlockerNeedsUserInput, stored.Blocker)

	// The attempt row carries the same document (it is what the continuation
	// commands and the inspection surface read).
	var attemptResult string
	require.NoError(t, delegations.DB.QueryRowContext(ctx,
		`SELECT COALESCE(result_json,'') FROM delegation_item_attempts WHERE child_run_id=?`, childID).
		Scan(&attemptResult))
	var attempt domain.SubmitResult
	require.NoError(t, json.Unmarshal([]byte(attemptResult), &attempt))
	assert.Equal(t, domain.BlockerNeedsUserInput, attempt.Blocker)
}
