package agentflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// Orchestrator is the meta-Run state machine: pure orchestration, no Provider
// calls, no loop decisions. It drives one child Run per task through the
// delegation substrate, writes task checkpoints, accumulates the flow budget,
// and terminalizes the meta-Run. It is testable through the FlowStore and
// ChildProvider seams.
type Orchestrator struct {
	Store    FlowStore
	Children ChildProvider
	Events   EventSink
	Checker  CheckRunner
	// Enqueue hands a created child Run to the run coordinator.
	Enqueue      func(ctx context.Context, runID string) error
	PollInterval time.Duration
	Now          func() time.Time
}

// FlowStore is the durable meta-Run substrate.
type FlowStore interface {
	GetRun(ctx context.Context, runID string) (*domain.RunAgentFlow, error)
	GetVersion(ctx context.Context, versionID string) (*domain.AgentFlowVersion, error)
	ListNodes(ctx context.Context, runID string) ([]*domain.RunAgentFlowNode, error)
	UpdateNode(ctx context.Context, runID string, upd NodeUpdate) error
	UpdateFlowState(ctx context.Context, runID string, state domain.FlowState, totalTokens int64, reason string) error
	AddTokenUsage(ctx context.Context, runID string, tokens int64) (int64, error)
	SetCancelRequested(ctx context.Context, runID string) error
	IsCancelRequested(ctx context.Context, runID string) (bool, error)
	SetAnchorRunning(ctx context.Context, runID string) error
	TerminateAnchor(ctx context.Context, runID string, status domain.RunStatus, code, message string) error
}

// NodeUpdate mirrors the checkpoint mutation the store applies.
type NodeUpdate struct {
	TaskIndex      int
	ExpectedStates []domain.FlowNodeState
	SetState       domain.FlowNodeState
	ChildRunID     string
	OutputRef      json.RawMessage
	GoalText       string
	ErrorCode      string
}

// ChildSpec is one task's child Run request.
type ChildSpec struct {
	Handle        string
	RoleVersionID string
	Assignment    string
	Budget        domain.BudgetCeilingJSON
}

// ChildInfo identifies the materialized child Run.
type ChildInfo struct {
	RunID   string
	ItemID  string
	GroupID string
}

// ChildProvider wraps the delegation substrate.
type ChildProvider interface {
	CreateTaskChild(ctx context.Context, parentRunID, sessionID string, spec ChildSpec) (ChildInfo, error)
	ChildRunStatus(ctx context.Context, runID string) (domain.RunStatus, error)
	ChildTerminalResult(ctx context.Context, runID string) (*domain.SubmitResult, error)
	ChildUsage(ctx context.Context, runID string) (domain.RunBudgetUsage, error)
	CancelChildRun(ctx context.Context, runID string) error
}

// EventSink publishes flow events after the durable commit (commit-before-publish).
type EventSink interface {
	PublishFlow(ctx context.Context, runID string, eventType string, payload map[string]any) error
}

// Start launches the meta-Run state machine in a background goroutine.
func (o *Orchestrator) Start(ctx context.Context, runID string) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 150 * time.Millisecond
	}
	go o.run(ctx, runID)
}

// Recover resumes every non-terminal meta-Run after a Worker restart. It is
// idempotent and safe to call concurrently with new starts.
func (o *Orchestrator) Recover(ctx context.Context, runIDs []string) {
	for _, runID := range runIDs {
		run, err := o.Store.GetRun(ctx, runID)
		if err != nil || run.State.Terminal() {
			continue
		}
		o.Start(ctx, runID)
	}
}

