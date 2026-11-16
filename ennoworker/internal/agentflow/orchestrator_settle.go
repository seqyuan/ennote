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
	"slices"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// taskTimeoutCode is the node error code of an attempt that ran past its
// declared timeout_ms. It is retryable, unlike a verdict about the product.
const taskTimeoutCode = "task_timeout"

// retryableFailureCode reports whether a task failure may be re-dispatched.
// Only the two outcomes that say "this attempt went wrong" are retryable: a
// child Run that ended failed or interrupted, and a timeout. A verdict about the
// product (acceptance_failed, report_contract_violation), a cancelled task and
// therefore a cancelled flow are not: re-running them without changing the
// input hides the real problem instead of fixing it.
func retryableFailureCode(code string) bool {
	switch code {
	case "task_failed", "task_interrupted", taskTimeoutCode:
		return true
	default:
		return false
	}
}

// attemptExpired reports the index of an in-flight attempt that ran past its
// task's wall-clock ceiling, or -1.
//
// The dispatch time is in memory only: it dies with the process, and no
// persisted dispatch timestamp is invented here. A restart therefore cannot
// know how long a still-running attempt had already run. That limitation is
// handled at recovery: a node left `running` whose child is already terminal is
// folded from that terminal fact, and one whose child is still active is reset
// to `interrupted` and re-dispatched through the ordinary poll (reconcileCrashedNodes) —
// never treated as expired just because the start time is gone. The ceiling
// still bounds every attempt this process dispatches, so a task can never run
// unbounded: at worst a restart re-runs one attempt from its own start.
func (o *Orchestrator) attemptExpired(def *domain.FlowDefinition, inFlight []inflightTask) int {
	now := o.now()
	for i := range inFlight {
		limit := taskTimeoutMS(def, inFlight[i].node.Handle)
		if limit <= 0 || inFlight[i].startedAt.IsZero() {
			continue
		}
		if now.Sub(inFlight[i].startedAt) >= limit {
			return i
		}
	}
	return -1
}

// waitAndFoldAny polls all in-flight hero children and folds the first one
// that settles, removing it from the set. The rest keep running; the caller
// re-evaluates the ready set. On flow cancellation it returns at once and the
// caller's next loop iteration terminalizes the run through cancelRemaining,
// which stops the in-flight children and observes the stops it actually made.
//
// An attempt past its task's timeout_ms is hard-cancelled here and recorded as
// task_timeout; the settlement then runs through the ordinary failure path
// (retry first, `optional` second, flow failure last). The in-flight siblings are
// left alone by the retry and optional outcomes, so a timed-out task never
// discards progress another task already holds, and a terminal failure only
// cancels what is still running.
func (o *Orchestrator) waitAndFoldAny(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	def *domain.FlowDefinition, inFlight *[]inflightTask) error {
	ticker := time.NewTicker(o.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if o.isCancelled(ctx, runID) {
			// The stop is NOT requested here. The loop top terminalizes through
			// cancelRemaining, which runs the whole stop -> cleanup -> publish sequence
			// over the nodes it reads, and the flow_cancelled payload must report what
			// THIS process observed. Cancelling the in-flight children here as well
			// would write their nodes `cancelled` first, so the later observation would
			// see settled nodes, count zero stopped children and have nothing to
			// report - a payload describing an observation nobody made.
			return nil // loop top terminalizes via cancelRemaining
		}
		// Settled first, expired second: a child that already ended is folded from
		// its terminal fact even when the poll happens to arrive after the ceiling.
		// A timeout means "the child had not settled by the deadline", not "the
		// deadline passed before we looked".
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
			if index := o.attemptExpired(def, *inFlight); index >= 0 {
				task := (*inFlight)[index]
				*inFlight = append((*inFlight)[:index], (*inFlight)[index+1:]...)
				// Re-read before cancelling: the child may have settled between the
				// settlement poll above and this decision (a race with the deadline).
				// A task that finished is folded from its own outcome - a success
				// recorded as task_timeout would fail a flow that did its work.
				status, err := o.Children.ChildRunStatus(ctx, task.childID)
				if err != nil {
					return err
				}
				if status.Terminal() {
					result, _ := o.Children.ChildTerminalResult(ctx, task.childID)
					return o.foldHeroChild(ctx, runID, flow, def, task.node, task.childID, status, result, *inFlight)
				}
				// Hard-cancel the timed-out child: the ceiling bounds the budget it
				// may spend, and the node must not keep a running child behind a
				// failed checkpoint. The settlement waits for that cancel to be
				// observable (settleTimedOutAttempt), because re-dispatching a retry
				// over a child that is still running would put two children for this
				// node in the same workspace.
				usage, _ := o.Children.ChildUsage(ctx, task.childID)
				total, _ := o.Store.AddTokenUsage(ctx, runID, usage.Tokens)
				o.settleTimedOutAttempt(ctx, runID, flow, def, task, *inFlight, total)
				return nil // whichever way it settled, the dispatch loop re-reads state
			}
			continue
		}
		task := (*inFlight)[settled]
		*inFlight = append((*inFlight)[:settled], (*inFlight)[settled+1:]...)
		result, _ := o.Children.ChildTerminalResult(ctx, task.childID)
		status, _ := o.Children.ChildRunStatus(ctx, task.childID)
		return o.foldHeroChild(ctx, runID, flow, def, task.node, task.childID, status, result, *inFlight)
	}
}

