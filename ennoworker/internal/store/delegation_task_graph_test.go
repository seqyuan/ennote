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

// taskItem builds a delegation item with a unique name and optional depends.
func taskItem(name string, depends ...string) store.CreateDelegationItemInput {
	item := explorerItem()
	item.Name = name
	item.Depends = depends
	return item
}

// settledOutput is a minimal successful child Run output.
func settledOutput(summary string) domain.RunOutput {
	return domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: summary},
	}
}

// ---- Task 1.2: topology validation (design §9 rows: dangling / cycle / no entry) ----

func TestDelegationTaskTopologyValidation(t *testing.T) {
	delegations, _, submission := setupRootBudgetParent(t, "dag-topology")
	ctx := context.Background()
	// Failure cases only: every invalid batch rolls back, leaving the parent
	// running. Success cases live in separate tests with their own parent.
	create := func(items ...store.CreateDelegationItemInput) error {
		_, _, _, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
			ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
			Strategy: domain.DelegationStrategyParallel, Items: items,
		}, submission.Run.SessionID)
		return err
	}
	t.Run("dangling reference rejected", func(t *testing.T) {
		err := create(taskItem("a", "missing"), taskItem("b"))
		require.Error(t, err)
		assert.Equal(t, domain.ErrorDelegationDagInvalid, domain.ErrorCodeOf(err))
	})
	t.Run("cycle rejected", func(t *testing.T) {
		err := create(taskItem("a", "b"), taskItem("b", "a"))
		require.Error(t, err)
		assert.Equal(t, domain.ErrorDelegationDagInvalid, domain.ErrorCodeOf(err))
	})
	t.Run("no entry task rejected", func(t *testing.T) {
		// a -> b -> c -> a: every task depends on something (cycle = no entry).
		err := create(taskItem("a", "c"), taskItem("b", "a"), taskItem("c", "b"))
		require.Error(t, err)
		assert.Equal(t, domain.ErrorDelegationDagInvalid, domain.ErrorCodeOf(err))
	})
	t.Run("duplicate names rejected with depends", func(t *testing.T) {
		err := create(taskItem("a"), taskItem("a", "a2"), taskItem("a2"))
		require.Error(t, err)
		assert.Equal(t, domain.ErrorDelegationDagInvalid, domain.ErrorCodeOf(err))
	})
}

func TestDelegationTaskTopologyAllowsLegacyAndValidChains(t *testing.T) {
	ctx := context.Background()
	t.Run("duplicate names allowed without depends", func(t *testing.T) {
		delegations, _, submission := setupRootBudgetParent(t, "dag-topo-dup")
		_, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
			ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
			Strategy:    domain.DelegationStrategyParallel,
			Items:       []store.CreateDelegationItemInput{explorerItem(), explorerItem()},
		}, submission.Run.SessionID)
		require.NoError(t, err)
		require.Len(t, children, 2)
	})
	t.Run("valid chain accepted", func(t *testing.T) {
		delegations, _, submission := setupRootBudgetParent(t, "dag-topo-chain")
		_, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
			ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
			Strategy: domain.DelegationStrategyParallel,
			Items:    []store.CreateDelegationItemInput{taskItem("a"), taskItem("b", "a"), taskItem("c", "b")},
		}, submission.Run.SessionID)
		require.NoError(t, err)
		require.Len(t, children, 3)
	})
}

// ---- Task 1.3: linear chain A -> B -> C scheduling ----

