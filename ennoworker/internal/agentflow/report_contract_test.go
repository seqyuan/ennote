package agentflow

// Report contract enforcement at fold time: a Graph task whose child Run
// succeeded still has to return the report shape its Hero promised. The verdict
// is the node's own error_code (report_contract_violation), the child's payload
// stays as output_ref, and the flow terminalizes only after the in-flight
// siblings have settled — the same settlement path acceptance verification uses.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freezeReportContract puts a Hero report contract on a task's node the way the
// Run freeze does: inside the node's frozen HeroDefinition, so the fold reads the
// definition the child actually ran under.
func freezeReportContract(t *testing.T, fake *fakeStore, handle string, contract *domain.HeroReportContract) {
	t.Helper()
	definition := domain.HeroDefinition{
		SchemaVersion: 1, HeroPrompt: "produce", ReportContract: contract,
		ModelBinding: domain.HeroModelBinding{Mode: domain.HeroModelFixed, ModelProfileID: "model-1"},
		ContextPolicy: domain.HeroContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}},
	}
	encoded, err := json.Marshal(definition)
	require.NoError(t, err)
	for _, node := range fake.nodes {
		if node.Handle == handle {
			node.HeroDefinitionJSON = encoded
			return
		}
	}
	t.Fatalf("no node %q", handle)
}

func requiredPayloadContract() *domain.HeroReportContract {
	return &domain.HeroReportContract{Version: domain.HeroReportContractV1,
		SummaryMaxBytes: 128, RequireArtifacts: true, PayloadFields: []string{"changedFiles"}}
}

// violationDefinition is the three-task fixture plus one independent sibling and
// one dependent task, so a failing task's drain behaviour is observable.
func violationDefinition() *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: "report-contract-siblings",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"alpha": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"beta":  {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "inspect", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"gamma": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "after beta", Depends: []string{"beta"}},
			"done":  {Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}, Depends: []string{"beta"}},
		},
	}
}

// The violating report fails its own task with report_contract_violation, keeps
// the child's output_ref (the report is the evidence of what came back), does not
// cancel an in-flight sibling, dispatches nothing new, and fails the flow after
// the drain with the accumulated token total.
func TestReportContractViolationDrainsSiblingsThenFailsFlow(t *testing.T) {
	fake, err := newFakeStore(violationDefinition())
	require.NoError(t, err)
	readOnlyNodes(fake)
	freezeReportContract(t, fake, "alpha", requiredPayloadContract())
	children := newFakeChildren()
	children.blockedHandles["beta"] = true // beta is in flight when alpha's report is folded
	// A well-formed report that simply does not keep the promise: no artifact and
	// no required payload key.
	children.results["alpha"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done",
		Payload: json.RawMessage(`{"summary":"done"}`),
	}
	events := &fakeEvents{}

	orch := newOrchestrator(fake, children, events)
	go func() {
		time.Sleep(50 * time.Millisecond)
		children.mu.Lock()
		children.status["child-beta"] = domain.RunSucceeded
		children.mu.Unlock()
	}()
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	byHandle := nodesByHandle(t, fake)
	assert.Equal(t, domain.FlowNodeFailed, byHandle["alpha"].TerminalState)
	assert.Equal(t, "report_contract_violation", byHandle["alpha"].ErrorCode)
	assert.JSONEq(t, `{"summary":"done"}`, string(byHandle["alpha"].OutputRef),
		"the child's own payload is the evidence of what it returned")
	assert.Nil(t, byHandle["alpha"].AcceptanceVerdict, "a contract verdict is not an acceptance verdict")

	assert.Equal(t, domain.FlowNodeCompleted, byHandle["beta"].TerminalState,
		"an in-flight sibling drains with its own checkpoint instead of being cancelled")
	assert.Equal(t, domain.FlowNodePending, byHandle["gamma"].TerminalState,
		"no task becomes ready after the violation is recorded")
	assert.Equal(t, 0, countHandle(children, "gamma"))

	run, err := fake.GetRun(context.Background(), "flow-run-1")
	require.NoError(t, err)
	assert.Greater(t, run.TotalTokensUsed, int64(0), "a failed flow must not report zero usage")
	assert.Contains(t, run.TerminalReason, "report_contract_violation")
	assert.Contains(t, run.TerminalReason, `"alpha"`)

	types := events.types()
	assert.Contains(t, types, "flow_task_report_contract_violation")
	assert.Contains(t, types, "flow_task_completed") // beta, folded during the drain
	assert.Contains(t, types, "flow_failed")
	assert.NotContains(t, types, "flow_completed")
	assert.Equal(t, domain.RunFailed, fake.anchorStatus)

	payload := events.payloadOf("flow_task_report_contract_violation")
	require.NotNil(t, payload)
	assert.Equal(t, "report_contract_violation", payload["reason"])
	assert.Contains(t, payload["message"], "at least one artifact")
	assert.JSONEq(t, `{"summary":"done"}`, payload["outputRef"].(string))
}

