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
	assert.Contains(t, assignment, "Acceptance (checked by the runtime after you finish, not from your report:")
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

	// The command leaf is rendered too: a child that is never told which
	// invocation will be checked is failing a contract it never saw.
	command := renderAcceptanceAssignment("do the work", []domain.FlowAcceptance{
		{Check: "tests-passed", Command: "go test ./..."},
	})
	assert.Contains(t, command, "`go test ./...`")
	assert.Contains(t, command, "recorded shell executions")
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
// The loser's checkpoint CAS fails because the winner already settled the node;
// reconcile must treat that as "settled", not as a reason to fail the flow. The
// property is pinned on reconcileCrashedNodes directly: driving it through the
// run loop would make the assertion race the recovery goroutine, which is how
// this test was flaky in CI.
func TestReconcileSwallowsCheckpointConflict(t *testing.T) {
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
	fake.conflictOnUpdate = map[int]bool{0: true}
	fake.mu.Unlock()

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	require.NoError(t, orch.reconcileCrashedNodes(context.Background(), "flow-run-1"),
		"losing the checkpoint CAS to another orchestrator is a settled node, not an error")
	assert.NotContains(t, events.types(), "flow_failed")
}

// The completed event carries the same run-level summary the API projects from
// the node rows, so an auditor reading the timeline sees which tasks were only
// "nothing was disproved" without opening the run. It is computed from the nodes
// the dispatch loop already holds: no extra query.
func TestAcceptanceSummaryRidesOnFlowCompleted(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Budget:     &domain.FlowTaskBudget{Tokens: 1000},
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	def.Tasks["reviewer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "review", Depends: []string{"producer"},
		Acceptance: []domain.FlowAcceptance{{File: "outputs/review.json", Check: "json-valid"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	evidence := newFakeEvidence()
	evidence.files["outputs/qc.csv"] = AcceptanceFile{Size: 12, Regular: true}
	evidence.contents["outputs/qc.csv"] = "sample,reads\ns1,10\n"
	// Registered but unreadable: the reviewer's clause is unverified, not failed.
	evidence.files["outputs/review.json"] = AcceptanceFile{Size: 20, Regular: true}

	orch := acceptanceOrchestrator(t, fake, children, events, evidence)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	payload := events.payloadOf("flow_completed")
	require.NotNil(t, payload)
	require.Contains(t, payload, "acceptance")
	assert.Equal(t, &domain.AcceptanceSummary{Total: 2, Holds: 1, Unverified: 1}, payload["acceptance"])
}

// A flow whose tasks declared nothing publishes exactly the payload it used to:
// no acceptance key, no zeroed summary that would read as "all verified".
func TestFlowCompletedWithoutAcceptanceOmitsSummary(t *testing.T) {
	fake, err := newFakeStore(threeTaskDef())
	require.NoError(t, err)
	events := &fakeEvents{}

	orch := newOrchestrator(fake, newFakeChildren(), events)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	payload := events.payloadOf("flow_completed")
	require.NotNil(t, payload)
	assert.NotContains(t, payload, "acceptance")
}

// --- artifact-published -----------------------------------------------------

// artifactAcceptanceTask makes the entry task declare the published-artifact
// leaf: the only clause of this fixture, so no workspace evidence is involved.
func artifactAcceptanceTask(t *testing.T, def *domain.FlowDefinition) {
	t.Helper()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Budget: &domain.FlowTaskBudget{Tokens: 1000},
		Acceptance: []domain.FlowAcceptance{
			{Artifact: "outputs/qc.csv", Check: "artifact-published"},
		},
	}
}

// artifactLookup answers the store seam for one recorded reference and nothing
// else, so a digest mismatch and a missing row are both reachable.
func artifactLookup(reference domain.ArtifactReference, present bool) func(context.Context, string, string) (bool, error) {
	return func(_ context.Context, artifactID, sha256 string) (bool, error) {
		return present && artifactID == reference.ArtifactID && sha256 == reference.SHA256, nil
	}
}

// A matching ref in the child's terminal submit_result holds, and the leaf needs
// no workspace evidence reader at all.
func TestAcceptanceArtifactPublishedHolds(t *testing.T) {
	def := threeTaskDef()
	artifactAcceptanceTask(t, def)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	reference := domain.ArtifactReference{ArtifactID: "art-1", Name: "outputs/qc.csv", SHA256: "abc123"}
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Payload: json.RawMessage(`{"changedFiles":["a.go"]}`),
		ArtifactRefs: []domain.ArtifactReference{reference},
	}
	events := &fakeEvents{}

	orch := newOrchestrator(fake, children, events)
	// An artifact-only clause never opens the workspace jail: the factory is
	// wired but must not be asked.
	orch.AcceptanceEvidence = func(context.Context, string) (AcceptanceEvidence, error) {
		t.Error("an artifact-only clause must not read workspace evidence")
		return newFakeEvidence(), nil
	}
	orch.ArtifactPresent = artifactLookup(reference, true)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Result)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, "artifact-published", verdict.Checks[0].Check)
	assert.Empty(t, verdict.Checks[0].Code)

	types := events.types()
	assert.Contains(t, types, "flow_task_completed")
	assert.NotContains(t, types, "flow_task_unverified")
	assert.NotContains(t, types, "flow_task_acceptance_failed")

	// The child is told the contract, in the same task-data channel.
	require.NotEmpty(t, children.created)
	assert.Contains(t, children.created[0].Assignment,
		"- artifact outputs/qc.csv must be published and still retrievable from managed storage")
}

