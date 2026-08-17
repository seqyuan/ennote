package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopRetriesOnlyBeforeDurableStreamEvents(t *testing.T) {
	retryable := func(status int) error {
		return &llm.ProviderError{StatusCode: status, Code: "busy", Message: "busy", Retryable: true}
	}
	provider := llm.NewFakeProvider(
		llm.FakeStep{Err: retryable(429)},
		llm.FakeStep{Err: retryable(503)},
		llm.FakeStep{TextDeltas: []string{"done"}, Completion: domain.Completion{
			Content: []domain.ContentBlock{textBlock("done")}, StopReason: domain.StopReasonStop,
		}},
	)
	writer := &memoryWriter{}
	var sleeps []time.Duration
	loop := &Loop{
		Provider: provider, Tools: &fakeTools{}, Events: writer,
		Retry: RetryPolicy{Delays: []time.Duration{time.Second, 5 * time.Second}, Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		}},
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "retry", Model: "real-model"})
	require.NoError(t, err)
	assert.Len(t, provider.Requests, 3)
	assert.Equal(t, []time.Duration{time.Second, 5 * time.Second}, sleeps)
	assert.Equal(t, 2, countEvent(writer.types(), "model_call_attempt_failed"))
	assert.Equal(t, 2, countEvent(writer.types(), "model_call_retry_scheduled"))
	assert.Equal(t, 1, countEvent(writer.types(), "text_delta"))
}

func TestLoopDoesNotRetryAfterTextOrUsageWasCommitted(t *testing.T) {
	providerError := &llm.ProviderError{StatusCode: 503, Code: "busy", Message: "busy", Retryable: true}
	tests := []struct {
		name string
		step llm.FakeStep
	}{
		{name: "text", step: llm.FakeStep{TextDeltas: []string{"partial"}, Err: providerError}},
		{name: "thinking", step: llm.FakeStep{ThinkingDeltas: []string{"partial reasoning"}, Err: providerError}},
		{name: "usage", step: llm.FakeStep{Completion: domain.Completion{Usage: domain.Usage{UncachedInputTokens: 1}}, Err: providerError}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := llm.NewFakeProvider(test.step, llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop}})
			writer := &memoryWriter{}
			loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: writer,
				Retry: RetryPolicy{Delays: []time.Duration{0}, Sleep: func(context.Context, time.Duration) error { return nil }}}
			_, err := loop.Run(context.Background(), RunInput{RunID: "committed-" + test.name, Model: "m"})
			require.Error(t, err)
			assert.Len(t, provider.Requests, 1)
			assert.NotContains(t, writer.types(), "model_call_retry_scheduled")
		})
	}
}

func TestLoopRetriesIncompleteStreamOnlyWhenNoToolFragmentWasCommitted(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Err: llm.ErrIncompleteStream},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop}},
	)
	loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: &memoryWriter{},
		Retry: RetryPolicy{Delays: []time.Duration{0}, Sleep: func(context.Context, time.Duration) error { return nil }}}
	_, err := loop.Run(context.Background(), RunInput{RunID: "incomplete", Model: "m"})
	require.NoError(t, err)
	assert.Len(t, provider.Requests, 2)

	var attempts atomic.Int32
	fragmentProvider := providerFunc(func(_ context.Context, _ domain.CompletionRequest, sink llm.StreamSink) (domain.Completion, error) {
		attempts.Add(1)
		require.NoError(t, sink.ToolCallDelta(llm.ToolCallDelta{Index: 0, ID: "call", Name: "read", ArgumentsFragment: `{"path":`}))
		return domain.Completion{}, llm.ErrIncompleteStream
	})
	_, err = (&Loop{Provider: fragmentProvider, Tools: &fakeTools{}, Events: &memoryWriter{},
		Retry: RetryPolicy{Delays: []time.Duration{0}, Sleep: func(context.Context, time.Duration) error { return nil }}}).
		Run(context.Background(), RunInput{RunID: "fragment", Model: "m"})
	require.Error(t, err)
	assert.Equal(t, int32(1), attempts.Load())
}