// Each rule of the contract fails the task on its own; a report that satisfies
// all of them proceeds to acceptance exactly as it did before.
func TestReportContractViolationPerRule(t *testing.T) {
	tests := []struct {
		name     string
		contract *domain.HeroReportContract
		result   *domain.SubmitResult
		message  string
	}{
		{name: "summary too long",
			contract: &domain.HeroReportContract{Version: 1, SummaryMaxBytes: 4},
			result:   &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "far too long"},
			message:  "report contract v1 allows at most 4"},
		{name: "artifact required",
			contract: &domain.HeroReportContract{Version: 1, SummaryMaxBytes: 64, RequireArtifacts: true},
			result:   &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "done"},
			message:  "requires at least one artifact"},
		{name: "payload key missing",
			contract: &domain.HeroReportContract{Version: 1, SummaryMaxBytes: 64, PayloadFields: []string{"changedFiles"}},
			result: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "done",
				Payload: json.RawMessage(`{"files":[]}`)},
			message: `missing required field "changedFiles"`},
		{name: "payload not an object",
			contract: &domain.HeroReportContract{Version: 1, SummaryMaxBytes: 64, PayloadFields: []string{"changedFiles"}},
			result: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "done",
				Payload: json.RawMessage(`["changedFiles"]`)},
			message: "payload to be a JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake, err := newFakeStore(threeTaskDef())
			require.NoError(t, err)
			freezeReportContract(t, fake, "producer", test.contract)
			children := newFakeChildren()
			children.results["producer"] = test.result
			events := &fakeEvents{}

			orch := newOrchestrator(fake, children, events)
			orch.Start(context.Background(), "flow-run-1")
			require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

			producer := nodesByHandle(t, fake)["producer"]
			assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
			assert.Equal(t, "report_contract_violation", producer.ErrorCode)
			assert.Contains(t, events.payloadOf("flow_task_report_contract_violation")["message"], test.message)
			assert.Equal(t, 0, countHandle(children, "reviewer"), "a violating report stops the flow")
		})
	}
}

// A compliant report is folded exactly like a contract-free one: the acceptance
// clause is still evaluated, in the same checkpoint write.
func TestReportContractCompliantReportProceedsToAcceptance(t *testing.T) {
	def := threeTaskDef()
	def.Tasks["producer"] = domain.FlowTask{
		Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "produce",
		Budget:     &domain.FlowTaskBudget{Tokens: 1000},
		Acceptance: []domain.FlowAcceptance{{File: "outputs/qc.csv", Check: "non-empty"}},
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	freezeReportContract(t, fake, "producer", requiredPayloadContract())
	children := newFakeChildren()
	reference := domain.ArtifactReference{ArtifactID: "art-1", Name: "outputs/qc.csv", SHA256: "abc123"}
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done",
		Payload:      json.RawMessage(`{"changedFiles":["a.go"]}`),
		ArtifactRefs: []domain.ArtifactReference{reference},
	}
	events := &fakeEvents{}
	evidence := newFakeEvidence()
	evidence.files["outputs/qc.csv"] = AcceptanceFile{Size: 12, Regular: true}
	evidence.contents["outputs/qc.csv"] = "sample,reads\ns1,10\n"

	orch := acceptanceOrchestrator(t, fake, children, events, evidence)
	orch.ArtifactPresent = artifactLookup(reference, true)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Empty(t, producer.ErrorCode)
	assert.Equal(t, domain.AcceptanceHolds, decodeVerdict(t, producer).Result)
	assert.JSONEq(t, `{"changedFiles":["a.go"]}`, string(producer.OutputRef))
	types := events.types()
	assert.NotContains(t, types, "flow_task_report_contract_violation")
	assert.Contains(t, types, "flow_completed")
}