// A child that reports success without publishing the declared artifact fails
// the task and the flow, and keeps its output_ref: the report is what shows what
// was (not) done.
func TestAcceptanceArtifactPublishedMissingFailsFlow(t *testing.T) {
	def := threeTaskDef()
	artifactAcceptanceTask(t, def)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done", Payload: json.RawMessage(`{"changedFiles":["a.go"]}`),
	}
	events := &fakeEvents{}

	orch := newOrchestrator(fake, children, events)
	orch.ArtifactPresent = artifactLookup(domain.ArtifactReference{ArtifactID: "art-1", Name: "outputs/qc.csv", SHA256: "abc123"}, true)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, "acceptance_failed", producer.ErrorCode)
	assert.JSONEq(t, `{"changedFiles":["a.go"]}`, string(producer.OutputRef))
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Result)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, "artifact_missing", verdict.Checks[0].Code)
	assert.Equal(t, 0, countHandle(children, "reviewer"), "a failed acceptance stops the flow")

	types := events.types()
	assert.Contains(t, types, "flow_task_acceptance_failed")
	assert.Contains(t, types, "flow_failed")
	assert.Equal(t, domain.RunFailed, fake.anchorStatus, "the meta-Run is failed, not left running")
}

// A runtime with no artifact-store seam cannot confirm anything: the task
// completes as unverified and the flow keeps going, visibly.
func TestAcceptanceArtifactPublishedWithoutStoreSeamIsUnverified(t *testing.T) {
	def := threeTaskDef()
	artifactAcceptanceTask(t, def)
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Payload: json.RawMessage(`{"changedFiles":["a.go"]}`),
		ArtifactRefs: []domain.ArtifactReference{
			{ArtifactID: "art-1", Name: "outputs/qc.csv", SHA256: "abc123"},
		},
	}
	events := &fakeEvents{}

	orch := newOrchestrator(fake, children, events) // ArtifactPresent stays nil
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, "evidence_unavailable", verdict.Checks[0].Code)

	types := events.types()
	assert.Contains(t, types, "flow_task_unverified")
	assert.NotContains(t, types, "flow_task_acceptance_failed")
}

// Crash recovery folds a child that succeeded before the crash. When its
// terminal result cannot be read back, the artifact leaf is unverified — not
// missing — because "the report is gone" and "the report has no such ref" are
// different facts.
func TestAcceptanceArtifactCrashRecoveryWithoutResultIsUnverified(t *testing.T) {
	def := threeTaskDef()
	artifactAcceptanceTask(t, def)
	// The next task reads no template from the folded output: the point of this
	// fixture is the verdict, and an empty payload would fail goal resolution.
	def.Tasks["reviewer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "review", Depends: []string{"producer"},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	// Succeeded, but no recorded submit_result survives (ChildTerminalResult
	// returns nil for an unknown handle).
	children.status["child-producer"] = domain.RunSucceeded
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	}))
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()

	orch := newOrchestrator(fake, children, &fakeEvents{})
	orch.ArtifactPresent = func(context.Context, string, string) (bool, error) {
		t.Fatal("the store must not be consulted when no ref was recorded")
		return false, nil
	}
	orch.Recover(context.Background(), []string{"flow-run-1"})
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState,
		"an unreadable result never fails the task it could not read")
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, "evidence_unavailable", verdict.Checks[0].Code)
}

// fakeExecutions is the ChildExecutions seam of the orchestrator: per child Run,
// the executions the test decides the child recorded.
type fakeExecutions struct {
	mu    sync.Mutex
	byRun map[string][]domain.ToolExecution
	err   error
	asked []string
}

func (f *fakeExecutions) seam(ctx context.Context, childRunID string) ([]domain.ToolExecution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, childRunID)
	if f.err != nil {
		return nil, f.err
	}
	return f.byRun[childRunID], nil
}

