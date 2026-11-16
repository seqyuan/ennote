package runs

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

type Lifecycle interface {
	Claim(context.Context, string) (*domain.AgentRun, error)
	Succeed(context.Context, string) error
	Fail(context.Context, string, string, string) error
	Cancel(context.Context, string) error
	Get(context.Context, string) (*domain.AgentRun, error)
}

type Executor interface {
	Execute(context.Context, *domain.AgentRun) (domain.RunOutput, error)
}

type ExecutorFunc func(context.Context, *domain.AgentRun) error

func (f ExecutorFunc) Execute(ctx context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
	return domain.RunOutput{}, f(ctx, run)
}

type successfulRunFinalizer interface {
	FinalizeSuccess(context.Context, string, domain.RunOutput) error
}

// childRunFinalizer terminalizes a delegated_agent Run with its structured
// submit_result contract and wakes the parent group when it settles.
type childRunFinalizer interface {
	FinalizeChildSuccess(context.Context, string, domain.RunOutput) error
	FinalizeChildFailure(context.Context, string, string, string) (string, bool, error)
}

// parentResolver finds a run's parent id (empty for top-level runs).
type parentResolver interface {
	ParentOfRun(context.Context, string) (string, error)
}

// successorResolver finds the delegated child Runs of a dynamic task graph
// whose dependencies have all settled successfully and are ready to start.
// It is the DAG wake channel: when a child settles, the coordinator enqueues
// its ready successors (and lifts blocked successors whose dependency retry
// succeeded).
type successorResolver interface {
	ReadySuccessorRuns(context.Context, string) ([]string, error)
}

type childRunResolver interface {
	OwnedChildIDs(context.Context, string) ([]string, error)
}

// RunClass is who a Run is for, and it decides which permits the Run needs.
//
// The coordinator holds MaxConcurrentRuns global permits plus max(1,
// MaxConcurrentRuns-1) batch permits, and a batch Run must hold a batch permit
// before it queues for a global one. The invariant that buys is narrower than
// "the person always runs next", and worth stating exactly: with more than one
// global permit, batch can never hold every one of them, so batch work (a Graph,
// a wave of delegated children) cannot make the person's next Run wait for batch
// work to finish. An interactive Run therefore starts as soon as a global permit
// is free — immediately, when every running Run is batch. With a single global
// permit there is nothing to reserve: the classes are simply serialised.
//
// It is not a priority queue. A batch Run already parked on the global semaphore
// can win the permit a finishing Run releases ahead of an interactive Run that
// arrives a moment later: the semaphore is a plain channel, and a blocked sender
// is served before a new arrival. Closing that gap needs an interactive-pending
// counter that parked batch waiters respect; it is a follow-up, deliberately not
// part of this change.
//
// The values are read by cmd/ennoworker when it classifies a Graph child, and
// agentflow mirrors them, so do not change a value without both.
type RunClass string

const (
	// ClassInteractive is a person's turn, or a Run they asked for.
	ClassInteractive RunClass = "interactive"
	// ClassBatch is a Graph task child or a delegated child Run: work the person
	// started but is not waiting on turn by turn.
	ClassBatch RunClass = "batch"
)

// ClassifyRun decides a Run's class from its own persisted facts, and it is the
// one rule for every site that re-schedules a Run it did not create: a person's
// own turn is an agent Run with no parent, and everything else is work that turn
// started.
//
// Classify by the Run, never by who woke it. A settled delegated child wakes its
// parent, and that parent is usually the person's own turn that yielded waiting
// on its children — the same wake-up reaches either class, so the wake-up itself
// says nothing about it.
//
// The rule is deliberately strict: only an unparented agent Run is interactive,
// so a kind this pair cannot vouch for (a context compaction, say) is throttled
// instead of being admitted to the reserved slot. The paths that know the Run is
// the person's — submit, retry, compaction, invocation — name ClassInteractive
// themselves rather than asking this function to guess.
func ClassifyRun(kind domain.RunKind, parentRunID string) RunClass {
	if kind == domain.RunKindAgent && parentRunID == "" {
		return ClassInteractive
	}
	return ClassBatch
}

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

type Coordinator struct {
	lifecycle Lifecycle
	executor  Executor
	// semaphore is the global concurrency ceiling: every running Run holds one
	// permit whatever its class.
	semaphore chan struct{}
	// batchSemaphore bounds batch work below that ceiling. It is structural, not
	// a poll: a batch Run must hold a batch permit before it queues for a global
	// one, so the slot the reservation guarantees is out of batch's reach. With
	// one global permit there is no slot to reserve and it is a plain throttle.
	batchSemaphore chan struct{}

	mu           sync.Mutex
	active       map[string]*activeRun
	onRunSettled func(context.Context, *domain.AgentRun) error
}

