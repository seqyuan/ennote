package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tool retry is a safety boundary, not a convenience: a retried tool runs twice
// against the same world. These tests pin who is allowed to retry, on what kind
// of error, and how many times — the three things a refactor can silently widen.

// retryFakeTools declares one tool's policy and answers with a scripted sequence
// of execution results, counting every attempt.
type retryFakeTools struct {
	fakeTools
	policy   domain.ToolRetryPolicy
	script   []error // per attempt; the last entry repeats
	before   func()  // runs at the start of each attempt
	attempts atomic.Int32
}

func (t *retryFakeTools) RetryPolicy(name string) domain.ToolRetryPolicy {
	return t.policy
}

func (t *retryFakeTools) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	attempt := int(t.attempts.Add(1))
	if t.before != nil {
		t.before()
	}
	index := attempt - 1
	if index >= len(t.script) {
		index = len(t.script) - 1
	}
	if err := t.script[index]; err != nil {
		return domain.ToolResult{}, err
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "ok"}, nil
}

func callOnce(t *testing.T, tools domain.ToolRunner, configRetries int) domain.ToolExecutionOutcome {
	t.Helper()
	loop := &Loop{
		Tools:         tools,
		ToolExecution: domain.ToolExecutionConfig{Mode: "sequential", MaxToolRetries: configRetries},
	}
	call := domain.ToolCall{ID: "call-1", Name: "read", Arguments: []byte(`{}`)}
	return loop.executeToolAttempts(context.Background(), "run-1", call)
}

func transientFailure(message string) error {
	return &domain.TransientToolError{Err: errors.New(message)}
}

func TestToolRetryRunsOnceForAToolThatNeverOptsIn(t *testing.T) {
	tools := &retryFakeTools{
		policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryNever},
		script: []error{transientFailure("connection reset")},
	}

	outcome := callOnce(t, tools, 5)

	assert.Equal(t, int32(1), tools.attempts.Load(), "a tool that does not opt in must not be retried")
	assert.Equal(t, domain.ToolInfrastructureFailed, outcome.Kind)
	assert.Equal(t, 1, outcome.AttemptCount)
}

func TestToolRetryStopsAtTheToolsOwnCap(t *testing.T) {
	tools := &retryFakeTools{
		policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 2},
		script: []error{transientFailure("busy")},
	}

	outcome := callOnce(t, tools, 9)

	// 1 original attempt + 2 retries, even though the run allowed 9.
	assert.Equal(t, int32(3), tools.attempts.Load())
	assert.Equal(t, 3, outcome.AttemptCount)
	assert.Equal(t, domain.ToolInfrastructureFailed, outcome.Kind)
}

func TestToolRetryUsesTheRunCapWhenItIsTighter(t *testing.T) {
	tools := &retryFakeTools{
		policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 8},
		script: []error{transientFailure("busy")},
	}

	callOnce(t, tools, 1)

	assert.Equal(t, int32(2), tools.attempts.Load(), "the run's cap wins when it is the smaller one")
}

func TestToolRetrySucceedsAfterATransientFailure(t *testing.T) {
	tools := &retryFakeTools{
		policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 2},
		script: []error{transientFailure("connection reset"), nil},
	}

	outcome := callOnce(t, tools, 2)

	assert.Equal(t, int32(2), tools.attempts.Load())
	assert.Equal(t, domain.ToolReturned, outcome.Kind)
	assert.Equal(t, "ok", outcome.Result.Content)
	assert.False(t, outcome.Result.IsError)
}

func TestToolRetryNeverRetriesATerminalError(t *testing.T) {
	// A deadline that already expired is the terminal case the domain classifies.
	terminal := fmt.Errorf("read timed out: %w", context.DeadlineExceeded)
	tools := &retryFakeTools{
		policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 3},
		script: []error{terminal},
	}

	outcome := callOnce(t, tools, 3)

	assert.Equal(t, int32(1), tools.attempts.Load(), "a terminal error is the tool's answer, not noise")
	assert.Equal(t, domain.ToolInfrastructureFailed, outcome.Kind)
}

func TestToolRetryNeverRetriesANonTransientError(t *testing.T) {
	tools := &retryFakeTools{
		policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 3},
		script: []error{errors.New("the arguments do not parse")},
	}

	callOnce(t, tools, 3)

	assert.Equal(t, int32(1), tools.attempts.Load())
}

func TestToolRetryGivesUpWhenTheContextIsCancelled(t *testing.T) {
	tools := &retryFakeTools{
		policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 5},
		script: []error{transientFailure("busy")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	loop := &Loop{Tools: tools, ToolExecution: domain.ToolExecutionConfig{MaxToolRetries: 5}}
	call := domain.ToolCall{ID: "call-1", Name: "read", Arguments: []byte(`{}`)}

	// Cancel from inside the first attempt: the loop must not start another one.
	tools.before = cancel
	outcome := loop.executeToolAttempts(ctx, "run-1", call)
	require.Error(t, ctx.Err())

	assert.Equal(t, domain.ToolCancelled, outcome.Kind)
	assert.Equal(t, int32(1), tools.attempts.Load())
}