func TestDelegationTaskChainReadinessAndSuccession(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "dag-chain")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
		Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{
			taskItem("a"), taskItem("b", "a"), taskItem("c", "b"),
		},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 3)
	byName := map[string]*domain.AgentRun{"a": children[0], "b": children[1], "c": children[2]}

	// Only the entry task is ready initially.
	ready, err := delegations.ReadyChildrenForEnqueue(ctx, []string{children[0].ID, children[1].ID, children[2].ID})
	require.NoError(t, err)
	require.Equal(t, []string{children[0].ID}, ready, "only the entry task may start first")

	// A succeeds -> B becomes ready.
	_, err = runs.Claim(ctx, byName["a"].ID); require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, byName["a"].ID, settledOutput("a done")))
	ready, err = runs.ReadySuccessorRuns(ctx, byName["a"].ID)
	require.NoError(t, err)
	require.Equal(t, []string{byName["b"].ID}, ready, "B must start after A settles")

	// B succeeds -> C becomes ready.
	_, err = runs.Claim(ctx, byName["b"].ID); require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, byName["b"].ID, settledOutput("b done")))
	ready, err = runs.ReadySuccessorRuns(ctx, byName["b"].ID)
	require.NoError(t, err)
	require.Equal(t, []string{byName["c"].ID}, ready, "C must start after B settles")

	// C succeeds -> group settles.
	_, err = runs.Claim(ctx, byName["c"].ID); require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, byName["c"].ID, settledOutput("c done")))
	var groupStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_groups WHERE id=?`, group.ID).Scan(&groupStatus))
	assert.Equal(t, "settled", groupStatus)
}

// ---- Task 1.3: fan-out A -> B,C and fan-in D(B,C) ----

func TestDelegationTaskFanOutFanIn(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "dag-fan")
	ctx := context.Background()
	_, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
		Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{
			taskItem("a"),
			taskItem("b", "a"), taskItem("c", "a"),
			taskItem("d", "b", "c"),
		},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	byName := map[string]*domain.AgentRun{"a": children[0], "b": children[1], "c": children[2], "d": children[3]}

	// A settles -> B and C become ready (parallel fan-out).
	_, err = runs.Claim(ctx, byName["a"].ID); require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, byName["a"].ID, settledOutput("a done")))
	ready, err := runs.ReadySuccessorRuns(ctx, byName["a"].ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{byName["b"].ID, byName["c"].ID}, ready, "fan-out: B and C ready after A")

	// Start both B and C (the coordinator enqueues every ready successor).
	_, err = runs.Claim(ctx, byName["b"].ID); require.NoError(t, err)
	_, err = runs.Claim(ctx, byName["c"].ID); require.NoError(t, err)

	// Only B settles -> D stays pending (C not settled yet); C is already
	// running and is not reported again.
	require.NoError(t, runs.FinalizeChildSuccess(ctx, byName["b"].ID, settledOutput("b done")))
	ready, err = runs.ReadySuccessorRuns(ctx, byName["b"].ID)
	require.NoError(t, err)
	require.Empty(t, ready, "D must wait for both B and C")

	// C settles -> D ready (fan-in).
	require.NoError(t, runs.FinalizeChildSuccess(ctx, byName["c"].ID, settledOutput("c done")))
	ready, err = runs.ReadySuccessorRuns(ctx, byName["c"].ID)
	require.NoError(t, err)
	require.Equal(t, []string{byName["d"].ID}, ready, "fan-in: D ready after B and C")

	_, err = runs.Claim(ctx, byName["d"].ID); require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, byName["d"].ID, settledOutput("d done")))
}

// ---- Task 1.4: failure propagation blocks descendants ----

func TestDelegationTaskFailureBlocksDescendantsAndSettlesGroup(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "dag-block")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
		Strategy: domain.DelegationStrategyParallel,
		Items:    []store.CreateDelegationItemInput{taskItem("a"), taskItem("b", "a"), taskItem("c", "b")},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	byName := map[string]*domain.AgentRun{"a": children[0], "b": children[1], "c": children[2]}

	// A fails -> B and C become blocked (transitive), zero budget consumed.
	_, err = runs.Claim(ctx, byName["a"].ID); require.NoError(t, err)
	_, _, err = runs.FinalizeChildFailure(ctx, byName["a"].ID, "boom", "explosion")
	require.NoError(t, err)

	for _, name := range []string{"b", "c"} {
		var itemStatus, attemptStatus string
		require.NoError(t, delegations.DB.QueryRow(
			`SELECT i.status,a.status FROM delegation_items i
			 JOIN delegation_item_attempts a ON a.item_id=i.id
			 WHERE i.name=? AND a.generation=0`, name).Scan(&itemStatus, &attemptStatus))
		assert.Equal(t, "blocked", itemStatus, "%s item must be blocked", name)
		assert.Equal(t, "blocked", attemptStatus, "%s attempt must be blocked", name)
	}
	// Blocked descendants never become ready.
	ready, err := runs.ReadySuccessorRuns(ctx, byName["a"].ID)
	require.NoError(t, err)
	assert.Empty(t, ready, "blocked descendants must never be ready")

	// The whole group settles: blocked items are terminal and excluded from
	// the remaining count.
	var groupStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_groups WHERE id=?`, group.ID).Scan(&groupStatus))
	assert.Equal(t, "settled", groupStatus)
	var genStatus string
	require.NoError(t, delegations.DB.QueryRow(
		`SELECT status FROM delegation_group_generations WHERE group_id=? AND generation=0`, group.ID).Scan(&genStatus))
	assert.Equal(t, "settled", genStatus, "generation settles with mixed succeeded/blocked/failed attempts")
}

