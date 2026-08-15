package agentflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
	GetConvergenceRounds(ctx context.Context, runID string) (map[string]int, error)
	SetConvergenceRounds(ctx context.Context, runID string, rounds map[string]int) error
	SetAnchorRunning(ctx context.Context, runID string) error
	TerminateAnchor(ctx context.Context, runID string, status domain.RunStatus, code, message string) error
}

// NodeUpdate mirrors the checkpoint mutation the store applies.
type NodeUpdate struct {
	TaskIndex      int
	ExpectedStates []domain.FlowNodeState
	SetState       domain.FlowNodeState
	ChildRunID     string
	ChildRunIDs    []string
	OutputRef      json.RawMessage
	GoalText       string
	ErrorCode      string
}

// ChildSpec is one task's child Run request.
type ChildSpec struct {
	Handle        string
	RoleVersionID string
	// SkillIDs are the task-level additive preload Skills frozen on the flow
	// node. They supplement, but never replace or widen, the Role policy.
	SkillIDs []string
	// RoleDefinitionJSON is the full resolved RoleDefinition frozen at Run
	// start. It is passed to child Run materialization so delegation never
	// re-reads mutable Role files or the removed global role SQL.
	RoleDefinitionJSON json.RawMessage
	Assignment         string
	Budget             domain.BudgetCeilingJSON
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

	// Parallel dispatch state: the active lane (reader/writer class) and the
	// set of in-flight role children. Independent ready tasks of the same
	// class dispatch concurrently up to parallelism.max; readers and writers
	// never share the workspace at the same time (mutation lane), and writers
	// only parallelize against each other when the flow opts into disjoint
	// writes scopes.
	lane := laneNone
	var inFlight []inflightTask
	maxParallel := def.EffectiveParallelismMax()

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
		// Convergence: a completed 'from' node triggers one durable back-edge
		// round (reset the loop path, count it, and stop at max_rounds).
		converged, err := o.applyConvergence(ctx, runID, &def, nodes)
		if err != nil {
			o.fail(ctx, runID, "convergence: "+err.Error())
			return
		}
		if converged {
			continue // nodes changed by the back-edge reset; re-read them
		}
		ready := readyRoleTasks(nodes, completed, &def)

		// Check and fan_out tasks are blocking gates: checks may mutate the
		// workspace (sandbox commands) and fan_out instances read it. They run
		// alone, only once the parallel lane has drained, so a concurrent
		// writer can never race their snapshot.
		if len(inFlight) == 0 {
			if c := firstSpecialTask(ready, &def, domain.FlowTaskCheck); c != nil {
				goal, err := ResolveGoalTemplate(def.Tasks[c.Handle].Goal, inputs, vars, taskOutputs)
				if err != nil {
					o.fail(ctx, runID, fmt.Sprintf("resolve goal for task %q: %v", c.Handle, err))
					return
				}
				if err := o.runCheckTask(ctx, runID, state, c, def.Tasks[c.Handle], goal, taskOutputs); err != nil {
					return
				}
				continue
			}
			if f := firstFanOutTask(ready, &def); f != nil {
				goal, err := ResolveGoalTemplate(def.Tasks[f.Handle].Goal, inputs, vars, taskOutputs)
				if err != nil {
					o.fail(ctx, runID, fmt.Sprintf("resolve goal for task %q: %v", f.Handle, err))
					return
				}
				if err := o.runFanOutTask(ctx, runID, state, f, def.Tasks[f.Handle], goal); err != nil {
					return
				}
				continue
			}
		}

		if len(ready) == 0 {
			// No runnable task: either the flow finished, the lane is still
			// draining, or a dependency failed.
			if len(completed) == len(nodes) {
				break // all tasks completed
			}
			// While children are still in flight, keep folding them: a batch
			// sibling may be exactly the dependency the terminal gate needs.
			if len(inFlight) > 0 {
				if err := o.waitAndFoldAny(ctx, runID, run, &inFlight); err != nil {
					return
				}
				continue
			}
			// Terminal gate: once every runnable task has settled, the unique
			// terminal task (if any) resolves the flow outputs from its
			// completed dependencies. Branch flows whose unselected paths stay
			// pending terminate here, never dispatching the inactive branch.
			if t := terminalGateTask(&def, completed); t != nil {
				outputs := resolveFlowOutputs(def, *t, completed, taskOutputs)
				if err := o.complete(ctx, runID, state.TotalTokensUsed, outputs); err != nil {
					return
				}
				return
			}
			o.fail(ctx, runID, "flow has no runnable task (dependency failure)")
			return
		}

		// Establish the lane. Reader-first: when both classes are ready, the
		// read-only tasks dispatch first so writers downstream get a stable
		// snapshot and the mutation lane drains before any write.
		if lane == laneNone {
			lane = firstReadyClass(ready)
		}

		batch, hasLane := selectLaneBatch(ready, lane, &def, maxParallel, inFlight)
		if !hasLane {
			// Nothing of the active lane is ready. If the lane is still busy,
			// drain it before switching so readers and writers never overlap;
			// otherwise flip the lane.
			if len(inFlight) > 0 {
				if err := o.waitAndFoldAny(ctx, runID, run, &inFlight); err != nil {
					return
				}
				continue
			}
			lane = 1 - lane
			continue
		}

		// Dispatch the batch: each task gets its own goal and child Run; nodes
		// are claimed with a CAS so a restart race never dispatches twice.
		for _, node := range batch {
			task := def.Tasks[node.Handle]
			// Dependency gate (defensive: ready-set computation already enforces
			// this, and the check turns any failed dependency into a clear
			// flow failure instead of a stuck run).
			if missing := uncompletedDep(task, completed); missing != "" {
				o.fail(ctx, runID, fmt.Sprintf("task %q depends on incomplete task %q", node.Handle, missing))
				return
			}
			goal, err := ResolveGoalTemplate(task.Goal, inputs, vars, taskOutputs)
			if err != nil {
				o.fail(ctx, runID, fmt.Sprintf("resolve goal for task %q: %v", node.Handle, err))
				return
			}
			child, err := o.dispatchRoleChild(ctx, runID, state, node, task, goal)
			if err != nil {
				return
			}
			inFlight = append(inFlight, inflightTask{node: node, childID: child.RunID})
		}

		if len(inFlight) > 0 {
			// Wait for any in-flight child to settle, fold its checkpoint, and
			// re-evaluate the ready set (completed dependencies unlock the next
			// tier while the rest of the batch keeps running).
			if err := o.waitAndFoldAny(ctx, runID, run, &inFlight); err != nil {
				return
			}
		}
	}
	// All tasks completed.
	if err := o.complete(ctx, runID, run.TotalTokensUsed, resolveFlowOutputsAll(def, taskOutputs)); err != nil {
		return
	}
}

