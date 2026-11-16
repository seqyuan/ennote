package agentflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fanOutDef: a read-only fan_out task (2 instances) whose aggregate feeds a
// downstream task; a terminal ends the flow.
func fanOutDef(min int) *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: "fan",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskHero, Hero: "writer@1",
				Goal: "produce", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"scan": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "scan", Depends: []string{"producer"},
				FanOut: &domain.FlowFanOut{Min: min, Max: 4}, Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"accept": {Type: domain.FlowTaskHero, Depends: []string{"scan"},
				Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}},
		},
	}
}

// Matrix 2C-1: N parallel children created, results aggregated in order.
func TestFanOutExecutesParallelAndAggregates(t *testing.T) {
	def := fanOutDef(2)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	// Give the two fan_out instances distinguishable payloads.
	children.results["scan-0"] = &domain.SubmitResult{Status: domain.SubmitCompleted,
		Payload: json.RawMessage(`{"file":"a.go"}`)}
	children.results["scan-1"] = &domain.SubmitResult{Status: domain.SubmitCompleted,
		Payload: json.RawMessage(`{"file":"b.go"}`)}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	// producer + scan-0 + scan-1 (both fan_out instances) dispatched.
	require.Len(t, children.created, 3)
	assert.Equal(t, 2, countHandle(children, "scan-0")+countHandle(children, "scan-1"))
	// The scan node checkpoint is the ordered aggregate array.
	nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
	var scanNode *domain.RunAgentFlowNode
	for _, n := range nodes {
		if n.Handle == "scan" {
			scanNode = n
		}
	}
	require.NotNil(t, scanNode)
	assert.Equal(t, domain.FlowNodeCompleted, scanNode.TerminalState)
	assert.Len(t, scanNode.ChildRunIDs, 2)
	assert.JSONEq(t, `[{"file":"a.go"},{"file":"b.go"}]`, string(scanNode.OutputRef))
}

// Matrix 2C-2: one failing instance fails the whole task (deterministic).
func TestFanOutSingleFailureFailsTask(t *testing.T) {
	def := fanOutDef(2)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	children.failHandles["scan-1"] = true
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateFailed, state)
	nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
	for _, n := range nodes {
		if n.Handle == "scan" {
			assert.Equal(t, domain.FlowNodeFailed, n.TerminalState)
		}
	}
}

// Matrix 2C-3: fan_out goal resolution supports array indexes downstream.
func TestFanOutArrayIndexResolution(t *testing.T) {
	outputs := map[string]json.RawMessage{
		"scan": json.RawMessage(`[{"file":"a.go"},{"file":"b.go"}]`),
	}
	resolved, err := ResolveGoalTemplate("first={task.scan.output.0.file} all={task.scan.output}",
		map[string]any{}, map[string]any{}, outputs)
	require.NoError(t, err)
	assert.Equal(t, `first=a.go all=[{"file":"a.go"},{"file":"b.go"}]`, resolved)
	// Out of range index fails loudly.
	_, err = ResolveGoalTemplate("x={task.scan.output.5.file}", map[string]any{}, map[string]any{}, outputs)
	require.Error(t, err)
}

// Matrix 2C-4: cancellation covers every parallel instance.
//
// The cancel is requested only once BOTH instances are recorded on the node, and
// that is the fix for the flake this test was reported for. `scan` is claimed
// `running` before its first instance exists (runFanOutTask claims the node, then
// dispatches), so "the node is running" was already true while the dispatcher was
// still creating instances; the dispatch loop stops starting new ones as soon as it
// observes a cancel, so a cancel landing in that window is a correct stop of a
// half-dispatched fan-out that leaves ONE instance - and the assertion below then
// failed on a race in the test, not in the orchestrator. See
// TestFanOutCancelLandedDuringDispatchStopsStartingInstances, which pins that
// window deterministically. The precondition this assertion actually needs is
// durable: both child Run ids are on the node, and both children are blocked, so
// they are still running when the cancel arrives.
func TestFanOutCancelCoversAllInstances(t *testing.T) {
	def := fanOutDef(2)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.blockedHandles["scan-0"] = true
	children.blockedHandles["scan-1"] = true
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	cancelRequested := make(chan error, 1)
	go func() {
		// A bounded poll on recorded state (the same idiom as waitTerminal), not a
		// sleep: the cancel is sent on a fact, and the fact is "both instances
		// exist".
		deadline := time.Now().Add(5 * time.Second)
		for {
			nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
			for _, node := range nodes {
				if node.Handle == "scan" && len(node.ChildRunIDs) >= 2 {
					cancelRequested <- fake.SetCancelRequested(context.Background(), "flow-run-1")
					return
				}
			}
			if time.Now().After(deadline) {
				cancelRequested <- errors.New("the fan_out node never recorded both instances")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	orch.Start(context.Background(), "flow-run-1")
	require.NoError(t, <-cancelRequested)
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCancelled, state)
	assert.Equal(t, 1, children.dispatched("scan-0"))
	assert.Equal(t, 1, children.dispatched("scan-1"))
	// Both instances were cancelled.
	children.mu.Lock()
	cancelled := 0
	for _, status := range children.status {
		if status == domain.RunCancelled {
			cancelled++
		}
	}
	children.mu.Unlock()
	assert.Equal(t, 2, cancelled)
}

// The window TestFanOutCancelCoversAllInstances used to race, made deterministic:
// a cancel that lands while the dispatcher is creating instances is a correct stop
// of the remaining instances, so the fan-out ends with the one instance it had
// already started. This is the orchestrator's contract, and it is why the test
// above waits for both instances to be recorded instead of waiting for the node to
// say `running`.
func TestFanOutCancelLandedDuringDispatchStopsStartingInstances(t *testing.T) {
	def := fanOutDef(2)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.blockedHandles["scan-0"] = true
	children.blockedHandles["scan-1"] = true
	// The first instance's creation is inside the window: the node is already
	// `running`, no instance id is recorded yet, and a user cancel can arrive.
	children.onCreate = func(handle string) {
		if handle == "scan-0" {
			_ = fake.SetCancelRequested(context.Background(), "flow-run-1")
		}
	}
	orch := newOrchestrator(fake, children, &fakeEvents{})
	orch.Start(context.Background(), "flow-run-1")
	assert.Equal(t, domain.FlowStateCancelled, waitTerminal(t, fake))
	assert.Equal(t, 1, children.dispatched("scan-0"))
	assert.Equal(t, 0, children.dispatched("scan-1"),
		"no instance is started after the cancel is observed")
}

// Regression: budget_exceeded for a fan_out task records the ACTUAL total
// usage across every parallel instance, not just the instances accounted
// before the limit was hit.
func TestFanOutBudgetExceededTotalsAllChildren(t *testing.T) {
	def := fanOutDef(2)
	def.Budget.MaxTotalTokens = 1500 // each instance uses 1000 -> total 2000
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren() // default usage 1000 tokens per instance
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateBudgetExceeded, state)
	run, _ := fake.GetRun(context.Background(), "flow-run-1")
	// producer (1000) + scan-0 (1000) + scan-1 (1000): every instance's usage
	// is recorded even though the limit was crossed.
	assert.Equal(t, int64(3000), run.TotalTokensUsed, "budget record must reflect all instances")
	assert.Contains(t, events.types(), "flow_budget_exceeded")
}
