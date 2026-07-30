package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirstRequestOverflowRecoversOnceAsNewGeneration(t *testing.T) {
	calls := 0
	provider := providerFunc(func(_ context.Context, _ domain.CompletionRequest, sink llm.StreamSink) (domain.Completion, error) {
		calls++
		if calls == 1 {
			return domain.Completion{}, &llm.ProviderError{StatusCode: 400, Code: "context_length_exceeded", Message: "maximum context window"}
		}
		require.NoError(t, sink.TextDelta("recovered"))
		return domain.Completion{Content: []domain.ContentBlock{textBlock("recovered")}, StopReason: "stop", ActualModel: "model"}, nil
	})
	writer := &memoryWriter{failTypes: map[string]error{}}
	recoveries := 0
	loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: writer, Retry: RetryPolicy{Delays: []time.Duration{}}}

	result, err := loop.Run(context.Background(), RunInput{RunID: "overflow", Model: "model",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("large history")}}},
		OverflowRecovery: func(context.Context) ([]domain.ChatMessage, error) {
			recoveries++
			return []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{
				Kind: domain.ContentContextSummary, Text: "checkpoint",
			}}}}, nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, 1, recoveries)
	assert.Equal(t, "recovered", result.Completion.Content[0].Text)
	assert.Contains(t, writer.types(), "context_overflow_recovery_started")
	assert.Contains(t, writer.types(), "context_overflow_recovery_completed")

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
	assert.Equal(t, []int{0, 1}, generations)
}

func TestSecondOverflowFailsAfterCompaction(t *testing.T) {
	provider := providerFunc(func(context.Context, domain.CompletionRequest, llm.StreamSink) (domain.Completion, error) {
		return domain.Completion{}, &llm.ProviderError{StatusCode: 400, Code: "context_length_exceeded", Message: "too many tokens"}
	})
	loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: &memoryWriter{failTypes: map[string]error{}},
		Retry: RetryPolicy{Delays: []time.Duration{}}}
	_, err := loop.Run(context.Background(), RunInput{RunID: "twice", Model: "model",
		OverflowRecovery: func(context.Context) ([]domain.ChatMessage, error) {
			return []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("summary")}}}, nil
		}})
	assert.Equal(t, domain.ErrorContextOverflowAfterCompaction, domain.ErrorCodeOf(err))
}