// dispatchRoleChild claims one role task (CAS pending -> running), creates its
// child Run, records the child on the node, publishes the started event, and
// enqueues the child. It never blocks: the caller batches role children and
// waits for any to settle via waitAndFoldAny.
func (o *Orchestrator) dispatchRoleChild(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	node *domain.RunAgentFlowNode, task domain.FlowTask, goal string) (ChildInfo, error) {
	budget := taskBudgetCeiling(node)
	// Claim the node before dispatching (CAS pending -> running) so two
	// orchestrators (restart race) never dispatch the same task twice.
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending, domain.FlowNodeInterrupted},
		SetState: domain.FlowNodeRunning, GoalText: goal,
	}); err != nil {
		return ChildInfo{}, err
	}
	child, err := o.Children.CreateTaskChild(ctx, runID, flow.SessionID, ChildSpec{
		Handle: node.Handle, RoleVersionID: node.RoleVersionID,
		SkillIDs: append([]string(nil), node.SkillDigests...), Assignment: goal, Budget: budget,
		RoleDefinitionJSON: node.RoleDefinitionJSON,
	})
	if err != nil {
		o.failTask(ctx, runID, node, err.Error())
		o.fail(ctx, runID, fmt.Sprintf("task %q could not start: %v", node.Handle, err))
		return ChildInfo{}, nil
	}
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeRunning, ChildRunID: child.RunID, GoalText: goal,
	}); err != nil {
		return ChildInfo{}, err
	}
	if err := o.Events.PublishFlow(ctx, runID, "flow_task_started", map[string]any{
		"flowRunId": runID, "task": node.Handle, "childRunId": child.RunID,
	}); err != nil {
		o.fail(ctx, runID, "flow_task_started event failed: "+err.Error())
		return ChildInfo{}, nil
	}
	if o.Enqueue != nil {
		if err := o.Enqueue(ctx, child.RunID); err != nil {
			o.fail(ctx, runID, fmt.Sprintf("enqueue child %s: %v", child.RunID, err))
			return ChildInfo{}, nil
		}
	}
	return child, nil
}

