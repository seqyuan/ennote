package graphrun_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type e2eChildren struct {
	mu        sync.Mutex
	created   []string
	statuses  map[string]domain.RunStatus
	succeed   bool
	recovered bool
	old       map[string]bool
}

func newE2EChildren(succeed bool) *e2eChildren {
	return &e2eChildren{statuses: map[string]domain.RunStatus{}, succeed: succeed, old: map[string]bool{}}
}

func (c *e2eChildren) CreateTaskChild(_ context.Context, _ string, _ string, spec agentflow.ChildSpec) (agentflow.ChildInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := fmt.Sprintf("child-%d", len(c.created))
	c.created = append(c.created, spec.Handle)
	c.statuses[id] = domain.RunQueued
	if c.recovered {
		c.old[id] = false
	}
	return agentflow.ChildInfo{RunID: id}, nil
}

func (c *e2eChildren) ChildRunStatus(_ context.Context, id string) (domain.RunStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if status, ok := c.statuses[id]; ok && status.Terminal() {
		return status, nil
	}
	if c.succeed {
		c.statuses[id] = domain.RunSucceeded
		return domain.RunSucceeded, nil
	}
	if c.recovered {
		if old, ok := c.old[id]; ok && old {
			c.statuses[id] = domain.RunInterrupted
			return domain.RunInterrupted, nil
		}
		c.statuses[id] = domain.RunSucceeded
		return domain.RunSucceeded, nil
	}
	return domain.RunRunning, nil
}

func (c *e2eChildren) ChildTerminalResult(_ context.Context, id string) (*domain.SubmitResult, error) {
	return &domain.SubmitResult{Status: domain.SubmitCompleted, Payload: json.RawMessage(`{"summary":"` + id + `"}`)}, nil
}
func (c *e2eChildren) ChildUsage(_ context.Context, _ string) (domain.RunBudgetUsage, error) {
	return domain.RunBudgetUsage{Tokens: 10}, nil
}
func (c *e2eChildren) CancelChildRun(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses[id] = domain.RunCancelled
	return nil
}
func (c *e2eChildren) hasHandle(handle string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, value := range c.created {
		if value == handle {
			return true
		}
	}
	return false
}
func (c *e2eChildren) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.created)
}

type e2eChecker struct {
	db         *sql.DB
	ask        bool
	mu         sync.Mutex
	executions int
}

func (c *e2eChecker) CheckPolicyForSession(context.Context, string) (*agentflow.CheckPolicy, error) {
	mode := "allow_existing_behavior"
	if c.ask {
		mode = string(domain.PermissionAsk)
	}
	return &agentflow.CheckPolicy{Mode: mode, AllowedExecutables: []string{"test"}}, nil
}
func (c *e2eChecker) CreateCheckApproval(ctx context.Context, runID string, taskIndex int, command string) error {
	return (&store.CheckTaskRunner{DB: c.db}).CreateCheckApproval(ctx, runID, taskIndex, command)
}
func (c *e2eChecker) CheckApprovalStatus(ctx context.Context, runID string, taskIndex int) (agentflow.CheckApprovalStatus, error) {
	return (&store.CheckTaskRunner{DB: c.db}).CheckApprovalStatus(ctx, runID, taskIndex)
}
func (c *e2eChecker) ExecuteCheck(_ context.Context, command string, _ int) (*agentflow.CheckOutcome, error) {
	c.mu.Lock()
	c.executions++
	c.mu.Unlock()
	return &agentflow.CheckOutcome{Pass: true, ExitCode: 0, Summary: "ok", Command: command}, nil
}
func (c *e2eChecker) executionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.executions
}

type e2eEvents struct{}

func (e2eEvents) PublishFlow(context.Context, string, string, map[string]any) error { return nil }

type e2eFlow struct {
	fixture  *flowRunFixture
	children *e2eChildren
	checker  *e2eChecker
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
}

func newE2EFlow(t *testing.T, children *e2eChildren, checker *e2eChecker) *e2eFlow {
	t.Helper()
	fixture := newFlowRunFixture(t)
	flow := &e2eFlow{fixture: fixture, children: children, checker: checker}
	flow.resetContext()
	return flow
}

func (f *e2eFlow) resetContext() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil {
		f.cancel()
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())
}

func (f *e2eFlow) service() *graphrun.Service {
	return &graphrun.Service{
		Sources: f.fixture.sources, Models: f.fixture.models, Sessions: f.fixture.sessions, Projects: f.fixture.projects,
		OnRunStarted: func(_ context.Context, db *sql.DB, _ string, runID string) error {
			f.mu.Lock()
			ctx := f.ctx
			f.mu.Unlock()
			orchestrator := &agentflow.Orchestrator{
				Store:    &store.OrchestratorStore{Runs: &store.AgentFlowRunRepo{DB: db}},
				Children: f.children, Events: e2eEvents{}, Checker: f.checker,
				PollInterval: 5 * time.Millisecond,
			}
			orchestrator.Start(ctx, runID)
			return nil
		},
	}
}

