package agentflow

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Fakes ---

type fakeStore struct {
	mu              sync.Mutex
	run             *domain.RunAgentFlow
	nodes           []*domain.RunAgentFlowNode
	version         *domain.AgentFlowVersion
	cancelRequested bool
	anchorStatus    domain.RunStatus
}

func newFakeStore(def *domain.FlowDefinition) (*fakeStore, error) {
	digest, err := ConfigDigest(def)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(def)
	inputsJSON := json.RawMessage(`{"inputs":{"target":"src/main.go"},"vars":{"mode":"fast"}}`)
	manifestDigest, _ := ManifestDigest(digest, inputsJSON)
	run := &domain.RunAgentFlow{
		RunID: "flow-run-1", SessionID: "session-1", ProjectID: "project-1",
		FlowVersionID: "version-1", ManifestDigest: manifestDigest,
		State: domain.FlowStatePending, InputsJSON: inputsJSON,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	order, err := TopologicalOrder(def.Tasks)
	if err != nil {
		return nil, err
	}
	nodes := make([]*domain.RunAgentFlowNode, 0, len(order))
	for i, name := range order {
		task := def.Tasks[name]
		nodes = append(nodes, &domain.RunAgentFlowNode{
			RunID: run.RunID, TaskIndex: i, Handle: name,
			GoalDigest: TaskGoalDigest(task.Goal), TerminalState: domain.FlowNodePending,
			BudgetJSON: json.RawMessage(`{"maxModelCalls":4,"maxToolCalls":8,"maxTotalTokens":50000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}`),
			CreatedAt:  time.Now(),
		})
	}
	return &fakeStore{
		run: run, nodes: nodes,
		version: &domain.AgentFlowVersion{ID: "version-1", ConfigDigest: digest, DefinitionJSON: encoded},
	}, nil
}

func (f *fakeStore) GetRun(ctx context.Context, runID string) (*domain.RunAgentFlow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	clone := *f.run
	return &clone, nil
}

func (f *fakeStore) GetVersion(ctx context.Context, versionID string) (*domain.AgentFlowVersion, error) {
	return f.version, nil
}

func (f *fakeStore) ListNodes(ctx context.Context, runID string) ([]*domain.RunAgentFlowNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*domain.RunAgentFlowNode, len(f.nodes))
	for i := range f.nodes {
		clone := *f.nodes[i]
		out[i] = &clone
	}
	return out, nil
}

func (f *fakeStore) UpdateNode(ctx context.Context, runID string, upd NodeUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.nodes {
		if f.nodes[i].TaskIndex != upd.TaskIndex {
			continue
		}
		if len(upd.ExpectedStates) > 0 {
			ok := false
			for _, expected := range upd.ExpectedStates {
				if f.nodes[i].TerminalState == expected {
					ok = true
					break
				}
			}
			if !ok {
				return errFakeConflict
			}
		}
		f.nodes[i].TerminalState = upd.SetState
		if upd.ChildRunID != "" {
			f.nodes[i].ChildRunID = upd.ChildRunID
		}
		if upd.OutputRef != nil {
			f.nodes[i].OutputRef = upd.OutputRef
		}
		if upd.GoalText != "" {
			f.nodes[i].GoalText = upd.GoalText
		}
		if upd.ErrorCode != "" {
			f.nodes[i].ErrorCode = upd.ErrorCode
		}
		return nil
	}
	return errFakeConflict
}

func (f *fakeStore) UpdateFlowState(ctx context.Context, runID string, state domain.FlowState,
	totalTokens int64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run.State = state
	f.run.TotalTokensUsed = totalTokens
	f.run.TerminalReason = reason
	return nil
}

func (f *fakeStore) AddTokenUsage(ctx context.Context, runID string, tokens int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run.TotalTokensUsed += tokens
	return f.run.TotalTokensUsed, nil
}

func (f *fakeStore) SetCancelRequested(ctx context.Context, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelRequested = true
	return nil
}

func (f *fakeStore) IsCancelRequested(ctx context.Context, runID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelRequested, nil
}

func (f *fakeStore) SetAnchorRunning(ctx context.Context, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anchorStatus = domain.RunRunning
	return nil
}

func (f *fakeStore) TerminateAnchor(ctx context.Context, runID string, status domain.RunStatus, code, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anchorStatus = status
	return nil
}

var errFakeConflict = &conflictError{}

type conflictError struct{}

func (e *conflictError) Error() string { return "fake node state conflict" }

// fakeChildren auto-settles each created child with a per-handle result.
type fakeChildren struct {
	mu          sync.Mutex
	created     []ChildSpec
	results     map[string]*domain.SubmitResult
	status      map[string]domain.RunStatus
	usage       map[string]domain.RunBudgetUsage
	runIDSeq    int
	failHandles map[string]bool
	// blockedHandles: children for these handles stay running until cancelled
	// or explicitly settled by the test.
	blockedHandles map[string]bool
}

