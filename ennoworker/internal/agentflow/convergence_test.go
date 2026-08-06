package agentflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sequenceChecker is a CheckRunner stub with a scripted verdict sequence.
type sequenceChecker struct {
	verdicts []bool
	calls    int
}

func (c *sequenceChecker) CheckPolicyForSession(ctx context.Context, sessionID string) (*CheckPolicy, error) {
	return &CheckPolicy{Mode: "allow_existing_behavior"}, nil
}
func (c *sequenceChecker) CreateCheckApproval(ctx context.Context, runID string, taskIndex int, command string) error {
	return nil
}
func (c *sequenceChecker) CheckApprovalStatus(ctx context.Context, runID string, taskIndex int) (CheckApprovalStatus, error) {
	return CheckApprovalNone, nil
}
func (c *sequenceChecker) ExecuteCheck(ctx context.Context, command string, timeoutSeconds int) (*CheckOutcome, error) {
	verdict := false
	if c.calls < len(c.verdicts) {
		verdict = c.verdicts[c.calls]
	}
	c.calls++
	return &CheckOutcome{Pass: verdict, ExitCode: 0, Summary: "ok", Command: command}, nil
}

// makerCheckerDef: reviewers -> decision(check, pass->accept, fail->revise)
// -> revise, with a declared back-edge revise -> reviewers (max 2 rounds).
func makerCheckerDef(maxRounds int) *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: "maker",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskRole, Role: "writer@1",
				Goal: "produce", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"reviewers": {Type: domain.FlowTaskRole, Role: "reader@1", Goal: "review", Depends: []string{"producer"},
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"decision": {Type: domain.FlowTaskCheck, Command: "echo ok", Depends: []string{"reviewers"},
				Next: map[string]string{"pass": "accept", "fail": "revise"}},
			"revise": {Type: domain.FlowTaskRole, Role: "writer@1", Goal: "fix", Depends: []string{"decision"},
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"accept": {Type: domain.FlowTaskRole, Depends: []string{"decision"},
				Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}},
		},
		Convergence: []domain.ConvergenceRule{{From: "revise", To: "reviewers", MaxRounds: maxRounds}},
	}
}

func runMakerChecker(t *testing.T, verdicts []bool, maxRounds int) (*fakeStore, *fakeChildren, *fakeEvents, *sequenceChecker) {
	t.Helper()
	def := makerCheckerDef(maxRounds)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	checker := &sequenceChecker{verdicts: verdicts}
	orch := newOrchestrator(fake, children, events)
	orch.Checker = checker
	orch.Start(context.Background(), "flow-run-1")
	return fake, children, events, checker
}

func countHandle(children *fakeChildren, handle string) int {
	children.mu.Lock()
	defer children.mu.Unlock()
	count := 0
	for _, spec := range children.created {
		if spec.Handle == handle {
			count++
		}
	}
	return count
}

// Matrix 2B-1: fail -> revise -> back-edge -> reviewers rerun -> pass -> done.
func TestConvergenceBackEdgeRerunsLoop(t *testing.T) {
	fake, children, events, _ := runMakerChecker(t, []bool{false, true}, 2)
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	// producer once; reviewers twice (initial + one back-edge); revise once;
	// the second decision passed so accept ends the flow.
	assert.Equal(t, 1, countHandle(children, "producer"))
	assert.Equal(t, 2, countHandle(children, "reviewers"))
	assert.Equal(t, 1, countHandle(children, "revise"))
	// One back-edge round was recorded durably.
	rounds, err := fake.GetConvergenceRounds(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, 1, rounds["revise\x00reviewers"])
	// The loop-path nodes were reset and re-completed: reviewers/decision
	// checkpoints are completed, producer untouched.
	nodes, _ := fake.ListNodes(context.Background(), "flow-run-1")
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, n := range nodes {
		byHandle[n.Handle] = n
	}
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["producer"].TerminalState)
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["reviewers"].TerminalState)
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["decision"].TerminalState)
	assert.Contains(t, events.types(), "flow_check_result")
}

