package agentflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	rounds          map[string]int
	// conflictOnUpdate forces UpdateNode to lose the CAS for a task index, which
	// is how a second orchestrator observing an already-settled node looks.
	conflictOnUpdate map[int]bool
	// completeOnCancelWrite simulates a node that reaches its own terminal fact
	// between the cleanup poll and the cancel write: the first cancel write for
	// that task index finds the node already completed, so the CAS must lose and
	// the node must keep its state AND its result.
	completeOnCancelWrite map[int]bool
	// cancelWrites counts the successful `cancelled` node writes per task index,
	// which is how "write the state once per node" is observable.
	cancelWrites map[int]int
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
			// Freeze the acceptance clause onto the node, exactly like the
			// production freeze (the fold path reads the node, not the version).
			Acceptance: append([]domain.FlowAcceptance(nil), task.Acceptance...),
			// `optional` is frozen the same way; readiness reads the node.
			Optional:  task.Optional,
			CreatedAt: time.Now(),
		})
	}
	return &fakeStore{
		run: run, nodes: nodes,
		version:               &domain.AgentFlowVersion{ID: "version-1", ConfigDigest: digest, DefinitionJSON: encoded},
		completeOnCancelWrite: map[int]bool{},
		cancelWrites:          map[int]int{},
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
		if f.conflictOnUpdate[upd.TaskIndex] {
			return ErrCheckpointConflict
		}
		// Simulate the node reaching its terminal fact after the cleanup poll and
		// before this write: the CAS below must lose against it.
		if upd.SetState == domain.FlowNodeCancelled && f.completeOnCancelWrite[upd.TaskIndex] {
			delete(f.completeOnCancelWrite, upd.TaskIndex)
			f.nodes[i].TerminalState = domain.FlowNodeCompleted
			f.nodes[i].OutputRef = json.RawMessage(`{"changedFiles":["late.go"]}`)
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
		if len(upd.ChildRunIDs) > 0 {
			f.nodes[i].ChildRunIDs = append([]string(nil), upd.ChildRunIDs...)
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
		if upd.ClearErrorCode {
			f.nodes[i].ErrorCode = ""
		}
		if upd.ClearOutputRef {
			f.nodes[i].OutputRef = nil
		}
		if upd.IncrementAttempts {
			f.nodes[i].Attempts++
		}
		// Tri-state verdict: nil leaves it, empty clears it, a value writes it.
		if upd.AcceptanceVerdict != nil {
			if len(*upd.AcceptanceVerdict) == 0 {
				f.nodes[i].AcceptanceVerdict = nil
			} else {
				f.nodes[i].AcceptanceVerdict = append(json.RawMessage(nil), (*upd.AcceptanceVerdict)...)
			}
		}
		if upd.SetState == domain.FlowNodeCancelled {
			f.cancelWrites[upd.TaskIndex]++
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
//
// It models one child Run per DISPATCH, not one per handle: the first dispatch
// of a handle keeps the historical id "child-<handle>" (many tests address it)
// and a re-dispatch gets "child-<handle>#<n>". Without that, a retried node
// would look like it re-used the child it was supposed to have stopped, and the
// "one child per node at a time" rule would be untestable.
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
	// failTimes/blockTimes limit the two behaviours to the first N dispatches of
	// a handle, which is how a retry test gets "the first attempt fails, the
	// second succeeds". dispatchCount is the per-handle CreateTaskChild count.
	failTimes      map[string]int
	blockTimes     map[string]int
	interruptTimes map[string]int
	dispatchCount  map[string]int
	// handles maps a child Run id back to the handle it was dispatched for, so a
	// per-dispatch id still answers result/usage queries by handle.
	handles map[string]string
	// childOrder is every created child Run id in dispatch order, so a test can
	// talk about "the first attempt" without depending on map iteration.
	childOrder []string
	// settledAtDispatch records, per dispatch, the earlier children of the same
	// handle that were still running when a new one was created. The substrate
	// allows one running child per node, so a test can assert this stays empty:
	// it is the observable form of "a retry never runs two children at once".
	settledAtDispatch map[string][]string
	// overOneAtATime records the dispatches that created a second child while an
	// earlier child of the same handle was still running (fan_out instances are
	// the legitimate exception: they dispatch several children at once).
	overOneAtATime []string
	// ignoreCancel models a substrate whose cancel does not stop the child (a
	// DB-only cancel): the call succeeds and the child keeps running. The flow
	// must not treat that as settled.
	ignoreCancel map[string]bool
	// raceOnCancel: a cancel of these handles finds the child already settled - it
	// won the race against the deadline - so the cancel is refused the way the
	// real RunRepo refuses a terminal Run. cancelBlocker delivers that settle.
	raceOnCancel  map[string]bool
	cancelBlocker map[string]domain.RunStatus
	// settleAt makes a running child finish on its own: the first status read
	// at/after the instant still reports running (the settlement poll of that
	// tick), the next one reports succeeded (the timeout path's re-read).
	settleAt  map[string]time.Time
	raceReads map[string]int
	clock     func() time.Time
}

func newFakeChildren() *fakeChildren {
	return &fakeChildren{results: map[string]*domain.SubmitResult{}, status: map[string]domain.RunStatus{}, usage: map[string]domain.RunBudgetUsage{}, failHandles: map[string]bool{}, blockedHandles: map[string]bool{}, failTimes: map[string]int{}, blockTimes: map[string]int{}, interruptTimes: map[string]int{}, dispatchCount: map[string]int{}, handles: map[string]string{}, settledAtDispatch: map[string][]string{}, raceOnCancel: map[string]bool{}, ignoreCancel: map[string]bool{}, cancelBlocker: map[string]domain.RunStatus{}, settleAt: map[string]time.Time{}, raceReads: map[string]int{}, clock: time.Now}
}

// dispatched is the number of times a handle was handed to the substrate.
func (c *fakeChildren) dispatched(handle string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dispatchCount[handle]
}

// childIDsOf returns the child Run ids created for one handle, in dispatch order.
func (c *fakeChildren) childIDsOf(handle string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for _, id := range c.childOrder {
		if c.handles[id] == handle {
			ids = append(ids, id)
		}
	}
	return ids
}

// unsettledAtDispatch returns, for one handle, the dispatches that started while
// an earlier child of that handle was still running.
func (c *fakeChildren) unsettledAtDispatch(handle string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.settledAtDispatch[handle]...)
}

// setStatus settles a child from the test side.
func (c *fakeChildren) setStatus(runID string, status domain.RunStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status[runID] = status
}

func (c *fakeChildren) CreateTaskChild(ctx context.Context, parentRunID, sessionID string, spec ChildSpec) (ChildInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created = append(c.created, spec)
	c.runIDSeq++
	c.dispatchCount[spec.Handle]++
	attempt := c.dispatchCount[spec.Handle]
	// Honesty check: a new child for a handle whose previous child has not
	// settled is two children of one node at once.
	if attempt > 1 && !isFanOutInstance(spec.Handle) {
		for id, handle := range c.handles {
			if handle != spec.Handle || c.status[id].Terminal() {
				continue
			}
			c.settledAtDispatch[spec.Handle] = append(c.settledAtDispatch[spec.Handle], id)
			c.overOneAtATime = append(c.overOneAtATime, spec.Handle)
		}
	}
	id := "child-" + spec.Handle
	if attempt > 1 {
		id = fmt.Sprintf("child-%s#%d", spec.Handle, attempt)
	}
	c.handles[id] = spec.Handle
	c.childOrder = append(c.childOrder, id)
	switch {
	case c.blockedHandles[spec.Handle] || attempt <= c.blockTimes[spec.Handle]:
		c.status[id] = domain.RunRunning
	case attempt <= c.interruptTimes[spec.Handle]:
		c.status[id] = domain.RunInterrupted
	case c.failHandles[spec.Handle] || attempt <= c.failTimes[spec.Handle]:
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

// isFanOutInstance reports whether a handle names one instance of a fan_out task
// ("<task>-<index>"), the one case where several children of a handle are
// dispatched at once by design.
func isFanOutInstance(handle string) bool {
	_, index, ok := strings.Cut(handle, "-")
	if !ok || index == "" {
		return false
	}
	_, err := strconv.Atoi(index)
	return err == nil
}

func (c *fakeChildren) ChildRunStatus(ctx context.Context, runID string) (domain.RunStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status, ok := c.status[runID]
	if !ok {
		return domain.RunQueued, nil
	}
	// A child that finishes exactly at its deadline: the first read at/after the
	// instant still says running, the next one says succeeded.
	if status == domain.RunRunning {
		if instant, ok := c.settleAt[c.handleOf(runID)]; ok && !c.clock().Before(instant) {
			c.raceReads[runID]++
			if c.raceReads[runID] > 1 {
				c.status[runID] = domain.RunSucceeded
				return domain.RunSucceeded, nil
			}
		}
	}
	return status, nil
}

// handleOf maps a child Run id to its handle. A child created through
// CreateTaskChild is registered; an id a test seeded directly (a crash fixture)
// falls back to the historical "child-<handle>" shape.
func (c *fakeChildren) handleOf(runID string) string {
	if handle, ok := c.handles[runID]; ok {
		return handle
	}
	handle := strings.TrimPrefix(runID, "child-")
	base, _, _ := strings.Cut(handle, "#")
	return base
}

func (c *fakeChildren) ChildTerminalResult(ctx context.Context, runID string) (*domain.SubmitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	handle := c.handleOf(runID)
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
	handle := c.handleOf(runID)
	return c.usage[handle], nil
}

// CancelChildRun delivers a cancel. A child that already settled refuses it the
// way the real substrate does (ErrInvalidRunState), which is the race the
// timeout path resolves by re-reading the status instead of recording a timeout.
func (c *fakeChildren) CancelChildRun(ctx context.Context, runID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.status[runID]
	if !ok {
		return nil
	}
	if current.Terminal() {
		return fmt.Errorf("%w: %s -> cancelled", errFakeInvalidRunState, current)
	}
	if c.ignoreCancel[c.handleOf(runID)] {
		// Delivered, but it did not stop the child: exactly what a DB-only cancel
		// looks like from the flow's side.
		return nil
	}
	if c.raceOnCancel[c.handleOf(runID)] {
		// The child won the race: it settled at the deadline, so the cancel
		// cannot be applied and the caller must fold the real outcome.
		c.status[runID] = c.cancelBlocker[c.handles[runID]]
		return fmt.Errorf("%w: %s -> cancelled", errFakeInvalidRunState, c.status[runID])
	}
	// Delivered: the cancel is what stops the child. Nothing else in this fake
	// ever settles a running child, so a flow that assumes a cancel worked - or
	// never delivers one - leaves it running and is caught.
	c.status[runID] = domain.RunCancelled
	return nil
}

// errFakeInvalidRunState mirrors store.ErrInvalidRunState for the fake substrate.
var errFakeInvalidRunState = errors.New("invalid run state")

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
			"producer": {Type: domain.FlowTaskHero, Hero: "writer@1",
				Goal: "Implement {inputs.target} and {flow.vars.mode}", Budget: &domain.FlowTaskBudget{Tokens: 50000}},
			"reviewer": {Type: domain.FlowTaskHero, Hero: "reader@1",
				Goal: "Review {task.producer.output.changedFiles}", Depends: []string{"producer"}},
			"accept": {Type: domain.FlowTaskHero, Depends: []string{"reviewer"},
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
	for _, node := range fake.nodes {
		if node.Handle == "producer" {
			node.FrozenSkills = domain.FrozenSkillRefs{{ID: "skill-go-dev", Digest: "sha256:frozen"}}
		}
	}
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	require.Len(t, children.created, 2, "producer+reviewer are hero tasks; accept is terminal")
	assert.Equal(t, "Implement src/main.go and fast", children.created[0].Assignment)
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "skill-go-dev", Digest: "sha256:frozen"}}, children.created[0].Skills,
		"the frozen content digest must reach child materialization")
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
	assert.Contains(t, events.types(), "flow_cancelled")
	assert.NotContains(t, events.types(), "flow_failed")
	// The durable event carries the cancelled child run ids.
	payload := events.payloadOf("flow_cancelled")
	assert.NotNil(t, payload["cancelledChildRuns"])
}

// --- Cancellation in three phases (stop -> cleanup -> publish) ---

// shortenCancelCleanup bounds the cleanup phase for one test and restores the
// package default afterwards. The timeout is a var exactly for this.
func shortenCancelCleanup(t *testing.T, limit time.Duration) {
	t.Helper()
	previous := cancelCleanupTimeout
	cancelCleanupTimeout = limit
	t.Cleanup(func() { cancelCleanupTimeout = previous })
}

// requestCancelWhenRunning requests the flow cancel once the named node has been
// dispatched, so a cancellation test stops a real in-flight child instead of
// racing one. It is meant to run in a goroutine (it only calls t.Errorf, which is
// safe from there).
func requestCancelWhenRunning(t *testing.T, fake *fakeStore, handle string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		nodes, err := fake.ListNodes(context.Background(), "flow-run-1")
		if err == nil {
			for _, node := range nodes {
				if node.Handle != handle || node.TerminalState != domain.FlowNodeRunning {
					continue
				}
				if err := fake.SetCancelRequested(context.Background(), "flow-run-1"); err != nil {
					t.Errorf("request cancel: %v", err)
				}
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Errorf("task %q never started, so the cancel was never requested", handle)
}

// cleanupOf reads the cancellation observation out of the flow_cancelled event.
func cleanupOf(t *testing.T, events *fakeEvents) map[string]any {
	t.Helper()
	payload := events.payloadOf("flow_cancelled")
	require.NotNil(t, payload, "flow_cancelled must be published")
	cleanup, ok := payload["cleanup"].(map[string]any)
	require.True(t, ok, "flow_cancelled must carry the cleanup observation, got %#v", payload["cleanup"])
	return cleanup
}

// cancelWriteCount reports how many times the `cancelled` state was written to
// one node. Two writes would mean "cancelled" was published before it was
// observed and then written again.
func (f *fakeStore) cancelWriteCount(taskIndex int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelWrites[taskIndex]
}

// Everything stops: the nodes are cancelled with no incomplete code, the event
// reports one stopped child and nothing unconfirmed, the reason carries no
// suffix, the node state is written once, and the ledger keeps the total the
// flow had already accumulated.
func TestCancelStopsEveryChildAndPublishesTheObservation(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.blockedHandles["reviewer"] = true // the second child never settles on its own
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)

	// Cancel while the reviewer's child is in flight. The producer already
	// completed, so the ledger holds something a cancellation could lose.
	go requestCancelWhenRunning(t, fake, "reviewer")
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCancelled, waitTerminal(t, fake))

	nodes := nodesByHandle(t, fake)
	assert.Equal(t, domain.FlowNodeCompleted, nodes["producer"].TerminalState,
		"a node that settled on its own keeps its outcome")
	assert.NotEmpty(t, nodes["producer"].OutputRef)
	for _, handle := range []string{"reviewer", "accept"} {
		assert.Equal(t, domain.FlowNodeCancelled, nodes[handle].TerminalState, handle)
		assert.Empty(t, nodes[handle].ErrorCode, "a confirmed stop carries no incomplete code: %s", handle)
		assert.Equal(t, 1, fake.cancelWriteCount(nodes[handle].TaskIndex),
			"the cancelled state is written exactly once: %s", handle)
	}

	cleanup := cleanupOf(t, events)
	assert.Equal(t, len(children.childIDsOf("reviewer")), cleanup["stopped"])
	assert.Equal(t, []string{}, cleanup["unconfirmed"], "an empty list, not a nil one")

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, "cancelled by user", run.TerminalReason, "no suffix when every child confirmed")
	assert.Equal(t, int64(1000), run.TotalTokensUsed, "a cancelled flow keeps its ledger")
}

