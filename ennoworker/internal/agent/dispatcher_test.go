package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeParallelStartsAdjacentReadOnlyCallsTogether(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	tools := &fakeTools{
		classes: map[string]domain.ExecutionClass{"read": domain.ExecutionReadOnly},
		execute: func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
			entered <- call.ID
			<-release
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: call.ID}, nil
		},
	}
	loop := &Loop{Tools: tools, Events: &memoryWriter{}, ToolExecution: domain.ToolExecutionConfig{
		Mode: "safe_parallel", MaxConcurrentReadTools: 2,
	}}
	done := make(chan struct{})
	var results []domain.ToolResult
	var runErr error
	go func() {
		results, runErr = loop.executeToolBatch(context.Background(), "parallel", 1, []domain.ToolCall{
			{ID: "first", Name: "read"}, {ID: "second", Name: "read"},
		})
		close(done)
	}()
	started := map[string]bool{receiveWithin(t, entered): true, receiveWithin(t, entered): true}
	assert.Equal(t, map[string]bool{"first": true, "second": true}, started)
	close(release)
	waitWithin(t, done)
	require.NoError(t, runErr)
	require.Len(t, results, 2)
	assert.Equal(t, "first", results[0].Content)
	assert.Equal(t, "second", results[1].Content)
}

func TestSafeParallelHonorsReadConcurrencyLimit(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var current, maximum atomic.Int32
	tools := &fakeTools{
		classes: map[string]domain.ExecutionClass{"read": domain.ExecutionReadOnly},
		execute: func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
			value := current.Add(1)
			for {
				old := maximum.Load()
				if value <= old || maximum.CompareAndSwap(old, value) {
					break
				}
			}
			entered <- struct{}{}
			<-release
			current.Add(-1)
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name}, nil
		},
	}
	loop := &Loop{Tools: tools, Events: &memoryWriter{}, ToolExecution: domain.ToolExecutionConfig{
		Mode: "safe_parallel", MaxConcurrentReadTools: 2,
	}}
	done := make(chan struct{})
	go func() {
		_, _ = loop.executeToolBatch(context.Background(), "limit", 1, []domain.ToolCall{
			{ID: "1", Name: "read"}, {ID: "2", Name: "read"}, {ID: "3", Name: "read"}, {ID: "4", Name: "read"},
		})
		close(done)
	}()
	receiveSignalWithin(t, entered)
	receiveSignalWithin(t, entered)
	assert.Equal(t, int32(2), maximum.Load())
	close(release)
	waitWithin(t, done)
	assert.Equal(t, int32(2), maximum.Load())
}

func TestSafeParallelKeepsExclusiveBarriersInCallOrder(t *testing.T) {
	entered := make(chan string, 3)
	releases := map[string]chan struct{}{
		"read-before": make(chan struct{}), "write": make(chan struct{}), "read-after": make(chan struct{}),
	}
	tools := &fakeTools{
		classes: map[string]domain.ExecutionClass{
			"read": domain.ExecutionReadOnly, "write": domain.ExecutionWorkspaceWrite,
		},
		execute: func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
			entered <- call.ID
			<-releases[call.ID]
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: call.ID}, nil
		},
	}
	loop := &Loop{Tools: tools, Events: &memoryWriter{}, ToolExecution: domain.ToolExecutionConfig{Mode: "safe_parallel", MaxConcurrentReadTools: 4}}
	done := make(chan struct{})
	go func() {
		_, _ = loop.executeToolBatch(context.Background(), "barrier", 1, []domain.ToolCall{
			{ID: "read-before", Name: "read"}, {ID: "write", Name: "write"}, {ID: "read-after", Name: "read"},
		})
		close(done)
	}()
	assert.Equal(t, "read-before", receiveWithin(t, entered))
	assertNoSignal(t, entered)
	close(releases["read-before"])
	assert.Equal(t, "write", receiveWithin(t, entered))
	assertNoSignal(t, entered)
	close(releases["write"])
	assert.Equal(t, "read-after", receiveWithin(t, entered))
	close(releases["read-after"])
	waitWithin(t, done)
}

func TestSafeParallelNeverOverlapsWorkspaceWritesOrBash(t *testing.T) {
	var current, maximum atomic.Int32
	tools := &fakeTools{
		classes: map[string]domain.ExecutionClass{
			"write": domain.ExecutionWorkspaceWrite, "bash": domain.ExecutionExclusive,
		},
		execute: func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
			active := current.Add(1)
			for {
				old := maximum.Load()
				if active <= old || maximum.CompareAndSwap(old, active) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			current.Add(-1)
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name}, nil
		},
	}
	loop := &Loop{Tools: tools, Events: &memoryWriter{}, ToolExecution: domain.ToolExecutionConfig{
		Mode: "safe_parallel", MaxConcurrentReadTools: 4,
	}}
	_, err := loop.executeToolBatch(context.Background(), "exclusive", 1, []domain.ToolCall{
		{ID: "write-1", Name: "write"}, {ID: "write-2", Name: "write"}, {ID: "bash", Name: "bash"},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), maximum.Load())
}

