package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupDelegationParent(t *testing.T) (*store.DelegationRepo, *store.RunRepo, *domain.TurnSubmission) {
	t.Helper()
	repo, submission := setupSubmittedRun(t, "delegation-parent")
	_, err := repo.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	return &store.DelegationRepo{DB: repo.DB}, repo, submission
}

func explorerItem() store.CreateDelegationItemInput {
	return store.CreateDelegationItemInput{
		Name: "explore", RoleVersionID: "builtin-workspace-explorer-v2",
		AssignmentJSON: json.RawMessage(`{"objective":"inspect the workspace"}`), OutputContract: "text-v1",
		Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
			MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
	}
}

// insertChildRun inserts a valid delegated_agent child row (parent, no turn,
// private_to_parent, format 2) so FK/CHECK constraints hold.
func insertChildRun(t *testing.T, db *sql.DB, id, sessionID, parentRunID string) {
	t.Helper()
	now := "2026-08-03T00:00:00Z"
	_, err := db.Exec(`INSERT INTO agent_runs
		(id,session_id,run_kind,status,parent_run_id,publish_mode,commit_format_version,created_at)
		VALUES(?,?,'delegated_agent','queued',?,'private_to_parent',2,?)`,
		id, sessionID, parentRunID, now)
	require.NoError(t, err)
}

func TestCreateGroupPersistsItemsAndRejectsDuplicateToolCall(t *testing.T) {
	delegations, _, submission := setupDelegationParent(t)
	ctx := context.Background()
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationGroupPending, group.Status)

	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "explore", items[0].Name)
	assert.Equal(t, "builtin-workspace-explorer-v2", items[0].RoleVersionID)
	assert.Equal(t, domain.DelegationItemPending, items[0].Status)

	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	assert.ErrorIs(t, err, store.ErrDelegationGroupExists)
}

func TestListActivityUsesFrozenChildIdentityAndTerminalResult(t *testing.T) {
	delegations, _, submission := setupDelegationParent(t)
	ctx := context.Background()
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "provider-call", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: submission.Run.ID, ItemID: items[0].ID, SessionID: submission.Run.SessionID,
	})
	require.NoError(t, err)

	page, err := delegations.ListActivity(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.Len(t, page.Groups, 1)
	assert.Equal(t, "provider-call", page.Groups[0].ParentToolCallID)
	require.Len(t, page.Groups[0].Children, 1)
	activity := page.Groups[0].Children[0]
	assert.Equal(t, child.ID, activity.ChildRunID)
	assert.Equal(t, "workspace-explorer", activity.RoleHandle)
	assert.Equal(t, domain.RunQueued, activity.RunStatus)
	assert.Nil(t, activity.Result)

	_, err = delegations.DB.Exec(`UPDATE delegation_groups SET status='settled' WHERE id=?`, group.ID)
	require.NoError(t, err)
	_, err = delegations.DB.Exec(`UPDATE delegation_items SET status='succeeded',result_json=? WHERE id=?`,
		`{"status":"completed","summary":"Workspace is empty."}`, items[0].ID)
	require.NoError(t, err)
	_, err = delegations.DB.Exec(`UPDATE agent_runs SET status='succeeded' WHERE id=?`, child.ID)
	require.NoError(t, err)
	page, err = delegations.ListActivity(ctx, submission.Run.ID)
	require.NoError(t, err)
	activity = page.Groups[0].Children[0]
	require.NotNil(t, activity.Result)
	assert.Equal(t, "Workspace is empty.", activity.Result.Summary)
}

func TestParallelGroupMaterializesEveryChildAfterParentStartsWaiting(t *testing.T) {
	delegations, runs, submission := setupDelegationParent(t)
	ctx := context.Background()
	second := explorerItem()
	second.Name = "review"
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "parallel-call", Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{explorerItem(), second},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	for _, item := range items {
		_, err = delegations.CreateChildRun(ctx, store.CreateChildRunInput{
			ParentRunID: submission.Run.ID, ItemID: item.ID, SessionID: submission.Run.SessionID,
		})
		require.NoError(t, err)
	}

	activity, err := delegations.ListActivity(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.Len(t, activity.Groups, 1)
	assert.Equal(t, domain.DelegationStrategyParallel, activity.Groups[0].Strategy)
	require.Len(t, activity.Groups[0].Children, 2)
	parent, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunWaitingChildren, parent.Status)
}