// settleTimedOutAttempt records one attempt that ran past its ceiling, and it
// records it only once the child is observably terminal.
//
// The cancel is not fire-and-forget: a DB-only cancel leaves the child's loop
// executing, and `retry` would then dispatch a second child for the same node in
// the same workspace. So the error is no longer discarded, the status is re-read
// (a child that won the race settled on its own, and its own outcome owns the
// node), and a cancel that does not reach the child stops the flow loudly
// instead of guessing. An attempt stuck in that state is an infrastructure
// fact, not a task verdict: `optional` does not absorb it.
func (o *Orchestrator) settleTimedOutAttempt(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	def *domain.FlowDefinition, task inflightTask, remaining []inflightTask, total int64) {
	cancelErr := o.Children.CancelChildRun(ctx, task.childID)
	// A cancel can fail because the child settled first (a terminal Run refuses
	// the transition); that is a race, not a broken cancel, and the re-read is
	// what tells the two apart.
	status, statusErr := o.awaitChildTerminal(ctx, task.childID)
	if statusErr == nil && status.Terminal() {
		if status != domain.RunCancelled {
			// Settled concurrently with the deadline: fold the real outcome
			// instead of recording a timeout that did not happen.
			result, _ := o.Children.ChildTerminalResult(ctx, task.childID)
			_ = o.foldHeroChild(ctx, runID, flow, def, task.node, task.childID, status, result, remaining)
			return
		}
		o.settleAttemptFailure(ctx, runID, def, task.node, taskTimeoutCode, total, remaining)
		return
	}
	reason := fmt.Sprintf("task %q %s: the timed-out attempt did not stop", task.node.Handle, taskTimeoutCode)
	switch {
	case cancelErr != nil:
		reason += ": cancel failed: " + cancelErr.Error()
	case statusErr != nil:
		reason += ": status unreadable: " + statusErr.Error()
	default:
		reason += ": the child is still " + string(status)
	}
	o.failTask(ctx, runID, task.node, taskTimeoutCode)
	outcome := o.cancelInflight(ctx, runID, remaining)
	o.failKeepingUsage(ctx, runID, total, reason+outcome.reasonSuffix())
}

// cancelSettlePolls bounds how long a cancelled attempt is awaited. The poll
// count, not a second timeout, keeps the wait short and bounded: a child that is
// terminal in the substrate answers on the first read.
const cancelSettlePolls = 20

// awaitChildTerminal polls a child until it is terminal, the budget of polls is
// spent, or the caller context ends. It returns the last observed status so a
// caller can say what it actually saw.
func (o *Orchestrator) awaitChildTerminal(ctx context.Context, childID string) (domain.RunStatus, error) {
	var status domain.RunStatus
	var lastErr error
	for poll := 0; poll < cancelSettlePolls; poll++ {
		if poll > 0 {
			select {
			case <-ctx.Done():
				return status, ctx.Err()
			case <-time.After(o.PollInterval):
			}
		}
		observed, err := o.Children.ChildRunStatus(ctx, childID)
		if err == nil && observed.Terminal() {
			return observed, nil
		}
		status, lastErr = observed, err
	}
	return status, lastErr
}

