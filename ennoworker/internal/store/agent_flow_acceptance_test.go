package store_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acceptanceRunFixture freezes one flow whose "producer" task declares an
// acceptance clause, exactly the way a Graph Run freezes one.
func acceptanceRunFixture(t *testing.T) (*store.AgentFlowRunRepo, string, string) {
	t.Helper()
	db, flowRuns, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(flowYAML("acceptance-store", "")))
	require.NoError(t, err)
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "flow-worker@1", Skills: []string{"go-dev"},
		Goal: "Implement {inputs.target}", Budget: &domain.FlowTaskBudget{Tokens: 50000},
		Acceptance: []domain.FlowAcceptance{
			{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads", "dup_rate"}},
			{File: "outputs/qc.json", Check: "json-valid"},
		},
	}
	version := flowVersionFromDef(def)
	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "src/main.go"}, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := freezeFlowForTest(t, db, flowRuns, projectID, "", def, inputs)
	require.NoError(t, err, diagnostics)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: sqlCreateSession(t, db, projectID).ID, ProjectID: projectID,
		FlowVersionID: version.ID, DefinitionJSON: version.DefinitionJSON,
		ConfigDigest: version.ConfigDigest, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)
	return flowRuns, run.RunID, "producer"
}

func nodeByHandle(t *testing.T, flowRuns *store.AgentFlowRunRepo, runID, handle string) *domain.RunAgentFlowNode {
	t.Helper()
	nodes, err := flowRuns.ListNodes(context.Background(), runID)
	require.NoError(t, err)
	for _, node := range nodes {
		if node.Handle == handle {
			return node
		}
	}
	t.Fatalf("node %q not found", handle)
	return nil
}

// The acceptance specification is frozen onto the node at Run start, so the
// fold path never depends on a successful version read.
func TestFlowNodeAcceptanceSpecsRoundTrip(t *testing.T) {
	flowRuns, runID, handle := acceptanceRunFixture(t)

	producer := nodeByHandle(t, flowRuns, runID, handle)
	assert.Equal(t, []domain.FlowAcceptance{
		{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads", "dup_rate"}},
		{File: "outputs/qc.json", Check: "json-valid"},
	}, producer.Acceptance)
	assert.Empty(t, producer.AcceptanceVerdict, "a fresh node has no verdict")

	reviewer := nodeByHandle(t, flowRuns, runID, "reviewer")
	assert.Empty(t, reviewer.Acceptance, "a task without an acceptance clause freezes the empty list")

	// GetNode reads the same columns as ListNodes.
	single, err := flowRuns.GetNode(context.Background(), runID, producer.TaskIndex)
	require.NoError(t, err)
	assert.Equal(t, producer.Acceptance, single.Acceptance)
}

// The verdict write is tri-state: nil leaves the column, empty clears it, a
// value writes it. Anything else either clears verdicts on unrelated updates or
// cannot clear one on a reset.
func TestFlowNodeAcceptanceVerdictIsTriState(t *testing.T) {
	flowRuns, runID, handle := acceptanceRunFixture(t)
	ctx := context.Background()
	producer := nodeByHandle(t, flowRuns, runID, handle)
	index := producer.TaskIndex
	verdict := json.RawMessage(`{"result":"holds","checks":[{"check":"json-valid","verdict":"holds"}]}`)

	_, err := flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: index, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, AcceptanceVerdict: &verdict,
	})
	require.NoError(t, err)
	assert.JSONEq(t, string(verdict), string(nodeByHandle(t, flowRuns, runID, handle).AcceptanceVerdict))

	// nil: an unrelated update (dispatch/dispatch-again/cancel) leaves it alone.
	_, err = flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: index, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeRunning, GoalText: "renamed goal",
	})
	require.NoError(t, err)
	kept := nodeByHandle(t, flowRuns, runID, handle)
	assert.JSONEq(t, string(verdict), string(kept.AcceptanceVerdict),
		"an update without a verdict must not clear the column")
	assert.Equal(t, "renamed goal", kept.GoalText)

	// non-nil, empty: clears it (a node reset to pending keeps no stale verdict).
	empty := json.RawMessage{}
	_, err = flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: index, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodePending, AcceptanceVerdict: &empty,
	})
	require.NoError(t, err)
	assert.Empty(t, nodeByHandle(t, flowRuns, runID, handle).AcceptanceVerdict)

	// non-nil, value: writes it again.
	_, err = flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: index, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, AcceptanceVerdict: &verdict,
	})
	require.NoError(t, err)
	assert.JSONEq(t, string(verdict), string(nodeByHandle(t, flowRuns, runID, handle).AcceptanceVerdict))
}

