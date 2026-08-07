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

func TestGenerationZeroCreatesGenerationAndAttempts(t *testing.T) {
	delegations, _, submission := setupRootBudgetParent(t, "attempt-gen0")
	ctx := context.Background()
	second := explorerItem()
	second.Name = "review"
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{explorerItem(), second},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Len(t, items, 2)
	require.Len(t, children, 2)

	// Exactly one generation 0 in running state.
	var generationID, kind, status, selectionJSON, authDigest string
	var generation int
	require.NoError(t, delegations.DB.QueryRow(`SELECT id,generation,kind,status,retry_selection_json,authorization_snapshot_digest
		FROM delegation_group_generations WHERE group_id=?`, group.ID).
		Scan(&generationID, &generation, &kind, &status, &selectionJSON, &authDigest))
	assert.Equal(t, 0, generation)
	assert.Equal(t, "initial", kind)
	assert.Equal(t, "running", status)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, authDigest)
	// Selection is explicit ordinal order, never timestamp inference. The
	// initial round's selection lives in the authorization snapshot; retry
	// selection is empty.
	var selection []map[string]string
	require.NoError(t, json.Unmarshal([]byte(selectionJSON), &selection))
	require.Len(t, selection, 0, "initial generation has no retry selection")
	var authJSON string
	require.NoError(t, delegations.DB.QueryRow(`SELECT authorization_snapshot_json
		FROM delegation_group_generations WHERE group_id=? AND generation=0`, group.ID).Scan(&authJSON))
	var authSelection []map[string]string
	require.NoError(t, json.Unmarshal([]byte(authJSON), &authSelection))
	require.Len(t, authSelection, 2)
	assert.Equal(t, items[0].ID, authSelection[0]["itemId"])
	assert.Equal(t, items[1].ID, authSelection[1]["itemId"])

	// One attempt per child Run, ordered by item ordinal.
	rows, err := delegations.DB.Query(`SELECT a.item_id,a.generation,a.status,a.child_run_id,a.authorization_snapshot_digest
		FROM delegation_item_attempts a JOIN delegation_items i ON i.id=a.item_id
		WHERE i.group_id=? ORDER BY i.ordinal`, group.ID)
	require.NoError(t, err)
	var attempts []map[string]string
	for rows.Next() {
		var itemID, gen, status, childRunID, digest string
		require.NoError(t, rows.Scan(&itemID, &gen, &status, &childRunID, &digest))
		attempts = append(attempts, map[string]string{
			"itemId": itemID, "generation": gen, "status": status, "childRunId": childRunID, "digest": digest,
		})
	}
	require.NoError(t, rows.Close())
	require.Len(t, attempts, 2)
	assert.Equal(t, items[0].ID, attempts[0]["itemId"])
	assert.Equal(t, children[0].ID, attempts[0]["childRunId"])
	assert.Equal(t, "0", attempts[0]["generation"])
	assert.Equal(t, "queued", attempts[0]["status"])
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, attempts[0]["digest"])
	assert.Equal(t, children[1].ID, attempts[1]["childRunId"])
}

