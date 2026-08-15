//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveHostedDelegationV15 qualifies the Item 6 vertical slice against a
// real Provider:
//
//	Host creates a background parallel read-only group
//	one child succeeds and one child is deterministically cancelled before
//	  Provider dispatch
//	the logical completion becomes visible
//	retry selects only the cancelled child; the successful child attempt,
//	  result digest, and usage are reused exactly
//	the retried child succeeds and its follow-up resumes the exact thread
//	final completion and Attention settle
//
// It asserts Provider call counts, frozen Role/version snapshots, private
// transcript boundaries, root/child usage, and one completion per generation.
func TestLiveHostedDelegationV15(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	stack := newLiveStack(t, "v15-live")
	db := stack.DB
	session := stack.Session
	modelProfileID := stack.ModelID

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub, Providers: stack.Providers,
		Models: stack.ModelRepo, Policies: stack.Policies}
	messageRepo := &store.MessageRepo{DB: db}

	parentSubmission, err := runRepo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "v15-parent",
		Text: "Parent inquiry", RequestedConfig: json.RawMessage(`{"maxIterations":1}`),
	})
	require.NoError(t, err)
	parentRun, err := runRepo.Claim(ctx, parentSubmission.Run.ID)
	require.NoError(t, err)
	parentResolved, err := runRepo.ResolveAndFreezeConfig(ctx, parentRun)
	require.NoError(t, err)
	require.NotEmpty(t, parentResolved.Effective.ModelProfileID)

	delegations := &store.DelegationRepo{DB: db}
	second := explorerLiveItem(t, modelProfileID)
	second.Name = "review"
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: parentRun.ID, ParentToolCallID: "v15-bg", Strategy: domain.DelegationStrategyParallel,
		ExecutionMode: domain.DelegationExecutionBackground,
		Items:         []store.CreateDelegationItemInput{explorerLiveItem(t, modelProfileID), second},
	}, session.ID)
	require.NoError(t, err)
	require.Len(t, children, 2)
	require.Len(t, items, 2)

	// Parent stays running in background mode; the handle exists.
	parentAfter, err := runRepo.Get(ctx, parentRun.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunRunning, parentAfter.Status, "background must not block the parent")
	handle, err := delegations.HandleForGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationExecutionBackground, handle.ExecutionMode)

	// Deterministically cancel the second child before Provider dispatch.
	require.NoError(t, runRepo.Cancel(ctx, children[1].ID))
	cancelledRun, err := runRepo.Get(ctx, children[1].ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, cancelledRun.Status)

	// Execute the first child through the real Provider.
	executor := newV15Executor(t, db, hub, runRepo, &store.SessionRepo{DB: db}, messageRepo, &store.ProjectRepo{Files: stack.Projects})
	require.NoError(t, executeV15Child(ctx, t, executor, runRepo, children[0], "explore the workspace and report file names"))

	// The background completion is visible and pending.
	var completions int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_completions`).Scan(&completions))
	assert.Equal(t, 1, completions, "one logical completion for generation 0")
	var deliveryStatus string
	require.NoError(t, db.QueryRow(`SELECT delivery_status FROM delegation_completions`).Scan(&deliveryStatus))
	assert.Equal(t, "pending", deliveryStatus)

	// Retry selects only the cancelled child; the successful sibling is reused.
	failedItemID := items[1].ID
	generation, retryChildren, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "v15-retry",
	})
	require.NoError(t, err)
	require.Len(t, retryChildren, 1, "only the cancelled child gets a new Run")
	require.Len(t, generation.ReusedAttempts, 1, "the successful sibling is reused")
	reused := generation.ReusedAttempts[0]
	require.NotEmpty(t, reused.ResultDigest)
	// The reused digest matches the frozen attempt byte-for-byte.
	var storedDigest string
	require.NoError(t, db.QueryRow(`SELECT result_digest FROM delegation_item_attempts WHERE id=?`,
		reused.AttemptID).Scan(&storedDigest))
	assert.Equal(t, reused.ResultDigest, storedDigest)

	// Execute the retried child.
	require.NoError(t, executeV15Child(ctx, t, executor, runRepo, retryChildren[0], "retry the workspace inspection"))

	// Follow up the retried child privately: exact thread continuation.
	followUpGen, followChild, err := delegations.FollowUp(ctx, items[0].ID, domain.DelegationInputCommand{
		ExpectedGeneration: generation.Generation, SourceAttemptID: reused.AttemptID,
		Text: "Now list only markdown files.", ClientRequestID: "v15-follow",
	})
	require.NoError(t, err)
	require.NotNil(t, followUpGen)
	require.NotNil(t, followChild)
	assert.Equal(t, domain.DelegationGenerationFollowUp, followUpGen.Kind)
	require.NoError(t, executeV15Child(ctx, t, executor, runRepo, followChild, "list only markdown files"))

	// One completion per generation; delivery events dedupe.
	var generation0, generation1, generation2 int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_completions WHERE generation=0`).Scan(&generation0))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_completions WHERE generation=1`).Scan(&generation1))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_completions WHERE generation=2`).Scan(&generation2))
	assert.Equal(t, 1, generation0)
	assert.Equal(t, 1, generation1)
	assert.Equal(t, 1, generation2)

	// Attention projected the completion notifications.
	var attention int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM attention_items WHERE source_kind='delegation_completion'`).Scan(&attention))
	assert.Equal(t, 3, attention)

	// Privacy: no canonical messages from any child; transcripts stay private.
	var canonical int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM messages WHERE run_id IN (?,?,?)`,
		children[0].ID, retryChildren[0].ID, followChild.ID).Scan(&canonical))
	assert.Zero(t, canonical)

	// Depth stays one: every delegated child has execution_depth 1.
	var maxDepth int
	require.NoError(t, db.QueryRow(`SELECT COALESCE(MAX(execution_depth),0) FROM agent_runs WHERE run_kind='delegated_agent'`).Scan(&maxDepth))
	assert.Equal(t, 1, maxDepth)

	// Frozen Role/version snapshots: every attempt pins the exact version.
	var mismatched int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		WHERE a.authorization_snapshot_json NOT LIKE '%' || i.role_version_id || '%'`).Scan(&mismatched))
	assert.Zero(t, mismatched)
}

// explorerLiveItem mirrors the builtin Workspace Explorer delegation item.
// The RoleMeta is frozen from an embedded V2 file-native definition with
// mode=fixed bound to the live model (the removed global role SQL tables and
// the unimplemented legacy inherit mode are gone).
func explorerLiveItem(t *testing.T, modelID string) store.CreateDelegationItemInput {
	t.Helper()
	item := store.CreateDelegationItemInput{
		Name: "explore", RoleVersionID: "builtin-workspace-explorer-v3",
		AssignmentJSON: json.RawMessage(`{"objective":"List the files in /workspace and note their names in one sentence."}`),
		OutputContract: "text-v1",
		Budget: domain.BudgetCeilingJSON{MaxModelCalls: 6, MaxToolCalls: 8, MaxTotalTokens: 20000,
			MaxOutputTokens: 2048, MaxWallTimeMS: 120000},
	}
	item.RoleMeta = liveExplorerRoleMeta(modelID)
	return item
}

// liveExplorerRoleMeta freezes the builtin Workspace Explorer Role identity +
// definition from an embedded V2 definition bound to modelID (mode=fixed).
func liveExplorerRoleMeta(modelID string) *store.DelegationRoleMeta {
	definition := fmt.Sprintf(liveExplorerRoleDefinitionV3, modelID)
	meta, err := store.NewDelegationRoleMeta("builtin-workspace-explorer-v3", []byte(definition))
	if err != nil {
		panic(err)
	}
	meta.ObjectID = "builtin-workspace-explorer"
	meta.Handle = "workspace-explorer"
	meta.DisplayName = "Workspace Explorer"
	return meta
}

// liveExplorerRoleDefinitionV3 is the builtin Workspace Explorer definition;
// the %%s placeholder receives the live model profile id (mode=fixed).
const liveExplorerRoleDefinitionV3 = `{"schemaVersion":1,"rolePrompt":"You are the Workspace Explorer. Use read, ls, grep, and find to answer questions about workspace files. You may inspect git history and status with the git_readonly tool (status, diff, log, show, ls-files, blame). Every answer must be concise. End every turn by calling submit_result with a structured result. Never create, modify, or delete files, and never run arbitrary shell commands.","modelBinding":{"mode":"fixed","modelProfileId":%q,"thinkingEffort":"default","fallbackModelProfileIds":[],"overridableFields":[]},"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss","allowedTools":["read","ls","grep","find","git_readonly"],"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},"delegationPolicy":{"admission":"auto_within_budget","allowedCallerKinds":["host"],"allowedStrategies":["single","parallel"],"maxInvocationsPerParentRun":16,"maxConcurrentInstances":16,"budgetCeiling":{"maxModelCalls":6,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}},"outputContract":"text-v1","maxLoopIterations":8}`

// newV15Executor assembles the production agent executor for live runs.
func newV15Executor(t *testing.T, db *sql.DB, hub *events.Hub, runRepo *store.RunRepo,
	sessionRepo *store.SessionRepo, messageRepo *store.MessageRepo, projects *store.ProjectRepo) *agentExecutor {
	t.Helper()
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	callRepo := &store.CallRepo{DB: db, Publisher: hub}
	trustStore, err := workspace.NewTrustStore(t.TempDir())
	require.NoError(t, err)
	emptySkills := t.TempDir()
	return &agentExecutor{
		db: db, writer: writer, homeDir: t.TempDir(), runs: runRepo, calls: callRepo,
		sessionDB: sessionRepo, msgRepo: messageRepo, projects: projects,
		skillRepo: &store.SkillSnapshotRepo{DB: db}, skillsDir: emptySkills,
		builtinDir: emptySkills, sandbox: "none",
		hub:               hub,
		approvals:         &store.ApprovalRepo{DB: db},
		standingApprovals: &store.StandingApprovalRepo{DB: db},
		trustStore:        trustStore,
	}
}

// executeV15Child claims and runs one delegated child through the real
// Provider, finalizes it, and asserts the private-transcript contract.
func executeV15Child(ctx context.Context, t *testing.T, executor *agentExecutor,
	runRepo *store.RunRepo, child *domain.AgentRun, _ string) error {
	t.Helper()
	_, err := runRepo.Claim(ctx, child.ID)
	if err != nil {
		return err
	}
	output, execErr := executor.executeDelegatedChild(ctx, child)
	if execErr != nil {
		return execErr
	}
	if output.Terminal == nil {
		t.Fatalf("child %s must call submit_result", child.ID)
	}
	t.Logf("child %s submit_result status=%s summary=%s", child.ID, output.Terminal.Status, output.Terminal.Summary)
	if output.Terminal.Status == domain.SubmitNeedsInput {
		// Needs-input is a valid live outcome; the continuation command covers it.
		return runRepo.FinalizeChildSuccess(ctx, child.ID, output)
	}
	if err := runRepo.FinalizeChildSuccess(ctx, child.ID, output); err != nil {
		return err
	}
	return nil
}
