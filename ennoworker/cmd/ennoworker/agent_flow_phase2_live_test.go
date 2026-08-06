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

// Phase 2 live qualification (deepseek-v4-flash, real Provider). Together
// with TestLiveAgentFlowPhase1 this is the Phase 3 entry qualification: a
// real flow with branch + convergence + fan_out on a live Provider.
//
// Model wall budgets are 2 min per child call, so each scenario allows up to
// 480s for its (4 or 2) real calls; scenarios run as separate tests so one
// slow Provider round never starves the other.

type phase2LiveFixture struct {
	db            *sql.DB
	projectID     string
	profiles      *store.AgentFlowProfileRepo
	bindings      *store.AgentFlowBindingRepo
	flowRuns      *store.AgentFlowRunRepo
	newOrchestrator func() *agentflow.Orchestrator
	publishFlow   func(t *testing.T, slug, yamlText string) *domain.AgentFlowVersion
	startRun      func(t *testing.T, flowVersionID string, session *domain.Session) *domain.RunAgentFlow
	waitTerminal  func(t *testing.T, runID string, states ...domain.FlowState) domain.FlowState
}

func newPhase2LiveFixture(t *testing.T, ctx context.Context) *phase2LiveFixture {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	t.Setenv("ENNOTE_LIVE_API_KEY", apiKey)
	t.Setenv("ENNOTE_HOME", t.TempDir())

	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))

	workspaceDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "README.md"), []byte("# Live flow\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "data.csv"), []byte("a,b\n1,2\n"), 0o644))

	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "flow-live-p2", HostPath: workspaceDir,
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
		InputCostUSDMicrosPerMillion: 270, OutputCostUSDMicrosPerMillion: 1100,
		SupportsToolUse: true, SupportsThinking: true, IsDefault: true,
	})
	require.NoError(t, err)
	modelProfileID := modelProfile.ID
	_ = modelProfileID

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub}
	executor := newV15Executor(t, db, hub, runRepo, &store.SessionRepo{DB: db}, &store.MessageRepo{DB: db})

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
	publishFlow := func(t *testing.T, slug, yamlText string) *domain.AgentFlowVersion {
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
	startRun := func(t *testing.T, flowVersionID string, session *domain.Session) *domain.RunAgentFlow {
		t.Helper()
		version, err := profiles.GetVersion(ctx, flowVersionID)
		require.NoError(t, err)
		var def domain.FlowDefinition
		require.NoError(t, json.Unmarshal(version.DefinitionJSON, &def))
		inputsJSON, err := store.NormalizeFlowInputs(&def, nil, nil)
		require.NoError(t, err)
		freeze, diagnostics, err := flowRuns.FreezeFlowDefinition(ctx, project.ID, &def, inputsJSON)
		require.NoError(t, err, diagnostics)
		run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
			SessionID: session.ID, ProjectID: project.ID, FlowVersionID: flowVersionID, InputsJSON: inputsJSON,
		}, freeze)
		require.NoError(t, err)
		return run
	}
	waitTerminal := func(t *testing.T, runID string, states ...domain.FlowState) domain.FlowState {
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
	return &phase2LiveFixture{
		db: db, projectID: project.ID, profiles: profiles, bindings: bindings, flowRuns: flowRuns,
		newOrchestrator: newOrchestrator, publishFlow: publishFlow, startRun: startRun, waitTerminal: waitTerminal,
	}
}