func TestCreateGroupWithChildrenRollsBackWholeTreeOnChildFailure(t *testing.T) {
	delegations, runs, submission := setupDelegationParent(t)
	ctx := context.Background()
	require.NoError(t, func() error {
		_, err := delegations.DB.Exec(`CREATE TRIGGER fail_second_delegated_child
			BEFORE INSERT ON agent_runs
			WHEN NEW.run_kind='delegated_agent' AND
				(SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=NEW.parent_run_id)=1
			BEGIN SELECT RAISE(ABORT, 'injected_child_failure'); END`)
		return err
	}())
	second := explorerItem()
	second.Name = "review"
	_, _, _, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "atomic-call", Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{explorerItem(), second},
	}, submission.Run.SessionID)
	require.Error(t, err)

	var groups, items, children, budgets int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_groups WHERE parent_run_id=?`,
		submission.Run.ID).Scan(&groups))
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_items`).Scan(&items))
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?`,
		submission.Run.ID).Scan(&children))
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM run_budgets`).Scan(&budgets))
	assert.Zero(t, groups)
	assert.Zero(t, items)
	assert.Zero(t, children)
	assert.Zero(t, budgets)
	parent, getErr := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, getErr)
	assert.Equal(t, domain.RunRunning, parent.Status)
}

func TestCreateGroupRejectsNonRunningAndNestedParents(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "delegation-guards")
	ctx := context.Background()
	delegations := &store.DelegationRepo{DB: repo.DB}

	// Parent not running → reject.
	_, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "c", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	assert.Error(t, err)

	// A nested parent (execution_depth=1, agent with a real turn) → reject.
	now := "2026-08-03T00:00:00Z"
	_, err = repo.DB.Exec(`INSERT INTO agent_runs
		(id,turn_id,session_id,run_kind,status,execution_depth,parent_run_id,publish_mode,commit_format_version,created_at)
		VALUES(?,?,?,'agent','running',1,?,'public_final',1,?)`, "nested-parent", submission.TurnID,
		submission.Run.SessionID, submission.Run.ID, now)
	require.NoError(t, err)
	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: "nested-parent", ParentToolCallID: "c", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	assert.Error(t, err)
}

func TestReserveChildBudgetCASAndRecordUsage(t *testing.T) {
	delegations, _, submission := setupDelegationParent(t)
	ctx := context.Background()
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-budget", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)

	childID := "explorer-child-1"
	insertChildRun(t, delegations.DB, childID, submission.Run.SessionID, submission.Run.ID)
	_, err = delegations.ReserveChildBudget(ctx, childID, items[0].ID)
	require.NoError(t, err)
	// Second reservation is a CAS rejection.
	_, err = delegations.ReserveChildBudget(ctx, childID, items[0].ID)
	assert.ErrorIs(t, err, store.ErrDelegationBudgetReserved)

	// Within limits → consumed.
	require.NoError(t, delegations.RecordBudgetUsage(ctx, childID, 1, 2, 1000, 500, 10))
	// Exceed model calls → rejected and unchanged.
	err = delegations.RecordBudgetUsage(ctx, childID, 10, 0, 0, 0, 0)
	assert.ErrorIs(t, err, store.ErrDelegationBudgetExceeded)
	// Exceed total tokens → rejected.
	err = delegations.RecordBudgetUsage(ctx, childID, 0, 0, 999999, 0, 0)
	assert.ErrorIs(t, err, store.ErrDelegationBudgetExceeded)
}