// foldRoleChild settles one role child from an already-known terminal status:
// budget accounting, over-budget termination, failure/cancel handling, and the
// completed checkpoint + event. On failure it cancels every other in-flight
// child so a parallel batch never leaks running children.
func (o *Orchestrator) foldRoleChild(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	node *domain.RunAgentFlowNode, childID string, status domain.RunStatus,
	result *domain.SubmitResult, remaining []inflightTask) error {
	// Budget accounting: fold the child's actual usage into the flow ledger.
	usage, _ := o.Children.ChildUsage(ctx, childID)
	total, _ := o.Store.AddTokenUsage(ctx, runID, usage.Tokens)
	var version *domain.AgentFlowVersion
	if v, err := o.Store.GetVersion(ctx, flow.FlowVersionID); err == nil {
		version = v
	}
	if version != nil {
		var def domain.FlowDefinition
		if json.Unmarshal(version.DefinitionJSON, &def) == nil &&
			def.Budget.MaxTotalTokens > 0 && total > def.Budget.MaxTotalTokens {
			o.cancelInflight(ctx, runID, remaining)
			_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateBudgetExceeded, total,
				fmt.Sprintf("flow budget exceeded: %d > %d", total, def.Budget.MaxTotalTokens))
			_ = o.Events.PublishFlow(ctx, runID, "flow_budget_exceeded", map[string]any{
				"flowRunId": runID, "used": total, "limit": def.Budget.MaxTotalTokens,
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
		o.cancelInflight(ctx, runID, remaining)
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
		"flowRunId": runID, "task": node.Handle, "outputRef": string(payload), "childRunId": childID,
	})
}

// waitAndFoldAny polls all in-flight role children and folds the first one
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
		return o.foldRoleChild(ctx, runID, flow, task.node, task.childID, status, result, *inFlight)
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
	if err := o.Events.PublishFlow(ctx, runID, "flow_task_completed", map[string]any{
		"flowRunId": runID, "task": node.Handle, "outputRef": string(payload), "check": true,
	}); err != nil {
		return err
	}
	// Phase 2: the deterministic check verdict drives branch routing. Emit the
	// routed target so the timeline and consumers see the exact gate decision.
	nextTarget := ""
	if task.Next != nil {
		if outcome.Pass {
			nextTarget = task.Next["pass"]
		} else {
			nextTarget = task.Next["fail"]
		}
	}
	return o.Events.PublishFlow(ctx, runID, "flow_check_result", map[string]any{
		"flowRunId": runID, "task": node.Handle, "pass": outcome.Pass,
		"summary": outcome.Summary, "next": nextTarget,
	})
}

