package runs

import (
	"context"
	"errors"
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

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

type Coordinator struct {
	lifecycle Lifecycle
	executor  Executor
	semaphore chan struct{}

	mu     sync.Mutex
	active map[string]*activeRun
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

	output, err := c.executor.Execute(ctx, run)
	switch {
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		state.err = c.lifecycle.Cancel(context.Background(), runID)
	case err != nil:
		code := domain.ErrorCodeOf(err)
		state.err = c.lifecycle.Fail(context.Background(), runID, string(code), err.Error())
	case output.Suspended:
		return
	default:
		var finalizeErr error
		if finalizer, ok := c.lifecycle.(successfulRunFinalizer); ok && len(output.Messages) > 0 {
			finalizeErr = finalizer.FinalizeSuccess(context.Background(), runID, output)
		} else {
			finalizeErr = c.lifecycle.Succeed(context.Background(), runID)
		}
		if finalizeErr != nil {
			state.err = c.lifecycle.Fail(context.Background(), runID,
				string(domain.ErrorEventPersistence), finalizeErr.Error())
		}
	}
}

func (c *Coordinator) Cancel(ctx context.Context, runID string) error {
	c.mu.Lock()
	state := c.active[runID]
	if state != nil {
		state.cancel()
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