// ---- Task 1.6: v1.5 DAG-aware retry ----

func TestDelegationTaskRetryLiftsBlockedDescendants(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "dag-retry")
	ctx := context.Background()
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
		Strategy: domain.DelegationStrategyParallel,
		Items:    []store.CreateDelegationItemInput{taskItem("a"), taskItem("b", "a")},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	byName := map[string]*domain.AgentRun{"a": children[0], "b": children[1]}
	aItemID, bItemID := items[0].ID, items[1].ID

	// A fails -> B blocked (see TestDelegationTaskFailureBlocksDescendants...).
	_, err = runs.Claim(ctx, byName["a"].ID); require.NoError(t, err)
	_, _, err = runs.FinalizeChildFailure(ctx, byName["a"].ID, "boom", "explosion")
	require.NoError(t, err)

	// Retry only A: B is auto-selected as a blocked descendant.
	generation, retryChildren, approval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{aItemID},
		ClientRequestID: "retry-a",
	})
	require.NoError(t, err)
	require.Nil(t, approval)
	require.NotNil(t, generation)
	require.Len(t, retryChildren, 2, "blocked descendant B must be auto-retried")
	assert.Contains(t, generation.RetrySelection, aItemID)
	assert.Contains(t, generation.RetrySelection, bItemID)

	// The retry child of B stays queued until A's retry settles successfully.
	ready, err := delegations.ReadyChildrenForEnqueue(ctx, []string{retryChildren[0].ID, retryChildren[1].ID})
	require.NoError(t, err)
	require.Len(t, ready, 1, "only the retried dependency may start first")

	// A retry succeeds -> B retry becomes ready and lifts the blocked state.
	_, err = runs.Claim(ctx, retryChildren[0].ID); require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, retryChildren[0].ID, settledOutput("a retried")))
	ready, err = runs.ReadySuccessorRuns(ctx, retryChildren[0].ID)
	require.NoError(t, err)
	require.Equal(t, []string{retryChildren[1].ID}, ready, "blocked descendant must resume after dependency retry succeeds")

	// B retry succeeds -> whole flow completes.
	_, err = runs.Claim(ctx, retryChildren[1].ID); require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, retryChildren[1].ID, settledOutput("b done")))
	var genStatus string
	require.NoError(t, delegations.DB.QueryRow(
		`SELECT status FROM delegation_group_generations WHERE group_id=? AND generation=1`, group.ID).Scan(&genStatus))
	assert.Equal(t, "settled", genStatus)
}

// ---- Task 1.6: dependency retry failure keeps descendants blocked ----

