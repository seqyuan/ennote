package agentflow

// Acceptance verification at fold time: a Graph task whose child Run succeeded
// still has to satisfy the acceptance clause it declared. The verdict is written
// in the same checkpoint CAS as the task state, and the child's own report is
// never evidence. This file holds the fold side; the evaluator and its leaf
// vocabulary live in acceptance.go, and the authoring-side validation lives in
// validate_acceptance.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// acceptanceFailedCode is the node error code of a task whose declared
// acceptance conditions did not hold. It is also the durable fact the run loop
// reads to stop dispatching and terminalize the flow (no extra column/state).

const acceptanceFailedCode = "acceptance_failed"

// reportContractViolationCode is the node error code of a task whose child ended
// completed with a report that does not keep the shape its Hero's report
// contract promised. It is a verdict like acceptanceFailedCode, not a task
// crash: the checkpoint carries it, the child's output_ref stays, and the run
// loop terminalizes the flow only after the in-flight siblings have settled.
const reportContractViolationCode = string(domain.ErrorReportContractViolation)

// blockedBlockerPrefix is the node error-code prefix of a task whose child
// ended `blocked` or `needs_input`. The code is `blocked:<blocker>` so the
// checkpoint row states the reason in a value a reader (or a filter) can match,
// the way acceptance_failed already states its own verdict fact.
const blockedBlockerPrefix = "blocked:"

// blockedChildCode returns the node error code of a child that ended blocked or
// needs_input with a typed blocker. The second result reports whether the node
// is blocked at all: a result written before the field existed carries no
// blocker, and its fold must stay exactly what it was (a completed node), never
// a guessed reason.
func blockedChildCode(result *domain.SubmitResult) (string, bool) {
	if result == nil || result.Blocker == "" {
		return "", false
	}
	switch result.Status {
	case domain.SubmitBlocked, domain.SubmitNeedsInput:
		return blockedBlockerPrefix + string(result.Blocker), true
	default:
		return "", false
	}
}

// foldBlockedChild records a task whose child ended blocked/needs_input as a
// `blocked` checkpoint carrying `blocked:<blocker>`, and terminalizes the flow
// with a reason that names the same code. It deliberately does not reuse
// failTask: a blocked node is not a failed one, and the checkpoint state is the
// fact a reader filters on. Every fold path (live hero child, fan_out instance,
// crash recovery) goes through here so a restart cannot turn a blocked child
// into a completed node.
func (o *Orchestrator) foldBlockedChild(ctx context.Context, runID string, node *domain.RunAgentFlowNode,
	code string, total int64) {
	_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeBlocked, ErrorCode: code,
	})
	_ = o.Events.PublishFlow(ctx, runID, "flow_task_blocked", map[string]any{
		"flowRunId": runID, "task": node.Handle, "childRunId": node.ChildRunID,
		"reason": code, "blocker": strings.TrimPrefix(code, blockedBlockerPrefix),
	})
	o.failKeepingUsage(ctx, runID, total, fmt.Sprintf("task %q %s", node.Handle, code))
}

// foldBlockedHeroChild folds a hero child that ended blocked/needs_input: it
// cancels the in-flight siblings (a blocked task is a dead end, exactly like a
// failed one) and reports whether it was blocked at all. A result without a
// blocker (written before the field existed) is not blocked here, so the caller
// keeps its pre-existing behaviour: the node completes.
func (o *Orchestrator) foldBlockedHeroChild(ctx context.Context, runID string, node *domain.RunAgentFlowNode,
	result *domain.SubmitResult, total int64, remaining []inflightTask) bool {
	code, blocked := blockedChildCode(result)
	if !blocked {
		return false
	}
	o.cancelInflight(ctx, runID, remaining)
	o.foldBlockedChild(ctx, runID, node, code, total)
	return true
}

// foldBlockedInstance is the same fold for one instance of a fan_out task, where
// there is no sibling to cancel and the caller already counted every instance's
// usage.
func (o *Orchestrator) foldBlockedInstance(ctx context.Context, runID string, node *domain.RunAgentFlowNode,
	result *domain.SubmitResult, total int64) bool {
	code, blocked := blockedChildCode(result)
	if !blocked {
		return false
	}
	o.foldBlockedChild(ctx, runID, node, code, total)
	return true
}

// foldRecoveredBlocker applies the same rule when a crashed fold is replayed: a
// child that settled blocked before the crash must not become a completed
// checkpoint after it. The ledger is read at fold time because the recovery loop
// has already folded every recorded child's usage into it.
func (o *Orchestrator) foldRecoveredBlocker(ctx context.Context, runID string,
	node *domain.RunAgentFlowNode, results []*domain.SubmitResult) bool {
	for _, result := range results {
		code, blocked := blockedChildCode(result)
		if !blocked {
			continue
		}
		total := int64(0)
		if run, err := o.Store.GetRun(ctx, runID); err == nil {
			total = run.TotalTokensUsed
		}
		o.foldBlockedChild(ctx, runID, node, code, total)
		return true
	}
	return false
}

