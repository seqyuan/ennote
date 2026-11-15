package agentflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEvidence is an in-memory AcceptanceEvidence: the tests decide exactly what
// the workspace says, so each verdict is reachable without touching the disk.
type fakeEvidence struct {
	mu       sync.Mutex
	files    map[string]AcceptanceFile
	contents map[string]string
	failWith error
}

func newFakeEvidence() *fakeEvidence {
	return &fakeEvidence{files: map[string]AcceptanceFile{}, contents: map[string]string{}}
}

func (f *fakeEvidence) Stat(rel string) (AcceptanceFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return AcceptanceFile{}, f.failWith
	}
	file, ok := f.files[rel]
	if !ok {
		return AcceptanceFile{Missing: true}, nil
	}
	return file, nil
}

func (f *fakeEvidence) Open(rel string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	content, ok := f.contents[rel]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func acceptanceOrchestrator(t *testing.T, fake *fakeStore, children *fakeChildren, events *fakeEvents,
	evidence AcceptanceEvidence) *Orchestrator {
	t.Helper()
	orch := newOrchestrator(fake, children, events)
	orch.AcceptanceEvidence = func(context.Context, string) (AcceptanceEvidence, error) {
		return evidence, nil
	}
	return orch
}

func nodesByHandle(t *testing.T, fake *fakeStore) map[string]*domain.RunAgentFlowNode {
	t.Helper()
	nodes, err := fake.ListNodes(context.Background(), "flow-run-1")
	require.NoError(t, err)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodes {
		byHandle[node.Handle] = node
	}
	return byHandle
}

func decodeVerdict(t *testing.T, node *domain.RunAgentFlowNode) AcceptanceVerdictValue {
	t.Helper()
	require.NotEmpty(t, node.AcceptanceVerdict, "node %q must carry a verdict", node.Handle)
	var verdict AcceptanceVerdictValue
	require.NoError(t, json.Unmarshal(node.AcceptanceVerdict, &verdict))
	return verdict
}

// readOnlyNodes marks every task read-only so the fixture dispatches a parallel
// batch (writers are exclusive unless the flow opts into disjoint writes).
func readOnlyNodes(fake *fakeStore) {
	for _, node := range fake.nodes {
		node.ReadOnly = true
	}
}

// holds: the node completes with the verdict in the same checkpoint and gets no
// extra event. The child is told the contract; the checkpoint goal stays the
// resolved goal.
func TestAcceptanceHoldsCompletesWithVerdict(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1",
		Goal:   "Implement {inputs.target} and {flow.vars.mode}",
		Budget: &domain.FlowTaskBudget{Tokens: 50000},
		Acceptance: []domain.FlowAcceptance{
			{File: "outputs/qc.csv", Check: "non-empty"},
			{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads"}},
		},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	evidence := newFakeEvidence()
	evidence.files["outputs/qc.csv"] = AcceptanceFile{Size: 12, Regular: true}
	evidence.contents["outputs/qc.csv"] = "sample,reads\ns1,10\n"

	orch := acceptanceOrchestrator(t, fake, children, events, evidence)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState, "holds keeps the task completed")
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Result)
	require.Len(t, verdict.Checks, 2)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Checks[1].Verdict)

	types := events.types()
	assert.Contains(t, types, "flow_task_completed")
	assert.NotContains(t, types, "flow_task_unverified")
	assert.NotContains(t, types, "flow_task_acceptance_failed")
	assert.NotContains(t, types, "flow_failed")

	// The child receives the contract as task data, deterministically.
	require.NotEmpty(t, children.created)
	assignment := children.created[0].Assignment
	assert.Contains(t, assignment, "Acceptance (checked by the runtime against the workspace after you finish; your own report is not evidence):")
	assert.Contains(t, assignment, "- outputs/qc.csv must have columns [sample, reads]")
	// The checkpoint still shows what the task asked for, not the contract.
	assert.Equal(t, "Implement src/main.go and fast", producer.GoalText)
}

