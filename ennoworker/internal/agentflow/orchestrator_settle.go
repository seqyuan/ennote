package agentflow

// Child settlement and flow terminalization: folding a settled child, failing a
// task, and writing the meta-Run's terminal state. Kept apart from the dispatch
// loop in orchestrator.go so a reader can follow one concern at a time.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// waitAndFoldAny polls all in-flight hero children and folds the first one
// that settles, removing it from the set. The rest keep running; the caller
// re-evaluates the ready set. On flow cancellation every in-flight child is
// hard-cancelled and the caller's next loop iteration terminalizes the run.
func (o *Orchestrator) waitAndFoldAny(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	inFlight *[]inflightTask) error {
	ticker := time.NewTicker(o.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if o.isCancelled(ctx, runID) {
			o.cancelInflight(ctx, runID, *inFlight)
			return nil // loop top terminalizes via cancelRemaining
		}
		settled := -1
		for i := range *inFlight {
			status, err := o.Children.ChildRunStatus(ctx, (*inFlight)[i].childID)
			if err != nil {
				return err
			}
			if status.Terminal() {
				settled = i
				break
			}
		}
		if settled < 0 {
			continue
		}
		task := (*inFlight)[settled]
		*inFlight = append((*inFlight)[:settled], (*inFlight)[settled+1:]...)
		result, _ := o.Children.ChildTerminalResult(ctx, task.childID)
		status, _ := o.Children.ChildRunStatus(ctx, task.childID)
		return o.foldHeroChild(ctx, runID, flow, task.node, task.childID, status, result, *inFlight)
	}
}

// cancelInflight hard-cancels a set of in-flight children and marks their
// nodes cancelled, without touching the flow state (used on parallel task
// failure and budget overrun; user cancellation takes the cancelRemaining
// path instead).
func (o *Orchestrator) cancelInflight(ctx context.Context, runID string, inFlight []inflightTask) {
	for _, task := range inFlight {
		_ = o.Children.CancelChildRun(ctx, task.childID)
		_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
			TaskIndex: task.node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
			SetState: domain.FlowNodeCancelled,
		})
	}
}

// acceptanceSummaryOf folds the declared acceptance clauses of the nodes the
// dispatch loop already holds: the run-level summary of a completed flow costs
// no extra query. It returns nil when no node declared a clause, so a flow
// without acceptance keeps publishing exactly the payload it used to.
func acceptanceSummaryOf(nodes []*domain.RunAgentFlowNode) *domain.AcceptanceSummary {
	var summary *domain.AcceptanceSummary
	for _, node := range nodes {
		if node == nil || len(node.Acceptance) == 0 {
			continue
		}
		if summary == nil {
			summary = &domain.AcceptanceSummary{}
		}
		summary.AddDeclared(domain.AcceptanceResultFromVerdict(node.AcceptanceVerdict))
	}
	return summary
}

func (o *Orchestrator) complete(ctx context.Context, runID string, totalTokens int64,
	outputs map[string]json.RawMessage, nodes []*domain.RunAgentFlowNode) error {
	payload := map[string]any{"flowRunId": runID, "outputs": map[string]any{}}
	for name, value := range outputs {
		payload["outputs"].(map[string]any)[name] = json.RawMessage(value)
	}
	// The event carries the same summary the API projects from the node rows, so
	// an auditor reading the timeline sees which tasks were only "nothing was
	// disproved" without opening the run. Absent when nothing declared a clause.
	if summary := acceptanceSummaryOf(nodes); summary != nil {
		payload["acceptance"] = summary
	}
	if err := o.Store.UpdateFlowState(ctx, runID, domain.FlowStateCompleted, totalTokens, ""); err != nil {
		return err
	}
	if err := o.Events.PublishFlow(ctx, runID, "flow_completed", payload); err != nil {
		return err
	}
	return o.Store.TerminateAnchor(ctx, runID, domain.RunSucceeded, "", "")
}

func (o *Orchestrator) fail(ctx context.Context, runID, reason string) {
	_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateFailed, 0, reason)
	_ = o.Events.PublishFlow(ctx, runID, "flow_failed", map[string]any{"flowRunId": runID, "reason": reason})
	_ = o.Store.TerminateAnchor(ctx, runID, domain.RunFailed, "flow_failed", reason)
}

// failKeepingUsage fails the flow while preserving the token ledger it already
// accumulated. fail writes 0 (an existing defect at its other call sites); a
// flow that runs tasks and then fails must not look like it consumed nothing.
func (o *Orchestrator) failKeepingUsage(ctx context.Context, runID string, total int64, reason string) {
	_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateFailed, total, reason)
	_ = o.Events.PublishFlow(ctx, runID, "flow_failed", map[string]any{"flowRunId": runID, "reason": reason})
	_ = o.Store.TerminateAnchor(ctx, runID, domain.RunFailed, "flow_failed", reason)
}

func (o *Orchestrator) failTask(ctx context.Context, runID string, node *domain.RunAgentFlowNode, code string) {
	_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeFailed, ErrorCode: code,
	})
	_ = o.Events.PublishFlow(ctx, runID, "flow_task_failed", map[string]any{
		"flowRunId": runID, "task": node.Handle, "childRunId": node.ChildRunID, "reason": code,
	})
}
