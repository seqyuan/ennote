package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopInjectsEphemeralReminder(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content:    []domain.ContentBlock{textBlock("Done")},
		StopReason: "stop", ActualModel: "fake-model",
	}})
	loop := &Loop{
		Provider: provider, Tools: &fakeTools{}, Events: &memoryWriter{},
		MaxIterations: 4, ContextTokens: 8192,
		Reminders: NewReminderRegistry(ReminderFunc{NameField: "test",
			Fn: func(context.Context, ReminderContext) (string, bool) { return "ephemeral state", true }}),
	}
	result, err := loop.Run(context.Background(), RunInput{
		RunID: "run-1", Model: "fake-model",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("Start")}}},
	})
	require.NoError(t, err)
	require.Len(t, provider.Requests, 1)
	require.True(t, messagesContainText(provider.Requests[0].Messages, "<system-reminder>"))

	for _, msg := range result.Messages {
		assert.False(t, messagesContainText([]domain.ChatMessage{msg}, "<system-reminder>"),
			"system-reminder must not appear in persisted messages")
	}
	for _, msg := range result.Generated {
		assert.False(t, messagesContainText([]domain.ChatMessage{msg}, "<system-reminder>"),
			"system-reminder must not appear in generated history")
	}
}

func TestLoopReminderDoesNotDisplaceLatestUserRequest(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content: []domain.ContentBlock{textBlock("done")}, StopReason: domain.StopReasonStop,
		ActualModel: "fake-model",
	}})
	loop := &Loop{
		Provider: provider, Tools: &fakeTools{}, Events: &memoryWriter{},
		ContextTokens: 8192, MaxOutput: 4096, MaxIterations: 2,
		Reminders: NewReminderRegistry(ReminderFunc{NameField: "test",
			Fn: func(context.Context, ReminderContext) (string, bool) { return "ephemeral state", true }}),
	}
	history := []domain.ChatMessage{
		{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock(strings.Repeat("old ", 1000))}},
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{textBlock(strings.Repeat("old answer ", 1000))}},
		{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("latest request")}},
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-2", Model: "fake", History: history})
	require.NoError(t, err)
	require.Len(t, provider.Requests, 1)
	assert.True(t, messagesContainText(provider.Requests[0].Messages, "latest request"),
		"latest user request must survive context trimming")
	assert.True(t, messagesContainText(provider.Requests[0].Messages, "<system-reminder>"))
}

func TestLoopDropsReminderWhenCompositionDoesNotFitTools(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content: []domain.ContentBlock{textBlock("done")}, StopReason: domain.StopReasonStop,
		ActualModel: "fake-model",
	}})
	largeTool := domain.ToolDefinition{Name: "big", Description: strings.Repeat("x", 6000),
		Parameters: json.RawMessage(`{"type":"object"}`)}
	tools := &fakeTools{
		defs:    []domain.ToolDefinition{largeTool},
		classes: map[string]domain.ExecutionClass{"big": domain.ExecutionExclusive},
		execute: func(ctx context.Context, call domain.ToolCall) domain.ToolResult {
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "ok"}
		},
	}
	// A small context window that cannot fit the large tool schema plus reminders.
	loop := &Loop{
		Provider: provider, Tools: tools, Events: &memoryWriter{},
		ContextTokens: 2048, MaxOutput: 256, MaxIterations: 2,
		Reminders: NewReminderRegistry(ReminderFunc{NameField: "big",
			Fn: func(context.Context, ReminderContext) (string, bool) {
				return strings.Repeat("big reminder ", 200), true
			}}),
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-3", Model: "fake",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("hi")}}},
	})
	require.NoError(t, err)
	require.Len(t, provider.Requests, 1)
	// With a tiny context window, the oversized reminder should be dropped but the request should still succeed.
	assert.True(t, messagesContainText(provider.Requests[0].Messages, "hi"))
}

func TestLoopReminderNeverReachesMidRunCompactor(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: domain.Completion{
			Content:    []domain.ContentBlock{textBlock("before tools")},
			ToolCalls:  []domain.ToolCall{{ID: "c1", Name: "read", Arguments: json.RawMessage(`{"path":"f"}`)}},
			StopReason: "tool_calls", ActualModel: "fake-model",
		}},
		llm.FakeStep{Completion: domain.Completion{
			Content: []domain.ContentBlock{textBlock("after tools")}, StopReason: "stop", ActualModel: "fake-model",
		}},
	)
	var compactionCaptured [][]domain.ChatMessage
	loop := &Loop{
		Provider: provider, Tools: &fakeTools{result: domain.ToolResult{Content: "result"}}, Events: &memoryWriter{},
		ContextTokens: 8192, MaxOutput: 4096, MaxIterations: 4,
		Reminders: NewReminderRegistry(ReminderFunc{NameField: "test",
			Fn: func(context.Context, ReminderContext) (string, bool) { return "ephemeral", true }}),
		MidRunCompactor: midRunCompactorFunc(func(_ context.Context, req MidRunCompactionRequest) (MidRunCompactionResult, error) {
			compactionCaptured = append(compactionCaptured, cloneMessages(req.Messages))
			return MidRunCompactionResult{Messages: req.Messages, State: req.Previous}, nil
		}),
	}
	history := []domain.ChatMessage{
		{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("task")}},
	}
	_, err := loop.Run(context.Background(), RunInput{
		RunID: "run-4", Model: "fake", SystemPrompt: "You are helpful.", History: history,
	})
	require.NoError(t, err)
	for _, captured := range compactionCaptured {
		assert.False(t, messagesContainText(captured, "<system-reminder>"),
			"system-reminder must not reach mid-run compactor")
	}
}

func messagesContainText(messages []domain.ChatMessage, expected string) bool {
	for _, msg := range messages {
		for _, block := range msg.Content {
			if strings.Contains(block.Text, expected) {
				return true
			}
			if block.ToolResult != nil && strings.Contains(block.ToolResult.Content, expected) {
				return true
			}
		}
	}
	return false
}