// A child that ignores the cancel is the whole point: the flow must still reach
// a terminal state, bounded, and must say that the stop was not observed instead
// of writing a plain `cancelled`.
func TestCancelReportsUnconfirmedStopAndSuffixesTheReason(t *testing.T) {
	shortenCancelCleanup(t, 60*time.Millisecond)
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.blockedHandles["producer"] = true
	children.ignoreCancel["producer"] = true // DB-only cancel: the child keeps running
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)

	go requestCancelWhenRunning(t, fake, "producer")
	start := time.Now()
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCancelled, waitTerminal(t, fake))
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 60*time.Millisecond, "the flow waited for the stop to be observable")
	assert.Less(t, elapsed, 3*time.Second, "and gave up at the cleanup budget instead of blocking")

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCancelled, producer.TerminalState,
		"an unconfirmed stop is still cancelled; the code records why it is not clean")
	assert.Equal(t, cancelCleanupIncompleteCode, producer.ErrorCode)
	assert.Equal(t, 1, fake.cancelWriteCount(producer.TaskIndex))

	cleanup := cleanupOf(t, events)
	assert.Equal(t, 0, cleanup["stopped"])
	assert.Equal(t, []string{"child-producer"}, cleanup["unconfirmed"])

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Contains(t, run.TerminalReason, "; 1 task did not confirm stop")
	assert.Equal(t, domain.RunCancelled, fake.anchorStatus)
}