func (o *Orchestrator) run(ctx context.Context, runID string) {
	run, err := o.Store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	version, err := o.Store.GetVersion(ctx, run.FlowVersionID)
	if err != nil {
		o.fail(ctx, runID, "flow version unavailable: "+err.Error())
		return
	}
	var def domain.FlowDefinition
	if err := json.Unmarshal(version.DefinitionJSON, &def); err != nil {
		o.fail(ctx, runID, "decode flow version: "+err.Error())
		return
	}
	// Durable freeze identity check: the manifest digest must match the frozen
	// run (fail-closed on any drift, e.g. a version rewritten underneath us).
	manifestDigest, err := ManifestDigest(version.ConfigDigest, run.InputsJSON)
	if err != nil || manifestDigest != run.ManifestDigest {
		o.fail(ctx, runID, "flow manifest identity mismatch (fail-closed)")
		return
	}
	if err := o.Store.SetAnchorRunning(ctx, runID); err != nil {
		o.fail(ctx, runID, "anchor recovery failed: "+err.Error())
		return
	}
	if run.State == domain.FlowStatePending {
		if err := o.Store.UpdateFlowState(ctx, runID, domain.FlowStateRunning, run.TotalTokensUsed, ""); err != nil {
			return
		}
		// flow_started is emitted once, on first start; recovery resumes the
		// timeline from the existing checkpoints without a second start event.
		if err := o.Events.PublishFlow(ctx, runID, "flow_started", map[string]any{
			"flowRunId": runID, "flowVersionId": run.FlowVersionID,
			"manifestDigest": run.ManifestDigest, "entryTask": entryTaskHandle(&def),
		}); err != nil {
			o.fail(ctx, runID, "flow_started event failed: "+err.Error())
			return
		}
	}

	var inputs map[string]any
	var vars map[string]any
	var wrapped struct {
		Inputs map[string]any `json:"inputs"`
		Vars   map[string]any `json:"vars"`
	}
	_ = json.Unmarshal(run.InputsJSON, &wrapped)
	inputs = wrapped.Inputs
	vars = wrapped.Vars
	if inputs == nil {
		inputs = map[string]any{}
	}
	if vars == nil {
		vars = map[string]any{}
	}

	taskOutputs := map[string]json.RawMessage{}

	// Crash recovery: any node left in 'running' by a crashed orchestrator is
	// reconciled against its child Run's terminal fact (folded when terminal,
	// reset to interrupted otherwise) before the dispatch loop runs, so a
	// completed child's output is never lost and an interrupted child is
	// re-dispatched exactly once. Idempotent: the loop re-runs it each
	// iteration as a safety net for double-start races.
	if err := o.reconcileCrashedNodes(ctx, runID); err != nil {
		o.fail(ctx, runID, "reconcile crashed nodes: "+err.Error())
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if o.isCancelled(ctx, runID) {
			o.cancelRemaining(ctx, runID, "")
			return
		}
		state, err := o.Store.GetRun(ctx, runID)
		if err != nil {
			return
		}
		if state.State.Terminal() {
			return
		}
		nodes, err := o.Store.ListNodes(ctx, runID)
		if err != nil {
			o.fail(ctx, runID, "list nodes: "+err.Error())
			return
		}
		completed := map[string]*domain.RunAgentFlowNode{}
		for i := range nodes {
			if nodes[i].TerminalState == domain.FlowNodeCompleted {
				completed[nodes[i].Handle] = nodes[i]
				if len(nodes[i].OutputRef) > 0 {
					taskOutputs[nodes[i].Handle] = nodes[i].OutputRef
				}
			}
		}
		next := nextReadyTask(nodes, completed)
		if next == nil {
			// No runnable task: either the flow finished or a dependency failed.
			if len(completed) == len(nodes) {
				break // all tasks completed
			}
			o.fail(ctx, runID, "flow has no runnable task (dependency failure)")
			return
		}
		task := def.Tasks[next.Handle]
		// Dependency gate: a pending task may only dispatch once every task it
		// depends on has a completed checkpoint (defensive: the topological
		// order makes this true in the happy path, and the check turns any
		// failed/cancelled dependency into a clear flow failure).
		if missing := uncompletedDep(task, completed); missing != "" {
			o.fail(ctx, runID, fmt.Sprintf("task %q depends on incomplete task %q", next.Handle, missing))
			return
		}
		if task.Terminal != nil {
			// Terminal gate: gather the flow outputs from the terminal task's
			// dependency outputs and complete the flow.
			outputs := resolveFlowOutputs(def, task, completed, taskOutputs)
			if err := o.complete(ctx, runID, state.TotalTokensUsed, outputs); err != nil {
				return
			}
			return
		}
		goal, err := ResolveGoalTemplate(task.Goal, inputs, vars, taskOutputs)
		if err != nil {
			o.fail(ctx, runID, fmt.Sprintf("resolve goal for task %q: %v", next.Handle, err))
			return
		}
		switch task.Type {
		case domain.FlowTaskCheck:
			if err := o.runCheckTask(ctx, runID, state, next, task, goal, taskOutputs); err != nil {
				return
			}
		default:
			if err := o.runRoleTask(ctx, runID, state, next, task, goal); err != nil {
				return
			}
		}
		// Re-check cancel after the task: a cancelled child settles as
		// cancelled and the loop converts it to the flow terminal state.
		if o.isCancelled(ctx, runID) {
			o.cancelRemaining(ctx, runID, "")
			return
		}
	}
	// All tasks completed.
	if err := o.complete(ctx, runID, run.TotalTokensUsed, resolveFlowOutputsAll(def, taskOutputs)); err != nil {
		return
	}
}