// agentFlowApproval* alias the CheckRunner statuses to keep the orchestrator
// free of import coupling beyond the interface.
const (
	agentFlowApprovalApproved = CheckApprovalApproved
	agentFlowApprovalRejected = CheckApprovalRejected
)

// runFanOutTask executes a read-only fan_out task: exactly N = min parallel
// child Runs, each a separate delegation group; results aggregate by instance
// order into one checkpoint (a JSON array); any instance failure fails the
// task; usage accumulates into the flow budget; cancellation covers every
// instance.
func (o *Orchestrator) runFanOutTask(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	node *domain.RunAgentFlowNode, task domain.FlowTask, goal string) error {
	count := task.FanOut.Min
	if count < 1 {
		count = 1
	}
	budget := taskBudgetCeiling(node)
	// Claim the node before dispatching (CAS pending/interrupted -> running).
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending, domain.FlowNodeInterrupted},
		SetState: domain.FlowNodeRunning, GoalText: goal,
	}); err != nil {
		return err
	}
	childIDs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		if o.isCancelled(ctx, runID) {
			_ = o.cancelRemaining(ctx, runID, "")
			return nil
		}
		child, err := o.Children.CreateTaskChild(ctx, runID, flow.SessionID, ChildSpec{
			Handle:        fmt.Sprintf("%s-%d", node.Handle, i),
			RoleVersionID: node.RoleVersionID,
			SkillIDs:      append([]string(nil), node.SkillDigests...),
			Assignment:    goal, Budget: budget,
			RoleDefinitionJSON: node.RoleDefinitionJSON,
		})
		if err != nil {
			o.failTask(ctx, runID, node, "fan_out_child_create_failed")
			o.fail(ctx, runID, fmt.Sprintf("fan_out task %q could not start: %v", node.Handle, err))
			return nil
		}
		childIDs = append(childIDs, child.RunID)
	}
	_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeRunning, ChildRunIDs: childIDs, GoalText: goal,
	})
	for _, childID := range childIDs {
		if o.Enqueue != nil {
			if err := o.Enqueue(ctx, childID); err != nil {
				o.fail(ctx, runID, fmt.Sprintf("enqueue fan_out child %s: %v", childID, err))
				return nil
			}
		}
	}
	if err := o.Events.PublishFlow(ctx, runID, "flow_task_started", map[string]any{
		"flowRunId": runID, "task": node.Handle, "childRunIds": childIDs, "fanOut": true,
	}); err != nil {
		o.fail(ctx, runID, "flow_task_started event failed: "+err.Error())
		return nil
	}
	results, statuses := o.waitChildren(ctx, runID, childIDs)
	if o.isCancelled(ctx, runID) {
		return o.cancelRemaining(ctx, runID, "")
	}
	for _, status := range statuses {
		if status != domain.RunSucceeded {
			code := "task_failed"
			if status == domain.RunCancelled {
				code = "task_cancelled"
			} else if status == domain.RunInterrupted {
				code = "task_interrupted"
			}
			o.failTask(ctx, runID, node, code)
			o.fail(ctx, runID, fmt.Sprintf("fan_out task %q %s", node.Handle, code))
			return nil
		}
	}
	// Accumulate every instance's usage into the flow ledger FIRST so a
	// budget-exceeded record reflects the actual total consumption across all
	// parallel children (not just the children accounted before the limit).
	var budgetLimit int64
	if v, err := o.Store.GetVersion(ctx, flow.FlowVersionID); err == nil {
		var def domain.FlowDefinition
		if json.Unmarshal(v.DefinitionJSON, &def) == nil {
			budgetLimit = def.Budget.MaxTotalTokens
		}
	}
	total := int64(0)
	for _, childID := range childIDs {
		usage, _ := o.Children.ChildUsage(ctx, childID)
		total, _ = o.Store.AddTokenUsage(ctx, runID, usage.Tokens)
	}
	if budgetLimit > 0 && total > budgetLimit {
		_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateBudgetExceeded, total,
			fmt.Sprintf("flow budget exceeded: %d > %d", total, budgetLimit))
		_ = o.Events.PublishFlow(ctx, runID, "flow_budget_exceeded", map[string]any{
			"flowRunId": runID, "used": total, "limit": budgetLimit,
		})
		_ = o.Store.TerminateAnchor(ctx, runID, domain.RunFailed, "budget_exceeded", "flow budget exceeded")
		return nil
	}
	// Aggregate by instance order.
	aggregate := make([]json.RawMessage, 0, len(childIDs))
	for _, childID := range childIDs {
		result := results[childID]
		payload := json.RawMessage{}
		if result != nil && len(result.Payload) > 0 {
			payload = result.Payload
		} else if result != nil {
			payload, _ = json.Marshal(map[string]any{"summary": result.Summary})
		}
		aggregate = append(aggregate, payload)
	}
	aggregateJSON, _ := json.Marshal(aggregate)
	if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeCompleted, OutputRef: aggregateJSON,
	}); err != nil {
		return err
	}
	return o.Events.PublishFlow(ctx, runID, "flow_task_completed", map[string]any{
		"flowRunId": runID, "task": node.Handle, "outputRef": string(aggregateJSON), "fanOut": true,
	})
}