func waitFlowTerminal(t *testing.T, db *sql.DB, runID string) *domain.RunAgentFlow {
	t.Helper()
	repo := &store.AgentFlowRunRepo{DB: db}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := repo.GetRun(context.Background(), runID)
		require.NoError(t, err)
		if run.State.Terminal() {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.FailNow(t, "flow did not terminalize")
	return nil
}

func TestE2EFullFlowCheckFanOutTerminal(t *testing.T) {
	children := newE2EChildren(true)
	flow := newE2EFlow(t, children, &e2eChecker{ask: false})
	defer flow.cancel()
	revision := flow.fixture.publish(t, "e2e-flow", true, "e2e prompt")
	flow.fixture.bind(t, "e2e-flow", revision.ID(), true)
	// The checker needs the session DB, which is opened by the service.
	flow.checker.db = openFlowDB(t, flow.fixture)

	run, err := flow.service().StartFlow(context.Background(), flow.fixture.projectID, "e2e-flow", 0, flow.fixture.sessionID, nil, nil)
	require.NoError(t, err)
	final := waitFlowTerminal(t, flow.checker.db, run.RunID)
	assert.Equal(t, domain.FlowStateCompleted, final.State, final.TerminalReason)
	assert.True(t, children.hasHandle("review"))
	assert.True(t, children.hasHandle("scan-0"))
	assert.True(t, children.hasHandle("scan-1"))
	assert.Equal(t, 1, flow.checker.executionCount())

	nodes, err := (&store.AgentFlowRunRepo{DB: flow.checker.db}).ListNodes(context.Background(), run.RunID)
	require.NoError(t, err)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodes {
		byHandle[node.Handle] = node
	}
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["review"].TerminalState)
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["gate"].TerminalState)
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["scan"].TerminalState)
	assert.Equal(t, domain.FlowNodePending, byHandle["done"].TerminalState, "terminal gate completes the flow without a child")
	assert.Len(t, byHandle["scan"].ChildRunIDs, 2)
}

func TestE2EWorkerRestartResumesRoleTask(t *testing.T) {
	children := newE2EChildren(false)
	flow := newE2EFlow(t, children, &e2eChecker{ask: false})
	defer flow.cancel()
	revision := flow.fixture.publish(t, "resume-flow", false, "resume prompt")
	flow.fixture.bind(t, "resume-flow", revision.ID(), true)
	flow.checker.db = openFlowDB(t, flow.fixture)

	service := flow.service()
	run, err := service.StartFlow(context.Background(), flow.fixture.projectID, "resume-flow", 0, flow.fixture.sessionID, nil, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return children.count() > 0 }, 5*time.Second, 10*time.Millisecond)
	// Simulate a Worker stop: cancel the orchestrator context, leaving the node
	// running with its child recorded.
	flow.cancel()
	time.Sleep(20 * time.Millisecond)

	// New Worker: the old child is interrupted, the node is reset, and a new
	// child completes.
	children.mu.Lock()
	for id := range children.statuses {
		children.old[id] = true
	}
	children.recovered = true
	children.succeed = true
	children.mu.Unlock()
	flow.resetContext()
	require.NoError(t, service.RecoverSession(context.Background(), flow.fixture.sessionID))
	final := waitFlowTerminal(t, flow.checker.db, run.RunID)
	assert.Equal(t, domain.FlowStateCompleted, final.State, final.TerminalReason)
}

func TestE2ECheckApprovalSurvivesRestart(t *testing.T) {
	children := newE2EChildren(true)
	flow := newE2EFlow(t, children, &e2eChecker{ask: true})
	defer flow.cancel()
	revision := flow.fixture.publish(t, "approval-flow", false, "approval prompt")
	flow.fixture.bind(t, "approval-flow", revision.ID(), true)
	flow.checker.db = openFlowDB(t, flow.fixture)

	service := flow.service()
	run, err := service.StartFlow(context.Background(), flow.fixture.projectID, "approval-flow", 0, flow.fixture.sessionID, nil, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		status, err := flow.checker.CheckApprovalStatus(context.Background(), run.RunID, 1)
		return err == nil && status == agentflow.CheckApprovalPending
	}, 5*time.Second, 10*time.Millisecond)
	flow.cancel()
	time.Sleep(20 * time.Millisecond)

	_, err = (&store.CheckTaskRunner{DB: flow.checker.db}).DecideCheckApproval(context.Background(), run.RunID, 1, true, "e2e-approve")
	require.NoError(t, err)
	flow.resetContext()
	require.NoError(t, service.RecoverSession(context.Background(), flow.fixture.sessionID))
	final := waitFlowTerminal(t, flow.checker.db, run.RunID)
	assert.Equal(t, domain.FlowStateCompleted, final.State, final.TerminalReason)
	assert.Equal(t, 1, flow.checker.executionCount())
}

func openFlowDB(t *testing.T, fixture *flowRunFixture) *sql.DB {
	t.Helper()
	db, err := fixture.sessions.OpenSession(context.Background(), fixture.sessionID)
	require.NoError(t, err)
	return db
}