// runRoleTask dispatches one role task as a child Run and settles its
// checkpoint from the folded child result.
func (o *Orchestrator) runRoleTask(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	node *domain.RunAgentFlowNode, task domain.FlowTask, goal string) error {
	budget := taskBudgetCeiling(node)
	// Claim the node before dispatching (CAS pending -> running) so two
	// orchestrators (restart race) never dispatch the same task twice.
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending, domain.FlowNodeInterrupted},
		SetState: domain.FlowNodeRunning, GoalText: goal,
	}); err != nil {
		return err
	}
	child, err := o.Children.CreateTaskChild(ctx, runID, flow.SessionID, ChildSpec{
		Handle: node.Handle, RoleVersionID: node.RoleVersionID, Assignment: goal, Budget: budget,
	})
	if err != nil {
		o.failTask(ctx, runID, node, err.Error())
		o.fail(ctx, runID, fmt.Sprintf("task %q could not start: %v", node.Handle, err))
		return nil
	}
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeRunning, ChildRunID: child.RunID, GoalText: goal,
	}); err != nil {
		return err
	}
	if err := o.Events.PublishFlow(ctx, runID, "flow_task_started", map[string]any{
		"flowRunId": runID, "task": node.Handle, "childRunId": child.RunID,
	}); err != nil {
		o.fail(ctx, runID, "flow_task_started event failed: "+err.Error())
		return nil
	}
	if o.Enqueue != nil {
		if err := o.Enqueue(ctx, child.RunID); err != nil {
			o.fail(ctx, runID, fmt.Sprintf("enqueue child %s: %v", child.RunID, err))
			return nil
		}
	}
	status, result, err := o.waitChild(ctx, runID, child.RunID)
	if err != nil {
		return err
	}
	if o.isCancelled(ctx, runID) || status == domain.RunCancelled {
		return o.cancelRemaining(ctx, runID, child.RunID)
	}
	// Budget accounting: fold the child's actual usage into the flow ledger.
	usage, _ := o.Children.ChildUsage(ctx, child.RunID)
	total, _ := o.Store.AddTokenUsage(ctx, runID, usage.Tokens)
	var version *domain.AgentFlowVersion
	if v, err := o.Store.GetVersion(ctx, flow.FlowVersionID); err == nil {
		version = v
	}
	if version != nil {
		var def domain.FlowDefinition
		if json.Unmarshal(version.DefinitionJSON, &def) == nil &&
			def.Budget.MaxTotalTokens > 0 && total > def.Budget.MaxTotalTokens {
			_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateBudgetExceeded, total,
				fmt.Sprintf("flow budget exceeded: %d > %d", total, def.Budget.MaxTotalTokens))
			_ = o.Events.PublishFlow(ctx, runID, "flow_failed", map[string]any{
				"flowRunId": runID, "reason": "budget_exceeded", "used": total, "limit": def.Budget.MaxTotalTokens,
			})
			_ = o.Store.TerminateAnchor(ctx, runID, domain.RunFailed, "budget_exceeded", "flow budget exceeded")
			return nil
		}
	}
	if status != domain.RunSucceeded {
		code := "task_failed"
		if status == domain.RunCancelled {
			code = "task_cancelled"
		} else if status == domain.RunInterrupted {
			code = "task_interrupted"
		}
		o.failTask(ctx, runID, node, code)
		o.fail(ctx, runID, fmt.Sprintf("task %q %s", node.Handle, code))
		return nil
	}
	payload := json.RawMessage{}
	if result != nil && len(result.Payload) > 0 {
		payload = result.Payload
	} else if result != nil {
		payload, _ = json.Marshal(map[string]any{"summary": result.Summary})
	}
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeCompleted, OutputRef: payload,
	}); err != nil {
		return err
	}
	return o.Events.PublishFlow(ctx, runID, "flow_task_completed", map[string]any{
		"flowRunId": runID, "task": node.Handle, "outputRef": string(payload), "childRunId": child.RunID,
	})
}