func NewCoordinator(lifecycle Lifecycle, executor Executor, maxConcurrent int) *Coordinator {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	// MaxConcurrentRuns-1 reserved for interactive, but never zero: with one
	// global permit there is no slot to reserve, and a zero-size batchSemaphore
	// would wedge every batch Run forever.
	batchSlots := maxConcurrent - 1
	if batchSlots < 1 {
		batchSlots = 1
	}
	return &Coordinator{
		lifecycle:      lifecycle,
		executor:       executor,
		semaphore:      make(chan struct{}, maxConcurrent),
		batchSemaphore: make(chan struct{}, batchSlots),
		active:         make(map[string]*activeRun),
	}
}

// SetRunSettledHook installs a post-terminal callback used for durable work
// such as session auto-resume. The callback runs after the terminal DB commit.
func (c *Coordinator) SetRunSettledHook(hook func(context.Context, *domain.AgentRun) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onRunSettled = hook
}

// Enqueue schedules a Run a person is waiting on. It is EnqueueWithClass with
// ClassInteractive, which is what every caller meant before RunClass existed.
func (c *Coordinator) Enqueue(parent context.Context, runID string) error {
	return c.EnqueueWithClass(parent, runID, ClassInteractive)
}

// EnqueueWithClass schedules one Run in its class. An already-enqueued run id is
// a no-op whatever class it names: dedupe is per run id, and a Run's class is
// fixed at scheduling time rather than mutated under a running goroutine.
func (c *Coordinator) EnqueueWithClass(parent context.Context, runID string, class RunClass) error {
	c.mu.Lock()
	if _, exists := c.active[runID]; exists {
		c.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	state := &activeRun{cancel: cancel, done: make(chan struct{})}
	c.active[runID] = state
	c.mu.Unlock()

	go c.execute(ctx, runID, state, class)
	return nil
}

// acquireSlot blocks until this class of Run may execute and returns the
// release for the permits it holds. Only ClassInteractive is exempt from the
// batch reservation: any other value is throttled, so a Run this code does not
// recognise is treated as batch rather than being let into the reserved slot.
//
// The batch permit is taken BEFORE the global one by design. A batch Run that
// held a global permit while queueing for a batch permit would block the very
// interactive Run the reservation exists to let through.
func (c *Coordinator) acquireSlot(ctx context.Context, class RunClass) (func(), bool) {
	batch := class != ClassInteractive
	if batch {
		select {
		case c.batchSemaphore <- struct{}{}:
		case <-ctx.Done():
			return nil, false
		}
	}
	select {
	case c.semaphore <- struct{}{}:
	case <-ctx.Done():
		if batch {
			<-c.batchSemaphore
		}
		return nil, false
	}
	return func() {
		<-c.semaphore
		if batch {
			<-c.batchSemaphore
		}
	}, true
}

func (c *Coordinator) execute(ctx context.Context, runID string, state *activeRun, class RunClass) {
	defer func() {
		state.cancel()
		c.mu.Lock()
		delete(c.active, runID)
		c.mu.Unlock()
		close(state.done)
	}()

	release, acquired := c.acquireSlot(ctx, class)
	if !acquired {
		// Cancelled while queued: the Run never started, so it terminalises
		// cancelled exactly as it did when one semaphore gated both classes.
		state.err = c.lifecycle.Cancel(context.Background(), runID)
		return
	}
	defer release()

	run, err := c.lifecycle.Claim(ctx, runID)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			state.err = c.lifecycle.Cancel(context.Background(), runID)
		} else {
			state.err = err
		}
		return
	}
	defer func() {
		if hookErr := c.notifyRunSettled(run); state.err == nil && hookErr != nil {
			state.err = hookErr
		}
	}()

	output, err := c.runExecutor(ctx, run)
	switch {
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		state.err = c.lifecycle.Cancel(context.Background(), runID)
	case err != nil:
		code := domain.ErrorCodeOf(err)
		if run.RunKind == domain.RunKindDelegatedAgent {
			if finalizer, ok := c.lifecycle.(childRunFinalizer); ok {
				parentID, wakeParent, finalizeErr := finalizer.FinalizeChildFailure(
					context.Background(), runID, string(code), err.Error())
				state.err = finalizeErr
				if finalizeErr == nil && wakeParent {
					state.err = c.enqueueSettledParent(parentID)
				}
				if finalizeErr == nil {
					state.err = c.enqueueReadySuccessors(runID)
				}
				return
			}
		}
		state.err = c.lifecycle.Fail(context.Background(), runID, string(code), err.Error())
	case output.Suspended:
		return
	case output.Waiting:
		// Run deliberately yielded (e.g. waiting for delegated children).
		// Do not finalize or fail; the run is waiting_children in DB.
		return
	default:
		var finalizeErr error
		if output.Terminal != nil {
			if childFinalizer, ok := c.lifecycle.(childRunFinalizer); ok {
				finalizeErr = childFinalizer.FinalizeChildSuccess(context.Background(), runID, output)
			} else {
				finalizeErr = fmt.Errorf("child finalizer unavailable")
			}
			if finalizeErr == nil {
				// Only wake the parent when child finalization settled the group and
				// atomically moved the parent back to queued.
				if resolver, ok := c.lifecycle.(parentResolver); ok {
					if parentID, parentErr := resolver.ParentOfRun(context.Background(), runID); parentErr == nil {
						finalizeErr = c.enqueueSettledParent(parentID)
					}
				}
				// Wake ready dependent tasks of this task graph (idempotent).
				successorErr := c.enqueueReadySuccessors(runID)
				if finalizeErr == nil {
					finalizeErr = successorErr
				}
			}
		} else if finalizer, ok := c.lifecycle.(successfulRunFinalizer); ok && len(output.Messages) > 0 {
			finalizeErr = finalizer.FinalizeSuccess(context.Background(), runID, output)
		} else {
			finalizeErr = c.lifecycle.Succeed(context.Background(), runID)
		}
		if finalizeErr != nil {
			if run.RunKind == domain.RunKindDelegatedAgent {
				if finalizer, ok := c.lifecycle.(childRunFinalizer); ok {
					parentID, wakeParent, childErr := finalizer.FinalizeChildFailure(context.Background(), runID,
						string(domain.ErrorEventPersistence), finalizeErr.Error())
					state.err = childErr
					if childErr == nil && wakeParent {
						state.err = c.enqueueSettledParent(parentID)
					}
					return
				}
			}
			state.err = c.lifecycle.Fail(context.Background(), runID,
				string(domain.ErrorEventPersistence), finalizeErr.Error())
		}
	}
}