// Cancellation between tasks: nothing is in flight, so nothing was stopped and
// nothing is unconfirmed, but the ledger still survives.
func TestCancelBetweenTasksCountsNoCleanup(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	_, err = fake.AddTokenUsage(context.Background(), "flow-run-1", 250)
	require.NoError(t, err)

	require.NoError(t, orch.cancelRemaining(context.Background(), "flow-run-1", ""))

	for handle, node := range nodesByHandle(t, fake) {
		assert.Equal(t, domain.FlowNodeCancelled, node.TerminalState, handle)
		assert.Empty(t, node.ErrorCode, "a node with no child is cancelled cleanly: %s", handle)
		assert.Equal(t, 1, fake.cancelWriteCount(node.TaskIndex), handle)
	}
	assert.Empty(t, children.created, "cancelling between tasks starts no child")

	cleanup := cleanupOf(t, events)
	assert.Equal(t, 0, cleanup["stopped"])
	assert.Equal(t, []string{}, cleanup["unconfirmed"])

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, "cancelled by user", run.TerminalReason)
	assert.Equal(t, int64(250), run.TotalTokensUsed, "cancelling an idle flow must not zero the ledger")
}

// A node that reaches its terminal fact between the cleanup poll and the cancel
// write is not overwritten: the CAS loses and the node keeps its state and its
// result, while the flow still terminalizes as cancelled.
func TestCancelDoesNotOverwriteANodeThatFinishedFirst(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)

	producer := nodesByHandle(t, fake)["producer"]
	// The node is mid-attempt when the cancel starts...
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: producer.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	}))
	children.status["child-producer"] = domain.RunRunning
	// ...and it settles on its own between the poll and the write.
	fake.completeOnCancelWrite[producer.TaskIndex] = true

	require.NoError(t, orch.cancelRemaining(context.Background(), "flow-run-1", ""))

	nodes := nodesByHandle(t, fake)
	assert.Equal(t, domain.FlowNodeCompleted, nodes["producer"].TerminalState,
		"a node that finished concurrently keeps its terminal state")
	assert.Equal(t, json.RawMessage(`{"changedFiles":["late.go"]}`), nodes["producer"].OutputRef,
		"and its result")
	assert.Equal(t, 0, fake.cancelWriteCount(producer.TaskIndex), "the cancel write lost the CAS")
	assert.Equal(t, domain.FlowNodeCancelled, nodes["reviewer"].TerminalState)

	// The stop itself was observed, so the flow may report it even though the
	// node it belonged to kept its own (winning) outcome.
	cleanup := cleanupOf(t, events)
	assert.Equal(t, 1, cleanup["stopped"])
	assert.Equal(t, []string{}, cleanup["unconfirmed"])
	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, domain.FlowStateCancelled, run.State)
	assert.Equal(t, "cancelled by user", run.TerminalReason)
}

// A cancelled flow keeps its ledger, including the parallel case: the fan_out
// instances are stopped, counted, and their unfurled usage is not invented.
func TestCancelledFlowKeepsItsLedger(t *testing.T) {
	def := fanOutDef(2)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.blockedHandles["scan-0"] = true
	children.blockedHandles["scan-1"] = true
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)

	go requestCancelWhenRunning(t, fake, "scan")
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCancelled, waitTerminal(t, fake))

	cleanup := cleanupOf(t, events)
	assert.Equal(t, 2, cleanup["stopped"], "both instances are stopped and counted")
	assert.Equal(t, []string{}, cleanup["unconfirmed"])

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1000), run.TotalTokensUsed,
		"the ledger is the accumulated total (the producer's 1000), never 0")
	assert.Equal(t, "cancelled by user", run.TerminalReason)
	for _, id := range children.childIDsOf("scan-0") {
		assert.Equal(t, domain.RunCancelled, children.status[id])
	}
}

// The helper's contract on its own: a cancel the substrate ACCEPTED is not a
// child that stopped, and an unconfirmed child is named by node for the reason
// and by Run id for the payload.
func TestCancelInflightReportsDeliveredButUnconfirmedStop(t *testing.T) {
	shortenCancelCleanup(t, 40*time.Millisecond)
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.blockedHandles["producer"] = true
	children.ignoreCancel["producer"] = true
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)

	producer := nodesByHandle(t, fake)["producer"]
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: producer.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	}))
	children.status["child-producer"] = domain.RunRunning
	node, err := fake.GetNode(context.Background(), "flow-run-1", producer.TaskIndex)
	require.NoError(t, err)

	outcome := orch.cancelInflight(context.Background(), "flow-run-1",
		[]inflightTask{{node: node, childID: "child-producer"}})

	require.Contains(t, outcome.stopErrors, "child-producer", "the cancel error is kept, not discarded")
	assert.NoError(t, outcome.stopErrors["child-producer"], "the substrate accepted the cancel")
	assert.Equal(t, 0, outcome.stopped, "an accepted cancel is not a stopped child")
	assert.Equal(t, []string{"child-producer"}, outcome.unconfirmed)
	assert.Equal(t, 1, outcome.unconfirmedNodes)
	assert.Equal(t, "; 1 task did not confirm stop", outcome.reasonSuffix())
	assert.Equal(t, map[string]any{"stopped": 0, "unconfirmed": []string{"child-producer"}}, outcome.payload())

	cancelled := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCancelled, cancelled.TerminalState)
	assert.Equal(t, cancelCleanupIncompleteCode, cancelled.ErrorCode)
	// cancelInflight never touches the flow state: the caller terminalizes.
	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.False(t, run.State.Terminal(), "the flow state belongs to the caller")
}