// runCheckTask executes one deterministic check gate: policy gate, durable
// Ask-mode approval, sandbox execution, and checkpoint write. Check tasks are
// never a bypass: Discuss mode denies, Ask mode suspends on approval.
func (o *Orchestrator) runCheckTask(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	node *domain.RunAgentFlowNode, task domain.FlowTask, goal string, taskOutputs map[string]json.RawMessage) error {
	if o.Checker == nil {
		o.fail(ctx, runID, "check runner is unavailable")
		return nil
	}
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending, domain.FlowNodeInterrupted},
		SetState: domain.FlowNodeRunning, GoalText: goal,
	}); err != nil {
		return err
	}
	policy, err := o.Checker.CheckPolicyForSession(ctx, flow.SessionID)
	if err != nil {
		o.failTask(ctx, runID, node, "policy_unavailable")
		o.fail(ctx, runID, fmt.Sprintf("check task %q: %v", node.Handle, err))
		return nil
	}
	argv := ParseCheckCommand(task.Command)
	decision := EvaluateCheck(policy, argv)
	switch decision.Action {
	case CheckDeny:
		o.failTask(ctx, runID, node, decision.Code)
		o.fail(ctx, runID, fmt.Sprintf("check task %q denied by tool policy: %s", node.Handle, decision.Reason))
		return nil
	case CheckRequireAsk:
		if err := o.Checker.CreateCheckApproval(ctx, runID, node.TaskIndex, task.Command); err != nil {
			o.failTask(ctx, runID, node, "approval_create_failed")
			o.fail(ctx, runID, fmt.Sprintf("check task %q approval failed: %v", node.Handle, err))
			return nil
		}
		if err := o.Events.PublishFlow(ctx, runID, "flow_task_started", map[string]any{
			"flowRunId": runID, "task": node.Handle, "check": true, "waitingApproval": true,
		}); err != nil {
			o.fail(ctx, runID, "flow_task_started event failed: "+err.Error())
			return nil
		}
		// Wait for the user's decision (or flow cancellation).
		ticker := time.NewTicker(o.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
			if o.isCancelled(ctx, runID) {
				return o.cancelRemaining(ctx, runID, "")
			}
			status, err := o.Checker.CheckApprovalStatus(ctx, runID, node.TaskIndex)
			if err != nil {
				o.failTask(ctx, runID, node, "approval_read_failed")
				o.fail(ctx, runID, fmt.Sprintf("check task %q approval read failed: %v", node.Handle, err))
				return nil
			}
			switch status {
			case agentFlowApprovalApproved:
				goto run
			case agentFlowApprovalRejected:
				o.failTask(ctx, runID, node, "approval_rejected")
				o.fail(ctx, runID, fmt.Sprintf("check task %q approval rejected", node.Handle))
				return nil
			}
		}
	}
run:
	outcome, err := o.Checker.ExecuteCheck(WithCheckSession(ctx, flow.SessionID), task.Command, 0)
	if err != nil {
		o.failTask(ctx, runID, node, "check_execution_failed")
		o.fail(ctx, runID, fmt.Sprintf("check task %q execution failed: %v", node.Handle, err))
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"pass": outcome.Pass, "exitCode": outcome.ExitCode,
		"summary": outcome.Summary, "command": task.Command,
	})
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeCompleted, OutputRef: payload,
	}); err != nil {
		return err
	}
	return o.Events.PublishFlow(ctx, runID, "flow_task_completed", map[string]any{
		"flowRunId": runID, "task": node.Handle, "outputRef": string(payload), "check": true,
	})
}