func TestModelErrorCodeUsesSharedProviderClassification(t *testing.T) {
	tests := []struct {
		err  error
		code domain.ErrorCode
	}{
		{&llm.ProviderError{StatusCode: 401, Code: "invalid_api_key"}, domain.ErrorProviderAuthentication},
		{&llm.ProviderError{StatusCode: 400, Code: "model_not_found"}, domain.ErrorProviderModelNotFound},
		{&llm.ProviderError{StatusCode: 429, Code: "rate_limit"}, domain.ErrorProviderRateLimited},
		{&llm.ProviderError{StatusCode: 408, Code: "timeout"}, domain.ErrorProviderTimeout},
		{&llm.ProviderError{StatusCode: 400, Code: "bad_request"}, domain.ErrorProviderRequestRejected},
	}
	for _, test := range tests {
		assert.Equal(t, test.code, modelErrorCode(test.err))
	}
}

func TestLoopStopsAfterConfiguredRetryBudgetAndCancelsBackoff(t *testing.T) {
	providerError := func() error {
		return &llm.ProviderError{StatusCode: 503, Code: "busy", Message: "busy", Retryable: true}
	}
	provider := llm.NewFakeProvider(
		llm.FakeStep{Err: providerError()}, llm.FakeStep{Err: providerError()},
		llm.FakeStep{Err: providerError()}, llm.FakeStep{Err: providerError()},
	)
	loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: &memoryWriter{},
		Retry: RetryPolicy{Delays: []time.Duration{0, 0, 0}, Sleep: func(context.Context, time.Duration) error { return nil }}}
	_, err := loop.Run(context.Background(), RunInput{RunID: "exhausted", Model: "m"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderUnavailable, domain.ErrorCodeOf(err))
	assert.Len(t, provider.Requests, 4)

	cancelProvider := llm.NewFakeProvider(llm.FakeStep{Err: providerError()}, llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop}})
	ctx, cancel := context.WithCancel(context.Background())
	cancelLoop := &Loop{Provider: cancelProvider, Tools: &fakeTools{}, Events: &memoryWriter{},
		Retry: RetryPolicy{Delays: []time.Duration{time.Second}, Sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}}}
	_, err = cancelLoop.Run(ctx, RunInput{RunID: "cancel-backoff", Model: "m"})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Len(t, cancelProvider.Requests, 1)
}

func TestLoopDoesNotRetryWhenRetryScheduleEventFails(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Err: &llm.ProviderError{StatusCode: 503, Code: "busy", Message: "busy", Retryable: true}},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop}},
	)
	writer := &memoryWriter{failTypes: map[string]error{"model_call_retry_scheduled": errors.New("disk full")}}
	loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: writer,
		Retry: RetryPolicy{Delays: []time.Duration{0}, Sleep: func(context.Context, time.Duration) error { return nil }}}
	_, err := loop.Run(context.Background(), RunInput{RunID: "retry-event-failure", Model: "m"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorEventPersistence, domain.ErrorCodeOf(err))
	assert.Len(t, provider.Requests, 1)
}

func TestLoopDoesNotRetryWhenStreamEventPersistenceFails(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{TextDeltas: []string{"partial"}, Err: &llm.ProviderError{
		StatusCode: 503, Code: "busy", Message: "busy", Retryable: true,
	}})
	writer := &memoryWriter{failTypes: map[string]error{"text_delta": errors.New("disk full")}}
	loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: writer,
		Retry: RetryPolicy{Delays: []time.Duration{0}, Sleep: func(context.Context, time.Duration) error { return nil }}}
	_, err := loop.Run(context.Background(), RunInput{RunID: "sink-failure", Model: "m"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorEventPersistence, domain.ErrorCodeOf(err))
	assert.Len(t, provider.Requests, 1)
}

func TestLoopRejectsInvalidNormalToolProtocolBeforeExecution(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		StopReason: domain.StopReasonToolCalls,
		ToolCalls:  []domain.ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage(`{"path":`)}},
	}})
	tools := &fakeTools{}
	_, err := (&Loop{Provider: provider, Tools: tools, Events: &memoryWriter{}}).Run(
		context.Background(), RunInput{RunID: "invalid-protocol", Model: "m"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorModelProtocol, domain.ErrorCodeOf(err))
	assert.Empty(t, tools.calls)
}