func TestFinalizeChildSuccessSettlesAttemptAndGeneration(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "attempt-success")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Len(t, children, 1)

	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, delegations.RecordBudgetUsage(ctx, children[0].ID, 2, 3, 1000, 500, 50))
	// Simulate the parent's recorded delegate_tasks tool call so folding lands
	// on exactly one result_preview.
	_, err = delegations.DB.Exec(`INSERT INTO tool_calls
		(id,run_id,seq,tool_call_id,tool_name,arguments_json,status,started_at)
		VALUES('tc-1',?,1,'call-1','delegate_tasks','{}','completed',CURRENT_TIMESTAMP)`,
		submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "found README"},
	}))

	// Attempt terminalized with result, digest, usage, and reconciliation.
	var attemptStatus, resultJSON, terminalKind, reconciledAt string
	var usageJSON string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status,COALESCE(result_json,''),COALESCE(terminal_kind,''),
		COALESCE(root_reconciled_at,''),actual_usage_json FROM delegation_item_attempts WHERE child_run_id=?`,
		children[0].ID).Scan(&attemptStatus, &resultJSON, &terminalKind, &reconciledAt, &usageJSON))
	assert.Equal(t, "succeeded", attemptStatus)
	assert.Equal(t, "completed", terminalKind)
	assert.JSONEq(t, `{"status":"completed","summary":"found README"}`, resultJSON)
	assert.NotEmpty(t, reconciledAt)
	var usage map[string]int64
	require.NoError(t, json.Unmarshal([]byte(usageJSON), &usage))
	assert.EqualValues(t, 2, usage["modelCalls"])
	assert.EqualValues(t, 1000, usage["tokens"])

	// Generation settled; the original folded item columns keep generation 0.
	var generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations WHERE group_id=?`,
		group.ID).Scan(&generationStatus))
	assert.Equal(t, "settled", generationStatus)
	var itemStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_items WHERE id=?
		AND child_run_id=?`, (func() string {
		var id string
		require.NoError(t, delegations.DB.QueryRow(`SELECT item_id FROM delegation_item_attempts WHERE child_run_id=?`,
			children[0].ID).Scan(&id))
		return id
	})(), children[0].ID).Scan(&itemStatus))
	assert.Equal(t, "succeeded", itemStatus)

	// Exactly one folded result on the parent tool call; replaying the
	// finalizer must not fold a second time.
	var folded int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id=? AND result_preview LIKE '%settled%'`,
		submission.Run.ID).Scan(&folded))
	assert.Equal(t, 1, folded)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "found README"},
	}))
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE run_id=? AND result_preview LIKE '%settled%'`,
		submission.Run.ID).Scan(&folded))
	assert.Equal(t, 1, folded, "replayed finalizer must not double-fold")
}

func TestFinalizeChildFailureSettlesAttempt(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "attempt-fail")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Len(t, children, 1)

	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	parentID, wake, err := runs.FinalizeChildFailure(ctx, children[0].ID, "provider_unavailable", "model failed")
	require.NoError(t, err)
	assert.Equal(t, submission.Run.ID, parentID)
	assert.True(t, wake)

	var attemptStatus, errorCode string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status,COALESCE(error_code,'') FROM delegation_item_attempts
		WHERE child_run_id=?`, children[0].ID).Scan(&attemptStatus, &errorCode))
	assert.Equal(t, "failed", attemptStatus)
	assert.Equal(t, "provider_unavailable", errorCode)
	var generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations WHERE group_id=?`,
		group.ID).Scan(&generationStatus))
	assert.Equal(t, "settled", generationStatus)
}

func TestCancelSyncsAttemptAndGeneration(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "attempt-cancel")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Len(t, children, 1)

	require.NoError(t, runs.Cancel(ctx, submission.Run.ID))
	var attemptStatus, generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_item_attempts WHERE child_run_id=?`,
		children[0].ID).Scan(&attemptStatus))
	assert.Equal(t, "cancelled", attemptStatus)
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations WHERE group_id=?`,
		group.ID).Scan(&generationStatus))
	assert.Equal(t, "cancelled", generationStatus)
}

func TestDirectChildCancelSettlesAttemptSoSiblingSettlesGeneration(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "direct-child-cancel")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategyParallel,
		ExecutionMode: domain.DelegationExecutionBackground,
		Items:         []store.CreateDelegationItemInput{explorerItem(), explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 2)

	// Deterministically cancel one child before Provider dispatch (direct child
	// cancel). This guards the live-qualification fix in RunRepo.Cancel: the
	// cancelled child's own attempt must settle, otherwise the sibling's
	// FinalizeChildSuccess can never settle the generation (a queued attempt
	// remains) and no logical completion is created.
	require.NoError(t, runs.Cancel(ctx, children[1].ID))

	var attemptStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_item_attempts WHERE child_run_id=?`,
		children[1].ID).Scan(&attemptStatus))
	assert.Equal(t, "cancelled", attemptStatus, "direct child cancel must settle its own attempt")

	// Sibling succeeds and finalizes.
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "ok"},
	}))

	// Exactly one logical completion for generation 0 (mixed succeeded+cancelled).
	var completions int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_completions`).Scan(&completions))
	assert.Equal(t, 1, completions, "sibling settlement must create exactly one logical completion")

	var genStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations WHERE group_id=?`,
		group.ID).Scan(&genStatus))
	assert.Equal(t, "settled", genStatus)
}

func TestReapOrphansSyncsAttemptAndGeneration(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "attempt-orphan")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Len(t, children, 1)

	require.NoError(t, runs.Interrupt(ctx, children[0].ID, "worker restarted"))
	_, err = delegations.ReapOrphans(ctx)
	require.NoError(t, err)

	var attemptStatus, generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_item_attempts WHERE child_run_id=?`,
		children[0].ID).Scan(&attemptStatus))
	assert.Equal(t, "interrupted", attemptStatus)
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations WHERE group_id=?`,
		group.ID).Scan(&generationStatus))
	assert.Equal(t, "settled", generationStatus)
}

func TestGenerationZeroIsExplicitlySelectedNotByTimestamp(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "attempt-ordering")
	ctx := context.Background()
	// Create two groups back to back; the second must never select by the
	// latest timestamp — each group's generation 0 selection is its own ordinal
	// list only.
	groupA, itemsA, childrenA, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-a", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	// Settle group A so the parent can be woken and materialize group B.
	_, err = runs.Claim(ctx, childrenA[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, childrenA[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "ok"},
	}))
	_, err = delegations.DB.Exec(`UPDATE agent_runs SET status='running' WHERE id=?`, submission.Run.ID)
	require.NoError(t, err)

	groupB, _, childrenB, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-b", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)

	// Group A selection contains only A's item; group B only B's. The initial
	// round selection lives in the authorization snapshot.
	selectionOf := func(groupID string) []map[string]string {
		var raw string
		require.NoError(t, delegations.DB.QueryRow(`SELECT authorization_snapshot_json FROM delegation_group_generations
			WHERE group_id=? AND generation=0`, groupID).Scan(&raw))
		var selection []map[string]string
		require.NoError(t, json.Unmarshal([]byte(raw), &selection))
		return selection
	}
	selA := selectionOf(groupA.ID)
	selB := selectionOf(groupB.ID)
	require.Len(t, selA, 1)
	require.Len(t, selB, 1)
	assert.Equal(t, itemsA[0].ID, selA[0]["itemId"])
	assert.NotEqual(t, itemsA[0].ID, selB[0]["itemId"])
	_ = childrenB
}