// agentFlowApproval* alias the CheckRunner statuses to keep the orchestrator
// free of import coupling beyond the interface.
const (
	agentFlowApprovalApproved = CheckApprovalApproved
	agentFlowApprovalRejected = CheckApprovalRejected
)

// waitChild polls the child Run until terminal, honoring cancellation and the
// caller context.
func (o *Orchestrator) waitChild(ctx context.Context, runID, childRunID string) (domain.RunStatus, *domain.SubmitResult, error) {
	ticker := time.NewTicker(o.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-ticker.C:
		}
		status, err := o.Children.ChildRunStatus(ctx, childRunID)
		if err != nil {
			return "", nil, err
		}
		if status.Terminal() {
			result, _ := o.Children.ChildTerminalResult(ctx, childRunID)
			return status, result, nil
		}
		if o.isCancelled(ctx, runID) {
			// Hard-cancel the active child; its settlement becomes visible on
			// the next poll.
			_ = o.Children.CancelChildRun(ctx, childRunID)
		}
	}
}

func (o *Orchestrator) complete(ctx context.Context, runID string, totalTokens int64, outputs map[string]json.RawMessage) error {
	payload := map[string]any{"outputs": map[string]any{}}
	for name, value := range outputs {
		payload["outputs"].(map[string]any)[name] = json.RawMessage(value)
	}
	if err := o.Store.UpdateFlowState(ctx, runID, domain.FlowStateCompleted, totalTokens, ""); err != nil {
		return err
	}
	if err := o.Events.PublishFlow(ctx, runID, "flow_completed", map[string]any{
		"flowRunId": runID, "outputs": payload["outputs"],
	}); err != nil {
		return err
	}
	return o.Store.TerminateAnchor(ctx, runID, domain.RunSucceeded, "", "")
}

func (o *Orchestrator) fail(ctx context.Context, runID, reason string) {
	_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateFailed, 0, reason)
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

// cancelRemaining terminalizes the meta-Run as cancelled: the active child is
// hard-cancelled (already cancelled by the poll), future tasks are never
// scheduled, and every non-terminal node is marked cancelled.
func (o *Orchestrator) cancelRemaining(ctx context.Context, runID string, activeChild string) error {
	nodes, err := o.Store.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	for i := range nodes {
		if nodes[i].TerminalState.Terminal() {
			continue
		}
		if nodes[i].TerminalState == domain.FlowNodeRunning && nodes[i].ChildRunID != "" {
			_ = o.Children.CancelChildRun(ctx, nodes[i].ChildRunID)
		}
		_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
			TaskIndex: nodes[i].TaskIndex, ExpectedStates: []domain.FlowNodeState{nodes[i].TerminalState},
			SetState: domain.FlowNodeCancelled,
		})
	}
	_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateCancelled, 0, "cancelled by user")
	_ = o.Events.PublishFlow(ctx, runID, "flow_failed", map[string]any{"flowRunId": runID, "reason": "cancelled"})
	_ = o.Store.TerminateAnchor(ctx, runID, domain.RunCancelled, "flow_cancelled", "cancelled by user")
	return nil
}

func (o *Orchestrator) isCancelled(ctx context.Context, runID string) bool {
	value, err := o.Store.IsCancelRequested(ctx, runID)
	return err == nil && value
}

// nextReadyTask picks the first dispatchable task: the lowest-index node that
// is pending or interrupted (completed/running/failed/blocked/cancelled are
// never re-selected). Combined with the topological ordering and the
// dependency gate in run(), a node is only dispatched after every prerequisite
// has a completed checkpoint.
func nextReadyTask(nodes []*domain.RunAgentFlowNode, _ map[string]*domain.RunAgentFlowNode) *domain.RunAgentFlowNode {
	for i := range nodes {
		node := nodes[i]
		if node.TerminalState == domain.FlowNodePending || node.TerminalState == domain.FlowNodeInterrupted {
			return node
		}
	}
	return nil
}