// does_not_hold: the node fails with the child's output_ref kept, the siblings
// settle with their own checkpoints, no pending task is dispatched afterwards,
// and the flow fails with the accumulated token total (never 0).
func TestAcceptanceDoesNotHoldDrainsSiblingsThenFailsFlow(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "acceptance-siblings",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"alpha": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce", Budget: &domain.FlowTaskBudget{Tokens: 1000},
				Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}}},
			"beta":  {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "inspect", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"gamma": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "after beta", Depends: []string{"beta"}},
			"done":  {Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}, Depends: []string{"beta"}},
		},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	readOnlyNodes(fake)
	children := newFakeChildren()
	children.blockedHandles["beta"] = true // beta is in flight while alpha fails
	events := &fakeEvents{}
	evidence := newFakeEvidence() // outputs/qc.csv is missing

	orch := acceptanceOrchestrator(t, fake, children, events, evidence)
	go func() {
		// Let alpha fail first, then settle beta: the drain must fold it instead
		// of cancelling it, and must start nothing new.
		time.Sleep(50 * time.Millisecond)
		children.mu.Lock()
		children.status["child-beta"] = domain.RunSucceeded
		children.mu.Unlock()
	}()
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	byHandle := nodesByHandle(t, fake)
	assert.Equal(t, domain.FlowNodeFailed, byHandle["alpha"].TerminalState)
	assert.Equal(t, "acceptance_failed", byHandle["alpha"].ErrorCode)
	assert.JSONEq(t, `{"changedFiles":["a.go"]}`, string(byHandle["alpha"].OutputRef),
		"the artifacts are the only evidence of what went wrong")
	verdict := decodeVerdict(t, byHandle["alpha"])
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Result)

	assert.Equal(t, domain.FlowNodeCompleted, byHandle["beta"].TerminalState,
		"an in-flight sibling keeps its own checkpoint instead of being cancelled")
	assert.Equal(t, domain.FlowNodePending, byHandle["gamma"].TerminalState,
		"a task that becomes ready after the failure is never dispatched")
	assert.Equal(t, 0, countHandle(children, "gamma"))

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Greater(t, run.TotalTokensUsed, int64(0), "a failed flow must not report zero usage")

	types := events.types()
	assert.Contains(t, types, "flow_task_acceptance_failed")
	assert.Contains(t, types, "flow_failed")
	assert.NotContains(t, types, "flow_completed")
	assert.Equal(t, domain.RunFailed, fake.anchorStatus)
}

// unverified: the task completes with the verdict recorded and the flow keeps
// going, but the run says so.
func TestAcceptanceUnverifiedCompletesAndEmitsEvent(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Budget:     &domain.FlowTaskBudget{Tokens: 1000},
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	evidence := newFakeEvidence()
	evidence.failWith = errors.New("i/o error") // undecidable, never a pass

	orch := acceptanceOrchestrator(t, fake, children, events, evidence)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)

	types := events.types()
	assert.Contains(t, types, "flow_task_completed")
	assert.Contains(t, types, "flow_task_unverified")
	assert.NotContains(t, types, "flow_task_acceptance_failed")
}

// A nil factory (the runtime cannot read evidence at all) is every leaf
// unverified, never a silent pass.
func TestAcceptanceWithoutEvidenceReaderIsUnverified(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Acceptance: []domain.FlowAcceptance{
			{File: "outputs/qc.csv", Check: "non-empty"},
			{File: "outputs/report.md", Check: "exists"},
		},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}

	orch := newOrchestrator(fake, children, events) // no AcceptanceEvidence
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
	require.Len(t, verdict.Checks, 2)
	for _, check := range verdict.Checks {
		assert.Equal(t, domain.AcceptanceUnverified, check.Verdict)
		assert.Equal(t, "evidence_unavailable", check.Code)
	}
	assert.Contains(t, events.types(), "flow_task_unverified")
}

// An evidence factory that fails is unverified too: a reader that cannot be
// built must never read as "nothing to check".
func TestAcceptanceEvidenceFactoryFailureIsUnverified(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "exists"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}

	orch := newOrchestrator(fake, children, events)
	orch.AcceptanceEvidence = func(context.Context, string) (AcceptanceEvidence, error) {
		return nil, errors.New("workspace unavailable")
	}
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	verdict := decodeVerdict(t, nodesByHandle(t, fake)["producer"])
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, "evidence_unavailable", verdict.Checks[0].Code)
}

// Resume never replays a completed node, and its verdict with it.
func TestAcceptanceResumeKeepsCompletedNodeAndVerdict(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "Implement {inputs.target}",
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	verdict := json.RawMessage(`{"result":"holds","checks":[{"check":"non-empty","verdict":"holds"}]}`)
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, OutputRef: json.RawMessage(`{"changedFiles":["kept.go"]}`),
		AcceptanceVerdict: &verdict,
	}))
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	orch.Recover(context.Background(), []string{"flow-run-1"})
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	assert.Equal(t, 0, countHandle(children, "producer"), "a completed node is never replayed")
	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.JSONEq(t, string(verdict), string(producer.AcceptanceVerdict))
	// The reviewer consumed the checkpoint output unchanged.
	require.NotEmpty(t, children.created)
	assert.Equal(t, "Review [\"kept.go\"]", children.created[0].Assignment)
}

