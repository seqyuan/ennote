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

// branchFlowDef: check gate routes to passTask or failTask; each terminal.
func branchFlowDef() *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: "branch",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"producer": {Type: domain.FlowTaskRole, Role: "writer@1",
				Goal: "produce", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"gate": {Type: domain.FlowTaskCheck, Command: "echo ok", Depends: []string{"producer"},
				Next: map[string]string{"pass": "passWork", "fail": "failWork"}},
			"passWork": {Type: domain.FlowTaskRole, Role: "reader@1", Goal: "pass work", Depends: []string{"gate"},
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"failWork": {Type: domain.FlowTaskRole, Role: "reader@1", Goal: "fail work", Depends: []string{"gate"},
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"acceptPass": {Type: domain.FlowTaskRole, Depends: []string{"passWork"},
				Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}},
			"acceptFail": {Type: domain.FlowTaskRole, Depends: []string{"failWork"},
				Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}},
		},
	}
}

// branchChecker is a CheckRunner stub with a scripted verdict.
type branchChecker struct {
	verdict bool
}

func (c *branchChecker) CheckPolicyForSession(ctx context.Context, sessionID string) (*CheckPolicy, error) {
	return &CheckPolicy{Mode: "allow_existing_behavior"}, nil
}
func (c *branchChecker) CreateCheckApproval(ctx context.Context, runID string, taskIndex int, command string) error {
	return nil
}
func (c *branchChecker) CheckApprovalStatus(ctx context.Context, runID string, taskIndex int) (CheckApprovalStatus, error) {
	return CheckApprovalNone, nil
}
func (c *branchChecker) ExecuteCheck(ctx context.Context, command string, timeoutSeconds int) (*CheckOutcome, error) {
	return &CheckOutcome{Pass: c.verdict, ExitCode: 0, Summary: "ok", Command: command}, nil
}

func runBranchFlow(t *testing.T, verdict bool) (*fakeStore, *fakeChildren, *fakeEvents, *branchChecker) {
	t.Helper()
	def := branchFlowDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	checker := &branchChecker{verdict: verdict}
	orch := newOrchestrator(fake, children, events)
	orch.Checker = checker
	orch.Start(context.Background(), "flow-run-1")
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	return fake, children, events, checker
}

// Matrix 2A-1: check pass -> only the pass target is dispatched.
func TestBranchRoutingPassActivatesPassTarget(t *testing.T) {
	_, children, events, _ := runBranchFlow(t, true)
	require.Len(t, children.created, 2) // producer + passWork
	assert.Equal(t, "producer", children.created[0].Handle)
	assert.Equal(t, "passWork", children.created[1].Handle)
	// The fail branch was never dispatched (no extra delegation).
	for _, spec := range children.created {
		assert.NotEqual(t, "failWork", spec.Handle)
	}
	assert.Contains(t, events.types(), "flow_check_result")
}

// Matrix 2A-2: check fail -> only the fail target is dispatched.
func TestBranchRoutingFailActivatesFailTarget(t *testing.T) {
	_, children, _, _ := runBranchFlow(t, false)
	require.Len(t, children.created, 2)
	assert.Equal(t, "failWork", children.created[1].Handle)
	for _, spec := range children.created {
		assert.NotEqual(t, "passWork", spec.Handle)
	}
}

// Matrix 2A-3: recovery replays the same branch from the check checkpoint.
func TestBranchRoutingRecoveryReplaysSameRoute(t *testing.T) {
	def := branchFlowDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	// Crash after producer completed and the gate resolved pass; passTask
	// still pending.
	err = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, OutputRef: json.RawMessage(`{"changedFiles":["a.go"]}`),
	})
	require.NoError(t, err)
	err = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 1, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, OutputRef: json.RawMessage(`{"pass":true,"exitCode":0,"summary":"ok"}`),
	})
	require.NoError(t, err)
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()

	checker := &branchChecker{verdict: true} // scripted; not used for recovery replay
	orch := newOrchestrator(fake, children, events)
	orch.Checker = checker
	orch.Recover(context.Background(), []string{"flow-run-1"})
	state := waitTerminal(t, fake)
	assert.Equal(t, domain.FlowStateCompleted, state)
	// Only passWork was dispatched after recovery (producer + gate checkpoints
	// were folded; the gate was not re-executed, no new check run).
	require.Len(t, children.created, 1)
	assert.Equal(t, "passWork", children.created[0].Handle)
}

// Matrix 2A-4: branch validation rejections.
func TestValidationBranchRouting(t *testing.T) {
	// next target unknown.
	result := validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {role: writer@1, goal: "a", budget: {tokens: 100}}
  g:
    type: check
    command: "go test ./..."
    depends: [a]
    next: {pass: nope, fail: b}
  b: {role: reader@1, goal: "b", depends: [g], budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "next_target_unknown"))

	// next target not downstream of the check.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {role: writer@1, goal: "a", budget: {tokens: 100}}
  g:
    type: check
    command: "go test ./..."
    depends: [a]
    next: {pass: a, fail: b}
  b: {role: reader@1, goal: "b", depends: [g], budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "next_target_not_downstream"))

	// pass and fail target the same task.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {role: writer@1, goal: "a", budget: {tokens: 100}}
  g:
    type: check
    command: "go test ./..."
    depends: [a]
    next: {pass: b, fail: b}
  b: {role: reader@1, goal: "b", depends: [g], budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "next_target_same"))

	// One task claimed by two different checks.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 30000}
tasks:
  a: {role: writer@1, goal: "a", budget: {tokens: 100}}
  g1:
    type: check
    command: "go test ./..."
    depends: [a]
    next: {pass: c, fail: b}
  g2:
    type: check
    command: "go test ./..."
    depends: [a]
    next: {pass: c, fail: b}
  b: {role: reader@1, goal: "b", depends: [g1], budget: {tokens: 100}}
  c: {role: reader@1, goal: "c", depends: [g1], budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "next_target_conflict"))

	// Valid branch flow passes.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {role: writer@1, goal: "a", budget: {tokens: 100}}
  g:
    type: check
    command: "go test ./..."
    depends: [a]
    next: {pass: p, fail: f}
  p: {role: reader@1, goal: "p", depends: [g], budget: {tokens: 100}}
  f: {role: reader@1, goal: "f", depends: [g], budget: {tokens: 100}}
`)
	assert.True(t, result.Valid, "%v", result.Diagnostics)
}

var _ = time.Now