func TestSafeParallelDrainsAllWorkersAfterExternalCancellation(t *testing.T) {
	entered := make(chan struct{}, 4)
	var started, exited, active atomic.Int32
	tools := &fakeTools{
		classes: map[string]domain.ExecutionClass{"read": domain.ExecutionReadOnly},
		execute: func(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
			started.Add(1)
			active.Add(1)
			entered <- struct{}{}
			<-ctx.Done()
			active.Add(-1)
			exited.Add(1)
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, IsError: true}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	loop := &Loop{Tools: tools, Events: &memoryWriter{}, ToolExecution: domain.ToolExecutionConfig{
		Mode: "safe_parallel", MaxConcurrentReadTools: 2,
	}}
	done := make(chan error, 1)
	go func() {
		_, err := loop.executeToolBatch(ctx, "cancel", 1, []domain.ToolCall{
			{ID: "1", Name: "read"}, {ID: "2", Name: "read"}, {ID: "3", Name: "read"}, {ID: "4", Name: "read"},
		})
		done <- err
	}()
	receiveSignalWithin(t, entered)
	receiveSignalWithin(t, entered)
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("parallel tool batch did not drain after cancellation")
	}
	assert.Zero(t, active.Load())
	assert.Equal(t, started.Load(), exited.Load())
}

func TestSafeParallelDoesNotCancelSiblingOnToolError(t *testing.T) {
	tools := &fakeTools{
		classes: map[string]domain.ExecutionClass{"read": domain.ExecutionReadOnly},
		execute: func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: call.ID, IsError: call.ID == "bad"}, nil
		},
	}
	loop := &Loop{Tools: tools, Events: &memoryWriter{}, ToolExecution: domain.ToolExecutionConfig{Mode: "safe_parallel", MaxConcurrentReadTools: 2}}
	results, err := loop.executeToolBatch(context.Background(), "errors", 1, []domain.ToolCall{
		{ID: "bad", Name: "read"}, {ID: "good", Name: "read"},
	})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "good", results[1].Content)
}

func TestSafeParallelCancelsAndDrainsWorkersAfterEventFailure(t *testing.T) {
	tools := &fakeTools{
		classes: map[string]domain.ExecutionClass{"read": domain.ExecutionReadOnly},
		execute: func(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
			if call.ID == "first" {
				return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name}, nil
			}
			<-ctx.Done()
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: ctx.Err().Error(), IsError: true}, nil
		},
	}
	writer := &memoryWriter{failTypes: map[string]error{"tool_call_completed": errors.New("disk full")}}
	loop := &Loop{Tools: tools, Events: writer, ToolExecution: domain.ToolExecutionConfig{Mode: "safe_parallel", MaxConcurrentReadTools: 2}}
	_, err := loop.executeToolBatch(context.Background(), "event-failure", 1, []domain.ToolCall{
		{ID: "first", Name: "read"}, {ID: "second", Name: "read"},
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorEventPersistence, domain.ErrorCodeOf(err))
}

func TestSafeParallelEventFailureDoesNotDrainSteering(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		StopReason: domain.StopReasonToolCalls, ToolCalls: []domain.ToolCall{
			{ID: "first", Name: "read", Arguments: []byte(`{}`)},
			{ID: "second", Name: "read", Arguments: []byte(`{}`)},
		},
	}})
	tools := &fakeTools{classes: map[string]domain.ExecutionClass{"read": domain.ExecutionReadOnly}}
	writer := &memoryWriter{failTypes: map[string]error{"tool_call_completed": errors.New("disk full")}}
	queue := &scriptedQueue{steerAt: 2}
	loop := &Loop{Provider: provider, Tools: tools, Events: writer, QueuedInputs: queue,
		ToolExecution: domain.ToolExecutionConfig{Mode: "safe_parallel", MaxConcurrentReadTools: 2}}
	_, err := loop.Run(context.Background(), RunInput{RunID: "event-no-steer", Model: "m"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorEventPersistence, domain.ErrorCodeOf(err))
	assert.Equal(t, 1, queue.steerCalls, "only the initial pre-model steer drain is allowed")
}

func receiveWithin(t *testing.T, channel <-chan string) string {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for value")
		return ""
	}
}

func receiveSignalWithin(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func waitWithin(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completion")
	}
}

func assertNoSignal(t *testing.T, channel <-chan string) {
	t.Helper()
	select {
	case value := <-channel:
		t.Fatalf("unexpected execution before barrier: %s", value)
	case <-time.After(20 * time.Millisecond):
	}
}