func newFakeChildren() *fakeChildren {
	return &fakeChildren{results: map[string]*domain.SubmitResult{}, status: map[string]domain.RunStatus{}, usage: map[string]domain.RunBudgetUsage{}, failHandles: map[string]bool{}, blockedHandles: map[string]bool{}}
}

func (c *fakeChildren) CreateTaskChild(ctx context.Context, parentRunID, sessionID string, spec ChildSpec) (ChildInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created = append(c.created, spec)
	c.runIDSeq++
	id := "child-" + spec.Handle
	switch {
	case c.blockedHandles[spec.Handle]:
		c.status[id] = domain.RunRunning
	case c.failHandles[spec.Handle]:
		c.status[id] = domain.RunFailed
	default:
		c.status[id] = domain.RunSucceeded
	}
	if c.results[spec.Handle] == nil {
		c.results[spec.Handle] = &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "done", Payload: json.RawMessage(`{"changedFiles":["a.go"]}`)}
	}
	if c.usage[spec.Handle].Tokens == 0 {
		c.usage[spec.Handle] = domain.RunBudgetUsage{Tokens: 1000}
	}
	return ChildInfo{RunID: id, ItemID: "item-" + spec.Handle, GroupID: "group-" + spec.Handle}, nil
}

func (c *fakeChildren) ChildRunStatus(ctx context.Context, runID string) (domain.RunStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status, ok := c.status[runID]
	if !ok {
		return domain.RunQueued, nil
	}
	return status, nil
}

func (c *fakeChildren) ChildTerminalResult(ctx context.Context, runID string) (*domain.SubmitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	handle := runID[len("child-"):]
	result := c.results[handle]
	if result == nil {
		return nil, nil
	}
	clone := *result
	return &clone, nil
}

func (c *fakeChildren) ChildUsage(ctx context.Context, runID string) (domain.RunBudgetUsage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	handle := runID[len("child-"):]
	return c.usage[handle], nil
}

func (c *fakeChildren) CancelChildRun(ctx context.Context, runID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.status[runID]; ok {
		c.status[runID] = domain.RunCancelled
	}
	return nil
}

type fakeEvents struct {
	mu      sync.Mutex
	events  []string
	payload map[string]map[string]any
}

func (e *fakeEvents) PublishFlow(ctx context.Context, runID string, eventType string, payload map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, eventType)
	if e.payload == nil {
		e.payload = map[string]map[string]any{}
	}
	e.payload[eventType] = payload
	return nil
}

func (e *fakeEvents) types() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.events))
	copy(out, e.events)
	return out
}

func (e *fakeEvents) payloadOf(eventType string) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.payload[eventType]
}

// --- Fixture ---

func threeTaskDef() *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: "maker",
		Inputs:  map[string]domain.FlowPort{"target": {Type: domain.PortTypePath, Required: true}},
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskRole, Role: "writer@1",
				Goal: "Implement {inputs.target} and {flow.vars.mode}", Budget: &domain.FlowTaskBudget{Tokens: 50000}},
			"reviewer": {Type: domain.FlowTaskRole, Role: "reader@1",
				Goal: "Review {task.producer.output.changedFiles}", Depends: []string{"producer"}},
			"accept": {Type: domain.FlowTaskRole, Depends: []string{"reviewer"},
				Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}},
		},
	}
}

func newOrchestrator(fake *fakeStore, children *fakeChildren, events *fakeEvents) *Orchestrator {
	return &Orchestrator{
		Store: fake, Children: children, Events: events,
		PollInterval: 5 * time.Millisecond,
	}
}

func waitTerminal(t *testing.T, fake *fakeStore) domain.FlowState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := fake.GetRun(context.Background(), "flow-run-1")
		if run.State.Terminal() {
			return run.State
		}
		time.Sleep(5 * time.Millisecond)
	}
	run, _ := fake.GetRun(context.Background(), "flow-run-1")
	t.Fatalf("flow did not reach a terminal state; state=%s", run.State)
	return ""
}

// Matrix 8+9: serial topology, typed handoff, unique entry dispatch.
func TestOrchestratorSerialExecutionAndHandoff(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	require.Len(t, children.created, 2, "producer+reviewer are role tasks; accept is terminal")
	assert.Equal(t, "Implement src/main.go and fast", children.created[0].Assignment)
	assert.Equal(t, "Review [\"a.go\"]", children.created[1].Assignment)
	assert.Equal(t, domain.RunSucceeded, fake.anchorStatus)
	// Node checkpoints.
	nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, n := range nodes {
		byHandle[n.Handle] = n
	}
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["producer"].TerminalState)
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["reviewer"].TerminalState)
	eventsList := events.types()
	assert.Contains(t, eventsList, "flow_started")
	assert.Contains(t, eventsList, "flow_task_started")
	assert.Contains(t, eventsList, "flow_task_completed")
	assert.Contains(t, eventsList, "flow_completed")
}

