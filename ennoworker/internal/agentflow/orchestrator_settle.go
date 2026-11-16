package agentflow

// Child settlement and flow terminalization: folding a settled child (live or
// recovered after a crash), failing a task, and writing the meta-Run's terminal
// state. Kept apart from the dispatch loop in orchestrator.go so a reader can
// follow one concern at a time.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// reconcileCrashedNodes folds any node left in 'running' state by a crashed
// orchestrator. For each such node:
//   - child Run reached a terminal fact -> the result is folded into the
//     checkpoint (completed + output_ref / failed / cancelled) and usage is
//     accumulated, exactly like a normal dispatch settlement;
//   - child Run is not terminal (or unknown) -> the node is reset to
//     interrupted so the dispatch loop re-runs the task once.
//
// The flow budget check is applied after folding so a recovered run that
// already exceeded its ceiling terminalizes as budget_exceeded instead of
// dispatching more tasks.
func (o *Orchestrator) reconcileCrashedNodes(ctx context.Context, runID string) error {
	nodes, err := o.Store.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	flow, err := o.Store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	budgetLimit := o.flowBudgetLimit(ctx, flow.FlowVersionID)
	for i := range nodes {
		node := nodes[i]
		if node.TerminalState != domain.FlowNodeRunning {
			continue
		}
		reset := func() {
			_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
				TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
				SetState: domain.FlowNodeInterrupted,
			})
		}
		childIDs := o.nodeChildIDs(node)
		if len(childIDs) == 0 {
			reset()
			continue
		}
		// Every recorded child must be terminal with the same verdict for the
		// checkpoint to be foldable; otherwise the whole task is re-dispatched.
		foldable := true
		for _, childID := range childIDs {
			status, err := o.Children.ChildRunStatus(ctx, childID)
			if err != nil || !status.Terminal() || status != domain.RunSucceeded {
				foldable = false
				break
			}
		}
		if !foldable {
			// Still active (or the row is gone, or an instance failed): reset
			// for re-dispatch. Cancel only the non-succeeded stragglers;
			// succeeded instances keep their folded results untouched.
			for _, childID := range childIDs {
				status, err := o.Children.ChildRunStatus(ctx, childID)
				if err != nil || status != domain.RunSucceeded {
					_ = o.Children.CancelChildRun(ctx, childID)
				}
			}
			reset()
			continue
		}
		aggregate := make([]json.RawMessage, 0, len(childIDs))
		results := make([]*domain.SubmitResult, 0, len(childIDs))
		for _, childID := range childIDs {
			result, _ := o.Children.ChildTerminalResult(ctx, childID)
			results = append(results, result)
			payload := json.RawMessage{}
			if result != nil && len(result.Payload) > 0 {
				payload = result.Payload
			} else if result != nil {
				payload, _ = json.Marshal(map[string]any{"summary": result.Summary})
			}
			usage, _ := o.Children.ChildUsage(ctx, childID)
			total, _ := o.Store.AddTokenUsage(ctx, runID, usage.Tokens)
			if budgetLimit > 0 && total > budgetLimit {
				_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateBudgetExceeded, total,
					fmt.Sprintf("flow budget exceeded: %d > %d", total, budgetLimit))
				_ = o.Events.PublishFlow(ctx, runID, "flow_budget_exceeded", map[string]any{
					"flowRunId": runID, "used": total, "limit": budgetLimit,
				})
				_ = o.Store.TerminateAnchor(ctx, runID, domain.RunFailed, "budget_exceeded", "flow budget exceeded")
				return nil
			}
			aggregate = append(aggregate, payload)
		}
		output := aggregate[0]
		if len(aggregate) > 1 {
			output, _ = json.Marshal(aggregate)
		}
		// Recovery folds the same facts as the live fold.
		if o.foldRecoveredBlocker(ctx, runID, node, results) {
			return nil
		}
		// A crashed fold must not record a violating report as a completed
		// checkpoint either: the contract is re-checked here from the same frozen
		// definition, for every instance of a fan_out task.
		if contract := heroReportContractOf(node); contract != nil {
			for index, result := range results {
				violation := domain.ValidateReportAgainstContract(result, contract)
				if violation == nil {
					continue
				}
				if err := o.foldReportContractViolation(ctx, runID, node, childIDs[index], output, violation); err != nil {
					// A concurrent orchestrator may have settled this node already;
					// that is a settled node, not a flow failure.
					if !errors.Is(err, ErrCheckpointConflict) {
						return err
					}
				}
				return nil
			}
		}
		// A crashed fold must not become a silent skip: a task that declares
		// acceptance is verified here too, with the verdict written in the same
		// CAS as the checkpoint. Otherwise a crash between the child's success
		// and the fold would record a plain `completed` and never verify.
		// fan_out cannot declare acceptance (publish rejects it), so an
		// acceptance node always has exactly one recorded child.
		if len(node.Acceptance) > 0 {
			// A fan-out parent cannot declare acceptance (publish rejects it), so
			// a verified node always has exactly one child. Guard anyway: an
			// aggregate cannot be verified against a single instance.
			if len(childIDs) > 1 {
				verdict := unverifiedAcceptanceVerdict(node.Acceptance, codeAcceptanceUnavailable,
					"a fan-out task cannot be verified from one child Run")
				raw, _ := json.Marshal(verdict)
				_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
					TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
					SetState: domain.FlowNodeCompleted, OutputRef: output,
					AcceptanceVerdict: (*json.RawMessage)(&raw),
				})
				continue
			}
			if err := o.foldAcceptance(ctx, runID, flow, node, childIDs[0], output, results[0]); err != nil {
				// Another orchestrator (recovery races a new start) may have
				// settled this node already; that is a settled node, not a flow
				// failure.
				if errors.Is(err, ErrCheckpointConflict) {
					continue
				}
				return err
			}
			continue
		}
		_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
			TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
			SetState: domain.FlowNodeCompleted, OutputRef: output,
		})
		_ = o.Events.PublishFlow(ctx, runID, "flow_task_completed", map[string]any{
			"flowRunId": runID, "task": node.Handle, "outputRef": string(output), "childRunIds": childIDs,
		})
	}
	return nil
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