func TestDelegationTaskRetryFailureKeepsDescendantsBlocked(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "dag-retry-fail")
	ctx := context.Background()
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
		Strategy: domain.DelegationStrategyParallel,
		Items:    []store.CreateDelegationItemInput{taskItem("a"), taskItem("b", "a")},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	byName := map[string]*domain.AgentRun{"a": children[0], "b": children[1]}

	_, err = runs.Claim(ctx, byName["a"].ID); require.NoError(t, err)
	_, _, err = runs.FinalizeChildFailure(ctx, byName["a"].ID, "boom", "explosion")
	require.NoError(t, err)

	generation, retryChildren, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{items[0].ID}, ClientRequestID: "retry-a",
	})
	require.NoError(t, err)
	require.NotNil(t, generation)
	require.Len(t, retryChildren, 2)

	// A retry fails again -> B retry is blocked again, never started.
	_, err = runs.Claim(ctx, retryChildren[0].ID); require.NoError(t, err)
	_, _, err = runs.FinalizeChildFailure(ctx, retryChildren[0].ID, "boom2", "explosion again")
	require.NoError(t, err)
	var bAttemptStatus string
	require.NoError(t, delegations.DB.QueryRow(
		`SELECT a.status FROM delegation_item_attempts a JOIN delegation_items i ON i.id=a.item_id
		 WHERE i.group_id=? AND i.name='b' AND a.generation=1`, group.ID).Scan(&bAttemptStatus))
	assert.Equal(t, "blocked", bAttemptStatus, "descendant must be blocked again after dependency retry fails")

	ready, err := runs.ReadySuccessorRuns(ctx, retryChildren[0].ID)
	require.NoError(t, err)
	assert.Empty(t, ready, "descendant stays blocked after failed dependency retry")
}

// ---- persisted depends_json round trip ----

func TestDelegationTaskDependsPersistedAndProjected(t *testing.T) {
	delegations, _, submission := setupRootBudgetParent(t, "dag-persist")
	ctx := context.Background()
	group, _, _, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
		Strategy: domain.DelegationStrategyParallel,
		Items:    []store.CreateDelegationItemInput{taskItem("a"), taskItem("b", "a")},
	}, submission.Run.SessionID)
	require.NoError(t, err)

	var stored string
	require.NoError(t, delegations.DB.QueryRow(
		`SELECT depends_json FROM delegation_items WHERE group_id=? AND name='b'`, group.ID).Scan(&stored))
	var depends []string
	require.NoError(t, json.Unmarshal([]byte(stored), &depends))
	assert.Equal(t, []string{"a"}, depends)

	storedItems, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	var b domain.DelegationItem
	for _, item := range storedItems {
		if item.Name == "b" {
			b = item
		}
	}
	assert.Equal(t, []string{"a"}, b.Depends)
}

// ---- Task 1.5: restart recovery rebuilds readiness ----

func TestDelegationTaskRecoveryResumesReadySuccessors(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "dag-recovery")
	ctx := context.Background()
	_, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1",
		Strategy: domain.DelegationStrategyParallel,
		Items:    []store.CreateDelegationItemInput{taskItem("a"), taskItem("b", "a")},
	}, submission.Run.SessionID)
	require.NoError(t, err)

	// A settles successfully, but the worker crashes before B is enqueued.
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, settledOutput("a done")))

	// Restart recovery: RecoverActive returns queued runs; the readiness filter
	// re-evaluates B against A's settled attempt and lets it through, while a
	// hypothetical dependent C whose dependency never settled stays queued.
	queued, err := runs.RecoverActive(ctx)
	require.NoError(t, err)
	require.Contains(t, queued, children[1].ID, "B stays queued after crash")

	ready, err := delegations.ReadyChildrenForEnqueue(ctx, queued)
	require.NoError(t, err)
	require.Contains(t, ready, children[1].ID, "recovery must resume ready successors")

	// Recovery must also settle generations whose attempts are all terminal
	// (defensive re-advance is idempotent).
	_, err = delegations.RecoverDelegation(ctx)
	require.NoError(t, err)
}
