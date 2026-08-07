//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveAgentFlowPhase1 qualifies the item 7 Phase 1 vertical slice against
// a real Provider (deepseek-v4-flash):
//
//	Part A: a serial flow (explore -> review -> accept) runs through real
//	  child Runs; the typed handoff resolves {task.explore.output} into the
//	  review goal; the meta-Run completes with durable checkpoints, events,
//	  budget accumulation, and a succeeded anchor.
//	Part B: crash/cancel -> resume replays only unfinished tasks (explore's
//	  completed checkpoint is never re-run; review is re-dispatched once).
//	Part C: an Ask-mode check task suspends on a durable approval, then runs
//	  and completes after the approval is granted.
//
// This is the Phase 2 entry qualification: a real flow run + recovery +
// approval path on a live Provider.
func TestLiveAgentFlowPhase1(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	t.Setenv("ENNOTE_LIVE_API_KEY", apiKey)
	t.Setenv("ENNOTE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()

	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))

	workspaceDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "README.md"), []byte("# Live flow\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "data.csv"), []byte("a,b\n1,2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "notes.txt"), []byte("notes\n"), 0o644))

	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "flow-live", HostPath: workspaceDir,
	})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "live-provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: baseURL, CredentialRef: "env:ENNOTE_LIVE_API_KEY",
	})
	require.NoError(t, err)
	modelProfile, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: model, DisplayName: model,
		ContextWindow: 64000, MaxOutputTokens: 512,
		InputCostUSDMicrosPerMillion:  270,  // approximate deepseek pricing; required for delegation budget
		OutputCostUSDMicrosPerMillion: 1100,
		SupportsToolUse: true, SupportsThinking: true, IsDefault: true,
	})
	require.NoError(t, err)
	modelProfileID := modelProfile.ID
	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "flow live", DefaultModelProfileID: &modelProfileID,
	})
	require.NoError(t, err)

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub}
	sessionRepo := &store.SessionRepo{DB: db}
	messageRepo := &store.MessageRepo{DB: db}
	executor := newV15Executor(t, db, hub, runRepo, sessionRepo, messageRepo)

	profiles := &store.AgentFlowProfileRepo{DB: db}
	bindings := &store.AgentFlowBindingRepo{DB: db}
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{}}
	flowValidator := store.NewFlowValidator(store.FlowPublishOptions{
		DB: db, Skills: map[string]bool{}, CheckAllowlist: []string{"go", "python3", "sh", "bash", "echo"},
	})
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	checkRunner := &store.CheckTaskRunner{
		DB: db, MaxOutputBytes: 32 * 1024, DefaultTimeoutSeconds: 60,
		ManagerBuilder: func(ctx context.Context, sessionID string) (*workspace.Manager, error) {
			session, err := (&store.SessionRepo{DB: db}).FindByID(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			wSpace, err := (&store.ProjectRepo{DB: db}).FindWorkspaceByProjectID(ctx, session.ProjectID)
			if err != nil {
				return nil, err
			}
			canonicalRoot, err := workspace.CanonicalWorkspaceRoot(wSpace.HostPath)
			if err != nil {
				return nil, err
			}
			ioDir := filepath.Join(t.TempDir(), "io")
			if err := os.MkdirAll(ioDir, 0o700); err != nil {
				return nil, err
			}
			return workspace.NewManager(canonicalRoot, ioDir, "", workspace.SandboxNone)
		},
	}
	newOrchestrator := func() *agentflow.Orchestrator {
		return &agentflow.Orchestrator{
			Store:    &store.OrchestratorStore{Runs: flowRuns, Profiles: profiles},
			Children: &store.OrchestratorChildren{DB: db, Delegations: &store.DelegationRepo{DB: db}},
			Events:   &store.FlowEventSink{Writer: writer},
			Checker:  checkRunner,
			Enqueue: func(ctx context.Context, runID string) error {
				return executeLiveFlowChild(ctx, t, executor, runRepo, runID)
			},
			PollInterval: 150 * time.Millisecond,
		}
	}
	publishFlow := func(slug, yamlText string) *domain.AgentFlowVersion {
		t.Helper()
		def, err := agentflow.ParseDefinition([]byte(yamlText))
		require.NoError(t, err)
		profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
			Name: slug, Slug: slug, SourceKind: domain.FlowSourceManaged,
		})
		require.NoError(t, err)
		_, err = profiles.UpdateDraft(ctx, profile.ID, def, yamlText, 0)
		require.NoError(t, err)
		version, err := profiles.Publish(ctx, profile.ID, 1, flowValidator)
		require.NoError(t, err)
		binding, err := bindings.EnsureBindingExists(ctx, project.ID, version.ID)
		require.NoError(t, err)
		_, err = bindings.Update(ctx, binding.ID, true)
		require.NoError(t, err)
		return version
	}
	startRun := func(flowVersionID string, inputs map[string]any) *domain.RunAgentFlow {
		t.Helper()
		version, err := profiles.GetVersion(ctx, flowVersionID)
		require.NoError(t, err)
		var def domain.FlowDefinition
		require.NoError(t, json.Unmarshal(version.DefinitionJSON, &def))
		inputsJSON, err := store.NormalizeFlowInputs(&def, inputs, nil)
		require.NoError(t, err)
		freeze, diagnostics, err := flowRuns.FreezeFlowDefinition(ctx, project.ID, version.ProfileID, &def, inputsJSON)
		require.NoError(t, err, diagnostics)
		run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
			SessionID: session.ID, ProjectID: project.ID, FlowVersionID: flowVersionID, InputsJSON: inputsJSON,
		}, freeze)
		require.NoError(t, err)
		return run
	}
	waitTerminal := func(runID string, states ...domain.FlowState) domain.FlowState {
		t.Helper()
		deadline := time.Now().Add(480 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				t.Fatalf("test context expired before flow %s terminalized", runID)
			default:
			}
			current, err := flowRuns.GetRun(ctx, runID)
			require.NoError(t, err)
			for _, expected := range states {
				if current.State == expected {
					return expected
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
		t.Fatalf("flow run %s did not reach terminal state", runID)
		return ""
	}
	waitNode := func(runID, handle string, want domain.FlowNodeState) {
		t.Helper()
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			nodes, err := flowRuns.ListNodes(ctx, runID)
			require.NoError(t, err)
			for _, node := range nodes {
				if node.Handle == handle && node.TerminalState == want {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("node %s in run %s did not reach %s", handle, runID, want)
	}

	// ---- Part A: real serial flow with typed handoff ----
	serialYAML := `schemaVersion: 1
id: live-flow
inputs:
  query: {type: string, required: true}
outputs:
  report: {type: string}
budget:
  max_total_tokens: 80000
tasks:
  explore:
    role: workspace-explorer@3
    goal: "Inspect the workspace for {inputs.query} and report the file names"
    budget: {tokens: 15000}
  review:
    role: workspace-explorer@3
    goal: "Using {task.explore.output}, decide which files are markdown"
    depends: [explore]
    budget: {tokens: 15000}
  accept:
    terminal: {status: success, output: report}
    output: report
    depends: [review]
`
	serialVersion := publishFlow("live-flow", serialYAML)
	runA := startRun(serialVersion.ID, map[string]any{"query": "data files"})
	newOrchestrator().Start(ctx, runA.RunID)
	assert.Equal(t, domain.FlowStateCompleted, waitTerminal(runA.RunID, domain.FlowStateCompleted))

	// Every executable node checkpoint is completed; the terminal gate node
	// stays pending (it is the flow-completion signal, not an executable
	// task). The anchor ended succeeded.
	nodesA, err := flowRuns.ListNodes(ctx, runA.RunID)
	require.NoError(t, err)
	byHandleA := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodesA {
		byHandleA[node.Handle] = node
	}
	assert.Equal(t, domain.FlowNodeCompleted, byHandleA["explore"].TerminalState)
	assert.Equal(t, domain.FlowNodeCompleted, byHandleA["review"].TerminalState)
	assert.Equal(t, domain.FlowNodePending, byHandleA["accept"].TerminalState, "terminal gate is not an executable node")
	var anchorStatus string
	require.NoError(t, db.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runA.RunID).Scan(&anchorStatus))
	assert.Equal(t, "succeeded", anchorStatus)

	// Typed handoff: the review child assignment embeds explore's output.
	var reviewAssignment string
	require.NoError(t, db.QueryRow(`SELECT assignment_json FROM delegation_items WHERE name='review'
		AND child_run_id IN (SELECT id FROM agent_runs WHERE root_run_id=?)`, runA.RunID).Scan(&reviewAssignment))
	assert.Contains(t, reviewAssignment, "README", "review goal must embed explore's output")

	// Budget accumulated from real child usage.
	runAFinal, err := flowRuns.GetRun(ctx, runA.RunID)
	require.NoError(t, err)
	assert.Greater(t, runAFinal.TotalTokensUsed, int64(0), "flow budget must accumulate real child usage")

	// Durable events are consumable by the timeline.
	eventTypes := liveFlowEventTypes(ctx, t, db, runA.RunID)
	assert.Contains(t, eventTypes, "flow_started")
	assert.Contains(t, eventTypes, "flow_task_started")
	assert.Contains(t, eventTypes, "flow_task_completed")
	assert.Equal(t, "flow_completed", eventTypes[len(eventTypes)-1])

	// ---- Part B: cancel -> resume replays only unfinished tasks ----
	runB := startRun(serialVersion.ID, map[string]any{"query": "data files"})
	newOrchestrator().Start(ctx, runB.RunID)
	waitNode(runB.RunID, "explore", domain.FlowNodeCompleted)
	// Cancel: durable flag + hard-cancel any in-flight child.
	require.NoError(t, flowRuns.SetCancelRequested(ctx, runB.RunID))
	nodesB, err := flowRuns.ListNodes(ctx, runB.RunID)
	require.NoError(t, err)
	for _, node := range nodesB {
		if node.TerminalState == domain.FlowNodeRunning && node.ChildRunID != "" {
			_ = runRepo.Cancel(ctx, node.ChildRunID)
		}
	}
	assert.Equal(t, domain.FlowStateCancelled, waitTerminal(runB.RunID, domain.FlowStateCancelled))
	require.NoError(t, waitNodeCancelSettled(ctx, t, flowRuns, runB.RunID))

	// Resume with a FRESH orchestrator (worker-restart semantics).
	_, err = flowRuns.ResumeFlowRun(ctx, runB.RunID)
	require.NoError(t, err)
	newOrchestrator().Start(ctx, runB.RunID)
	assert.Equal(t, domain.FlowStateCompleted, waitTerminal(runB.RunID, domain.FlowStateCompleted))

	// explore's completed checkpoint was NEVER replayed: exactly one explore
	// delegation group exists for this run (review may have been dispatched
	// once or twice depending on the cancel race, but explore is fixed).
	var exploreGroups int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_groups g
		JOIN delegation_items i ON i.group_id=g.id
		WHERE g.parent_run_id=? AND i.name='explore'`, runB.RunID).Scan(&exploreGroups))
	assert.Equal(t, 1, exploreGroups, "completed task must not be replayed on resume")

	// ---- Part C: Ask-mode check task suspends on approval ----
	_, err = db.ExecContext(ctx, `INSERT OR REPLACE INTO settings (key,value)
		VALUES ('default_tool_policy_profile_id','builtin-tool-ask-v1')`)
	require.NoError(t, err)
	checkYAML := `schemaVersion: 1
id: check-flow
outputs:
  report: {type: string}
budget:
  max_total_tokens: 40000
tasks:
  gate:
    type: check
    command: "echo live-check-ok"
  worker:
    role: workspace-explorer@3
    goal: "Inspect the workspace and report its files"
    depends: [gate]
  accept:
    terminal: {status: success, output: report}
    output: report
    depends: [worker]
`
	checkVersion := publishFlow("check-flow", checkYAML)
	runC := startRun(checkVersion.ID, nil)
	newOrchestrator().Start(ctx, runC.RunID)
	// The check gate suspends on a durable approval (Ask mode).
	waitApproval(ctx, t, checkRunner, runC.RunID, 0)
	status, err := checkRunner.DecideCheckApproval(ctx, runC.RunID, 0, true, "live-approve")
	require.NoError(t, err)
	assert.Equal(t, agentflow.CheckApprovalApproved, status)
	assert.Equal(t, domain.FlowStateCompleted, waitTerminal(runC.RunID, domain.FlowStateCompleted))
	gateNode, err := flowRuns.GetNode(ctx, runC.RunID, 0)
	require.NoError(t, err)
	assert.Equal(t, domain.FlowNodeCompleted, gateNode.TerminalState)
	var gateOut struct {
		Pass bool `json:"pass"`
	}
	require.NoError(t, json.Unmarshal(gateNode.OutputRef, &gateOut))
	assert.True(t, gateOut.Pass, "check gate must pass after approval")
}

// executeLiveFlowChild claims and runs one flow child through the real
// Provider and settles its terminal contract, mirroring the coordinator's
// child finalization path.
func executeLiveFlowChild(ctx context.Context, t *testing.T, executor *agentExecutor,
	runRepo *store.RunRepo, runID string) error {
	t.Helper()
	child, err := runRepo.Get(ctx, runID)
	if err != nil {
		return err
	}
	if _, err := runRepo.Claim(ctx, runID); err != nil {
		return err
	}
	output, execErr := executor.executeDelegatedChild(ctx, child)
	if execErr != nil {
		_, _, finalizeErr := runRepo.FinalizeChildFailure(ctx, runID, "exec_failed", execErr.Error())
		return finalizeErr
	}
	if output.Terminal == nil {
		_, _, finalizeErr := runRepo.FinalizeChildFailure(ctx, runID, "incomplete_terminal_contract",
			"flow child did not call submit_result")
		return finalizeErr
	}
	t.Logf("flow child %s submit_result status=%s summary=%s", runID, output.Terminal.Status, output.Terminal.Summary)
	return runRepo.FinalizeChildSuccess(ctx, runID, output)
}

func waitNodeCancelSettled(ctx context.Context, t *testing.T, flowRuns *store.AgentFlowRunRepo, runID string) error {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var running int
		err := flowRuns.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_agent_flow_nodes
			WHERE run_id=? AND terminal_state='running'`, runID).Scan(&running)
		if err != nil {
			return err
		}
		if running == 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("flow run %s still has running nodes after cancel", runID)
	return nil
}

func waitApproval(ctx context.Context, t *testing.T, checkRunner *store.CheckTaskRunner, runID string, taskIndex int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, err := checkRunner.CheckApprovalStatus(ctx, runID, taskIndex)
		require.NoError(t, err)
		if status == agentflow.CheckApprovalPending {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("check approval for run %s task %d never became pending", runID, taskIndex)
}

func liveFlowEventTypes(ctx context.Context, t *testing.T, db *sql.DB, runID string) []string {
	t.Helper()
	eventsRepo := &store.EventRepo{DB: db}
	committed, err := eventsRepo.After(ctx, runID, 0, 200)
	require.NoError(t, err)
	types := make([]string, 0, len(committed))
	for _, event := range committed {
		types = append(types, event.EventType)
	}
	return types
}