// acceptanceFailureNode returns the first checkpoint marked failed by acceptance
// verification, or nil.
func acceptanceFailureNode(nodes []*domain.RunAgentFlowNode) *domain.RunAgentFlowNode {
	for _, node := range nodes {
		if node.TerminalState == domain.FlowNodeFailed && node.ErrorCode == acceptanceFailedCode {
			return node
		}
	}
	return nil
}

// heroReportContractOf is the report contract frozen on a task's Hero. The node
// freezes the whole resolved HeroDefinition at Run start, so the fold reads the
// exact contract the child ran under instead of re-resolving a mutable Hero
// file. The substring pre-check is what keeps a contract-free Graph as cheap as
// it was: no Hero declares a contract, so no definition is decoded.
func heroReportContractOf(node *domain.RunAgentFlowNode) *domain.HeroReportContract {
	if node == nil || len(node.HeroDefinitionJSON) == 0 ||
		!bytes.Contains(node.HeroDefinitionJSON, []byte(`"reportContract"`)) {
		return nil
	}
	var definition domain.HeroDefinition
	if err := json.Unmarshal(node.HeroDefinitionJSON, &definition); err != nil {
		// The freeze writes the definition atomically with the node row, so an
		// unreadable one is corruption, not a declaration. The child could not
		// have run without a readable definition, and inventing a contract here
		// would be a second source of truth.
		return nil
	}
	return definition.ReportContract
}

// foldReportContractViolation records a task whose child ended completed with a
// report that breaks its Hero's contract. Like a does_not_hold acceptance
// verdict it does NOT cancel the in-flight siblings and it keeps the child's
// payload as output_ref: the report is the evidence of what the child actually
// returned, and a sibling that already finished must not be discarded. The run
// loop terminalizes the flow once the in-flight children have settled.
func (o *Orchestrator) foldReportContractViolation(ctx context.Context, runID string,
	node *domain.RunAgentFlowNode, childID string, payload json.RawMessage, violation error) error {
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeFailed, ErrorCode: reportContractViolationCode, OutputRef: payload,
	}); err != nil {
		return err
	}
	return o.Events.PublishFlow(ctx, runID, "flow_task_report_contract_violation", map[string]any{
		"flowRunId": runID, "task": node.Handle, "childRunId": childID,
		"reason": reportContractViolationCode, "message": violation.Error(), "outputRef": string(payload),
	})
}

// settlementFailureNode returns the first checkpoint whose recorded verdict must
// fail the flow once the in-flight siblings drain. Acceptance verification and
// report contract enforcement both record their verdict as a failed checkpoint
// with a code, so they share one settlement path in the dispatch loop.
//
// A failed-but-optional node is skipped: `optional` means "this task's failure
// does not fail the flow", and a verdict against the product is exactly a
// failure of that task. Retries cover the attempt-shaped failures (see
// retryableFailureCode); a verdict is never retried, so `optional` is the only
// knob that can absorb it.
func settlementFailureNode(nodes []*domain.RunAgentFlowNode) *domain.RunAgentFlowNode {
	for _, node := range nodes {
		if node.TerminalState != domain.FlowNodeFailed || node.Optional {
			continue
		}
		if node.ErrorCode == acceptanceFailedCode || node.ErrorCode == reportContractViolationCode {
			return node
		}
	}
	return nil
}

// clearedAcceptanceVerdict is the non-nil empty verdict of a NodeUpdate: a node
// reset to pending must not keep the previous attempt's verdict.
func clearedAcceptanceVerdict() *json.RawMessage {
	empty := json.RawMessage{}
	return &empty
}

// errNoArtifactLookup is the lookup answer of a runtime that cannot confirm a
// published artifact at all (no store seam configured). It becomes
// unverified/evidence_unavailable, never a pass.
var errNoArtifactLookup = errors.New("no published-artifact lookup is configured")

// childArtifacts is the AcceptanceArtifacts view of one child Run. Refs are the
// child's recorded submit_result.artifactRefs; the lookup is the orchestrator's
// store seam. A missing terminal result yields nil, so its artifact leaves are
// unverified instead of falsely missing: "the report could not be read" and "the
// report does not contain the ref" are different facts.
func (o *Orchestrator) childArtifacts(result *domain.SubmitResult) AcceptanceArtifacts {
	if result == nil {
		return nil
	}
	return childArtifactRefs{refs: result.ArtifactRefs, present: o.ArtifactPresent}
}

// childArtifactRefs is the artifact evidence of one settled child.
type childArtifactRefs struct {
	refs    []domain.ArtifactReference
	present func(ctx context.Context, artifactID, sha256 string) (bool, error)
}

func (c childArtifactRefs) Refs() []domain.ArtifactReference { return c.refs }

func (c childArtifactRefs) Present(ctx context.Context, artifactID, sha256 string) (bool, error) {
	if c.present == nil {
		return false, errNoArtifactLookup
	}
	return c.present(ctx, artifactID, sha256)
}

