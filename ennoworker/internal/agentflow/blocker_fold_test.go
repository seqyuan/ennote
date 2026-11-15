package agentflow

// Typed blocker: a child that ends `blocked` / `needs_input` says WHY, and the
// Graph fold carries that reason instead of leaving a reader to parse the prose.
// The node error code is `blocked:<blocker>` (the same shape as
// acceptance_failed: the code names the fact that stopped the task), and the
// flow's terminal reason names it too.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func blockedChildResult(status domain.SubmitStatus, blocker domain.SubmitBlocker) *domain.SubmitResult {
	return &domain.SubmitResult{Status: status, Blocker: blocker,
		Summary: "stopped", Payload: json.RawMessage(`{"reason":"partial"}`)}
}

// Every typed blocker becomes its own error code, so a filtered checkpoint list
// can tell "evidence missing" from "wants a human" from "something external".
func TestBlockedChildFailsFlowWithTypedBlocker(t *testing.T) {
	for _, tc := range []struct {
		status  domain.SubmitStatus
		blocker domain.SubmitBlocker
	}{
		{domain.SubmitBlocked, domain.BlockerMissingEvidence},
		{domain.SubmitBlocked, domain.BlockerExternalWait},
		{domain.SubmitNeedsInput, domain.BlockerNeedsUserInput},
	} {
		t.Run(string(tc.status)+"/"+string(tc.blocker), func(t *testing.T) {
			def := threeTaskDef()
			fake, err := newFakeStore(def)
			require.NoError(t, err)
			children := newFakeChildren()
			children.results["reviewer"] = blockedChildResult(tc.status, tc.blocker)
			events := &fakeEvents{}
			orch := newOrchestrator(fake, children, events)
			orch.Start(context.Background(), "flow-run-1")

			require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))
			wantCode := "blocked:" + string(tc.blocker)
			byHandle := nodesByHandle(t, fake)
			assert.Equal(t, domain.FlowNodeBlocked, byHandle["reviewer"].TerminalState,
				"a blocked child is not a failed Run and not a completed task")
			assert.Equal(t, wantCode, byHandle["reviewer"].ErrorCode)
			assert.Equal(t, domain.FlowNodeCompleted, byHandle["producer"].TerminalState,
				"finished tasks keep their checkpoints")

			run, err := fake.GetRun(context.Background(), "flow-run-1")
			require.NoError(t, err)
			assert.Contains(t, run.TerminalReason, wantCode, "the flow reason must name the blocker")
			assert.Contains(t, run.TerminalReason, `"reviewer"`)
			assert.Equal(t, int64(2000), run.TotalTokensUsed,
				"a blocked flow keeps the ledger it accumulated")

			assert.Contains(t, events.types(), "flow_task_blocked")
			blocked := events.payloadOf("flow_task_blocked")
			require.NotNil(t, blocked)
			assert.Equal(t, wantCode, blocked["reason"])
			assert.Equal(t, string(tc.blocker), blocked["blocker"])
			assert.Contains(t, events.types(), "flow_failed")
		})
	}
}

// Regression: a result written before the field existed carries no blocker, so
// the fold keeps its previous behaviour exactly - the node completes and the
// flow continues. The absence is never read as a reason.
func TestBlockedChildWithoutBlockerKeepsPreviousBehaviour(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.results["reviewer"] = &domain.SubmitResult{Status: domain.SubmitBlocked,
		Summary: "legacy blocked result", Payload: json.RawMessage(`{"legacy":true}`)}
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")

	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))
	byHandle := nodesByHandle(t, fake)
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["reviewer"].TerminalState)
	assert.Empty(t, byHandle["reviewer"].ErrorCode, "no code is invented for an old result")
	assert.NotContains(t, events.types(), "flow_task_blocked")
	assert.JSONEq(t, `{"legacy":true}`, string(byHandle["reviewer"].OutputRef))
}

// A blocked result never wins over acceptance as a "verdict": the task could not
// produce what it had to produce, so its acceptance clause is not decided.
func TestBlockedChildIsNotVerifiedAgainstAcceptance(t *testing.T) {
	def := threeTaskDef()
	reviewer := def.Tasks["reviewer"]
	reviewer.Acceptance = []domain.FlowAcceptance{{File: "outputs/report.md", Check: "exists"}}
	def.Tasks["reviewer"] = reviewer
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.results["reviewer"] = blockedChildResult(domain.SubmitBlocked, domain.BlockerMissingEvidence)
	orch := newOrchestrator(fake, children, &fakeEvents{})
	orch.Start(context.Background(), "flow-run-1")

	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))
	node := nodesByHandle(t, fake)["reviewer"]
	assert.Equal(t, domain.FlowNodeBlocked, node.TerminalState)
	assert.Equal(t, "blocked:missing_evidence", node.ErrorCode)
	assert.Empty(t, node.AcceptanceVerdict, "a blocked task has no verdict to write")
}

// Crash recovery folds the same facts as the live fold: a blocked child that had
// already settled when the Worker died must not become a completed checkpoint.
func TestRecoveryFoldsBlockedChildAsBlocked(t *testing.T) {
	def := threeTaskDef()
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.results["reviewer"] = blockedChildResult(domain.SubmitNeedsInput, domain.BlockerNeedsUserInput)
	children.status["child-reviewer"] = domain.RunSucceeded
	err = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, OutputRef: json.RawMessage(`{"changedFiles":["a.go"]}`),
	})
	require.NoError(t, err)
	err = fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 1, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-reviewer",
	})
	require.NoError(t, err)
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()

	orch := newOrchestrator(fake, children, &fakeEvents{})
	orch.Recover(context.Background(), []string{"flow-run-1"})

	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))
	node := nodesByHandle(t, fake)["reviewer"]
	assert.Equal(t, domain.FlowNodeBlocked, node.TerminalState)
	assert.Equal(t, "blocked:needs_user_input", node.ErrorCode)
}