// Cancellation is best effort: a caller context cancelled during the cleanup ends
// the wait, keeps what is known, and still terminalises the flow.
func TestCancelTerminalisesWhenTheCallerContextIsCancelled(t *testing.T) {
	shortenCancelCleanup(t, 30*time.Second) // the context, not the budget, must end the wait
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.blockedHandles["producer"] = true
	children.ignoreCancel["producer"] = true
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)

	producer := nodesByHandle(t, fake)["producer"]
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: producer.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	}))
	children.status["child-producer"] = domain.RunRunning

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	start := time.Now()
	require.NoError(t, orch.cancelRemaining(ctx, "flow-run-1", ""))
	assert.Less(t, time.Since(start), 5*time.Second, "a cancelled caller context ends the cleanup wait")

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, domain.FlowStateCancelled, run.State,
		"a caller that gave up must not leave the flow non-terminal")
	assert.Equal(t, domain.RunCancelled, fake.anchorStatus)

	node := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCancelled, node.TerminalState)
	assert.Equal(t, cancelCleanupIncompleteCode, node.ErrorCode,
		"a stop nobody observed is recorded as unconfirmed")
	assert.Equal(t, []string{"child-producer"}, cleanupOf(t, events)["unconfirmed"])
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
	assert.Contains(t, events.types(), "flow_budget_exceeded")
	assert.NotContains(t, events.types(), "flow_failed")
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

// UpdateNodeByHandle completes the node with the given handle (test helper
// for convergence scenarios where the topological index is not hardcoded).
func (f *fakeStore) UpdateNodeByHandle(ctx context.Context, runID, handle string, outputRef json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.nodes {
		if f.nodes[i].Handle == handle {
			f.nodes[i].TerminalState = domain.FlowNodeCompleted
			f.nodes[i].OutputRef = outputRef
			return nil
		}
	}
	return errFakeConflict
}

// A flow can carry more than one terminal task (branch flows do). The gate must
// inspect them in task order, not sorted handle order and not map order: here
// the locked terminal ("a_locked") sorts first by handle but last by task
// order, and the unlocked one must win on every call.
func TestTerminalGateTaskDeterministic(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "three-terminals",
		Budget: domain.FlowBudget{MaxTotalTokens: 10000},
		Tasks: map[string]domain.FlowTask{
			// Sorts first by handle, last by task order (it depends on
			// z_producer), and is locked: z_producer has no checkpoint.
			"a_locked":   {Terminal: &domain.FlowTerminal{Status: "success", Output: "o-locked"}, Depends: []string{"z_producer"}},
			"m_unlocked": {Terminal: &domain.FlowTerminal{Status: "success", Output: "o-unlocked"}},
			"z_producer": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce"},
		},
	}
	completed := map[string]*domain.RunAgentFlowNode{}

	for i := 0; i < 200; i++ {
		got, ambiguous := terminalGateTask(def, completed)
		require.False(t, ambiguous, "call %d: only one terminal is unlocked", i)
		require.NotNil(t, got, "call %d", i)
		require.Equal(t, "o-unlocked", got.Terminal.Output, "call %d must select the unlocked terminal", i)
	}

	// Once the producer completes both terminals are unlocked: the gate refuses
	// to pick one and reports the ambiguity instead.
	completed["z_producer"] = &domain.RunAgentFlowNode{Handle: "z_producer", TerminalState: domain.FlowNodeCompleted}
	for i := 0; i < 50; i++ {
		got, ambiguous := terminalGateTask(def, completed)
		assert.True(t, ambiguous, "call %d: two terminals unlocked is ambiguous", i)
		assert.Nil(t, got, "call %d must not pick a terminal silently", i)
	}
}

// Runtime terminal gate: exactly one unlocked terminal completes the flow; two
// unlocked terminals fail the flow loudly (terminal_gate_ambiguous) instead of
// silently picking one by task order.
func TestTerminalGateRuntime(t *testing.T) {
	// Two terminals fed by the same completed producer, no check gate between
	// them: both unlock together.
	ambiguousDef := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "ambiguous-terminals",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"t1": {Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}, Depends: []string{"producer"}},
			"t2": {Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}, Depends: []string{"producer"}},
		},
	}
	fake, err := newFakeStore(ambiguousDef)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))
	run, _ := fake.GetRun(context.Background(), "flow-run-1")
	assert.Contains(t, run.TerminalReason, "terminal_gate_ambiguous")
	assert.Equal(t, domain.RunFailed, fake.anchorStatus)
	assert.NotContains(t, events.types(), "flow_completed")
	assert.Contains(t, events.types(), "flow_failed")
	require.Len(t, children.created, 1, "only the producer is dispatched; terminals never are")

	// One unlocked terminal (the other branch stays pending) completes as before.
	fake, err = newFakeStore(branchFlowDef())
	require.NoError(t, err)
	children = newFakeChildren()
	events = &fakeEvents{}
	orch = newOrchestrator(fake, children, events)
	orch.Checker = &branchChecker{verdict: true}
	orch.Start(context.Background(), "flow-run-1")
	assert.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))
	assert.Contains(t, events.types(), "flow_completed")
	assert.NotContains(t, events.types(), "flow_failed")
}

// --- Scheduling knobs: retry / timeout / optional (P2 D6) ---

// fakeClock is the orchestrator's Now seam. A test advances it instead of
// sleeping out a real timeout_ms.
type fakeClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

// advanceUntilTerminal pushes the clock forward until the run settles. It is the
// deterministic form of "wait out the ceiling": an attempt that never stops
// running will cross it whatever moment its dispatch time was captured at, and
// the push stops as soon as the flow is terminal so a settled attempt is never
// retroactively expired (waitAndFoldAny checks settlement before expiry anyway).
func advanceUntilTerminal(fake *fakeStore, clock *fakeClock, step time.Duration) func() {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if run, err := fake.GetRun(context.Background(), "flow-run-1"); err == nil && run.State.Terminal() {
					return
				}
				clock.advance(step)
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// scheduledDef is the three-task fixture with one task rewritten. The reviewer's
// goal deliberately does NOT read the producer's output: a test about scheduling
// must not fail on goal resolution for a dependency that produced nothing.
func scheduledDef(mutate func(task *domain.FlowTask)) *domain.FlowDefinition {
	def := threeTaskDef()
	def.Tasks["reviewer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "Review the producer's work",
		Depends: []string{"producer"},
	}
	producer := def.Tasks["producer"]
	mutate(&producer)
	def.Tasks["producer"] = producer
	return def
}

func newScheduledFlow(t *testing.T, def *domain.FlowDefinition) (*fakeStore, *fakeChildren, *fakeEvents, *Orchestrator) {
	t.Helper()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	return fake, children, events, newOrchestrator(fake, children, events)
}

// retry: a child that ended failed is re-dispatched on the SAME node, the node
// ends completed, and the attempt it spent is recorded durably on the row.
func TestTaskRetryRedispatchesFailedChild(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 2} })
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failTimes["producer"] = 1 // first attempt fails, second succeeds

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	require.Equal(t, 2, children.dispatched("producer"), "the failed attempt must be re-dispatched")
	require.Len(t, children.created, 3, "producer twice + reviewer once")
	assert.Equal(t, "producer", children.created[0].Handle)
	assert.Equal(t, "producer", children.created[1].Handle, "a retry re-runs the same node")

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Equal(t, 1, producer.Attempts, "the spent attempt is part of the checkpoint")
	assert.Empty(t, producer.ErrorCode, "a re-dispatched node must not keep the failure code")
	assert.NotEmpty(t, producer.OutputRef, "the successful attempt's result is the checkpoint")

	types := events.types()
	assert.Contains(t, types, "flow_task_retrying")
	assert.Contains(t, types, "flow_completed")
	assert.NotContains(t, types, "flow_failed")
	assert.NotContains(t, types, "flow_task_failed", "a retried attempt is not a flow-level failure")
}