// waitChildren polls every child Run until all are terminal, honoring flow
// cancellation (hard-cancelling stragglers) and the caller context.
func (o *Orchestrator) waitChildren(ctx context.Context, runID string, childIDs []string) (
	map[string]*domain.SubmitResult, map[string]domain.RunStatus) {
	results := map[string]*domain.SubmitResult{}
	statuses := map[string]domain.RunStatus{}
	pending := make(map[string]bool, len(childIDs))
	for _, id := range childIDs {
		pending[id] = true
	}
	ticker := time.NewTicker(o.PollInterval)
	defer ticker.Stop()
	for len(pending) > 0 {
		select {
		case <-ctx.Done():
			for id := range pending {
				statuses[id] = domain.RunInterrupted
			}
			return results, statuses
		case <-ticker.C:
		}
		for id := range pending {
			status, err := o.Children.ChildRunStatus(ctx, id)
			if err != nil {
				statuses[id] = domain.RunInterrupted
				delete(pending, id)
				continue
			}
			if status.Terminal() {
				statuses[id] = status
				result, _ := o.Children.ChildTerminalResult(ctx, id)
				if result != nil {
					results[id] = result
				}
				delete(pending, id)
				continue
			}
			if o.isCancelled(ctx, runID) {
				_ = o.Children.CancelChildRun(ctx, id)
			}
		}
	}
	return results, statuses
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
		for _, childID := range o.nodeChildIDs(nodes[i]) {
			_ = o.Children.CancelChildRun(ctx, childID)
		}
		_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
			TaskIndex: nodes[i].TaskIndex, ExpectedStates: []domain.FlowNodeState{nodes[i].TerminalState},
			SetState: domain.FlowNodeCancelled,
		})
	}
	cancelledChildRuns := []string{}
	for i := range nodes {
		cancelledChildRuns = append(cancelledChildRuns, o.nodeChildIDs(nodes[i])...)
	}
	_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateCancelled, 0, "cancelled by user")
	_ = o.Events.PublishFlow(ctx, runID, "flow_cancelled", map[string]any{
		"flowRunId": runID, "cancelledChildRuns": cancelledChildRuns,
	})
	_ = o.Store.TerminateAnchor(ctx, runID, domain.RunCancelled, "flow_cancelled", "cancelled by user")
	return nil
}

func (o *Orchestrator) isCancelled(ctx context.Context, runID string) bool {
	value, err := o.Store.IsCancelRequested(ctx, runID)
	return err == nil && value
}

// Lane constants: the active ready-set concurrency class. Readers (read-only
// Roles) run in parallel up to parallelism.max; writers take the mutation
// lane. The two classes never overlap: readers and writers are dispatched in
// separate windows so a reader never sees a sibling writer's partial state.
const (
	laneNone   = -1
	laneReader = 0
	laneWriter = 1
)