// Task failure: completed tasks keep their checkpoints, flow fails.
func TestOrchestratorTaskFailureFailsFlow(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.failHandles["reviewer"] = true
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateFailed, state)
	nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, n := range nodes {
		byHandle[n.Handle] = n
	}
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["producer"].TerminalState, "completed task checkpoint stays")
	assert.Equal(t, domain.FlowNodeFailed, byHandle["reviewer"].TerminalState)
	assert.Contains(t, events.types(), "flow_task_failed")
	assert.Contains(t, events.types(), "flow_failed")
}

// Matrix 14: checkpoint recovery — completed tasks are not replayed; their
// outputs feed the next tasks.
func TestOrchestratorCheckpointResume(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	// Simulate a crash after producer completed: its checkpoint is durable.
	producerNode, err := fake.GetNode(context.Background(), "flow-run-1", 0)
	require.NoError(t, err)
	assert.NotNil(t, producerNode)
	err = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, OutputRef: json.RawMessage(`{"changedFiles":["crashed.go"]}`),
	})
	require.NoError(t, err)
	// Reviewer node is pending again (interrupted on restart).
	_ = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 1, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeInterrupted,
	})
	_ = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 1, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeInterrupted},
		SetState: domain.FlowNodePending,
	})
	run, _ := fake.GetRun(context.Background(), "flow-run-1")
	run.State = domain.FlowStateRunning // crash left the flow running

	orch := newOrchestrator(fake, children, events)
	orch.Recover(context.Background(), []string{"flow-run-1"})
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	// Producer was NOT re-dispatched; reviewer consumed its checkpoint output.
	require.Len(t, children.created, 1)
	assert.Equal(t, "reviewer", children.created[0].Handle)
	assert.Equal(t, "Review [\"crashed.go\"]", children.created[0].Assignment)
}

// Matrix 13: cancel — active child hard-cancelled, future tasks never
// scheduled, meta-Run cancelled.
func TestOrchestratorCancelPropagation(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.blockedHandles["producer"] = true // producer never settles on its own
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)

	// The first child never settles; the orchestrator's poll sees the durable
	// cancel flag and hard-cancels it.
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = fake.SetCancelRequested(context.Background(), "flow-run-1")
	}()
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCancelled, state)
	// Only the first task was dispatched; future tasks were never scheduled.
	assert.Equal(t, 1, len(children.created))
	nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, n := range nodes {
		byHandle[n.Handle] = n
	}
	assert.Equal(t, domain.FlowNodeCancelled, byHandle["producer"].TerminalState)
	assert.Equal(t, domain.FlowNodeCancelled, byHandle["reviewer"].TerminalState)
	assert.Equal(t, domain.RunCancelled, fake.anchorStatus)
}

// Matrix 12: budget exceeded -> budget_exceeded terminal state.
func TestOrchestratorBudgetExceeded(t *testing.T) {
	def := threeTaskDef()
	def.Budget.MaxTotalTokens = 500 // tiny: producer's 1000-token usage exceeds it
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateBudgetExceeded, state)
	assert.Contains(t, events.types(), "flow_failed")
}

// Goal template resolution unit tests.
func TestResolveGoalTemplate(t *testing.T) {
	inputs := map[string]any{"target": "src/a.go", "count": 3}
	vars := map[string]any{"mode": "fast"}
	outputs := map[string]json.RawMessage{
		"producer": json.RawMessage(`{"changedFiles":["a.go","b.go"],"meta":{"ok":true}}`),
	}
	resolved, err := ResolveGoalTemplate("do {inputs.target} n={inputs.count} with {task.producer.output.changedFiles} {flow.vars.mode} {task.producer.output.meta.ok}",
		inputs, vars, outputs)
	require.NoError(t, err)
	assert.Equal(t, `do src/a.go n=3 with ["a.go","b.go"] fast true`, resolved)
	_, err = ResolveGoalTemplate("use {task.other.output.x}", inputs, vars, outputs)
	require.Error(t, err)
	_, err = ResolveGoalTemplate("use {inputs.missing}", inputs, vars, outputs)
	require.Error(t, err)
	_, err = ResolveGoalTemplate("use {flow.vars.nope}", inputs, vars, outputs)
	require.Error(t, err)
	// Unresolvable dependency output fails loudly (no silent empty string).
	_, err = ResolveGoalTemplate("use {task.producer.output.missingField}", inputs, vars, outputs)
	require.Error(t, err)
}

