package agentflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ErrCheckpointConflict reports that a checkpoint CAS did not match: another
// orchestrator (a recovery racing a new start) settled the node first. It is a
// settled node, not a failure.
var ErrCheckpointConflict = errors.New("flow node state conflict")

// Orchestrator is the meta-Run state machine: pure orchestration, no Provider
// calls, no loop decisions. It drives one child Run per task through the
// delegation substrate, writes task checkpoints, accumulates the flow budget,
// and terminalizes the meta-Run. It is testable through its seams.
type Orchestrator struct {
	Store    FlowStore
	Children ChildProvider
	Events   EventSink
	Checker  CheckRunner
	// AcceptanceEvidence resolves the workspace evidence reader for one session.
	// nil means the runtime cannot read evidence: every declared check is then
	// UNVERIFIED, never silently absent.
	AcceptanceEvidence func(ctx context.Context, sessionID string) (AcceptanceEvidence, error)
	// ArtifactPresent reports whether a published artifact is retrievable with
	// the digest a child Run recorded; nil leaves artifact leaves UNVERIFIED.
	ArtifactPresent func(ctx context.Context, artifactID, sha256 string) (bool, error)
	// ChildExecutions returns one child Run's recorded shell executions. nil means the
	// runtime cannot read them: tests-passed is then UNVERIFIED, never a pass.
	ChildExecutions func(ctx context.Context, childRunID string) ([]domain.ToolExecution, error)
	// Enqueue hands a created child Run to the run coordinator, with the class the
	// scheduler must use. Every child the orchestrator creates is batch: a Graph
	// task must never take the slot reserved for a person's next turn, and the
	// class is passed explicitly so the coordinator never has to guess.
	Enqueue      func(ctx context.Context, runID string, class RunClass) error
	PollInterval time.Duration
	// CancelCleanupTimeout bounds how long a cancellation waits for its stop to
	// become observable. It is a field, not a package var, so two orchestrators
	// (and two parallel tests) never share one knob. Start defaults it.
	CancelCleanupTimeout time.Duration
	Now                  func() time.Time
}

// RunClass mirrors runs.RunClass across the agentflow -> runs boundary. The runs
// package imports agentflow (through store), so agentflow cannot import it; the
// two packages agree on the string values, and the worker's wiring is the only
// place a class crosses. Only ClassBatch is used here: every Run this package
// creates is a Graph child.
type RunClass string

const (
	// RunClassInteractive is a person's turn. agentflow never creates one.
	RunClassInteractive RunClass = "interactive"
	// RunClassBatch is a Graph task child.
	RunClassBatch RunClass = "batch"
)

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
	// AcceptanceVerdict is tri-state: nil leaves the column alone, a non-nil
	// empty value clears it (a node reset to pending must not keep a stale
	// verdict), and a non-nil value writes it in the same CAS as SetState.
	AcceptanceVerdict *json.RawMessage
	// ClearErrorCode un-records the failure code. It exists for a checkpoint
	// reset: COALESCE cannot clear, and a retried node must not still look
	// failed by acceptance.
	ClearErrorCode bool
	// ClearOutputRef un-records the checkpoint output. It exists for the same
	// reset: a re-dispatched node must not expose the previous attempt's result
	// as its checkpoint ({task.X.output} would read a failed attempt).
	ClearOutputRef bool
	// IncrementAttempts spends one retry attempt in the same CAS as the reset, so
	// the budget is durable and a resume can never re-grant an attempt.
	IncrementAttempts bool
}