// inflightTask is one dispatched role child awaiting settlement.
type inflightTask struct {
	node    *domain.RunAgentFlowNode
	childID string
}

// readyRoleTasks returns every dispatchable role/fan_out/check node: pending
// or interrupted, not on an inactive check-gate branch, and with every
// depends task completed (the ready set). Nodes are already ordered by task
// index (ListNodes), which keeps dispatch deterministic and recovery
// reproducible. Terminal tasks are excluded: they complete the flow instead
// of dispatching a child.
func readyRoleTasks(nodes []*domain.RunAgentFlowNode, completed map[string]*domain.RunAgentFlowNode,
	def *domain.FlowDefinition) []*domain.RunAgentFlowNode {
	byHandle := make(map[string]*domain.RunAgentFlowNode, len(nodes))
	for i := range nodes {
		byHandle[nodes[i].Handle] = nodes[i]
	}
	var ready []*domain.RunAgentFlowNode
	for i := range nodes {
		node := nodes[i]
		if node.TerminalState != domain.FlowNodePending && node.TerminalState != domain.FlowNodeInterrupted {
			continue
		}
		if inactiveBranch(node.Handle, def, byHandle, map[string]bool{}) {
			continue
		}
		if task, ok := def.Tasks[node.Handle]; ok {
			if task.Terminal != nil {
				continue // terminal gates complete the flow; never dispatched
			}
			if uncompletedDep(task, completed) != "" {
				continue // depends not yet completed; not ready
			}
		}
		ready = append(ready, node)
	}
	return ready
}

// firstSpecialTask returns the first ready check task (or nil). Check tasks
// are deterministic gates that run alone, once the parallel lane has drained.
func firstSpecialTask(ready []*domain.RunAgentFlowNode, def *domain.FlowDefinition, taskType string) *domain.RunAgentFlowNode {
	for _, node := range ready {
		if task, ok := def.Tasks[node.Handle]; ok && task.Type == taskType {
			return node
		}
	}
	return nil
}

// firstFanOutTask returns the first ready fan_out task (or nil). Fan_out
// tasks are read-only (validated) and run alone like checks.
func firstFanOutTask(ready []*domain.RunAgentFlowNode, def *domain.FlowDefinition) *domain.RunAgentFlowNode {
	for _, node := range ready {
		if task, ok := def.Tasks[node.Handle]; ok && task.FanOut != nil {
			return node
		}
	}
	return nil
}

// terminalGateTask returns the flow's terminal task when every one of its
// depends has a completed checkpoint, or nil if it is not yet unlocked.
func terminalGateTask(def *domain.FlowDefinition, completed map[string]*domain.RunAgentFlowNode) *domain.FlowTask {
	for _, task := range def.Tasks {
		if task.Terminal == nil {
			continue
		}
		if uncompletedDep(task, completed) == "" {
			value := task
			return &value
		}
	}
	return nil
}

// firstReadyClass picks the lane for the next dispatch window. Reader-first:
// when both classes are ready, read-only tasks go first so the mutation lane
// drains before any writer touches the workspace.
func firstReadyClass(ready []*domain.RunAgentFlowNode) int {
	for _, node := range ready {
		if node.ReadOnly {
			return laneReader
		}
	}
	return laneWriter
}