// settleAttemptFailure records one failed attempt of a task and decides what it
// does to the flow. The order is fixed and load-bearing: the retry budget is
// spent FIRST, then `optional` may absorb the outcome, and only then does the
// flow fail. A task declaring both therefore retries before it gives up.
//
// It returns nothing because the durable node state is the answer: a retry
// leaves the node `pending` (the dispatch loop picks it up again), an absorbed
// failure leaves it `failed` with its code (its dependents are satisfied), and a
// flow failure terminalizes the run.
func (o *Orchestrator) settleAttemptFailure(ctx context.Context, runID string, def *domain.FlowDefinition,
	node *domain.RunAgentFlowNode, code string, total int64, remaining []inflightTask) {
	if o.retryAttempt(ctx, runID, def, node, code) {
		return
	}
	o.failTask(ctx, runID, node, code)
	if node.Optional {
		// The failure is recorded on the node and nowhere else: the flow keeps
		// running, the in-flight siblings are untouched, and every dependent of
		// this task is still schedulable (depSatisfied).
		return
	}
	// The siblings are stopped in the same three phases as a user cancel, and the
	// reason says so when a stop was not observed: the ledger total is the one the
	// fold already counted, never 0.
	outcome := o.cancelInflight(ctx, runID, remaining)
	o.failKeepingUsage(ctx, runID, total, fmt.Sprintf("task %q %s", node.Handle, code)+outcome.reasonSuffix())
}

// retryAttempt re-dispatches a task whose attempt failed retryably, while its
// retry budget lasts, and reports whether it did.
//
// The node is reset to `pending` in ONE CAS that also clears error_code,
// output_ref and the acceptance verdict and spends the attempt: two writes would
// leave a window where a crash either loses the increment (resume re-grants a
// spent attempt, turning retry.max into an unbounded loop across restarts) or
// keeps a stale failure on a node that is about to run again.
func (o *Orchestrator) retryAttempt(ctx context.Context, runID string, def *domain.FlowDefinition,
	node *domain.RunAgentFlowNode, code string) bool {
	if !retryableFailureCode(code) {
		return false
	}
	limit := 0
	if def != nil {
		if task, ok := def.Tasks[node.Handle]; ok && task.Retry != nil {
			limit = task.Retry.Max
		}
	}
	if limit <= 0 || node.Attempts >= limit {
		return false // no budget: the ordinary failure path owns this
	}
	// One child per node at a time: a retry re-dispatches the node, so every child
	// the previous attempt created must be observably terminal first. A timeout
	// already waits for its cancel (settleTimedOutAttempt); this is the same rule
	// at the dispatch gate, so no path can leave two children running for one
	// node in one workspace.
	if !o.nodeChildRunsSettled(ctx, node) {
		return false
	}
	// A cancelled flow is never retried: re-dispatching a task the user stopped
	// would keep work going after the cancel, and the cancellation path is about
	// to mark every non-terminal node cancelled anyway.
	if o.isCancelled(ctx, runID) {
		return false
	}
	err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodePending, ClearErrorCode: true, ClearOutputRef: true,
		AcceptanceVerdict: clearedAcceptanceVerdict(), IncrementAttempts: true,
	})
	if err != nil {
		// A lost CAS (or any write failure) leaves the attempt unspent and the node
		// with its failure fact: the caller's ordinary failure path decides next. It
		// must not guess that the node was settled.
		return false
	}
	_ = o.Events.PublishFlow(ctx, runID, "flow_task_retrying", map[string]any{
		"flowRunId": runID, "task": node.Handle, "reason": code,
		// The dispatch this event announces, not the attempt it spent: the
		// first retry is dispatch 2 (dispatch 1 was the attempt that failed),
		// and maxDispatches = 1 + retry.max. The names say which is which so a
		// reader cannot take one for the other.
		"dispatchOrdinal": node.Attempts + 2, "maxDispatches": limit + 1,
	})
	return true
}

// nodeChildRunsSettled reports whether every child Run recorded on a node has
// settled. An unreadable status is NOT settled: the gate exists to keep two
// children of one node from running at once, so it fails closed.
func (o *Orchestrator) nodeChildRunsSettled(ctx context.Context, node *domain.RunAgentFlowNode) bool {
	for _, childID := range o.nodeChildIDs(node) {
		status, err := o.Children.ChildRunStatus(ctx, childID)
		if err != nil || !status.Terminal() {
			return false
		}
	}
	return true
}

