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

type childRunResolver interface {
	OwnedChildIDs(context.Context, string) ([]string, error)
}

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

type Coordinator struct {
	lifecycle Lifecycle
	executor  Executor
	semaphore chan struct{}

	mu           sync.Mutex
	active       map[string]*activeRun
	onRunSettled func(context.Context, *domain.AgentRun) error
}

func NewCoordinator(lifecycle Lifecycle, executor Executor, maxConcurrent int) *Coordinator {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Coordinator{
		lifecycle: lifecycle,
		executor:  executor,
		semaphore: make(chan struct{}, maxConcurrent),
		active:    make(map[string]*activeRun),
	}
}

// SetRunSettledHook installs a post-terminal callback used for durable work
// such as session auto-resume. The callback runs after the terminal DB commit.
func (c *Coordinator) SetRunSettledHook(hook func(context.Context, *domain.AgentRun) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onRunSettled = hook
}

func (c *Coordinator) Enqueue(parent context.Context, runID string) error {
	c.mu.Lock()
	if _, exists := c.active[runID]; exists {
		c.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	state := &activeRun{cancel: cancel, done: make(chan struct{})}
	c.active[runID] = state
	c.mu.Unlock()

	go c.execute(ctx, runID, state)
	return nil
}

func (c *Coordinator) execute(ctx context.Context, runID string, state *activeRun) {
	defer func() {
		state.cancel()
		c.mu.Lock()
		delete(c.active, runID)
		c.mu.Unlock()
		close(state.done)
	}()

	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		state.err = c.lifecycle.Cancel(context.Background(), runID)
		return
	}

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

	output, err := c.executor.Execute(ctx, run)
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
	if err := c.Enqueue(context.Background(), parentID); err != nil && !errors.Is(err, store.ErrRunNotFound) {
		return err
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