// selectLaneBatch picks the dispatch batch for the active lane: up to
// parallelism.max same-class role tasks. Writers stay exclusive unless the
// flow opts into disjoint writes scopes; in that case the batch greedily adds
// writers whose frozen Writes are pairwise disjoint with the batch and with
// the in-flight writers. Returns the batch and whether any ready task belongs
// to the lane.
func selectLaneBatch(ready []*domain.RunAgentFlowNode, lane int, def *domain.FlowDefinition,
	maxParallel int, inFlight []inflightTask) ([]*domain.RunAgentFlowNode, bool) {
	var batch []*domain.RunAgentFlowNode
	found := false
	if lane == laneReader {
		for _, node := range ready {
			if !node.ReadOnly {
				continue
			}
			found = true
			if len(batch) < maxParallel {
				batch = append(batch, node)
			}
		}
		return batch, found
	}
	// Writer lane.
	disjoint := def.DisjointWritersEnabled()
	inFlightScopes := make([][]string, 0, len(inFlight))
	for _, f := range inFlight {
		inFlightScopes = append(inFlightScopes, f.node.Writes)
	}
	for _, node := range ready {
		if node.ReadOnly {
			continue
		}
		found = true
		if len(batch) >= maxParallel {
			continue
		}
		if !disjoint {
			if len(batch) == 0 {
				batch = append(batch, node)
			}
			continue
		}
		compatible := true
		for _, other := range inFlightScopes {
			if !writesDisjoint(node.Writes, other) {
				compatible = false
				break
			}
		}
		if compatible {
			for _, selected := range batch {
				if !writesDisjoint(node.Writes, selected.Writes) {
					compatible = false
					break
				}
			}
		}
		if compatible {
			batch = append(batch, node)
		}
	}
	return batch, found
}

// inactiveBranch reports whether a node lies on an inactive check-gate
// branch: it (or any of its depends ancestors) is the target of a check whose
// gate is unresolved or whose verdict does not select that branch. Inactive
// targets and everything downstream of them are never dispatched — this is
// what makes branch routing deterministic and keeps unselected branches from
// leaking into the DAG.
func inactiveBranch(handle string, def *domain.FlowDefinition, nodes map[string]*domain.RunAgentFlowNode,
	seen map[string]bool) bool {
	if seen[handle] {
		return false
	}
	seen[handle] = true
	if mismatchedTarget(handle, def, nodes) {
		return true
	}
	if task, ok := def.Tasks[handle]; ok {
		for _, dep := range task.Depends {
			if inactiveBranch(dep, def, nodes, seen) {
				return true
			}
		}
	}
	return false
}

// mismatchedTarget reports whether the node is the target of a check-gate
// branch that is unresolved or does not select it.
func mismatchedTarget(handle string, def *domain.FlowDefinition, nodes map[string]*domain.RunAgentFlowNode) bool {
	for name, task := range def.Tasks {
		if task.Type != domain.FlowTaskCheck || task.Next == nil {
			continue
		}
		wanted := ""
		if task.Next["pass"] == handle {
			wanted = "pass"
		} else if task.Next["fail"] == handle {
			wanted = "fail"
		} else {
			continue
		}
		checkNode := nodes[name]
		if checkNode == nil || checkNode.TerminalState != domain.FlowNodeCompleted {
			return true // gate not yet resolved -> target inactive
		}
		if checkNodePass(checkNode.OutputRef) != (wanted == "pass") {
			return true // verdict selects the other branch
		}
	}
	return false
}

// checkNodePass reads the deterministic pass verdict from a check checkpoint.
func checkNodePass(outputRef json.RawMessage) bool {
	if len(outputRef) == 0 {
		return false
	}
	var out struct {
		Pass bool `json:"pass"`
	}
	if err := json.Unmarshal(outputRef, &out); err != nil {
		return false
	}
	return out.Pass
}