// cancelCleanupIncompleteCode is the node error code of a cancelled node whose
// child Run never confirmed that it stopped. The node is still `cancelled`: the
// code records that the stop was not OBSERVED, not that it did not happen.
const cancelCleanupIncompleteCode = "cancel_cleanup_incomplete"

// cancelCleanupTimeout bounds the cleanup phase of a cancellation: how long the
// flow waits for a cancelled child's stop to become observable. It is a plain
// var so tests can shorten it.
//
// The bound is measured on the wall clock, never on o.Now: it bounds how long
// this process blocks, and a task's (possibly faked) ceiling clock must not be
// able to stretch it or skip it.
var cancelCleanupTimeout = 30 * time.Second

// cancelTarget is one node facing cancellation, plus every child Run recorded on
// it (a fan_out node records one per instance, a retried node one per attempt).
type cancelTarget struct {
	node     *domain.RunAgentFlowNode
	childIDs []string
}

// cancelOutcome is what a cancellation actually observed. It is the only input
// of the publish phase and of the terminal reason, so the words a cancelled flow
// reports are the words this process saw.
type cancelOutcome struct {
	// stopped counts the child Runs observed terminal after the stop request.
	stopped int
	// unconfirmed lists, in node/task order, the child Run ids whose stop was
	// never observed (not terminal when the cleanup budget ran out, or the caller
	// context ended first).
	unconfirmed []string
	// unconfirmedNodes counts the affected NODES with at least one unconfirmed
	// child: the reason talks about tasks, the payload talks about child Runs, and
	// one fan_out node can hold several unconfirmed instances.
	unconfirmedNodes int
	// stopErrors keeps each child's cancel error. A refused cancel (the child had
	// already settled) and an undelivered one are indistinguishable from the
	// return value alone; only the status read tells them apart. Kept for
	// diagnostics and tests, deliberately not published: the payload names what
	// was observed, and an error string would be a second, unverifiable claim.
	stopErrors map[string]error
}

// reasonSuffix names an unconfirmed stop at the end of a terminal reason, or
// nothing at all when every child confirmed.
func (o cancelOutcome) reasonSuffix() string {
	if o.unconfirmedNodes == 0 {
		return ""
	}
	plural := "s"
	if o.unconfirmedNodes == 1 {
		plural = ""
	}
	return fmt.Sprintf("; %d task%s did not confirm stop", o.unconfirmedNodes, plural)
}

// payload is the `cleanup` object of flow_cancelled. The unconfirmed list is
// always a list (never null) so a consumer can iterate it unconditionally, and
// it is empty exactly when every child confirmed.
func (o cancelOutcome) payload() map[string]any {
	unconfirmed := o.unconfirmed
	if unconfirmed == nil {
		unconfirmed = []string{}
	}
	return map[string]any{"stopped": o.stopped, "unconfirmed": unconfirmed}
}

// cancelInflight cancels a set of in-flight children and marks their nodes
// cancelled, without touching the flow state (used on parallel task failure,
// budget overrun, blocked children and the timeout path; user cancellation takes
// the cancelRemaining path instead, which also terminalizes the flow).
//
// It runs the SAME three phases as cancelRemaining (cancelNodes), so the node
// write and the reason suffix can never drift from the observation. The caller
// that terminalizes the flow appends outcome.reasonSuffix() to its reason and
// writes the ledger total it already holds. The worst case adds
// cancelCleanupTimeout to the caller's path, and never more.
func (o *Orchestrator) cancelInflight(ctx context.Context, runID string, inFlight []inflightTask) cancelOutcome {
	targets := make([]cancelTarget, 0, len(inFlight))
	for _, task := range inFlight {
		// Every child of the node is stopped, not only the one this loop is
		// waiting on: an earlier attempt's child recorded on the same node must not
		// outlive a cancelled flow either. task.childID is the attempt in flight
		// now, and it may not be on the node snapshot the loop holds yet.
		childIDs := o.nodeChildIDs(task.node)
		if task.childID != "" && !slices.Contains(childIDs, task.childID) {
			childIDs = append(childIDs, task.childID)
		}
		targets = append(targets, cancelTarget{node: task.node, childIDs: childIDs})
	}
	return o.cancelNodes(ctx, runID, targets)
}