// A Graph contract-free and acceptance-free keeps exactly its previous fold: the
// contract check is skipped without decoding the frozen definition at all.
func TestContractFreeFlowIsUnchanged(t *testing.T) {
	fake, err := newFakeStore(threeTaskDef())
	require.NoError(t, err)
	children := newFakeChildren()
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeCompleted, producer.TerminalState)
	assert.Empty(t, producer.ErrorCode)
	assert.JSONEq(t, `{"changedFiles":["a.go"]}`, string(producer.OutputRef))
	types := events.types()
	assert.NotContains(t, types, "flow_task_report_contract_violation")
	assert.Contains(t, types, "flow_completed")
	assert.Equal(t, domain.RunSucceeded, fake.anchorStatus)
}

// heroReportContractOf only decodes a definition that declares a contract, and a
// check-style node (no frozen Hero definition) declares none.
func TestHeroReportContractOfReadsOnlyDeclaredContracts(t *testing.T) {
	assert.Nil(t, heroReportContractOf(nil))
	assert.Nil(t, heroReportContractOf(&domain.RunAgentFlowNode{}))
	assert.Nil(t, heroReportContractOf(&domain.RunAgentFlowNode{HeroDefinitionJSON: json.RawMessage(`{}`)}))
	broken := &domain.RunAgentFlowNode{HeroDefinitionJSON: json.RawMessage(`{"reportContract":`)}
	assert.Nil(t, heroReportContractOf(broken), "an unreadable definition cannot declare a contract")

	encoded, err := json.Marshal(domain.HeroDefinition{ReportContract: requiredPayloadContract()})
	require.NoError(t, err)
	contract := heroReportContractOf(&domain.RunAgentFlowNode{HeroDefinitionJSON: encoded})
	require.NotNil(t, contract)
	assert.Equal(t, []string{"changedFiles"}, contract.PayloadFields)
}

// A crash between the child's success and the fold must not record a violating
// report as a completed checkpoint: recovery applies the same verdict.
func TestReportContractViolationOnCrashRecovery(t *testing.T) {
	fake, err := newFakeStore(threeTaskDef())
	require.NoError(t, err)
	freezeReportContract(t, fake, "producer", requiredPayloadContract())
	children := newFakeChildren()
	children.results["producer"] = &domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done",
		Payload: json.RawMessage(`{"summary":"done"}`),
	}
	children.status["child-producer"] = domain.RunSucceeded
	// The crashed orchestrator left the node running with its child recorded.
	for _, node := range fake.nodes {
		if node.Handle == "producer" {
			node.TerminalState = domain.FlowNodeRunning
			node.ChildRunID = "child-producer"
		}
	}
	events := &fakeEvents{}
	orch := newOrchestrator(fake, children, events)
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateFailed, waitTerminal(t, fake))

	producer := nodesByHandle(t, fake)["producer"]
	assert.Equal(t, domain.FlowNodeFailed, producer.TerminalState)
	assert.Equal(t, "report_contract_violation", producer.ErrorCode)
	assert.JSONEq(t, `{"summary":"done"}`, string(producer.OutputRef))
	assert.Contains(t, events.types(), "flow_task_report_contract_violation")
	assert.Equal(t, 0, countHandle(children, "reviewer"), "recovery must not dispatch the rest of the graph")
}