// nodeChildIDs returns every child Run id recorded on a node (single child
// plus the fan_out set, deduplicated).
func (o *Orchestrator) nodeChildIDs(node *domain.RunAgentFlowNode) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	add(node.ChildRunID)
	for _, id := range node.ChildRunIDs {
		add(id)
	}
	return ids
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
		for _, childID := range childIDs {
			result, _ := o.Children.ChildTerminalResult(ctx, childID)
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

// applyConvergence performs at most one back-edge round per rule per
// invocation: when the rule's from node is completed, the counter is
// incremented durably; if the counter exceeds max_rounds the meta-Run
// terminalizes as convergence_exceeded (never silent success); otherwise the
// loop path (to..from) is reset to pending so the next dispatch re-runs the
// loop. Nodes outside the loop keep their checkpoints.
func (o *Orchestrator) applyConvergence(ctx context.Context, runID string, def *domain.FlowDefinition,
	nodes []*domain.RunAgentFlowNode) (bool, error) {
	if len(def.Convergence) == 0 {
		return false, nil
	}
	byHandle := make(map[string]*domain.RunAgentFlowNode, len(nodes))
	for i := range nodes {
		byHandle[nodes[i].Handle] = nodes[i]
	}
	rounds, err := o.Store.GetConvergenceRounds(ctx, runID)
	if err != nil {
		return false, err
	}
	if rounds == nil {
		rounds = map[string]int{}
	}
	changed := false
	for _, rule := range def.Convergence {
		fromNode := byHandle[rule.From]
		if fromNode == nil || fromNode.TerminalState != domain.FlowNodeCompleted {
			continue // from has not completed in this round yet
		}
		key := rule.From + "\x00" + rule.To
		rounds[key]++
		changed = true
		if rounds[key] > rule.MaxRounds {
			// Persist the final counter BEFORE terminalizing so a recovery
			// between the two writes never observes a missing counter. If the
			// write fails, surface the error and let the caller fail the flow
			// (degraded terminal state) instead of silently dropping it.
			if err := o.Store.SetConvergenceRounds(ctx, runID, rounds); err != nil {
				return true, err
			}
			reason := fmt.Sprintf("convergence %s->%s exceeded max_rounds %d", rule.From, rule.To, rule.MaxRounds)
			_ = o.Store.UpdateFlowState(ctx, runID, domain.FlowStateConvergenceExceeded, 0, reason)
			_ = o.Events.PublishFlow(ctx, runID, "flow_convergence_exceeded", map[string]any{
				"flowRunId": runID, "from": rule.From, "to": rule.To, "rounds": rounds[key],
			})
			_ = o.Store.TerminateAnchor(ctx, runID, domain.RunFailed, "convergence_exceeded", reason)
			return true, nil
		}
		// Back-edge: reset every node on the loop path (to..from) so the next
		// dispatch re-runs the loop; nodes outside the loop keep checkpoints.
		for _, handle := range loopPathNodes(rule.From, rule.To, def) {
			node := byHandle[handle]
			if node == nil || !node.TerminalState.Terminal() {
				continue
			}
			_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
				TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{node.TerminalState},
				SetState: domain.FlowNodePending,
			})
		}
	}
	if changed {
		if err := o.Store.SetConvergenceRounds(ctx, runID, rounds); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// loopPathNodes returns the nodes on the declared loop path from 'to' to
// 'from' (inclusive), i.e. every node reachable from 'to' that also reaches
// 'from' in the depends DAG. These are exactly the nodes a back-edge must
// re-run; everything else keeps its checkpoint.
func loopPathNodes(from, to string, def *domain.FlowDefinition) []string {
	reach := reachabilityMatrix(def.Tasks, map[[2]string]int{})
	var path []string
	for handle := range def.Tasks {
		if reach[to][handle] && reach[handle][from] {
			path = append(path, handle)
		}
	}
	sort.Strings(path)
	return path
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
		var root any
		if err := json.Unmarshal(payload, &root); err != nil {
			return "", fmt.Errorf("task %q output is not valid JSON; cannot read field %q", task, field)
		}
		value, ok := lookupPath(root, field)
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

func lookupPath(root any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = root
	for _, part := range parts {
		if index, err := strconv.Atoi(part); err == nil {
			array, ok := current.([]any)
			if !ok || index < 0 || index >= len(array) {
				return nil, false
			}
			current = array[index]
			continue
		}
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