// retry: the budget is durable. A node that already spent its single attempt
// before the flow stopped must not be handed a fresh one by the next start.
func TestTaskRetryBudgetSurvivesResume(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 1} })
	fake, children, events, orch := newScheduledFlow(t, def)
	// Spent before the crash: the resumed run sees attempts=1 and max=1.
	for _, node := range fake.nodes {
		if node.Handle == "producer" {
			node.Attempts = 1
		}
	}
	children.failTimes["producer"] = 9

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	assert.Equal(t, 1, children.dispatched("producer"), "a spent attempt must not be re-granted")
	assert.Equal(t, 1, nodesByHandle(t, fake)["producer"].Attempts, "attempts is not reset by a start")
	assert.NotContains(t, events.types(), "flow_task_retrying")
}

// retry: exhausting max fails the flow exactly as today would have.
func TestTaskRetryExhaustedFailsFlow(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 1} })
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failHandles["producer"] = true

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	assert.Equal(t, 2, children.dispatched("producer"), "max:1 means at most two dispatches")
	assert.Equal(t, 0, children.dispatched("reviewer"), "a failed required dependency is never worked around")
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, "task_failed", producer.ErrorCode)
	assert.Equal(t, 1, producer.Attempts)
	assert.Contains(t, events.types(), "flow_failed")
}

// retry: a verdict about the product is not retried. `acceptance_failed` says
// the product does not satisfy its declared conditions; running the same task
// again on the same input hides that instead of fixing it.
func TestAcceptanceFailureIsNotRetried(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 3} })
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Retry:      &domain.FlowTaskRetry{Max: 3},
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	evidence := newFakeEvidence() // the file does not exist: does_not_hold

	orch := acceptanceOrchestrator(t, fake, children, events, evidence)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	assert.Equal(t, 1, children.dispatched("producer"), "an acceptance verdict is never re-run")
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, acceptanceFailedCode, producer.ErrorCode)
	assert.Equal(t, 0, producer.Attempts)
	assert.NotContains(t, events.types(), "flow_task_retrying")
}

// retry: a report contract violation is a verdict too (the report shape the
// Hero promised), so it is never retried either.
func TestReportContractViolationIsNotRetried(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 3} })
	fake, children, events, orch := newScheduledFlow(t, def)
	freezeReportContract(t, fake, "producer", requiredPayloadContract())
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done", Payload: json.RawMessage(`{"summary":"done"}`),
	}

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	assert.Equal(t, 1, children.dispatched("producer"))
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, reportContractViolationCode, producer.ErrorCode)
	assert.Equal(t, 0, producer.Attempts)
	assert.NotContains(t, events.types(), "flow_task_retrying")
}

// retry: a cancelled flow is never retried — re-dispatching a task the user
// stopped would keep work running after the cancel.
func TestCancelledFlowIsNotRetried(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 3} })
	fake, children, events, orch := newScheduledFlow(t, def)
	children.blockedHandles["producer"] = true

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = fake.SetCancelRequested(context.Background(), "flow-run-1")
	}()
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCancelled, waitTerminal(t, fake))

	assert.Equal(t, 1, children.dispatched("producer"))
	assert.Equal(t, domain.FlowNodeCancelled, nodesByHandle(t, fake)["producer"].TerminalState)
	assert.NotContains(t, events.types(), "flow_task_retrying")
}

// timeout: an attempt past its ceiling is hard-cancelled and recorded as
// task_timeout (a retryable code, so the same path can re-dispatch it).
func TestTaskTimeoutCancelsAttemptAndRecordsTaskTimeout(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.TimeoutMS = 60000 })
	fake, children, events, orch := newScheduledFlow(t, def)
	children.blockedHandles["producer"] = true
	clock := &fakeClock{}
	orch.Now = clock.now
	stop := advanceUntilTerminal(fake, clock, 90*time.Second)
	defer stop()

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, taskTimeoutCode, producer.ErrorCode)
	status, err := children.ChildRunStatus(context.Background(), "child-producer")
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, status, "the timed-out child must be hard-cancelled")
	assert.Contains(t, events.types(), "flow_task_failed")
	assert.NotContains(t, events.types(), "flow_completed")
}

// timeout: a task that settles inside its ceiling is untouched by the mechanism.
func TestTaskWithinTimeoutIsUntouched(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.TimeoutMS = 600000 })
	fake, children, events, orch := newScheduledFlow(t, def)
	clock := &fakeClock{}
	orch.Now = clock.now

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	assert.Equal(t, 1, children.dispatched("producer"))
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Empty(t, producer.ErrorCode)
	status, err := children.ChildRunStatus(context.Background(), "child-producer")
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, status, "a settled child must never be cancelled by the ceiling")
	assert.NotContains(t, events.types(), "flow_task_retrying")
}

// timeout then retry: the ceiling and the retry budget compose — a timed-out
// attempt is re-dispatched, and the second attempt completes.
func TestTaskTimeoutThenRetrySucceeds(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) {
		task.TimeoutMS = 60000
		task.Retry = &domain.FlowTaskRetry{Max: 1}
	})
	fake, children, events, orch := newScheduledFlow(t, def)
	children.blockTimes["producer"] = 1 // the first attempt never settles, the second does
	clock := &fakeClock{}
	orch.Now = clock.now
	stop := advanceUntilTerminal(fake, clock, 90*time.Second)
	defer stop()

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	assert.Equal(t, 2, children.dispatched("producer"))
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Equal(t, 1, producer.Attempts)
	assert.Empty(t, producer.ErrorCode, "the re-dispatched node must not keep task_timeout")
	assert.Contains(t, events.types(), "flow_task_retrying")
	assert.Contains(t, events.types(), "flow_completed")
}

// timeout: a timed-out attempt must not discard a sibling's progress. The
// sibling that already settled keeps its checkpoint when the flow fails.
func TestTaskTimeoutKeepsSiblingCheckpoints(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "timeout-siblings",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"alpha": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "slow", TimeoutMS: 60000,
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"beta": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "fast",
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"done": {Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}, Depends: []string{"beta"}},
		},
	}
	fake, children, events, orch := newScheduledFlow(t, def)
	readOnlyNodes(fake) // one parallel reader batch: alpha and beta are in flight together
	children.blockedHandles["alpha"] = true
	clock := &fakeClock{}
	orch.Now = clock.now
	stop := advanceUntilTerminal(fake, clock, 90*time.Second)
	defer stop()

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	nodes := nodesByHandle(t, fake)
	assert.Equal(t, taskTimeoutCode, nodes["alpha"].ErrorCode)
	assert.Equal(t, domain.FlowNodeCompleted, nodes["beta"].TerminalState,
		"a sibling that settled before the timeout keeps its checkpoint")
	assert.NotEmpty(t, nodes["beta"].OutputRef)
	assert.Contains(t, events.types(), "flow_failed")
}