// ChildSpec is one task's child Run request.
type ChildSpec struct {
	Handle        string
	HeroVersionID string
	// Skills are the task-level additive preload Skills frozen on the flow
	// node, including the content digest pinned at freeze time. They
	// supplement, but never replace or widen, the Hero policy.
	Skills domain.FrozenSkillRefs
	// HeroDefinitionJSON is the full resolved HeroDefinition frozen at Run
	// start. It is passed to child Run materialization so delegation never
	// re-reads mutable Hero files or the removed global hero SQL.
	HeroDefinitionJSON json.RawMessage
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
//
// CancelChildRun is one fact, not two: it must both record the cancellation
// durably AND stop the in-process execution of the child. A DB-only cancel
// leaves an executing Run looping to completion, so a retry would start a second
// child for the same node in the same workspace. After it returns, the child's
// status must be observably terminal (or becoming so); the flow re-reads the
// status instead of assuming it.
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
	if o.CancelCleanupTimeout <= 0 {
		o.CancelCleanupTimeout = defaultCancelCleanupTimeout
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
	// set of in-flight hero children. Independent ready tasks of the same
	// class dispatch concurrently up to parallelism.max; readers and writers
	// never share the workspace at the same time (mutation lane), and writers
	// only parallelize against each other when the flow opts into disjoint
	// writes scopes.
	lane := laneNone
	var inFlight []inflightTask
	maxParallel := def.EffectiveParallelismMax()
	// nodes is the last checkpoint read of the dispatch loop. The loop breaks only
	// when every node is completed, so the post-loop completion still has the
	// verdicts it needs for the run-level acceptance summary.
	var nodes []*domain.RunAgentFlowNode

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
			o.cancelRemaining(ctx, runID)
			return
		}
		state, err := o.Store.GetRun(ctx, runID)
		if err != nil {
			return
		}
		if state.State.Terminal() {
			return
		}
		nodes, err = o.Store.ListNodes(ctx, runID)
		if err != nil {
			o.fail(ctx, runID, "list nodes: "+err.Error())
			return
		}
		// byHandle is every checkpoint, not only the completed ones: a dependency
		// is satisfied by a completed node OR by a failed-but-optional one (see
		// depSatisfied), so readiness needs the whole set.
		byHandle := make(map[string]*domain.RunAgentFlowNode, len(nodes))
		for i := range nodes {
			byHandle[nodes[i].Handle] = nodes[i]
			if nodes[i].TerminalState == domain.FlowNodeCompleted && len(nodes[i].OutputRef) > 0 {
				taskOutputs[nodes[i].Handle] = nodes[i].OutputRef
			}
		}
		settlementFailure := settlementFailureNode(nodes)
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
		ready := readyHeroTasks(nodes, byHandle, &def)

		// Recorded-verdict settlement. A failed checkpoint that says the task's
		// report was not acceptable (acceptance_failed, report_contract_violation)
		// stops NEW dispatch immediately: while a sibling is still in flight the
		// loop only folds it, so "wait for the in-flight siblings" cannot turn into
		// "run the rest of the graph". Once nothing is in flight the flow fails
		// with the CURRENT token total (never 0) and the already-finished siblings
		// keep their own checkpoints. This runs before the terminal gate: an
		// unlocked terminal would otherwise complete a flow whose task's report was
		// rejected.
		if settlementFailure != nil {
			if len(inFlight) > 0 {
				if err := o.waitAndFoldAny(ctx, runID, run, &def, &inFlight); err != nil {
					return
				}
				continue
			}
			o.failKeepingUsage(ctx, runID, state.TotalTokensUsed,
				fmt.Sprintf("task %q %s", settlementFailure.Handle, settlementFailure.ErrorCode))
			return
		}

		// Check and fan_out tasks are blocking gates: checks may mutate the
		// workspace (sandbox commands) and fan_out instances read it. They run
		// alone, only once the parallel lane has drained, so a concurrent
		// writer can never race their snapshot.
		if len(inFlight) == 0 {
			if c := firstSpecialTask(ready, &def, domain.FlowTaskCheck); c != nil {
				goal, ok := o.resolveGoalForTask(ctx, runID, c.Handle, def.Tasks[c.Handle].Goal,
					inputs, vars, taskOutputs, byHandle, state.TotalTokensUsed)
				if !ok {
					return
				}
				if err := o.runCheckTask(ctx, runID, state, c, def.Tasks[c.Handle], goal, taskOutputs); err != nil {
					return
				}
				continue
			}
			if f := firstFanOutTask(ready, &def); f != nil {
				goal, ok := o.resolveGoalForTask(ctx, runID, f.Handle, def.Tasks[f.Handle].Goal,
					inputs, vars, taskOutputs, byHandle, state.TotalTokensUsed)
				if !ok {
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
			if allSettled(nodes) {
				break // every task is a satisfied dependency
			}
			// While children are still in flight, keep folding them: a batch
			// sibling may be exactly the dependency the terminal gate needs.
			if len(inFlight) > 0 {
				if err := o.waitAndFoldAny(ctx, runID, run, &def, &inFlight); err != nil {
					return
				}
				continue
			}
			// Terminal gate: once every runnable task has settled, the terminal
			// task resolves the flow outputs from its completed dependencies.
			// Branch flows whose unselected paths stay pending terminate here,
			// never dispatching the inactive branch. Terminals are mutually
			// exclusive at publish time, so at most one may be unlocked; if two
			// somehow are, the flow fails loudly instead of picking one.
			terminal, ambiguous := terminalGateTask(&def, byHandle)
			if ambiguous {
				o.fail(ctx, runID, "terminal_gate_ambiguous: more than one terminal task is unlocked at once, so the flow outputs are ambiguous")
				return
			}
			if terminal != nil {
				outputs := resolveFlowOutputs(def, *terminal, byHandle, taskOutputs)
				if err := o.complete(ctx, runID, state.TotalTokensUsed, outputs, nodes); err != nil {
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
				if err := o.waitAndFoldAny(ctx, runID, run, &def, &inFlight); err != nil {
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
			if missing := uncompletedDep(task, byHandle); missing != "" {
				o.fail(ctx, runID, fmt.Sprintf("task %q depends on incomplete task %q", node.Handle, missing))
				return
			}
			goal, ok := o.resolveGoalForTask(ctx, runID, node.Handle, task.Goal,
				inputs, vars, taskOutputs, byHandle, state.TotalTokensUsed)
			if !ok {
				return
			}
			child, err := o.dispatchHeroChild(ctx, runID, state, node, task, goal, taskOutputs)
			if err != nil {
				return
			}
			inFlight = append(inFlight, inflightTask{node: node, childID: child.RunID, startedAt: o.now()})
		}

		if len(inFlight) > 0 {
			// Wait for any in-flight child to settle, fold its checkpoint, and
			// re-evaluate the ready set (completed dependencies unlock the next
			// tier while the rest of the batch keeps running).
			if err := o.waitAndFoldAny(ctx, runID, run, &def, &inFlight); err != nil {
				return
			}
		}
	}
	// All tasks completed.
	if err := o.complete(ctx, runID, run.TotalTokensUsed, resolveFlowOutputsAll(def, taskOutputs), nodes); err != nil {
		return
	}
}

// frozenSkillRefs returns the digest-pinned task Skills of a frozen node. Nodes
// written before content pinning existed only carry the bare ID list, so they
// fall back to unpinned refs: the child then loads the current Skill content,
// exactly as it did before this schema.
func frozenSkillRefs(node *domain.RunAgentFlowNode) domain.FrozenSkillRefs {
	if len(node.FrozenSkills) > 0 {
		return append(domain.FrozenSkillRefs(nil), node.FrozenSkills...)
	}
	refs := make(domain.FrozenSkillRefs, 0, len(node.SkillDigests))
	for _, id := range node.SkillDigests {
		refs = append(refs, domain.FrozenSkillRef{ID: id})
	}
	return refs
}

// dispatchHeroChild claims one hero task (CAS pending -> running), creates its
// child Run, records the child on the node, publishes the started event, and
// enqueues the child. It never blocks: the caller batches hero children and
// waits for any to settle via waitAndFoldAny.
//
// taskOutputs is the checkpoint map the caller already holds; only a snapshot
// task reads it, to append the bounded upstream section to the assignment. The
// frozen goalText on the node stays the resolved goal in both modes.
func (o *Orchestrator) dispatchHeroChild(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	node *domain.RunAgentFlowNode, task domain.FlowTask, goal string, taskOutputs map[string]json.RawMessage) (ChildInfo, error) {
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
		Handle: node.Handle, HeroVersionID: node.HeroVersionID,
		Skills: frozenSkillRefs(node), Assignment: renderTaskAssignment(goal, node.Acceptance, task, taskOutputs), Budget: budget,
		HeroDefinitionJSON: node.HeroDefinitionJSON,
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
		if err := o.Enqueue(ctx, child.RunID, RunClassBatch); err != nil {
			o.fail(ctx, runID, fmt.Sprintf("enqueue child %s: %v", child.RunID, err))
			return ChildInfo{}, nil
		}
	}
	return child, nil
}

// foldHeroChild settles one hero child from an already-known terminal status:
// budget accounting, over-budget termination, failure/cancel handling, and the
// completed checkpoint + event. A failure goes through settleAttemptFailure,
// which re-dispatches it while its retry budget lasts, lets `optional` absorb it,
// and otherwise cancels every other in-flight child so a parallel batch never
// leaks running children. def is the frozen definition the run decoded once: it
// carries the retry budget the settlement reads.
func (o *Orchestrator) foldHeroChild(ctx context.Context, runID string, flow *domain.RunAgentFlow,
	def *domain.FlowDefinition, node *domain.RunAgentFlowNode, childID string, status domain.RunStatus,
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
			outcome := o.cancelInflight(ctx, runID, remaining)
			reason := fmt.Sprintf("flow budget exceeded: %d > %d", total, def.Budget.MaxTotalTokens) + outcome.reasonSuffix()
			_ = o.updateFlowStateKeepingUsage(ctx, runID, domain.FlowStateBudgetExceeded, total, reason)
			_ = o.Events.PublishFlow(ctx, runID, "flow_budget_exceeded", map[string]any{
				"flowRunId": runID, "used": total, "limit": def.Budget.MaxTotalTokens,
			})
			_ = o.Store.TerminateAnchor(ctx, runID, domain.RunFailed, "budget_exceeded",
				"flow budget exceeded"+outcome.reasonSuffix())
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
		// Retry first, `optional` second, flow failure last (settleAttemptFailure).
		// Keep the ledger: this path also runs while draining siblings after an
		// acceptance failure, and a flow that consumed tokens must not report 0.
		o.settleAttemptFailure(ctx, runID, def, node, code, total, remaining)
		return nil
	}
	// A blocked child is not a failed Run and not a completed task; see
	// foldBlockedHeroChild. Checked before acceptance: no evidence, no verdict.
	if o.foldBlockedHeroChild(ctx, runID, node, result, total, remaining) {
		return nil
	}
	payload := json.RawMessage{}
	if result != nil && len(result.Payload) > 0 {
		payload = result.Payload
	} else if result != nil {
		payload, _ = json.Marshal(map[string]any{"summary": result.Summary})
	}
	// The Hero's report contract is enforced before acceptance: a report that does
	// not have the promised shape is not evidence for anything, so a declared
	// acceptance clause is never evaluated against it.
	if contract := heroReportContractOf(node); contract != nil {
		if violation := domain.ValidateReportAgainstContract(result, contract); violation != nil {
			return o.foldReportContractViolation(ctx, runID, node, childID, payload, violation)
		}
	}
	// A declared acceptance clause is verified here, before the checkpoint
	// write: the child's own prose is never evidence. File leaves read the
	// workspace; artifact leaves read the refs this fold already holds.
	if len(node.Acceptance) > 0 {
		return o.foldAcceptance(ctx, runID, flow, node, childID, payload, result)
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

// failCheck records a check-gate failure and decides what it does to the flow.
// A gate is a task like any other for the `optional` knob: declaring it optional
// means the gate's failure is recorded on the node and the flow continues, with
// the gate's branch targets inactive (an unresolved gate selects no branch).
// Every check failure takes this path — policy denial, approval rejection,
// approval bookkeeping and execution error — because "this gate could not be
// decided" is one fact, and a positive list of exceptions would be a second rule
// to keep in sync.
func (o *Orchestrator) failCheck(ctx context.Context, runID string, node *domain.RunAgentFlowNode, code, reason string) {
	o.failTask(ctx, runID, node, code)
	if node.Optional {
		return
	}
	o.fail(ctx, runID, reason)
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
		o.failCheck(ctx, runID, node, "policy_unavailable", fmt.Sprintf("check task %q: %v", node.Handle, err))
		return nil
	}
	argv := ParseCheckCommand(task.Command)
	decision := EvaluateCheck(policy, argv)
	switch decision.Action {
	case CheckDeny:
		o.failCheck(ctx, runID, node, decision.Code,
			fmt.Sprintf("check task %q denied by tool policy: %s", node.Handle, decision.Reason))
		return nil
	case CheckRequireAsk:
		if err := o.Checker.CreateCheckApproval(ctx, runID, node.TaskIndex, task.Command); err != nil {
			o.failCheck(ctx, runID, node, "approval_create_failed",
				fmt.Sprintf("check task %q approval failed: %v", node.Handle, err))
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
				return o.cancelRemaining(ctx, runID)
			}
			status, err := o.Checker.CheckApprovalStatus(ctx, runID, node.TaskIndex)
			if err != nil {
				o.failCheck(ctx, runID, node, "approval_read_failed",
					fmt.Sprintf("check task %q approval read failed: %v", node.Handle, err))
				return nil
			}
			switch status {
			case agentFlowApprovalApproved:
				goto run
			case agentFlowApprovalRejected:
				o.failCheck(ctx, runID, node, "approval_rejected", fmt.Sprintf("check task %q approval rejected", node.Handle))
				return nil
			}
		}
	}
run:
	outcome, err := o.Checker.ExecuteCheck(WithCheckSession(ctx, flow.SessionID), task.Command, 0)
	if err != nil {
		o.failCheck(ctx, runID, node, "check_execution_failed",
			fmt.Sprintf("check task %q execution failed: %v", node.Handle, err))
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
			_ = o.cancelRemaining(ctx, runID)
			return nil
		}
		child, err := o.Children.CreateTaskChild(ctx, runID, flow.SessionID, ChildSpec{
			Handle:        fmt.Sprintf("%s-%d", node.Handle, i),
			HeroVersionID: node.HeroVersionID,
			Skills:        frozenSkillRefs(node),
			Assignment:    goal, Budget: budget,
			HeroDefinitionJSON: node.HeroDefinitionJSON,
		})
		if err != nil {
			o.failTask(ctx, runID, node, "fan_out_child_create_failed")
			o.fail(ctx, runID, fmt.Sprintf("fan_out task %q could not start: %v", node.Handle, err))
			return nil
		}
		childIDs = append(childIDs, child.RunID)
		// Record the child BEFORE creating the next one: a cancel that lands in
		// this loop reads the node to learn what to stop, so an id written only
		// after the loop is an id no cancel path can ever stop - a leaked child and
		// a payload claiming a clean stop with zero ids.
		if err := o.Store.UpdateNode(ctx, runID, NodeUpdate{
			TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
			SetState: domain.FlowNodeRunning, ChildRunIDs: append([]string(nil), childIDs...), GoalText: goal,
		}); err != nil {
			// The child exists but the node cannot record it, so no cancel path could
			// find it later: stop it here and fail loudly instead of leaking it.
			_ = o.Children.CancelChildRun(ctx, child.RunID)
			o.failTask(ctx, runID, node, "fan_out_child_record_failed")
			o.fail(ctx, runID, fmt.Sprintf("fan_out task %q could not record child %s: %v", node.Handle, child.RunID, err))
			return nil
		}
	}
	for _, childID := range childIDs {
		if o.Enqueue != nil {
			if err := o.Enqueue(ctx, childID, RunClassBatch); err != nil {
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
		return o.cancelRemaining(ctx, runID)
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
			if node.Optional {
				// Recorded on the node and absorbed: the flow continues, and a
				// dependent of this fan_out task is still schedulable.
				return nil
			}
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
	// Aggregate by instance order. A blocked instance is not a product: it is
	// folded in this same order, so the recorded code is deterministic.
	contract := heroReportContractOf(node)
	aggregate := make([]json.RawMessage, 0, len(childIDs))
	for _, childID := range childIDs {
		result := results[childID]
		if o.foldBlockedInstance(ctx, runID, node, result, total) {
			return nil
		}
		payload := json.RawMessage{}
		if result != nil && len(result.Payload) > 0 {
			payload = result.Payload
		} else if result != nil {
			payload, _ = json.Marshal(map[string]any{"summary": result.Summary})
		}
		// Every instance ran the task's Hero, so every instance's report must keep
		// the Hero's contract. The violating instance's own payload is the node's
		// output_ref: the aggregate would hide which instance broke the promise.
		if violation := domain.ValidateReportAgainstContract(result, contract); violation != nil {
			return o.foldReportContractViolation(ctx, runID, node, childID, payload, violation)
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

// waitChildren polls every child Run until all are terminal and honors the
// caller context.
//
// Flow cancellation is NOT handled here: it returns at once with the statuses it
// has read, and runFanOutTask hands the flow to cancelRemaining, which owns the
// stop -> cleanup -> publish sequence for the node's recorded children. A stop
// delivered here as well would write nothing, but it would make the cancel path
// observe already-settled children and report a stop it did not deliver - the
// payload has to name what this flow actually did.
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
		}
		if o.isCancelled(ctx, runID) {
			// The stop belongs to cancelRemaining (see the comment above).
			return results, statuses
		}
	}
	return results, statuses
}

// cancelRemaining terminalizes the meta-Run as cancelled: the user cancel is the
// entry point for the same three phases cancelInflight uses (stop -> cleanup ->
// publish), applied to every node that is still open, and only then does the flow
// terminalize.
//
// What it may claim is bounded by what it can observe: a node whose child Run was
// seen terminal after the cancel is `cancelled`; a node whose child did not
// confirm carries cancel_cleanup_incomplete, and the flow_cancelled payload
// carries the observation (cleanup.stopped / alreadyTerminal / completed /
// unconfirmed) plus a reason suffix, so "cancelled" never reads as "everything
// stopped" when it did not - and never as "nothing was produced" when a child had
// already succeeded.
//
// The ledger keeps the total the flow already accumulated AND the usage of the
// children this cancel watched stop: a cancelled flow that spent tokens must not
// report 0, and a child whose real outcome was stopped must not be discarded from
// the accounting either.
//
// It is idempotent on purpose. Three internal callers reach here in the middle of
// the dispatch loop (the check-approval wait, the fan_out instance loop and the
// waitChildren return), and the loop top calls it again after they return. Idempotent,
// but not a no-op on a half-written cancellation: see `repair` below.
func (o *Orchestrator) cancelRemaining(ctx context.Context, runID string) error {
	// A caller whose context ended during the cleanup still gets a terminal flow:
	// the observation is finished, so reading the ledger, writing the nodes and
	// recording the outcome must not be cancelled with it. database/sql drops a
	// write on a cancelled context, and what that leaves behind is a flow marked
	// cancelled whose nodes still say `running`.
	publishCtx := context.WithoutCancel(ctx)
	run, err := o.Store.GetRun(publishCtx, runID)
	if err != nil {
		return err
	}
	// A flow that is already `cancelled` may have committed that state and then
	// failed to settle its nodes or its anchor (a dropped write, a crash).
	// Returning early there would leave a `running` node behind a cancelled flow,
	// which is the one thing this path exists to prevent, so the remaining writes
	// are repaired. The observation itself is NOT published again: this process did
	// not make a second one. Any other terminal state is somebody else's fact and
	// is never repaired here.
	repair := run.State == domain.FlowStateCancelled
	if run.State.Terminal() && !repair {
		return nil
	}
	nodes, err := o.Store.ListNodes(publishCtx, runID)
	if err != nil {
		return err
	}
	targets := make([]cancelTarget, 0, len(nodes))
	cancelledChildRuns := []string{}
	for i := range nodes {
		childIDs := o.nodeChildIDs(nodes[i])
		cancelledChildRuns = append(cancelledChildRuns, childIDs...)
		switch {
		case nodes[i].TerminalState == domain.FlowNodeInterrupted && len(childIDs) > 0:
			// `interrupted` is what crash recovery writes when the child was NOT
			// confirmed terminal, so that child may still be running: it needs the
			// stop request even though the node's own state stays as it is (it is
			// the record of an attempt that never confirmed).
			targets = append(targets, cancelTarget{node: nodes[i], childIDs: childIDs, stopOnly: true})
		case nodes[i].TerminalState.Terminal():
			// A node that already reached a terminal fact keeps it (and its result);
			// the cancel only owns work that is still open. Its child Runs are still
			// listed in the payload: they are part of what the cancel covers.
		default:
			targets = append(targets, cancelTarget{node: nodes[i], childIDs: childIDs})
		}
	}
	outcome := o.cancelNodes(ctx, runID, targets)
	reason := "cancelled by user" + outcome.reasonSuffix()
	if repair {
		// Finish what the previous attempt did not: the node writes above are
		// idempotent CAS operations, and the anchor is re-terminated. The usage of
		// the children stopped above was accounted by the attempt that observed it;
		// adding it again here would count the same tokens twice.
		_ = o.Store.TerminateAnchor(publishCtx, runID, domain.RunCancelled, "flow_cancelled", run.TerminalReason)
		return nil
	}
	// The children this flow watched stop spent tokens that no fold recorded (the
	// ones the cancel settled from a real outcome already counted their own).
	accounted := o.accountCancelUsage(publishCtx, runID, outcome)
	total := run.TotalTokensUsed
	if accounted > total {
		total = accounted
	}
	if err := o.updateFlowStateKeepingUsage(publishCtx, runID, domain.FlowStateCancelled, total, reason); err != nil {
		// Commit before publish: a terminal event without its terminal row would
		// claim a state nobody can read. The nodes are already settled, so the next
		// call repairs them (idempotent CAS) and retries this write.
		return err
	}
	_ = o.Events.PublishFlow(publishCtx, runID, "flow_cancelled", map[string]any{
		"flowRunId": runID, "cancelledChildRuns": cancelledChildRuns,
		"cleanup": outcome.payload(),
	})
	_ = o.Store.TerminateAnchor(publishCtx, runID, domain.RunCancelled, "flow_cancelled", reason)
	return nil
}

// accountCancelUsage folds into the ledger the usage of every child this
// cancellation observed terminal and did not already settle through a fold - the
// same ChildUsage + AddTokenUsage the ordinary fold would have made. Empty or
// unreadable usage adds nothing and never fails the cancellation: losing a token
// count is not as bad as leaving the stop unrecorded. It returns the last durable
// total it saw, or 0 when it added nothing.
func (o *Orchestrator) accountCancelUsage(ctx context.Context, runID string, outcome cancelOutcome) int64 {
	total := int64(0)
	for _, childID := range outcome.ids {
		if !outcome.observed[childID].Terminal() || outcome.folded[childID] {
			continue
		}
		usage, err := o.Children.ChildUsage(ctx, childID)
		if err != nil || usage.Tokens == 0 {
			continue
		}
		updated, err := o.Store.AddTokenUsage(ctx, runID, usage.Tokens)
		if err != nil {
			continue
		}
		total = updated
	}
	return total
}

func (o *Orchestrator) isCancelled(ctx context.Context, runID string) bool {
	value, err := o.Store.IsCancelRequested(ctx, runID)
	return err == nil && value
}

// Lane constants: the active ready-set concurrency class. Readers (read-only
// Heroes) run in parallel up to parallelism.max; writers take the mutation
// lane. The two classes never overlap: readers and writers are dispatched in
// separate windows so a reader never sees a sibling writer's partial state.
const (
	laneNone   = -1
	laneReader = 0
	laneWriter = 1
)

// inflightTask is one dispatched hero child awaiting settlement. startedAt is
// the in-memory dispatch time the timeout measures from: it is deliberately not
// persisted (see attemptExpired for what a restart can and cannot recover).
type inflightTask struct {
	node      *domain.RunAgentFlowNode
	childID   string
	startedAt time.Time
}

// terminalGateTask returns the flow's single unlocked terminal task and whether
// the flow is ambiguous (two or more terminals unlocked at once). A terminal is
// unlocked when every one of its depends is satisfied, which includes a
// failed-but-optional dependency: that is how a flow with an optional failure
// still produces its outputs.
//
// Terminals are inspected in TaskOrder order, never in sorted handle order and
// never in map iteration order, so the flow's outputs never depend on which
// terminal some walk happened to reach first. Publish validation guarantees the
// terminals of a valid flow are mutually exclusive, so an unlocked pair means
// the graph is broken: the caller fails the flow loudly instead of silently
// picking one.
func terminalGateTask(def *domain.FlowDefinition, byHandle map[string]*domain.RunAgentFlowNode) (*domain.FlowTask, bool) {
	if def == nil {
		return nil, false
	}
	order, err := TaskOrder(def)
	if err != nil {
		return nil, false // cyclic/unorderable graph: no terminal gate, fail loud upstream
	}
	var unlocked []string
	for _, name := range order {
		task := def.Tasks[name]
		if task.Terminal == nil || uncompletedDep(task, byHandle) != "" {
			continue
		}
		unlocked = append(unlocked, name)
	}
	if len(unlocked) > 1 {
		return nil, true
	}
	if len(unlocked) == 0 {
		return nil, false
	}
	value := def.Tasks[unlocked[0]]
	return &value, false
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

// depSatisfied reports whether a checkpoint satisfies the dependency it is
// named by: it is completed, or it failed while declared `optional`. An
// optional task's failure is a recorded outcome, not a broken prerequisite, so
// its dependents are dispatchable and a terminal gate behind it unlocks.
//
// This is the ONE place that answers the question. Every readiness check
// (readyHeroTasks, the dispatch-time dependency gate, uncompletedDep and
// terminalGateTask) routes through it so the three cannot drift into "the
// terminal unlocks but nothing before it runs" or the reverse.
func depSatisfied(node *domain.RunAgentFlowNode) bool {
	if node == nil {
		return false
	}
	if node.TerminalState == domain.FlowNodeCompleted {
		return true
	}
	return node.Optional && node.TerminalState == domain.FlowNodeFailed
}

// allSettled reports whether every task is a satisfied dependency, i.e. the
// flow has nothing left to run: all tasks completed, or the only failures are
// optional ones (a pending node on an unselected branch is NOT settled; that
// case completes through the terminal gate instead).
func allSettled(nodes []*domain.RunAgentFlowNode) bool {
	for _, node := range nodes {
		if !depSatisfied(node) {
			return false
		}
	}
	return true
}

// uncompletedDep returns the first depends task that is not a satisfied
// dependency (see depSatisfied), or "" when every prerequisite is satisfied.
// byHandle must hold every checkpoint of the run, not only the completed ones:
// a failed optional dependency is satisfied without being completed.
func uncompletedDep(task domain.FlowTask, byHandle map[string]*domain.RunAgentFlowNode) string {
	for _, dep := range task.Depends {
		if !depSatisfied(byHandle[dep]) {
			return dep
		}
	}
	return ""
}

// now reads the orchestrator clock. Tests replace it through Now; a nil Now
// (an orchestrator driven without Start) falls back to the wall clock rather
// than measuring every timeout against the zero time.
func (o *Orchestrator) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// taskTimeoutMS reads a task's declared wall-clock ceiling for one attempt, or
// 0 when it declares none. The definition is the source: timeout is not frozen
// onto the node (only `optional`, which readiness needs, is).
func taskTimeoutMS(def *domain.FlowDefinition, handle string) time.Duration {
	if def == nil {
		return 0
	}
	task, ok := def.Tasks[handle]
	if !ok || task.TimeoutMS <= 0 {
		return 0
	}
	return time.Duration(task.TimeoutMS) * time.Millisecond
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
			// A reset node must not keep the previous round's verdict; the
			// verdict is part of the checkpoint, not of the frozen specs.
			// The reset must un-record the failure code as well: a node left with
			// error_code=acceptance_failed and a cleared verdict would make the
			// run loop fail a flow whose task is about to be retried.
			_ = o.Store.UpdateNode(ctx, runID, NodeUpdate{
				TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{node.TerminalState},
				SetState: domain.FlowNodePending, AcceptanceVerdict: clearedAcceptanceVerdict(),
				ClearErrorCode: true,
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
			// Typed, so the caller can ask WHICH task has no output instead of
			// matching on a message: a producer that failed while optional is a
			// named diagnosis, not a generic resolution error.
			return "", &missingTaskOutputError{task: task}
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

// missingTaskOutputError reports a goal reference to a task that has no recorded
// output. Its message is the historical one (the status is what an author needs
// to hear); the type is what lets the orchestrator name an optional producer.
type missingTaskOutputError struct{ task string }

func (e *missingTaskOutputError) Error() string {
	return fmt.Sprintf("task %q output is not available (dependency not completed)", e.task)
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