// uncompletedDep returns the first depends task that does not have a completed
// checkpoint, or "" when every prerequisite is satisfied.
func uncompletedDep(task domain.FlowTask, completed map[string]*domain.RunAgentFlowNode) string {
	for _, dep := range task.Depends {
		if _, ok := completed[dep]; !ok {
			return dep
		}
	}
	return ""
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
		if node.ChildRunID == "" {
			reset()
			continue
		}
		status, err := o.Children.ChildRunStatus(ctx, node.ChildRunID)
		if err != nil || !status.Terminal() {
			// Still active (or the row is gone): re-dispatch the task.
			reset()
			continue
		}
		if status == domain.RunSucceeded {
			result, _ := o.Children.ChildTerminalResult(ctx, node.ChildRunID)
			payload := json.RawMessage{}
			if result != nil && len(result.Payload) > 0 {
				payload = result.Payload
			} else if result != nil {
				payload, _ = json.Marshal(map[string]any{"summary": result.Summary})
			}
			usage, _ := o.Children.ChildUsage(ctx, node.ChildRunID)
			total, _ := o.Store.AddTokenUsage(ctx, runID, usage.Tokens)
			_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
				TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
				SetState: domain.FlowNodeCompleted, OutputRef: payload,
			})
			_ = o.Events.PublishFlow(ctx, runID, "flow_task_completed", map[string]any{
				"flowRunId": runID, "task": node.Handle, "outputRef": string(payload), "childRunId": node.ChildRunID,
			})
			if budgetLimit > 0 && total > budgetLimit {
				_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateBudgetExceeded, total,
					fmt.Sprintf("flow budget exceeded: %d > %d", total, budgetLimit))
				_ = o.Events.PublishFlow(ctx, runID, "flow_failed", map[string]any{
					"flowRunId": runID, "reason": "budget_exceeded", "used": total, "limit": budgetLimit,
				})
				_ = o.Store.TerminateAnchor(ctx, runID, domain.RunFailed, "budget_exceeded", "flow budget exceeded")
			}
			continue
		}
		code := "task_failed"
		if status == domain.RunCancelled {
			code = "task_cancelled"
		}
		_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
			TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
			SetState: domain.FlowNodeFailed, ErrorCode: code,
		})
		_ = o.Events.PublishFlow(ctx, runID, "flow_task_failed", map[string]any{
			"flowRunId": runID, "task": node.Handle, "childRunId": node.ChildRunID, "reason": code,
		})
	}
	return nil
}

// flowBudgetLimit decodes the frozen flow definition's budget ceiling.
func (o *Orchestrator) flowBudgetLimit(ctx context.Context, flowVersionID string) int64 {
	version, err := o.Store.GetVersion(ctx, flowVersionID)
	if err != nil {
		return 0
	}
	var def domain.FlowDefinition
	if err := json.Unmarshal(version.DefinitionJSON, &def); err != nil {
		return 0
	}
	return def.Budget.MaxTotalTokens
}

// entryTaskHandle returns the unique entry task (the only task with no
// depends). Published flows guarantee exactly one; callers treat "" as unknown.
func entryTaskHandle(def *domain.FlowDefinition) string {
	for name, task := range def.Tasks {
		if len(task.Depends) == 0 {
			return name
		}
	}
	return ""
}

// ResolveGoalTemplate fills {inputs.x}, {task.X.output(.field)}, {flow.vars.y}
// from the frozen run inputs/vars and the completed task checkpoints.
func ResolveGoalTemplate(template string, inputs, vars map[string]any, taskOutputs map[string]json.RawMessage) (string, error) {
	var builder strings.Builder
	last := 0
	for _, match := range goalRefPattern.FindAllStringSubmatchIndex(template, -1) {
		start, end := match[0], match[1]
		builder.WriteString(template[last:start])
		raw := template[start+1 : end-1]
		value, err := resolveRef(raw, inputs, vars, taskOutputs)
		if err != nil {
			return "", err
		}
		builder.WriteString(value)
		last = end
	}
	builder.WriteString(template[last:])
	return builder.String(), nil
}