// Matrix 2B-2: the loop re-runs until the gate passes, bounded by max_rounds.
func TestConvergenceMaxRoundsExceeded(t *testing.T) {
	fake, children, events, _ := runMakerChecker(t, []bool{false, false, false}, 2)
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateConvergenceExceeded, state)
	// 2 allowed back-edges: reviewers ran 3x, revise 3x; the third revise
	// completion pushed rounds to 3 > 2 -> exceeded.
	assert.Equal(t, 3, countHandle(children, "reviewers"))
	assert.Equal(t, 3, countHandle(children, "revise"))
	assert.Contains(t, events.types(), "flow_convergence_exceeded")
	rounds, err := fake.GetConvergenceRounds(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, 3, rounds["revise\x00reviewers"])
}

// Matrix 2B-3: an independent convergence rule counts separately.
func TestConvergenceIndependentCounters(t *testing.T) {
	def := makerCheckerDef(2)
	def.Convergence = []domain.ConvergenceRule{
		{From: "revise", To: "reviewers", MaxRounds: 2},
		{From: "revise", To: "decision", MaxRounds: 3}, // second independent loop
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	checker := &sequenceChecker{verdicts: []bool{false, true}}
	orch := newOrchestrator(fake, children, events)
	orch.Checker = checker
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	rounds, err := fake.GetConvergenceRounds(context.Background(), "flow-run-1")
	require.NoError(t, err)
	// Each rule counted its own back-edge from the same revise completion.
	assert.Equal(t, 1, rounds["revise\x00reviewers"])
	assert.Equal(t, 1, rounds["revise\x00decision"])
}

// Matrix 2B-4: a crash between back-edges keeps the durable counter; recovery
// continues from the persisted round count instead of restarting at zero.
func TestConvergenceRecoveryKeepsCounter(t *testing.T) {
	def := makerCheckerDef(2)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	// Crash after one back-edge round: reviewers/decision/revise completed
	// with rounds=1 persisted, producer completed, accept pending.
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, OutputRef: json.RawMessage(`{"changedFiles":["a.go"]}`),
	}))
	for _, handle := range []string{"reviewers", "decision", "revise"} {
		output := json.RawMessage(`{"pass":false,"exitCode":1,"summary":"fail"}`)
		if handle == "reviewers" || handle == "revise" {
			output = json.RawMessage(`{"changedFiles":["x.go"]}`)
		}
		node, err := fake.GetNode(context.Background(), "flow-run-1", 0)
		require.NoError(t, err)
		_ = node
		require.NoError(t, fake.UpdateNodeByHandle(context.Background(), "flow-run-1", handle, output))
	}
	require.NoError(t, fake.SetConvergenceRounds(context.Background(), "flow-run-1", map[string]int{"revise\x00reviewers": 1}))
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()

	children := newFakeChildren()
	events := &fakeEvents{}
	checker := &sequenceChecker{verdicts: []bool{true}} // next decision passes
	orch := newOrchestrator(fake, children, events)
	orch.Checker = checker
	orch.Recover(context.Background(), []string{"flow-run-1"})
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	// The loop re-ran once from the persisted counter (rounds 1 -> 2 <= 2),
	// reviewers was re-dispatched exactly once after recovery.
	assert.Equal(t, 1, countHandle(children, "reviewers"))
	rounds, err := fake.GetConvergenceRounds(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Equal(t, 2, rounds["revise\x00reviewers"])
}

// fakeStore round-trips convergence rounds for the orchestrator seam.
func (f *fakeStore) GetConvergenceRounds(ctx context.Context, runID string) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rounds, nil
}

func (f *fakeStore) SetConvergenceRounds(ctx context.Context, runID string, rounds map[string]int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rounds = rounds
	return nil
}

var _ = time.Now