// optional: a failed optional task records the failure on its node, its
// dependents still run, and the flow completes.
func TestOptionalFailureLetsDependentsRunAndFlowComplete(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Optional = true })
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failHandles["producer"] = true

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState, "an optional failure stays a failed node")
	assert.Equal(t, "task_failed", producer.ErrorCode, "the node row is the record of what happened")
	assert.Equal(t, 1, children.dispatched("reviewer"), "a failed optional dependency must not block its dependents")
	types := events.types()
	assert.Contains(t, types, "flow_completed")
	assert.NotContains(t, types, "flow_failed")
	assert.Contains(t, types, "flow_task_failed", "the failure is still on the timeline")
}

// optional: without the flag the same failure still fails the flow. The knob is
// opt-in per task, and the default is exactly today's behaviour.
func TestRequiredFailureStillFailsFlow(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) {})
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failHandles["producer"] = true

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	assert.Equal(t, 0, children.dispatched("reviewer"))
	assert.Equal(t, domain.FlowNodeFailed, nodesByHandle(t, fake)["producer"].TerminalState)
	assert.Contains(t, events.types(), "flow_failed")
	assert.NotContains(t, events.types(), "flow_completed")
}

// optional + retry: the retry budget is spent BEFORE `optional` absorbs the
// outcome, so a task that can still recover keeps trying.
func TestOptionalWithRetrySpendsRetriesFirst(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) {
		task.Optional = true
		task.Retry = &domain.FlowTaskRetry{Max: 2}
	})
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failHandles["producer"] = true

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	assert.Equal(t, 3, children.dispatched("producer"), "max:2 extra attempts before giving up")
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, 2, producer.Attempts)
	assert.Equal(t, 1, children.dispatched("reviewer"))
	assert.Contains(t, events.types(), "flow_completed")
}

// optional: the terminal gate unlocks through a failed optional dependency and
// the flow completes with that node still failed.
func TestTerminalGateUnlocksThroughOptionalFailure(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "optional-terminal",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce", Optional: true,
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"done": {Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}, Depends: []string{"producer"}},
		},
	}
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failHandles["producer"] = true

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState, "the flow completes, the task stays failed")
	assert.Contains(t, events.types(), "flow_completed")
	assert.NotContains(t, events.types(), "flow_failed")
}

// depSatisfied is the one answer readiness and the terminal gate share. Pinned
// as a table because three call sites now agree only if this helper is right.
func TestDepSatisfied(t *testing.T) {
	cases := []struct {
		name string
		node *domain.RunAgentFlowNode
		want bool
	}{
		{"nil is not satisfied", nil, false},
		{"completed", &domain.RunAgentFlowNode{TerminalState: domain.FlowNodeCompleted}, true},
		{"optional failure", &domain.RunAgentFlowNode{TerminalState: domain.FlowNodeFailed, Optional: true}, true},
		{"required failure", &domain.RunAgentFlowNode{TerminalState: domain.FlowNodeFailed}, false},
		{"optional but completed", &domain.RunAgentFlowNode{TerminalState: domain.FlowNodeCompleted, Optional: true}, true},
		{"pending", &domain.RunAgentFlowNode{TerminalState: domain.FlowNodePending}, false},
		{"interrupted", &domain.RunAgentFlowNode{TerminalState: domain.FlowNodeInterrupted}, false},
		{"optional but cancelled", &domain.RunAgentFlowNode{TerminalState: domain.FlowNodeCancelled, Optional: true}, false},
	}
	for _, testCase := range cases {
		assert.Equal(t, testCase.want, depSatisfied(testCase.node), testCase.name)
	}
}

// retryableFailureCode is the closed retry vocabulary: an attempt-shaped
// failure is retryable, a verdict about the product or about the user's intent
// is not.
func TestRetryableFailureCode(t *testing.T) {
	for code, want := range map[string]bool{
		"task_failed":                             true,
		"task_interrupted":                        true,
		taskTimeoutCode:                           true,
		"task_cancelled":                          false,
		acceptanceFailedCode:                      false,
		reportContractViolationCode:               false,
		blockedBlockerPrefix + "needs_user_input": false,
	} {
		assert.Equal(t, want, retryableFailureCode(code), code)
	}
}

// Regression: a Graph that declares none of the three knobs behaves exactly as
// before — same dispatch order, same checkpoints, same event list.
func TestGraphWithoutSchedulingKnobsBehavesExactlyAsBefore(t *testing.T) {
	fake, children, events, orch := newScheduledFlow(t, scheduledDef(func(task *domain.FlowTask) {}))

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	require.Len(t, children.created, 2)
	assert.Equal(t, "producer", children.created[0].Handle)
	assert.Equal(t, "reviewer", children.created[1].Handle)
	assert.Equal(t, []string{
		"flow_started", "flow_task_started", "flow_task_completed",
		"flow_task_started", "flow_task_completed", "flow_completed",
	}, events.types(), "the event list of a knob-free Graph is unchanged")

	for _, node := range nodesByHandle(t, fake) {
		assert.Equal(t, 0, node.Attempts, "%s never retried", node.Handle)
		assert.False(t, node.Optional, "%s is not optional", node.Handle)
	}
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Equal(t, domain.RunSucceeded, fake.anchorStatus)
}

// optional on a check gate: the gate's failure is recorded on its node and the
// flow continues. The gate's branch targets stay inactive (an unresolved gate
// selects no branch), so nothing downstream of it is dispatched.
func TestOptionalCheckGateFailureDoesNotFailFlow(t *testing.T) {
	failing := &failingChecker{}
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "optional-gate",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"gate": {Type: domain.FlowTaskCheck, Command: "go test ./...", Optional: true,
				Depends: []string{"producer"}},
			"done": {Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}, Depends: []string{"gate"}},
		},
	}
	fake, children, events, orch := newScheduledFlow(t, def)
	orch.Checker = failing

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	gate := nodesByHandle(t, fake)["gate"]
	assert.Equal(t, domain.FlowNodeFailed, gate.TerminalState, "the gate's failure stays on the row")
	assert.Equal(t, "check_execution_failed", gate.ErrorCode)
	assert.Contains(t, events.types(), "flow_completed")
	assert.NotContains(t, events.types(), "flow_failed")
	assert.Equal(t, 1, children.dispatched("producer"))

	// Without the flag the same failure fails the flow, exactly as before.
	def.Tasks["gate"] = domain.FlowTask{Type: domain.FlowTaskCheck, Command: "go test ./...",
		Depends: []string{"producer"}}
	fake, _, events, orch = newScheduledFlow(t, def)
	orch.Checker = failing
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))
	assert.Contains(t, events.types(), "flow_failed")
}

// failingChecker reports a sandbox execution error on every gate.
type failingChecker struct{}