// GetNode returns one node (fake).
func (f *fakeStore) GetNode(ctx context.Context, runID string, taskIndex int) (*domain.RunAgentFlowNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.nodes {
		if f.nodes[i].TaskIndex == taskIndex {
			clone := *f.nodes[i]
			return &clone, nil
		}
	}
	return nil, errFakeConflict
}

// Matrix 15: resume identity — the frozen manifest digest must match on
// resume; a drifted version fails closed (interrupted), never silently
// switching to a newer definition.
func TestOrchestratorResumeIdentityMismatch(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	run, _ := fake.GetRun(context.Background(), "flow-run-1")
	run.State = domain.FlowStateRunning // crash mid-run
	// Drift the frozen manifest identity (as if the version were rewritten).
	fake.mu.Lock()
	fake.run.ManifestDigest = "sha256:drifted"
	fake.mu.Unlock()

	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateFailed, state)
	terminal, _ := fake.GetRun(context.Background(), "flow-run-1")
	assert.Contains(t, terminal.TerminalReason, "manifest identity mismatch")
	assert.Empty(t, children.created, "no child was dispatched after identity drift")
	assert.Contains(t, events.types(), "flow_failed")
}

// Matrix 16: the Phase 1 event set is emitted in order and the durable event
// log is the timeline source. (Store-level persistence + EventWriter
// commit-before-publish is covered in the store integration test; here we
// assert the orchestrator emission order.)
func TestOrchestratorEventSequence(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	waitTerminal(t, fake)
	types := events.types()
	assert.Equal(t, "flow_started", types[0])
	// Started before completed per task; flow_completed closes the stream.
	assert.True(t, indexOf(types, "flow_task_started") < indexOf(types, "flow_task_completed"))
	assert.Equal(t, "flow_completed", types[len(types)-1])
}

func indexOf(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

// Matrix 14 regression: crash mid-dispatch leaves a node 'running' with an
// interrupted child. Recovery must re-dispatch that task (not skip it) and
// still fold its predecessor's completed output into the next goal.
func TestOrchestratorRecoveryRedispatchInterruptedChild(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	// Crash after producer completed its checkpoint but while the reviewer
	// child was mid-flight: reviewer node is 'running' with an interrupted
	// child (worker restart semantics).
	err = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, OutputRef: json.RawMessage(`{"changedFiles":["crashed.go"]}`),
	})
	require.NoError(t, err)
	err = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 1, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-reviewer-crashed",
	})
	require.NoError(t, err)
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()

	orch := newOrchestrator(fake, children, events)
	orch.Recover(context.Background(), []string{"flow-run-1"})
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	// The reviewer task was re-dispatched (exactly once) and consumed the
	// producer checkpoint output.
	require.Len(t, children.created, 1)
	assert.Equal(t, "reviewer", children.created[0].Handle)
	assert.Equal(t, "Review [\"crashed.go\"]", children.created[0].Assignment)
	// The crashed child run id was replaced by the new dispatch.
	nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, n := range nodes {
		byHandle[n.Handle] = n
	}
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["reviewer"].TerminalState)
	assert.NotEqual(t, "child-reviewer-crashed", byHandle["reviewer"].ChildRunID)
}

// Crash window: the child Run already succeeded (its terminal fact is folded
// into the delegation item) but the checkpoint was never written. Recovery
// must fold the result into the node instead of re-running the task.
func TestOrchestratorRecoveryFoldsCompletedChild(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	// Producer's child succeeded before the crash; the checkpoint write never
	// happened, so the node is still 'running' with a succeeded child.
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done",
		Payload: json.RawMessage(`{"changedFiles":["folded.go"]}`),
	}
	children.status["child-producer"] = domain.RunSucceeded
	err = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	})
	require.NoError(t, err)
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()

	orch := newOrchestrator(fake, children, events)
	orch.Recover(context.Background(), []string{"flow-run-1"})
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	// Producer was NOT re-dispatched: its checkpoint was folded from the
	// succeeded child; reviewer consumed the folded output.
	require.Len(t, children.created, 1)
	assert.Equal(t, "reviewer", children.created[0].Handle)
	assert.Equal(t, "Review [\"folded.go\"]", children.created[0].Assignment)
	nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, n := range nodes {
		byHandle[n.Handle] = n
	}
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["producer"].TerminalState)
	assert.JSONEq(t, `{"changedFiles":["folded.go"]}`, string(byHandle["producer"].OutputRef))
}

// flow_started carries the entry task (v2 §10 contract field).
func TestOrchestratorFlowStartedCarriesEntryTask(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	waitTerminal(t, fake)
	require.Equal(t, "flow_started", events.types()[0])
	assert.Equal(t, "producer", events.payloadOf("flow_started")["entryTask"])
}
