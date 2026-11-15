package agentflow

// Acceptance verification at fold time: a Graph task whose child Run succeeded
// still has to satisfy the acceptance clause it declared. The verdict is written
// in the same checkpoint CAS as the task state, and the child's own report is
// never evidence. This file holds the fold side; the evaluator and its leaf
// vocabulary live in acceptance.go, and the authoring-side validation lives in
// validate_acceptance.go.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// acceptanceFailedCode is the node error code of a task whose declared
// acceptance conditions did not hold. It is also the durable fact the run loop
// reads to stop dispatching and terminalize the flow (no extra column/state).

const acceptanceFailedCode = "acceptance_failed"

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

// evaluateAcceptance verifies a task's frozen acceptance clause under the task
// deadline. It is read-only: crash recovery may fold the same node twice, so it
// must not publish, notify or count anything.
func (o *Orchestrator) evaluateAcceptance(ctx context.Context, sessionID string,
	specs []domain.FlowAcceptance, artifacts AcceptanceArtifacts) AcceptanceVerdictValue {
	evalCtx, cancel := context.WithTimeout(ctx, acceptanceTaskTimeout)
	defer cancel()
	var evidence AcceptanceEvidence
	if needsWorkspaceEvidence(specs) {
		evidence = o.acceptanceEvidence(evalCtx, sessionID)
	}
	return EvaluateAcceptance(evalCtx, specs, evidence, artifacts)
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
	verdict := o.evaluateAcceptance(ctx, flow.SessionID, node.Acceptance, o.childArtifacts(result))
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