func (c *failingChecker) CheckPolicyForSession(ctx context.Context, sessionID string) (*CheckPolicy, error) {
	return &CheckPolicy{Mode: "allow_existing_behavior"}, nil
}
func (c *failingChecker) CreateCheckApproval(ctx context.Context, runID string, taskIndex int, command string) error {
	return nil
}
func (c *failingChecker) CheckApprovalStatus(ctx context.Context, runID string, taskIndex int) (CheckApprovalStatus, error) {
	return CheckApprovalNone, nil
}
func (c *failingChecker) ExecuteCheck(ctx context.Context, command string, timeoutSeconds int) (*CheckOutcome, error) {
	return nil, errors.New("sandbox unavailable")
}

// --- Review fixes: retry/timeout/optional (adversarial review of 0d2dccb) ---

// B1 (runtime net). A dependent whose goal reads the output of an optional task
// cannot be satisfied when that task fails. The publish gate refuses the shape
// (optional_output_referenced); a run frozen before that gate still reaches the
// resolution, and it must fail naming the optional failure - and keep the tokens
// it already spent instead of reporting zero.
func TestOptionalOutputReferenceNamesTheOptionalFailure(t *testing.T) {
	// The REAL dependency shape: the reviewer reads the producer's output.
	def := threeTaskDef()
	producer := def.Tasks["producer"]
	producer.Optional = true
	def.Tasks["producer"] = producer
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failHandles["producer"] = true

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	assert.Equal(t, 1, children.dispatched("producer"))
	assert.Equal(t, 0, children.dispatched("reviewer"), "the dependent cannot run without the output it reads")

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Contains(t, run.TerminalReason, `optional task "producer"`)
	assert.Contains(t, run.TerminalReason, "task_failed", "the reason quotes the code the node recorded")
	assert.Contains(t, run.TerminalReason, `"reviewer"`, "the reason names the task that could not resolve its goal")
	assert.Equal(t, int64(1000), run.TotalTokensUsed,
		"a flow that ran a child must not report a zero ledger")
	assert.Contains(t, events.types(), "flow_failed")
	assert.NotContains(t, events.types(), "flow_completed")
}

// A missing output whose producer is NOT optional keeps the plain resolution
// failure: the net is only about the optional shape, it does not reword every
// dependency problem, and it does not invent a token total the flow never spent.
func TestMissingOutputWithoutOptionalKeepsThePlainFailure(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) {})
	def.Tasks["reviewer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "Review {task.producer.output.changedFiles}",
		Depends: []string{"producer"},
	}
	fake, children, events, orch := newScheduledFlow(t, def)
	// The producer completes but records no output, so the reviewer's reference
	// cannot resolve while the producer is not optional.
	for _, node := range fake.nodes {
		if node.Handle != "producer" {
			continue
		}
		require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
			TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
			SetState: domain.FlowNodeCompleted,
		}))
	}

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Contains(t, run.TerminalReason, `output is not available`)
	assert.NotContains(t, run.TerminalReason, "optional task")
	assert.Equal(t, 0, children.dispatched("reviewer"))
	assert.Contains(t, events.types(), "flow_failed")
}

// B2. The fake substrate no longer stops a child by simply being asked; it
// stops it when the cancel is DELIVERED, and it records any dispatch that
// created a second child while the first was still running. A timed-out attempt
// therefore has to be observably terminal before the retry dispatches.
func TestTimedOutAttemptIsTerminalBeforeTheRetryDispatches(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) {
		task.TimeoutMS = 60000
		task.Retry = &domain.FlowTaskRetry{Max: 1}
	})
	fake, children, events, orch := newScheduledFlow(t, def)
	children.blockTimes["producer"] = 1 // the first attempt never settles on its own
	clock := &fakeClock{}
	orch.Now = clock.now
	stop := advanceUntilTerminal(fake, clock, 90*time.Second)
	defer stop()

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	require.Equal(t, 2, children.dispatched("producer"))
	assert.Empty(t, children.unsettledAtDispatch("producer"),
		"a retry must never dispatch while the previous attempt is still running")
	ids := children.childIDsOf("producer")
	require.Len(t, ids, 2, "each dispatch is its own child Run")
	assert.NotEqual(t, ids[0], ids[1], "the retry runs a different child Run")
	first, err := children.ChildRunStatus(context.Background(), ids[0])
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, first, "the ceiling stopped the first attempt")
	second, err := children.ChildRunStatus(context.Background(), ids[1])
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, second)
	assert.Contains(t, events.types(), "flow_task_retrying")
	assert.Contains(t, events.types(), "flow_completed")
	assert.Equal(t, domain.FlowNodeCompleted, nodesByHandle(t, fake)["producer"].TerminalState)
	assert.Equal(t, 1, nodesByHandle(t, fake)["producer"].Attempts)
}

// B2.2. A child that settles exactly at its deadline is folded from its own
// outcome. Recording task_timeout for it would fail a flow whose work is done -
// and, with a retry budget, would run the task a second time for nothing.
func TestChildSettlingAtTheDeadlineIsFoldedNotTimedOut(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) {
		task.TimeoutMS = 60000
		task.Retry = &domain.FlowTaskRetry{Max: 1}
	})
	fake, children, events, orch := newScheduledFlow(t, def)
	clock := &fakeClock{}
	orch.Now = clock.now
	children.clock = clock.now
	// The child finishes at its ceiling: the settlement poll of that tick still
	// sees it running, the re-read the timeout path performs sees it succeeded.
	children.blockedHandles["producer"] = true
	children.settleAt["producer"] = time.Now().Add(60 * time.Second)
	stop := advanceUntilTerminal(fake, clock, 90*time.Second)
	defer stop()

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Empty(t, producer.ErrorCode, "a settled child is not a timeout")
	assert.Equal(t, 1, children.dispatched("producer"), "a settled attempt is never re-dispatched")
	assert.NotContains(t, events.types(), "flow_task_retrying")
	status, err := children.ChildRunStatus(context.Background(), "child-producer")
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, status)
}

// B2.2. The cancel can be refused because the child settled first. The error is
// no longer discarded: the child's real outcome owns the node.
func TestCancelRefusedBySettledChildFoldsTheOutcome(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) {
		task.TimeoutMS = 60000
		task.Retry = &domain.FlowTaskRetry{Max: 2}
	})
	fake, children, events, orch := newScheduledFlow(t, def)
	children.blockedHandles["producer"] = true
	// The child wins the race at the deadline: the cancel finds it already
	// succeeded and is refused the way the real substrate refuses a terminal Run.
	children.raceOnCancel["producer"] = true
	children.cancelBlocker["producer"] = domain.RunSucceeded
	clock := &fakeClock{}
	orch.Now = clock.now
	stop := advanceUntilTerminal(fake, clock, 90*time.Second)
	defer stop()

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Empty(t, producer.ErrorCode)
	assert.Equal(t, 1, children.dispatched("producer"), "the settled attempt is not re-dispatched")
	assert.NotContains(t, events.types(), "flow_task_retrying")
}

// B2.3. A cancel that does not stop the child leaves the attempt unsettled. The
// flow must not record a timeout and must not retry over it (two children for one
// node in one workspace); it stops loudly instead.
func TestUnstoppableTimedOutChildFailsTheFlowWithoutRetry(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) {
		task.TimeoutMS = 60000
		task.Retry = &domain.FlowTaskRetry{Max: 3}
		task.Optional = true
	})
	fake, children, events, orch := newScheduledFlow(t, def)
	children.blockedHandles["producer"] = true
	children.ignoreCancel["producer"] = true // DB-only cancel: the child keeps running
	clock := &fakeClock{}
	orch.Now = clock.now
	stop := advanceUntilTerminal(fake, clock, 90*time.Second)
	defer stop()

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	assert.Equal(t, 1, children.dispatched("producer"), "an unsettled attempt is never retried")
	assert.Empty(t, children.unsettledAtDispatch("producer"))
	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Contains(t, run.TerminalReason, "did not stop")
	assert.Equal(t, int64(1000), run.TotalTokensUsed, "the ledger survives the loud failure")
	assert.NotContains(t, events.types(), "flow_completed")
	// `optional` does not absorb an infrastructure failure: the workspace is in an
	// unknown state, so the flow stops.
	assert.NotContains(t, events.types(), "flow_task_retrying")
}