// The convergence back-edge resets a loop node to pending; the verdict must not
// survive it, while the frozen specs do.
func TestConvergenceResetClearsAcceptanceVerdict(t *testing.T) {
	def := makerCheckerDef(2)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	verdict := json.RawMessage(`{"result":"holds","checks":[{"check":"exists","verdict":"holds"}]}`)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, node := range fake.nodes {
		byHandle[node.Handle] = node
	}
	for _, handle := range []string{"producer", "reviewers", "decision", "revise"} {
		node := byHandle[handle]
		node.TerminalState = domain.FlowNodeCompleted
		node.AcceptanceVerdict = append(json.RawMessage(nil), verdict...)
		node.Acceptance = []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "exists"}}
	}

	orch := newOrchestrator(fake, children, events)
	nodes, err := fake.ListNodes(context.Background(), "flow-run-1")
	require.NoError(t, err)
	changed, err := orch.applyConvergence(context.Background(), "flow-run-1", def, nodes)
	require.NoError(t, err)
	require.True(t, changed)

	after := nodesByHandle(t, fake)
	for _, handle := range []string{"reviewers", "decision", "revise"} {
		assert.Equal(t, domain.FlowNodePending, after[handle].TerminalState, handle)
		assert.Empty(t, after[handle].AcceptanceVerdict, "%s: a reset node must not keep the old verdict", handle)
		assert.NotEmpty(t, after[handle].Acceptance, "%s: the frozen specs survive the reset", handle)
	}
	assert.Equal(t, domain.FlowNodeCompleted, after["producer"].TerminalState, "nodes outside the loop keep their checkpoint")
	assert.JSONEq(t, string(verdict), string(after["producer"].AcceptanceVerdict))
}

// The rendered contract is deterministic and adds nothing when the task declares
// no acceptance clause.
func TestRenderAcceptanceAssignment(t *testing.T) {
	assert.Equal(t, "do the work", renderAcceptanceAssignment("do the work", nil))
	specs := []domain.FlowAcceptance{
		{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads", "dup_rate"}},
		{File: "outputs/qc.json", Check: "json-valid"},
		{File: "outputs/report.md", Check: "non-empty"},
		{File: "outputs/raw.bin", Check: "exists"},
	}
	first := renderAcceptanceAssignment("do the work", specs)
	assert.Equal(t, first, renderAcceptanceAssignment("do the work", specs), "rendering must be deterministic")
	assert.True(t, strings.HasPrefix(first, "do the work\n\nAcceptance ("))
	for _, want := range []string{
		"- outputs/qc.csv must have columns [sample, reads, dup_rate]",
		"- outputs/qc.json must be valid JSON",
		"- outputs/report.md must be a non-empty regular file",
		"- outputs/raw.bin must exist as a regular file",
	} {
		assert.Contains(t, first, want)
	}
}

// Crash recovery folds a child that already succeeded. When the task declares
// acceptance, that fold must verify too: a plain `completed` here would be an
// unverified task recorded as done (the fail-open case this feature removes).
func TestAcceptanceCrashRecoveryVerifiesInsteadOfSkipping(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	// The child succeeded before the crash; the checkpoint write never happened.
	children.status["child-producer"] = domain.RunSucceeded
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Payload: json.RawMessage(`{"changedFiles":["folded.go"]}`),
	}
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	}))
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	orch.Recover(context.Background(), []string{"flow-run-1"})
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, "acceptance_failed", producer.ErrorCode)
	assert.Equal(t, 0, countHandle(children, "producer"), "the succeeded child is folded, not re-run")
	assert.JSONEq(t, `{"changedFiles":["folded.go"]}`, string(producer.OutputRef))
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Result)
}

// A sibling that fails while the flow is draining an acceptance failure must not
// zero the token ledger: the whole point of the drain is that the work already
// done stays visible in the audit.
func TestAcceptanceDrainSiblingFailureKeepsUsage(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "acceptance-drain-failure",
		Budget: domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"alpha": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce", Budget: &domain.FlowTaskBudget{Tokens: 1000},
				Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}}},
			"beta": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "inspect", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
		},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	readOnlyNodes(fake)
	children := newFakeChildren()
	children.blockedHandles["beta"] = true
	events := &fakeEvents{}

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	go func() {
		time.Sleep(50 * time.Millisecond)
		children.mu.Lock()
		children.status["child-beta"] = domain.RunFailed
		children.mu.Unlock()
	}()
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Greater(t, run.TotalTokensUsed, int64(0),
		"a sibling failure during the drain must not overwrite the token total with 0")
	assert.Contains(t, events.types(), "flow_failed")
	assert.NotContains(t, events.types(), "flow_completed")
}

// Two orchestrators may reconcile the same run (a recovery racing a new start).
// The loser's CAS conflict means the node is already settled, so it must not
// fail the flow that the winner just finished verifying.
func TestAcceptanceReconcileConflictIsNotAFlowFailure(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	children.status["child-producer"] = domain.RunSucceeded
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	}))
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	// From here on the node CAS loses, exactly as it would to another
	// orchestrator that settled the node between ListNodes and UpdateNode.
	fake.conflictOnUpdate = map[int]bool{0: true}
	fake.mu.Unlock()

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	orch.Recover(context.Background(), []string{"flow-run-1"})

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.NotEqual(t, domain.FlowStateFailed, run.State,
		"losing the checkpoint CAS to another orchestrator is a settled node, not a flow failure")
	assert.NotContains(t, events.types(), "flow_failed")
}
