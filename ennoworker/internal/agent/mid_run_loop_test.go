package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type midRunCompactorFunc func(context.Context, MidRunCompactionRequest) (MidRunCompactionResult, error)

func (f midRunCompactorFunc) CompactRunContext(ctx context.Context, request MidRunCompactionRequest) (MidRunCompactionResult, error) {
	return f(ctx, request)
}

func oneReadCompletion() domain.Completion {
	return domain.Completion{StopReason: domain.StopReasonToolCalls, ActualModel: "model", ToolCalls: []domain.ToolCall{{
		ID: "read-1", Name: "read", Arguments: json.RawMessage(`{"path":"data.txt"}`),
	}}}
}

func compactedRunContext(request MidRunCompactionRequest, id string) MidRunCompactionResult {
	state := MidRunCompactionState{ID: id, Summary: "## Goal\nContinue the tool task.",
		SourceDigest: "digest-" + id, SummaryContractDigest: "contract", Count: request.Previous.Count + 1,
		CoveredGenerated: len(request.Generated) - 1}
	tailStart := len(request.Messages) - 2
	if tailStart < 0 {
		tailStart = 0
	}
	return MidRunCompactionResult{Compacted: true, State: state,
		Messages: RunCheckpointMessages(state, request.Messages[tailStart:])}
}

func TestLoopAppliesThresholdCompactionAfterCompleteToolExchange(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: oneReadCompletion()},
		llm.FakeStep{Completion: domain.Completion{StopReason: domain.StopReasonStop,
			Content: []domain.ContentBlock{textBlock("done")}}},
	)
	var requests []MidRunCompactionRequest
	compactor := midRunCompactorFunc(func(_ context.Context, request MidRunCompactionRequest) (MidRunCompactionResult, error) {
		requests = append(requests, request)
		return compactedRunContext(request, "run-compact-1"), nil
	})
	loop := &Loop{Provider: provider, Tools: &fakeTools{result: domain.ToolResult{Content: "large result"}},
		Events: &memoryWriter{}, MidRunCompactor: compactor, MaxIterations: 4}
	result, err := loop.Run(context.Background(), RunInput{RunID: "mid-run-threshold", Model: "model",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("analyze")}}}})
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, 2, requests[0].Iteration)
	assert.Equal(t, MidRunCompactionThreshold, requests[0].Reason)
	assert.Equal(t, 1, requests[0].RequestGeneration)
	require.Len(t, provider.Requests, 2)
	assert.True(t, messageIsContextSummary(provider.Requests[1].Messages[0]))
	require.Len(t, result.Generated, 3)
	assert.Equal(t, domain.RoleAssistant, result.Generated[0].Role)
	assert.Equal(t, domain.RoleTool, result.Generated[1].Role)
	assert.Equal(t, "done", messageText(result.Generated[2]))
}

func TestLoopRecoversIterationTwoOverflowWithRunLocalCompaction(t *testing.T) {
	modelCalls := 0
	provider := providerFunc(func(_ context.Context, _ domain.CompletionRequest, sink llm.StreamSink) (domain.Completion, error) {
		modelCalls++
		switch modelCalls {
		case 1:
			return oneReadCompletion(), nil
		case 2:
			return domain.Completion{}, &llm.ProviderError{StatusCode: 400, Code: "context_length_exceeded", Message: "too large"}
		default:
			require.NoError(t, sink.TextDelta("recovered"))
			return domain.Completion{StopReason: domain.StopReasonStop, ActualModel: "model",
				Content: []domain.ContentBlock{textBlock("recovered")}}, nil
		}
	})
	var reasons []MidRunCompactionReason
	compactor := midRunCompactorFunc(func(_ context.Context, request MidRunCompactionRequest) (MidRunCompactionResult, error) {
		reasons = append(reasons, request.Reason)
		if request.Reason == MidRunCompactionThreshold {
			return MidRunCompactionResult{Messages: request.Messages, State: request.Previous}, nil
		}
		return compactedRunContext(request, "run-overflow-1"), nil
	})
	writer := &memoryWriter{}
	loop := &Loop{Provider: provider, Tools: &fakeTools{result: domain.ToolResult{Content: "large result"}},
		Events: writer, MidRunCompactor: compactor, MaxIterations: 4, Retry: RetryPolicy{Delays: nil}}
	result, err := loop.Run(context.Background(), RunInput{RunID: "mid-run-overflow", Model: "model",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("analyze")}}}})
	require.NoError(t, err)
	assert.Equal(t, []MidRunCompactionReason{MidRunCompactionThreshold, MidRunCompactionOverflow}, reasons)
	assert.Equal(t, 3, modelCalls)
	assert.Equal(t, "recovered", result.Completion.Content[0].Text)

	var generations []int
	for _, event := range writer.events {
		if event.EventType != "model_call_started" {
			continue
		}
		var payload struct {
			RequestGeneration int `json:"requestGeneration"`
		}
		require.NoError(t, json.Unmarshal(event.Payload, &payload))
		generations = append(generations, payload.RequestGeneration)
	}
	assert.Equal(t, []int{0, 0, 1}, generations)
}

func TestLoopFailsSecondOverflowAfterThresholdCompaction(t *testing.T) {
	calls := 0
	provider := providerFunc(func(context.Context, domain.CompletionRequest, llm.StreamSink) (domain.Completion, error) {
		calls++
		if calls == 1 {
			return oneReadCompletion(), nil
		}
		return domain.Completion{}, &llm.ProviderError{StatusCode: 400, Code: "context_length_exceeded", Message: "still too large"}
	})
	compactor := midRunCompactorFunc(func(_ context.Context, request MidRunCompactionRequest) (MidRunCompactionResult, error) {
		return compactedRunContext(request, "threshold-before-overflow"), nil
	})
	loop := &Loop{Provider: provider, Tools: &fakeTools{result: domain.ToolResult{Content: "large result"}},
		Events: &memoryWriter{}, MidRunCompactor: compactor, MaxIterations: 4}
	_, err := loop.Run(context.Background(), RunInput{RunID: "mid-run-second-overflow", Model: "model",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("analyze")}}}})
	assert.Equal(t, domain.ErrorContextOverflowAfterCompaction, domain.ErrorCodeOf(err))
}