// childExecutions is the AcceptanceExecutions view of one child Run: the shell
// executions its own tool calls recorded. A runtime without the seam, or one
// whose query failed, yields the nil interface (not an empty list): "cannot be
// read" must stay distinguishable from "read, and there were none", or an
// infrastructure failure would read as evidence_no_execution.
type childExecutions struct{ executions []domain.ToolExecution }

func (c childExecutions) Executions() []domain.ToolExecution { return c.executions }

// childExecutionsOf resolves one child's recorded executions. Only a clause that
// declares tests-passed pays for the query.
func (o *Orchestrator) childExecutionsOf(ctx context.Context, specs []domain.FlowAcceptance,
	childID string) AcceptanceExecutions {
	if !needsExecutions(specs) || o.ChildExecutions == nil {
		return nil
	}
	executions, err := o.ChildExecutions(ctx, childID)
	if err != nil {
		return nil
	}
	return childExecutions{executions: executions}
}

// evaluateAcceptance verifies a task's frozen acceptance clause under the task
// deadline. It is read-only: crash recovery may fold the same node twice, so it
// must not publish, notify or count anything.
func (o *Orchestrator) evaluateAcceptance(ctx context.Context, sessionID, childID string,
	specs []domain.FlowAcceptance, artifacts AcceptanceArtifacts) AcceptanceVerdictValue {
	evalCtx, cancel := context.WithTimeout(ctx, acceptanceTaskTimeout)
	defer cancel()
	facts := AcceptanceFacts{Artifacts: artifacts, Executions: o.childExecutionsOf(evalCtx, specs, childID)}
	if needsWorkspaceEvidence(specs) {
		facts.Evidence = o.acceptanceEvidence(evalCtx, sessionID)
	}
	return EvaluateAcceptance(evalCtx, specs, facts)
}

// acceptanceEvidence resolves the workspace reader for one fold. A reader that
// cannot be built yields nil, which makes every FILE leaf
// unverified/evidence_unavailable: never a pass, never a silent skip. Artifact
// leaves do not depend on it, so they are still decided on their own evidence.
func (o *Orchestrator) acceptanceEvidence(ctx context.Context, sessionID string) AcceptanceEvidence {
	if o.AcceptanceEvidence == nil {
		return nil
	}
	evidence, err := o.AcceptanceEvidence(ctx, sessionID)
	if err != nil {
		return nil
	}
	return evidence
}

// foldAcceptance verifies a succeeded child's declared acceptance clause and
// writes the verdict in the same CAS as the checkpoint. Events are published
// only after that write commits (commit-before-publish).
//
// does_not_hold does NOT reuse failTask or cancelInflight: cancelling every
// in-flight sibling would discard valid progress, and the child's output_ref is
// exactly the evidence of what went wrong. The node records the failure and the
// run loop terminalizes the flow once the in-flight siblings have settled.
func (o *Orchestrator) foldAcceptance(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	node *domain.RunAgentFlowNode, childID string, payload json.RawMessage,
	result *domain.SubmitResult) error {
	verdict := o.evaluateAcceptance(ctx, flow.SessionID, childID, node.Acceptance, o.childArtifacts(result))
	encoded, err := json.Marshal(verdict)
	if err != nil {
		// Unreachable for the closed verdict shape, but a checkpoint written
		// without its verdict would be a silent pass; degrade to unverified.
		verdict = unverifiedAcceptanceVerdict(node.Acceptance, codeAcceptanceUnavailable,
			"acceptance verdict could not be encoded")
		encoded, _ = json.Marshal(verdict)
	}
	raw := json.RawMessage(encoded)

	if verdict.Result == domain.AcceptanceDoesNotHold {
		if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
			TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
			SetState: domain.FlowNodeFailed, ErrorCode: acceptanceFailedCode,
			OutputRef: payload, AcceptanceVerdict: &raw,
		}); err != nil {
			return err
		}
		return o.Events.PublishFlow(ctx, runID, "flow_task_acceptance_failed", map[string]any{
			"flowRunId": runID, "task": node.Handle, "childRunId": childID,
			"reason": acceptanceFailedCode, "verdict": verdict,
		})
	}

	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeCompleted, OutputRef: payload, AcceptanceVerdict: &raw,
	}); err != nil {
		return err
	}
	if err := o.Events.PublishFlow(ctx, runID, "flow_task_completed", map[string]any{
		"flowRunId": runID, "task": node.Handle, "outputRef": string(payload), "childRunId": childID,
	}); err != nil {
		return err
	}
	if verdict.Result == domain.AcceptanceUnverified {
		// The flow keeps going; the marker is what tells a human that "completed"
		// here means "nothing was disproved", not "everything was verified".
		return o.Events.PublishFlow(ctx, runID, "flow_task_unverified", map[string]any{
			"flowRunId": runID, "task": node.Handle, "childRunId": childID, "verdict": verdict,
		})
	}
	return nil
}