// Resume resets a failed task to pending: the verdict must not survive (the UI
// would show the previous conclusion while the task runs again), but the frozen
// specification must, because it belongs to the Run, not to the attempt.
//
// This also pins the NOT NULL column: the reset statement writes ” and a NULL
// write would fail the whole resume at runtime.
func TestResumeFlowRunClearsVerdictAndKeepsSpecs(t *testing.T) {
	flowRuns, runID, handle := acceptanceRunFixture(t)
	ctx := context.Background()
	index := nodeByHandle(t, flowRuns, runID, handle).TaskIndex
	verdict := json.RawMessage(`{"result":"does_not_hold","checks":[{"check":"json-valid","verdict":"does_not_hold","code":"json_invalid"}]}`)

	_, err := flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: index, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, OutputRef: json.RawMessage(`{"summary":"done"}`),
	})
	require.NoError(t, err)
	_, err = flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: index, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeFailed, ErrorCode: "acceptance_failed", AcceptanceVerdict: &verdict,
	})
	require.NoError(t, err)
	_, err = flowRuns.UpdateFlowState(ctx, runID, domain.FlowStateFailed, 4321, "acceptance_failed")
	require.NoError(t, err)

	_, err = flowRuns.ResumeFlowRun(ctx, runID)
	require.NoError(t, err, "a NOT NULL verdict column must not break resume")

	producer := nodeByHandle(t, flowRuns, runID, handle)
	assert.Equal(t, domain.FlowNodePending, producer.TerminalState)
	assert.Empty(t, producer.AcceptanceVerdict, "a resumed task must not carry the previous verdict")
	assert.Equal(t, []domain.FlowAcceptance{
		{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads", "dup_rate"}},
		{File: "outputs/qc.json", Check: "json-valid"},
	}, producer.Acceptance, "the specification is frozen for the run")
}

// acceptanceChildStub settles every child immediately and reports usage, so the
// fold path sees the same facts a real child produces.
type acceptanceChildStub struct {
	*stubMatrixChildren
}

func (c acceptanceChildStub) ChildUsage(context.Context, string) (domain.RunBudgetUsage, error) {
	return domain.RunBudgetUsage{Tokens: 1000}, nil
}

// acceptanceEvidenceStub answers from memory: the missing artifact is what makes
// the verdict does_not_hold without touching the workspace.
type acceptanceEvidenceStub struct{}

func (acceptanceEvidenceStub) Stat(string) (agentflow.AcceptanceFile, error) {
	return agentflow.AcceptanceFile{Missing: true}, nil
}

func (acceptanceEvidenceStub) Open(string) (io.ReadCloser, error) {
	return nil, os.ErrNotExist
}

// The whole fold, against the real store: the specs come from the frozen node,
// the verdict and the failure land in one checkpoint CAS, the child's payload is
// kept, and the flow fails with the tokens it actually consumed.
func TestOrchestratorVerifiesAcceptanceFromFrozenSpecs(t *testing.T) {
	db, flowRuns, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(flowYAML("acceptance-fold", "")))
	require.NoError(t, err)
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "flow-worker@1", Skills: []string{"go-dev"},
		Goal: "Implement {inputs.target}", Budget: &domain.FlowTaskBudget{Tokens: 50000},
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	version := flowVersionFromDef(def)
	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "src/main.go"}, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := freezeFlowForTest(t, db, flowRuns, projectID, "", def, inputs)
	require.NoError(t, err, diagnostics)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: sqlCreateSession(t, db, projectID).ID, ProjectID: projectID,
		FlowVersionID: version.ID, DefinitionJSON: version.DefinitionJSON,
		ConfigDigest: version.ConfigDigest, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)

	writer := events.NewWriter(&store.EventRepo{DB: db}, events.NewHub())
	children := acceptanceChildStub{stubMatrixChildren: &stubMatrixChildren{db: db,
		policies: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}}
	orch := &agentflow.Orchestrator{
		Store: &store.OrchestratorStore{Runs: flowRuns}, Children: children,
		Events:       &store.FlowEventSink{Writer: writer},
		PollInterval: 5 * time.Millisecond,
		AcceptanceEvidence: func(context.Context, string) (agentflow.AcceptanceEvidence, error) {
			return acceptanceEvidenceStub{}, nil
		},
	}
	orch.Start(ctx, run.RunID)

	var final *domain.RunAgentFlow
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current, err := flowRuns.GetRun(ctx, run.RunID)
		require.NoError(t, err)
		if current.State.Terminal() {
			final = current
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotNil(t, final, "the flow must terminalize")
	assert.Equal(t, domain.FlowStateFailed, final.State)
	assert.Equal(t, int64(1000), final.TotalTokensUsed, "a failed flow keeps the tokens it consumed")

	producer := nodeByHandle(t, flowRuns, run.RunID, "producer")
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, "acceptance_failed", producer.ErrorCode)
	assert.JSONEq(t, `{"changed_files":["a.go"]}`, string(producer.OutputRef),
		"the child's artifacts stay on the checkpoint")
	assert.Equal(t, []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}}, producer.Acceptance)
	var verdict agentflow.AcceptanceVerdictValue
	require.NoError(t, json.Unmarshal(producer.AcceptanceVerdict, &verdict))
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Result)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, "file_missing", verdict.Checks[0].Code)

	// The acceptance events are committed after the checkpoint write.
	eventRepo := &store.EventRepo{DB: db}
	seen := map[string]bool{}
	for eventDeadline := time.Now().Add(5 * time.Second); time.Now().Before(eventDeadline); {
		committed, err := eventRepo.After(ctx, run.RunID, 0, 200)
		require.NoError(t, err)
		for _, event := range committed {
			seen[event.EventType] = true
		}
		if seen["flow_task_acceptance_failed"] && seen["flow_failed"] {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.True(t, seen["flow_task_acceptance_failed"], "the timeline records the acceptance failure")
	assert.True(t, seen["flow_failed"], "and the flow failure")
}