// S2. A crash consumes the attempt it was running: the recovery reset spends it,
// so retry.max stays an upper bound on dispatches instead of a budget a crash
// loop is handed back for free.
func TestCrashedAttemptIsSpentByTheRecoveryReset(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 1} })
	fake, children, events, orch := newScheduledFlow(t, def)
	producerIndex := -1
	for _, node := range fake.nodes {
		if node.Handle == "producer" {
			producerIndex = node.TaskIndex
		}
	}
	require.NotNil(t, fake.run)
	// A crash mid-attempt: the node says running, its child is still executing.
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: producerIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	}))
	children.status["child-producer"] = domain.RunRunning
	fake.run.State = domain.FlowStateRunning

	require.NoError(t, orch.reconcileCrashedNodes(context.Background(), "flow-run-1"))
	crashed := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeInterrupted, crashed.TerminalState)
	assert.Equal(t, 1, crashed.Attempts, "a crash consumed one attempt")

	// The single re-dispatch is the last one the budget allows: when it fails, the
	// flow fails instead of being handed a second free attempt.
	children.failHandles["producer"] = true
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	assert.Equal(t, 1, children.dispatched("producer"), "the crash already spent the retry budget")
	assert.NotContains(t, events.types(), "flow_task_retrying")
	assert.Equal(t, 1, nodesByHandle(t, fake)["producer"].Attempts)
}

// N2. `flow_task_retrying` announces the dispatch about to happen, named so it
// cannot be read as the attempt that just failed: the first retry is dispatch 2.
func TestRetryEventReportsTheDispatchOrdinal(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 2} })
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failTimes["producer"] = 2 // two failures, then the third dispatch succeeds

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	require.Equal(t, 3, children.dispatched("producer"))
	payload := events.payloadOf("flow_task_retrying")
	require.NotNil(t, payload)
	assert.Equal(t, 3, payload["maxDispatches"], "1 + retry.max is the dispatch ceiling")
	assert.NotContains(t, payload, "attempt", "the ambiguous field is gone")
	assert.NotContains(t, payload, "maxAttempts")
	// The event map carries the LAST retry (dispatch 3): the fake records each
	// publish, the map keeps the last one.
	assert.Equal(t, 3, payload["dispatchOrdinal"])
	assert.Equal(t, "task_failed", payload["reason"])
}

// N3. `optional` on a fan_out task: a failed instance is absorbed, the dependents
// still run and the flow completes with the node failed.
func TestOptionalFanOutFailureIsAbsorbed(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "optional-fanout",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "survey",
				FanOut: &domain.FlowFanOut{Min: 2, Max: 2}, Optional: true,
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"reviewer": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "summarize",
				Depends: []string{"producer"}, Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"done": {Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}, Depends: []string{"reviewer"}},
		},
	}
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failHandles["producer-1"] = true // one instance of the fan_out fails

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, "task_failed", producer.ErrorCode)
	assert.Equal(t, 1, children.dispatched("reviewer"), "a failed optional fan_out still satisfies its dependents")
	assert.Contains(t, events.types(), "flow_completed")
	assert.NotContains(t, events.types(), "flow_failed")
}

// N3. `optional` absorbs an acceptance verdict: the node keeps the verdict and
// its code, and the flow continues.
func TestOptionalAbsorbsAcceptanceFailure(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Optional = true })
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce", Optional: true,
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence()) // the file does not exist

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, acceptanceFailedCode, producer.ErrorCode)
	assert.Contains(t, events.types(), "flow_task_acceptance_failed")
	assert.Equal(t, 1, children.dispatched("reviewer"))
	assert.Contains(t, events.types(), "flow_completed")
	assert.NotContains(t, events.types(), "flow_failed")
}

// N3. `optional` absorbs a report contract violation the same way: the verdict
// is on the node, the dependent still runs, the flow completes.
func TestOptionalAbsorbsReportContractViolation(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Optional = true })
	fake, children, events, orch := newScheduledFlow(t, def)
	freezeReportContract(t, fake, "producer", requiredPayloadContract())
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done", Payload: json.RawMessage(`{"summary":"done"}`),
	}

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, reportContractViolationCode, producer.ErrorCode)
	assert.Contains(t, events.types(), "flow_task_report_contract_violation")
	assert.Equal(t, 1, children.dispatched("reviewer"))
	assert.Contains(t, events.types(), "flow_completed")
	assert.NotContains(t, events.types(), "flow_failed")
}

// N3. The interrupted retry path: a child Run that ended interrupted is an
// attempt-shaped failure, so it is re-dispatched like a failed one.
func TestInterruptedChildIsRetried(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Retry = &domain.FlowTaskRetry{Max: 1} })
	fake, children, events, orch := newScheduledFlow(t, def)
	children.interruptTimes["producer"] = 1

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	require.Equal(t, 2, children.dispatched("producer"))
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Equal(t, 1, producer.Attempts)
	assert.Empty(t, producer.ErrorCode, "the re-dispatched node no longer looks interrupted")
	payload := events.payloadOf("flow_task_retrying")
	require.NotNil(t, payload)
	assert.Equal(t, "task_interrupted", payload["reason"])
	assert.Equal(t, 2, payload["dispatchOrdinal"])
	first, err := children.ChildRunStatus(context.Background(), "child-producer")
	require.NoError(t, err)
	assert.Equal(t, domain.RunInterrupted, first, "the interrupted attempt is the terminal fact the retry folded")
}

// S3. A completed flow that tolerated a failed optional task is not a clean one:
// the run's terminal reason and the flow_completed payload both say which tasks
// failed, instead of the flow reading exactly like a run where everything worked.
func TestCompletedFlowNamesFailedOptionalTasks(t *testing.T) {
	def := scheduledDef(func(task *domain.FlowTask) { task.Optional = true })
	fake, children, events, orch := newScheduledFlow(t, def)
	children.failHandles["producer"] = true

	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, "completed with 1 failed optional task: producer", run.TerminalReason)

	payload := events.payloadOf("flow_completed")
	require.NotNil(t, payload)
	entries, ok := payload["failedTasks"].([]map[string]string)
	require.True(t, ok, "flow_completed carries the failed optional tasks as data: %#v", payload["failedTasks"])
	require.Len(t, entries, 1)
	assert.Equal(t, "producer", entries[0]["task"])
	assert.Equal(t, "task_failed", entries[0]["errorCode"])

	// A clean flow keeps saying nothing: no reason, no failedTasks key.
	clean, cleanChildren, cleanEvents, cleanOrch := newScheduledFlow(t, scheduledDef(func(task *domain.FlowTask) {}))
	cleanOrch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, clean))
	assert.Equal(t, 1, cleanChildren.dispatched("producer"))
	cleanRun, err := clean.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Empty(t, cleanRun.TerminalReason, "a clean completion still reports no reason")
	assert.NotContains(t, cleanEvents.payloadOf("flow_completed"), "failedTasks")
}