func TestRuntimeBudgetAdmissionCapsCallsUsageAndCost(t *testing.T) {
	delegations, _, submission := setupDelegationParent(t)
	ctx := context.Background()
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "runtime-budget", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	childID := "budget-runtime-child"
	insertChildRun(t, delegations.DB, childID, submission.Run.SessionID, submission.Run.ID)
	_, err = delegations.ReserveChildBudget(ctx, childID, items[0].ID)
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: delegations.DB}).Create(ctx, store.CreateProviderInput{
		Name: "budget-provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test", CredentialRef: "env:BUDGET_KEY",
	})
	require.NoError(t, err)
	model, err := (&store.ModelRepo{DB: delegations.DB}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "budget-model", ContextWindow: 32000, MaxOutputTokens: 8000,
		InputCostUSDMicrosPerMillion: 1000000, OutputCostUSDMicrosPerMillion: 2000000,
	})
	require.NoError(t, err)

	allowed, err := delegations.AdmitModelCall(ctx, childID, model.ID, 1000, 10000)
	require.NoError(t, err)
	assert.Equal(t, 4000, allowed, "output must be clamped to the remaining child output ceiling")
	require.NoError(t, delegations.CompleteModelCall(ctx, childID, model.ID,
		domain.Usage{InputTokens: 1000, OutputTokens: 500}))
	require.NoError(t, delegations.AdmitToolCalls(ctx, childID, 8))
	assert.ErrorIs(t, delegations.AdmitToolCalls(ctx, childID, 1), store.ErrDelegationBudgetExceeded)

	var modelCalls, toolCalls int
	var tokens, outputTokens, cost int64
	require.NoError(t, delegations.DB.QueryRow(`SELECT consumed_model_calls,consumed_tool_calls,consumed_tokens,
		consumed_output_tokens,consumed_cost_usd_micros FROM run_budgets WHERE run_id=?`, childID).
		Scan(&modelCalls, &toolCalls, &tokens, &outputTokens, &cost))
	assert.Equal(t, 1, modelCalls)
	assert.Equal(t, 8, toolCalls)
	assert.Equal(t, int64(1500), tokens)
	assert.Equal(t, int64(500), outputTokens)
	assert.Equal(t, int64(2000), cost)
}

func TestFinalizeChildFailureSettlesGroupAndQueuesParent(t *testing.T) {
	runs, delegations, _, childID := setupParentWithChild(t)
	ctx := context.Background()
	parentID := runsMustGetParentID(t, runs, childID)
	_, err := runs.Claim(ctx, childID)
	require.NoError(t, err)

	returnedParent, wake, err := runs.FinalizeChildFailure(ctx, childID, "provider_unavailable", "provider failed")
	require.NoError(t, err)
	assert.Equal(t, parentID, returnedParent)
	assert.True(t, wake)
	child, err := runs.Get(ctx, childID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunFailed, child.Status)
	parent, err := runs.Get(ctx, parentID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, parent.Status)
	var itemStatus, groupStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_items WHERE child_run_id=?`, childID).Scan(&itemStatus))
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_groups WHERE parent_run_id=?`, parentID).Scan(&groupStatus))
	assert.Equal(t, "failed", itemStatus)
	assert.Equal(t, "settled", groupStatus)
}