func resolveRef(raw string, inputs, vars map[string]any, taskOutputs map[string]json.RawMessage) (string, error) {
	switch {
	case strings.HasPrefix(raw, "inputs."):
		name := strings.TrimPrefix(raw, "inputs.")
		value, ok := lookupPath(inputs, name)
		if !ok {
			return "", fmt.Errorf("input %q is not available", name)
		}
		return stringifyValue(value), nil
	case strings.HasPrefix(raw, "task."):
		rest := strings.TrimPrefix(raw, "task.")
		task, path, hasPath := strings.Cut(rest, ".")
		payload, ok := taskOutputs[task]
		if !ok {
			return "", fmt.Errorf("task %q output is not available (dependency not completed)", task)
		}
		if !hasPath || strings.TrimPrefix(path, "output") == "" {
			return string(payload), nil
		}
		field := strings.TrimPrefix(path, "output.")
		if field == "" {
			return string(payload), nil
		}
		var object map[string]any
		if err := json.Unmarshal(payload, &object); err != nil {
			return "", fmt.Errorf("task %q output is not an object; cannot read field %q", task, field)
		}
		value, ok := lookupPath(object, field)
		if !ok {
			return "", fmt.Errorf("task %q output has no field %q", task, field)
		}
		return stringifyValue(value), nil
	case strings.HasPrefix(raw, "flow.vars."):
		name := strings.TrimPrefix(raw, "flow.vars.")
		value, ok := lookupPath(vars, name)
		if !ok {
			return "", fmt.Errorf("flow var %q is not set", name)
		}
		return stringifyValue(value), nil
	default:
		return "", fmt.Errorf("unsupported goal reference {%s}", raw)
	}
}

func lookupPath(object map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = object
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func stringifyValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(encoded)
	}
}

// taskBudgetCeiling converts a frozen node budget JSON into the ceiling used
// for the delegation item.
func taskBudgetCeiling(node *domain.RunAgentFlowNode) domain.BudgetCeilingJSON {
	var ceiling domain.BudgetCeilingJSON
	_ = json.Unmarshal(node.BudgetJSON, &ceiling)
	return ceiling
}

// resolveFlowOutputs maps the terminal task's declared output port to its
// dependency task output (the task whose handle equals the port name, else the
// first depends task).
func resolveFlowOutputs(def domain.FlowDefinition, terminal domain.FlowTask,
	completed map[string]*domain.RunAgentFlowNode, taskOutputs map[string]json.RawMessage) map[string]json.RawMessage {
	outputs := map[string]json.RawMessage{}
	if terminal.Terminal == nil {
		return outputs
	}
	port := terminal.Terminal.Output
	source := ""
	if port != "" {
		for _, dep := range terminal.Depends {
			if dep == port {
				source = dep
				break
			}
		}
		if source == "" && len(terminal.Depends) > 0 {
			source = terminal.Depends[0]
		}
	} else if len(terminal.Depends) > 0 {
		source = terminal.Depends[0]
	}
	if source != "" {
		if payload, ok := taskOutputs[source]; ok {
			if port != "" {
				outputs[port] = payload
			} else {
				outputs[source] = payload
			}
		}
	}
	return outputs
}

func resolveFlowOutputsAll(def domain.FlowDefinition, taskOutputs map[string]json.RawMessage) map[string]json.RawMessage {
	outputs := map[string]json.RawMessage{}
	for name, port := range def.Outputs {
		if payload, ok := taskOutputs[name]; ok {
			outputs[name] = payload
			continue
		}
		_ = port
	}
	// Fall back to the last completed task output for declared ports without a
	// matching task handle.
	for name := range def.Outputs {
		if _, ok := outputs[name]; ok {
			continue
		}
		handles := make([]string, 0, len(taskOutputs))
		for handle := range taskOutputs {
			handles = append(handles, handle)
		}
		sort.Strings(handles)
		if len(handles) > 0 {
			outputs[name] = taskOutputs[handles[len(handles)-1]]
		}
	}
	return outputs
}