// cancelNodes applies a cancellation in three explicit phases and reports what
// the process actually observed.
//
// It is the ONE implementation both entry points call (cancelInflight for task
// failure / budget overrun / blocked children / timeout, cancelRemaining for user
// cancellation), so the two cannot drift into "stopped" meaning two things.
//
// Phase 1, stop: request the cancel for every child of every target and keep the
// error. Nothing is written - a cancel request is not a fact about the child yet.
//
// Phase 2, cleanup: poll each child until it is OBSERVABLY terminal, bounded by
// cancelCleanupTimeout and polled with o.PollInterval. A terminal child counts as
// stopped, the rest are unconfirmed. A cancelled caller context ends the wait and
// keeps what is known: cancellation is best effort by definition.
//
// Phase 3, publish: write each node cancelled exactly once, carrying
// cancel_cleanup_incomplete when one of its children is unconfirmed. The CAS
// never overwrites a node that reached a terminal state on its own (a task that
// finished keeps its own outcome and its result).
//
// A check task is NOT covered here: it runs synchronously inside the
// orchestrator (runCheckTask), so it has no child Run to stop and no separate
// lifecycle to observe. Cancelling a flow whose active task is a check is
// handled by that call returning to the loop, not by this helper.
func (o *Orchestrator) cancelNodes(ctx context.Context, runID string, targets []cancelTarget) cancelOutcome {
	out := cancelOutcome{stopErrors: map[string]error{}}

	// Phase 1 — stop: request, keep the error, publish nothing.
	var ids []string
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		for _, childID := range target.childIDs {
			if seen[childID] {
				continue
			}
			seen[childID] = true
			ids = append(ids, childID)
			out.stopErrors[childID] = o.Children.CancelChildRun(ctx, childID)
		}
	}

	// Phase 2 — cleanup: observe, bounded.
	statuses := o.awaitCancelCleanup(ctx, ids)
	unconfirmed := make(map[string]bool, len(ids))
	for _, childID := range ids {
		if statuses[childID].Terminal() {
			out.stopped++
			continue
		}
		unconfirmed[childID] = true
		out.unconfirmed = append(out.unconfirmed, childID)
	}
	unconfirmedTargets := make([]bool, len(targets))
	for index, target := range targets {
		unconfirmedTargets[index] = slices.ContainsFunc(target.childIDs, func(childID string) bool {
			return unconfirmed[childID]
		})
		if unconfirmedTargets[index] {
			out.unconfirmedNodes++
		}
	}

	// Phase 3 — publish: one write per node, CAS-guarded.
	for index, target := range targets {
		code := ""
		if unconfirmedTargets[index] {
			code = cancelCleanupIncompleteCode
		}
		_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
			// The CAS is the observation that matters: pending/running is "not
			// settled yet", every other state is somebody else's terminal fact
			// (completed, failed, blocked, interrupted, already cancelled) and is
			// never overwritten by a cancel.
			TaskIndex:      target.node.TaskIndex,
			ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending, domain.FlowNodeRunning},
			SetState:       domain.FlowNodeCancelled,
			ErrorCode:      code,
		})
	}
	return out
}