// runExecutor runs the executor and turns a panic into an error. Without the
// recover, a panicking executor kills the goroutine and the whole process with
// it: the Run never settles and nothing else on the Worker can report why.
func (c *Coordinator) runExecutor(ctx context.Context, run *domain.AgentRun) (output domain.RunOutput, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			output = domain.RunOutput{}
			err = fmt.Errorf("run executor panic: %v", recovered)
		}
	}()
	return c.executor.Execute(ctx, run)
}

func (c *Coordinator) notifyRunSettled(run *domain.AgentRun) error {
	current, err := c.lifecycle.Get(context.Background(), run.ID)
	if err != nil || !current.Status.Terminal() {
		return err
	}
	c.mu.Lock()
	hook := c.onRunSettled
	c.mu.Unlock()
	if hook == nil {
		return nil
	}
	return hook(context.Background(), current)
}

func (c *Coordinator) enqueueSettledParent(parentID string) error {
	if parentID == "" {
		return nil
	}
	parentRun, err := c.lifecycle.Get(context.Background(), parentID)
	if err != nil {
		return err
	}
	if parentRun.Status != domain.RunQueued {
		return nil
	}
	// Classify the parent by its own persisted facts, never by the fact that a
	// child woke it: the parent of a delegated child is often the person's own
	// turn, which yielded waiting on its children and must resume as interactive.
	if err := c.EnqueueWithClass(context.Background(), parentID, ClassifyRun(parentRun.RunKind, parentRun.ParentRunID)); err != nil && !errors.Is(err, store.ErrRunNotFound) {
		return err
	}
	return nil
}

// enqueueReadySuccessors wakes the dependent tasks of a settled task-graph
// child whose dependencies have all succeeded. It is idempotent: ReadySuccessorRuns
// re-evaluates the group against latest attempt states, so it is safe to call
// after every child settle and after startup recovery.
func (c *Coordinator) enqueueReadySuccessors(childRunID string) error {
	resolver, ok := c.lifecycle.(successorResolver)
	if !ok {
		return nil
	}
	ready, err := resolver.ReadySuccessorRuns(context.Background(), childRunID)
	if err != nil {
		return err
	}
	for _, successor := range ready {
		// A task-graph successor is one more child of a Graph the person already
		// started: batch, never the reserved interactive slot.
		if err := c.EnqueueWithClass(context.Background(), successor, ClassBatch); err != nil && !errors.Is(err, store.ErrRunNotFound) {
			return err
		}
	}
	return nil
}

func (c *Coordinator) Cancel(ctx context.Context, runID string) error {
	ids := []string{runID}
	if resolver, ok := c.lifecycle.(childRunResolver); ok {
		children, err := resolver.OwnedChildIDs(ctx, runID)
		if err != nil {
			return err
		}
		ids = append(ids, children...)
	}
	c.mu.Lock()
	for _, id := range ids {
		if state := c.active[id]; state != nil {
			state.cancel()
		}
	}
	c.mu.Unlock()

	run, err := c.lifecycle.Get(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status.Terminal() {
		return nil
	}
	if err := c.lifecycle.Cancel(ctx, runID); err != nil && !errors.Is(err, store.ErrInvalidRunState) {
		return err
	}
	return nil
}

func (c *Coordinator) Wait(ctx context.Context, runID string) error {
	c.mu.Lock()
	state := c.active[runID]
	c.mu.Unlock()
	if state == nil {
		return nil
	}
	select {
	case <-state.done:
		return state.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) ActiveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.active)
}