func TestCancelledChildCannotCommitSuccessTranscript(t *testing.T) {
	runs, _, _, childID := setupParentWithChild(t)
	ctx := context.Background()
	parentID := runsMustGetParentID(t, runs, childID)
	_, err := runs.Claim(ctx, childID)
	require.NoError(t, err)
	require.NoError(t, runs.Cancel(ctx, parentID))

	err = runs.FinalizeChildSuccess(ctx, childID, domain.RunOutput{Terminal: &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "late success"}, Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "late"}},
	}}})
	require.NoError(t, err)
	child, err := runs.Get(ctx, childID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, child.Status)
	var messages, successEvents int
	require.NoError(t, runs.DB.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, childID).Scan(&messages))
	require.NoError(t, runs.DB.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id=? AND event_type='run_succeeded'`, childID).Scan(&successEvents))
	assert.Zero(t, messages)
	assert.Zero(t, successEvents)
}

func TestChildTerminalArtifactMustBelongToChildRun(t *testing.T) {
	runs, _, _, childID := setupParentWithChild(t)
	ctx := context.Background()
	_, err := runs.Claim(ctx, childID)
	require.NoError(t, err)
	err = runs.FinalizeChildSuccess(ctx, childID, domain.RunOutput{Terminal: &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done", ArtifactRefs: []domain.ArtifactReference{{
			ArtifactID: "other-run-artifact", Name: "result.txt", Kind: domain.ArtifactKindText,
			MIMEType: "text/plain", SHA256: "sha256",
		}},
	}})
	require.Error(t, err)
	child, getErr := runs.Get(ctx, childID)
	require.NoError(t, getErr)
	assert.Equal(t, domain.RunRunning, child.Status)
}

func TestFoldChildResultSettlesGroupWhenAllItemsTerminal(t *testing.T) {
	delegations, _, submission := setupDelegationParent(t)
	ctx := context.Background()
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-settle", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)

	childID := "explorer-child-2"
	insertChildRun(t, delegations.DB, childID, submission.Run.SessionID, submission.Run.ID)
	require.NoError(t, delegations.AssignChild(ctx, items[0].ID, childID))
	_, err = delegations.ReserveChildBudget(ctx, childID, items[0].ID)
	require.NoError(t, err)
	_, err = delegations.DB.Exec(`UPDATE delegation_items SET status='running' WHERE id=?`, items[0].ID)
	require.NoError(t, err)

	settled, err := delegations.FoldChildResult(ctx, childID, domain.DelegationItemTerminal,
		json.RawMessage(`{"status":"completed","summary":"done"}`))
	require.NoError(t, err)
	assert.True(t, settled, "single item group must settle after its only child folds")

	stored, err := delegations.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationGroupSettled, stored.Status)

	// Folding again is a conflict.
	_, err = delegations.FoldChildResult(ctx, childID, domain.DelegationItemTerminal, json.RawMessage(`{}`))
	assert.ErrorIs(t, err, store.ErrDelegationConflict)
}

func TestReapOrphansInterruptsChildrenOfTerminalParents(t *testing.T) {
	delegations, repo, submission := setupDelegationParent(t)
	ctx := context.Background()
	// Parent becomes terminal.
	require.NoError(t, repo.Succeed(ctx, submission.Run.ID))

	now := "2026-08-03T00:00:00Z"
	childID := "orphan-child"
	_, err := repo.DB.Exec(`INSERT INTO agent_runs
		(id,session_id,run_kind,status,parent_run_id,publish_mode,commit_format_version,created_at)
		VALUES(?,?,'delegated_agent','running',?,'private_to_parent',2,?)`,
		childID, submission.Run.SessionID, submission.Run.ID, now)
	require.NoError(t, err)

	reaped, err := delegations.ReapOrphans(ctx)
	require.NoError(t, err)
	assert.Contains(t, reaped, childID)
	var status string
	require.NoError(t, repo.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, childID).Scan(&status))
	assert.Equal(t, "interrupted", status)
}

func TestReapOrphansReconcilesAlreadyInterruptedChildItems(t *testing.T) {
	runs, delegations, _, childID := setupParentWithChild(t)
	ctx := context.Background()
	parentID := runsMustGetParentID(t, runs, childID)
	_, err := runs.Claim(ctx, childID)
	require.NoError(t, err)
	require.NoError(t, runs.Interrupt(ctx, parentID, "restart"))
	require.NoError(t, runs.Interrupt(ctx, childID, "restart"))

	_, err = delegations.ReapOrphans(ctx)
	require.NoError(t, err)
	var itemStatus, groupStatus string
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM delegation_items WHERE child_run_id=?`, childID).Scan(&itemStatus))
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM delegation_groups WHERE parent_run_id=?`, parentID).Scan(&groupStatus))
	assert.Equal(t, "failed", itemStatus)
	assert.Equal(t, "settled", groupStatus)
}