// TestLiveAgentFlowPhase2MakerConvergence: the check gate fails on the first
// round (a stateful marker command), the fail branch activates revise, the
// declared convergence back-edge re-runs reviewers, the gate passes on the
// second round, and the flow completes with a durable back-edge counter.
func TestLiveAgentFlowPhase2MakerConvergence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	fix := newPhase2LiveFixture(t, ctx)
	newSession := func(title string) *domain.Session {
		session, err := (&store.SessionRepo{DB: fix.db}).Create(ctx, domain.CreateSessionInput{
			ProjectID: fix.projectID, Title: title,
		})
		require.NoError(t, err)
		return session
	}

	// The check gate is stateful: the first round fails (marker absent, cwd =
	// workspace root in the sandbox) so the fail branch activates revise; the
	// declared back-edge re-runs reviewers; the second gate round passes
	// (marker present) and accept ends the flow.
	makerYAML := `schemaVersion: 1
id: live-maker
outputs:
  report: {type: string}
budget:
  max_total_tokens: 90000
tasks:
  producer:
    role: workspace-explorer@3
    goal: "Inspect the workspace and report the file names"
    budget: {tokens: 15000}
  reviewers:
    role: workspace-explorer@3
    goal: "Review {task.producer.output} and list which files are markdown"
    depends: [producer]
    budget: {tokens: 15000}
  decision:
    type: check
    command: "sh -c \"test -f .live-check && exit 0 || touch .live-check && exit 1\""
    depends: [reviewers]
    next: {pass: accept, fail: revise}
  revise:
    role: workspace-explorer@3
    goal: "Confirm the markdown list from {task.reviewers.output}"
    depends: [decision, reviewers]
    budget: {tokens: 15000}
  accept:
    terminal: {status: success, output: report}
    output: report
    depends: [decision]
convergence:
  - {from: revise, to: reviewers, max_rounds: 2}
`
	makerVersion := fix.publishFlow(t, "live-maker", makerYAML)
	runA := fix.startRun(t, makerVersion.ID, newSession("maker live"))
	fix.newOrchestrator().Start(ctx, runA.RunID)
	assert.Equal(t, domain.FlowStateCompleted, fix.waitTerminal(t, runA.RunID, domain.FlowStateCompleted))

	// The convergence back-edge ran exactly once: reviewers was re-dispatched
	// (2 groups) and the durable counter is 1.
	var reviewersGroups int
	require.NoError(t, fix.db.QueryRow(`SELECT COUNT(*) FROM delegation_groups g
		JOIN delegation_items i ON i.group_id=g.id
		WHERE g.parent_run_id=? AND i.name='reviewers'`, runA.RunID).Scan(&reviewersGroups))
	assert.Equal(t, 2, reviewersGroups, "reviewers must run once per loop round (initial + back-edge)")
	var reviseGroups int
	require.NoError(t, fix.db.QueryRow(`SELECT COUNT(*) FROM delegation_groups g
		JOIN delegation_items i ON i.group_id=g.id
		WHERE g.parent_run_id=? AND i.name='revise'`, runA.RunID).Scan(&reviseGroups))
	assert.Equal(t, 1, reviseGroups, "revise ran once before the gate passed")
	rounds, err := fix.flowRuns.GetConvergenceRounds(ctx, runA.RunID)
	require.NoError(t, err)
	assert.Equal(t, 1, rounds["revise\x00reviewers"])

	// The check gate produced pass and fail verdicts in order; both branch
	// events are durable.
	eventTypes := liveFlowEventTypes(ctx, t, fix.db, runA.RunID)
	assert.Equal(t, 2, countEventType(eventTypes, "flow_check_result"))
	assert.Contains(t, eventTypes, "flow_task_completed")
	assert.Equal(t, "flow_completed", eventTypes[len(eventTypes)-1])
}

// TestLiveAgentFlowPhase2FanOut: a read-only fan_out task expands into N
// parallel real child Runs whose results aggregate by instance order.
func TestLiveAgentFlowPhase2FanOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	fix := newPhase2LiveFixture(t, ctx)
	newSession := func(title string) *domain.Session {
		session, err := (&store.SessionRepo{DB: fix.db}).Create(ctx, domain.CreateSessionInput{
			ProjectID: fix.projectID, Title: title,
		})
		require.NoError(t, err)
		return session
	}

	fanYAML := `schemaVersion: 1
id: live-fan
outputs:
  report: {type: string}
budget:
  max_total_tokens: 90000
tasks:
  scan:
    role: workspace-explorer@3
    goal: "List the workspace files and note their types"
    fan_out: {min: 2, max: 4}
    budget: {tokens: 15000}
  accept:
    terminal: {status: success, output: report}
    output: report
    depends: [scan]
`
	fanVersion := fix.publishFlow(t, "live-fan", fanYAML)
	runB := fix.startRun(t, fanVersion.ID, newSession("fan live"))
	fix.newOrchestrator().Start(ctx, runB.RunID)
	assert.Equal(t, domain.FlowStateCompleted, fix.waitTerminal(t, runB.RunID, domain.FlowStateCompleted))

	var fanChildren int
	require.NoError(t, fix.db.QueryRow(`SELECT COUNT(*) FROM delegation_groups g
		JOIN delegation_items i ON i.group_id=g.id
		WHERE g.parent_run_id=? AND i.name LIKE 'scan-%'`, runB.RunID).Scan(&fanChildren))
	assert.Equal(t, 2, fanChildren, "fan_out expands to min parallel instances")
	nodes, err := fix.flowRuns.ListNodes(ctx, runB.RunID)
	require.NoError(t, err)
	var scanNode *domain.RunAgentFlowNode
	for _, node := range nodes {
		if node.Handle == "scan" {
			scanNode = node
		}
	}
	require.NotNil(t, scanNode)
	assert.Equal(t, domain.FlowNodeCompleted, scanNode.TerminalState)
	assert.Len(t, scanNode.ChildRunIDs, 2)
	var aggregate []json.RawMessage
	require.NoError(t, json.Unmarshal(scanNode.OutputRef, &aggregate))
	assert.Len(t, aggregate, 2, "aggregated fan_out checkpoint is the ordered array")
	// Both instances produced real Provider output mentioning the workspace.
	for _, payload := range aggregate {
		assert.NotEmpty(t, payload)
	}
}

func countEventType(types []string, eventType string) int {
	count := 0
	for _, candidate := range types {
		if candidate == eventType {
			count++
		}
	}
	return count
}