func (f *fakeExecutions) children() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// testsPassedDef is a three-task flow whose producer must have run `go test -v`.
// The clause is verbose because a non-verbose `go test` clause is refused at
// publish: without per-test output a zero-test run prints the same banner as a
// real one.
func testsPassedDef() *domain.FlowDefinition {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "implement",
		Acceptance: []domain.FlowAcceptance{{Check: "tests-passed", Command: "go test -v ./..."}},
	}
	return def
}

// holds: the child recorded the declared command and its projected output shows
// a passing summary, so the node completes with a holds verdict.
func TestAcceptanceTestsPassedHoldsThroughTheFold(t *testing.T) {
	fake, err := newFakeStore(testsPassedDef())
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	executions := &fakeExecutions{byRun: map[string][]domain.ToolExecution{
		"child-producer": {{Seq: 4, Tool: "bash", Command: "go test -v ./...", ExitCode: 0,
			Output: "=== RUN   TestA\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.002s\n"}},
	}}

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	orch.ChildExecutions = executions.seam
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Result)
	require.Len(t, verdict.Checks, 1)
	assert.Equal(t, "tests-passed", verdict.Checks[0].Check)
	assert.Contains(t, executions.children(), "child-producer")
}

// does_not_hold: the recorded output of the declared command shows a failing
// run, so the task fails and the flow terminalizes as failed.
func TestAcceptanceTestsPassedFailureFailsFlow(t *testing.T) {
	fake, err := newFakeStore(testsPassedDef())
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	executions := &fakeExecutions{byRun: map[string][]domain.ToolExecution{
		"child-producer": {{Seq: 1, Tool: "bash", Command: "go test -v ./...", ExitCode: 1, IsError: true,
			Output: "--- FAIL: TestA (0.00s)\nFAIL\texample.com/x\t0.003s\n"}},
	}}

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	orch.ChildExecutions = executions.seam
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, "acceptance_failed", producer.ErrorCode)
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceDoesNotHold, verdict.Result)
	assert.Equal(t, "command_failed", verdict.Checks[0].Code)
	assert.Contains(t, events.types(), "flow_task_acceptance_failed")
}

// No execution seam: the leaf is unverified, the checkpoint records it, and the
// flow still completes. A runtime that cannot read executions must not be able
// to fail a task by omission, and must never report it as verified either.
func TestAcceptanceTestsPassedWithoutExecutionSeamIsUnverified(t *testing.T) {
	fake, err := newFakeStore(testsPassedDef())
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}

	orch := newOrchestrator(fake, children, events) // no ChildExecutions
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
	assert.Equal(t, "evidence_unavailable", verdict.Checks[0].Code)
	assert.Contains(t, events.types(), "flow_task_unverified")
}

// A failing execution query is unverified, not "no execution": an infrastructure
// failure must stay distinguishable from a child that never ran the command.
func TestAcceptanceTestsPassedExecutionQueryFailureIsUnverified(t *testing.T) {
	fake, err := newFakeStore(testsPassedDef())
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	executions := &fakeExecutions{err: errors.New("database is locked")}

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	orch.ChildExecutions = executions.seam
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	verdict := decodeVerdict(t, nodesByHandle(t, fake)["producer"])
	assert.Equal(t, domain.AcceptanceUnverified, verdict.Result)
	assert.Equal(t, "evidence_unavailable", verdict.Checks[0].Code)
}

// Crash recovery passes the same facts: a node folded after a restart reads the
// child's recorded executions instead of skipping the clause.
func TestAcceptanceTestsPassedCrashRecoveryUsesRecordedExecutions(t *testing.T) {
	fake, err := newFakeStore(testsPassedDef())
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	children.status["child-producer"] = domain.RunSucceeded
	children.results["producer"] = &domain.SubmitResult{Status: domain.SubmitCompleted,
		Payload: json.RawMessage(`{"changedFiles":["folded.go"]}`)}
	require.NoError(t, fake.UpdateNode(context.Background(), "flow-run-1", NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-producer",
	}))
	fake.mu.Lock()
	fake.run.State = domain.FlowStateRunning
	fake.mu.Unlock()
	executions := &fakeExecutions{byRun: map[string][]domain.ToolExecution{
		"child-producer": {{Seq: 7, Tool: "exec", Argv: []string{"go", "test", "-v", "./..."}, ExitCode: 0,
			Output: "=== RUN   TestA\n--- PASS: TestA (0.00s)\nok  \texample.com/x\t0.002s\n"}},
	}}

	orch := acceptanceOrchestrator(t, fake, children, events, newFakeEvidence())
	orch.ChildExecutions = executions.seam
	orch.Recover(context.Background(), []string{"flow-run-1"})
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	verdict := decodeVerdict(t, producer)
	assert.Equal(t, domain.AcceptanceHolds, verdict.Result)
	assert.Equal(t, 0, countHandle(children, "producer"), "the succeeded child is folded, not re-run")
	assert.Contains(t, executions.children(), "child-producer")
}