func TestLoopRecoversTruncatedToolCallWithoutExecutingIt(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: domain.Completion{
			StopReason: domain.StopReasonLength,
			ToolCalls: []domain.ToolCall{{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`),
				ArgumentsFragment: `{"path":"unterminated`, Partial: true}},
		}},
		llm.FakeStep{Completion: domain.Completion{
			StopReason: domain.StopReasonStop, Content: []domain.ContentBlock{textBlock("recovered")},
		}},
	)
	tools := &fakeTools{}
	writer := &memoryWriter{}
	loop := &Loop{Provider: provider, Tools: tools, Events: writer}
	result, err := loop.Run(context.Background(), RunInput{RunID: "truncated", Model: "m"})
	require.NoError(t, err)
	assert.Empty(t, tools.calls)
	assert.Equal(t, 1, countEvent(writer.types(), "tool_call_skipped"))
	require.Len(t, provider.Requests, 2)
	request := provider.Requests[1]
	var sawCall, sawResult bool
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.ToolCall != nil {
				sawCall = true
				assert.JSONEq(t, `{}`, string(block.ToolCall.Arguments))
			}
			if block.ToolResult != nil {
				sawResult = true
				assert.True(t, block.ToolResult.IsError)
			}
		}
	}
	assert.True(t, sawCall)
	assert.True(t, sawResult)
	assert.Equal(t, "recovered", messageText(result.Messages[len(result.Messages)-1]))
}

func TestLoopRejectsTruncatedToolCallWithoutIdentity(t *testing.T) {
	for _, call := range []domain.ToolCall{
		{Name: "read", Arguments: json.RawMessage(`{}`), Partial: true},
		{ID: "call", Arguments: json.RawMessage(`{}`), Partial: true},
	} {
		provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
			StopReason: domain.StopReasonLength, ToolCalls: []domain.ToolCall{call},
		}})
		tools := &fakeTools{}
		_, err := (&Loop{Provider: provider, Tools: tools, Events: &memoryWriter{}}).Run(
			context.Background(), RunInput{RunID: "missing-identity", Model: "m"})
		require.Error(t, err)
		assert.Equal(t, domain.ErrorModelProtocol, domain.ErrorCodeOf(err))
		assert.Empty(t, tools.calls)
	}
}

func TestLoopSkipsAllCallsInTruncatedCompletion(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonLength, ToolCalls: []domain.ToolCall{
			{ID: "one", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)},
			{ID: "two", Name: "read", Arguments: json.RawMessage(`{}`), ArgumentsFragment: `{"path":`, Partial: true},
		}}},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop}},
	)
	tools := &fakeTools{}
	writer := &memoryWriter{}
	_, err := (&Loop{Provider: provider, Tools: tools, Events: writer}).Run(context.Background(), RunInput{RunID: "all-skipped", Model: "m"})
	require.NoError(t, err)
	assert.Empty(t, tools.calls)
	assert.Equal(t, 2, countEvent(writer.types(), "tool_call_skipped"))
}

func TestLoopFailsDeterministicallyAfterTwoTruncationRecoveries(t *testing.T) {
	truncated := llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonLength,
		ToolCalls: []domain.ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage(`{}`), Partial: true}}}}
	provider := llm.NewFakeProvider(truncated, truncated)
	tools := &fakeTools{}
	_, err := (&Loop{Provider: provider, Tools: tools, Events: &memoryWriter{}}).Run(
		context.Background(), RunInput{RunID: "twice", Model: "m"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorModelOutputTruncated, domain.ErrorCodeOf(err))
	assert.Empty(t, tools.calls)
	assert.Len(t, provider.Requests, 2)
}

func TestLoopFailsTextOnlyLengthCompletion(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		StopReason: domain.StopReasonLength, Content: []domain.ContentBlock{textBlock("partial")},
	}})
	_, err := (&Loop{Provider: provider, Tools: &fakeTools{}, Events: &memoryWriter{}}).Run(
		context.Background(), RunInput{RunID: "text-length", Model: "m"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorModelOutputTruncated, domain.ErrorCodeOf(err))
}

type providerFunc func(context.Context, domain.CompletionRequest, llm.StreamSink) (domain.Completion, error)

func (f providerFunc) Stream(ctx context.Context, request domain.CompletionRequest, sink llm.StreamSink) (domain.Completion, error) {
	return f(ctx, request, sink)
}

func (providerFunc) Capabilities() llm.ModelCapabilities {
	return llm.ModelCapabilities{Streaming: true, ToolUse: true}
}

func countEvent(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}