// awaitCancelCleanup polls the children a cancel was delivered to until each one
// is observably terminal or the cleanup budget is spent.
//
// It returns the last status it read per child. A child that is absent from the
// returned map was never readable, which the caller reads as unconfirmed:
// "I could not see it stop" is not "it stopped".
//
// The budget is checked BETWEEN passes, so one pass of status reads may run past
// it by the cost of that pass. That is deliberate: the alternative is a
// per-child deadline, which would make the total wait grow with the fan-out.
func (o *Orchestrator) awaitCancelCleanup(ctx context.Context, ids []string) map[string]domain.RunStatus {
	statuses := make(map[string]domain.RunStatus, len(ids))
	if len(ids) == 0 {
		return statuses
	}
	pending := append([]string(nil), ids...)
	deadline := time.Now().Add(cancelCleanupTimeout)
	for {
		if ctx.Err() != nil {
			return statuses
		}
		remaining := make([]string, 0, len(pending))
		for _, childID := range pending {
			status, err := o.Children.ChildRunStatus(ctx, childID)
			if err == nil {
				statuses[childID] = status
			}
			if err != nil || !status.Terminal() {
				remaining = append(remaining, childID)
			}
		}
		pending = remaining
		if len(pending) == 0 {
			return statuses
		}
		// One observation pass always happens, even with an exhausted budget: a
		// child that is already terminal answers on the first read, and a zero
		// budget must still report what it saw instead of nothing.
		wait := o.PollInterval
		if left := time.Until(deadline); left < wait {
			wait = left
		}
		if wait <= 0 {
			return statuses
		}
		select {
		case <-ctx.Done():
			return statuses
		case <-time.After(wait):
		}
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
			// A crash consumed the attempt it was running: the reset SPENDS it, so
			// retry.max stays an upper bound on dispatches instead of a budget a
			// crash loop re-grants for free. The CAS is the same one that states
			// `interrupted`, so the counter cannot be lost or applied twice.
			_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
				TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
				SetState: domain.FlowNodeInterrupted, IncrementAttempts: true,
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
	// A completed flow that tolerated a failed optional task is not a clean one,
	// and it must not read like one. Both surfaces say so: the run's
	// terminal_reason names the tasks, and the payload carries them as data so a
	// consumer does not have to parse prose to find them.
	reason := ""
	if failed := failedOptionalTasks(nodes); len(failed) > 0 {
		handles := make([]string, 0, len(failed))
		entries := make([]map[string]string, 0, len(failed))
		for _, node := range failed {
			handles = append(handles, node.Handle)
			entries = append(entries, map[string]string{"task": node.Handle, "errorCode": node.ErrorCode})
		}
		plural := "s"
		if len(failed) == 1 {
			plural = ""
		}
		reason = fmt.Sprintf("completed with %d failed optional task%s: %s",
			len(failed), plural, strings.Join(handles, ", "))
		payload["failedTasks"] = entries
	}
	// The event carries the same summary the API projects from the node rows, so
	// an auditor reading the timeline sees which tasks were only "nothing was
	// disproved" without opening the run. Absent when nothing declared a clause.
	if summary := acceptanceSummaryOf(nodes); summary != nil {
		payload["acceptance"] = summary
	}
	if err := o.Store.UpdateFlowState(ctx, runID, domain.FlowStateCompleted, totalTokens, reason); err != nil {
		return err
	}
	if err := o.Events.PublishFlow(ctx, runID, "flow_completed", payload); err != nil {
		return err
	}
	return o.Store.TerminateAnchor(ctx, runID, domain.RunSucceeded, "", "")
}

// failedOptionalTasks returns the checkpoints that failed while declared
// optional, in task order (the node list is already ordered by task index).
// These are exactly the failures the flow absorbed: a completed run that holds
// them is not a clean run, and `complete` says so.
func failedOptionalTasks(nodes []*domain.RunAgentFlowNode) []*domain.RunAgentFlowNode {
	var failed []*domain.RunAgentFlowNode
	for _, node := range nodes {
		if node != nil && node.Optional && node.TerminalState == domain.FlowNodeFailed {
			failed = append(failed, node)
		}
	}
	return failed
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

// resolveGoalForTask resolves one ready task's goal, and it lives with the
// failure paths because that is what its error case is.
//
// A reference to the output of a task that failed while declared `optional` is
// refused at publish (optional_output_referenced): a failed task has no output,
// so the dependent could only fail. A run frozen before that gate existed still
// reaches this code, and it must not report a missing checkpoint as if nothing
// had run: the failure names the optional task and its error code, and keeps the
// ledger the flow already spent (failKeepingUsage). total is the ledger from the
// run row the dispatch loop already holds, so the honest total costs no read.
func (o *Orchestrator) resolveGoalForTask(ctx context.Context, runID, handle, template string,
	inputs, vars map[string]any, taskOutputs map[string]json.RawMessage,
	byHandle map[string]*domain.RunAgentFlowNode, total int64) (string, bool) {
	goal, err := ResolveGoalTemplate(template, inputs, vars, taskOutputs)
	if err == nil {
		return goal, true
	}
	var missing *missingTaskOutputError
	if errors.As(err, &missing) {
		if producer := byHandle[missing.task]; producer != nil &&
			producer.TerminalState == domain.FlowNodeFailed && producer.Optional {
			o.failKeepingUsage(ctx, runID, total,
				fmt.Sprintf("task %q cannot resolve its goal: optional task %q failed (%s), so {task.%s.output} is not available",
					handle, missing.task, producer.ErrorCode, missing.task))
			return "", false
		}
	}
	o.fail(ctx, runID, fmt.Sprintf("resolve goal for task %q: %v", handle, err))
	return "", false
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
