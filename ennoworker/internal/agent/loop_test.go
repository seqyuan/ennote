package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryWriter struct {
	mu        sync.Mutex
	events    []domain.RunEvent
	err       error
	failTypes map[string]error
}

func (w *memoryWriter) Append(_ context.Context, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return nil, w.err
	}
	var appended []domain.RunEvent
	for _, item := range pending {
		if err := w.failTypes[item.EventType]; err != nil {
			return nil, err
		}
		event := domain.RunEvent{
			EventID: int64(len(w.events) + 1), RunID: runID,
			Seq: int64(len(w.events) + 1), EventType: item.EventType, Payload: item.Payload,
		}
		w.events = append(w.events, event)
		appended = append(appended, event)
	}
	return appended, nil
}

func (w *memoryWriter) types() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]string, len(w.events))
	for index, event := range w.events {
		result[index] = event.EventType
	}
	return result
}

type fakeTools struct {
	mu      sync.Mutex
	calls   []domain.ToolCall
	result  domain.ToolResult
	defs    []domain.ToolDefinition
	classes map[string]domain.ExecutionClass
	execute func(context.Context, domain.ToolCall) (domain.ToolResult, error)
}

func (t *fakeTools) Definitions() []domain.ToolDefinition {
	if len(t.defs) > 0 {
		return t.defs
	}
	return []domain.ToolDefinition{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}}
}
func (t *fakeTools) ExecutionClass(name string) domain.ExecutionClass {
	if class, ok := t.classes[name]; ok {
		return class
	}
	return domain.ExecutionExclusive
}
func (t *fakeTools) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	t.mu.Lock()
	t.calls = append(t.calls, call)
	t.mu.Unlock()
	if t.execute != nil {
		return t.execute(ctx, call)
	}
	result := t.result
	result.ToolCallID = call.ID
	result.ToolName = call.Name
	return result, nil
}

func textBlock(value string) domain.ContentBlock {
	return domain.ContentBlock{Kind: domain.ContentText, Text: value}
}

func toolCompletion(id, arguments string) domain.Completion {
	return domain.Completion{
		ToolCalls:  []domain.ToolCall{{ID: id, Name: "read", Arguments: json.RawMessage(arguments)}},
		StopReason: "tool_calls", ActualModel: "fake",
	}
}

func TestLoopCompletesTextToolTextFlow(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{
			TextDeltas: []string{"Checking "},
			Completion: domain.Completion{
				Content:    []domain.ContentBlock{textBlock("Checking ")},
				ToolCalls:  []domain.ToolCall{{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"sample.txt"}`)}},
				StopReason: "tool_calls", ActualModel: "fake-model",
			},
		},
		llm.FakeStep{
			TextDeltas: []string{"Done"},
			Completion: domain.Completion{
				Content: []domain.ContentBlock{textBlock("Done")}, StopReason: "stop", ActualModel: "fake-model",
				Usage: domain.Usage{InputTokens: 10, OutputTokens: 2},
			},
		},
	)
	tools := &fakeTools{result: domain.ToolResult{Content: "sample metadata"}}
	writer := &memoryWriter{}
	loop := &Loop{Provider: provider, Tools: tools, Events: writer, MaxIterations: 4, ContextTokens: 4096}
	result, err := loop.Run(context.Background(), RunInput{
		RunID: "run-1", Model: "fake-model", SystemPrompt: "You are precise.",
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{textBlock("Inspect sample")}}},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Iterations)
	assert.Equal(t, "Done", messageText(result.Messages[len(result.Messages)-1]))
	require.Len(t, tools.calls, 1)
	assert.Equal(t, "read", tools.calls[0].Name)
	require.Len(t, provider.Requests, 2)
	secondRequest := provider.Requests[1]
	assert.Equal(t, domain.RoleTool, secondRequest.Messages[len(secondRequest.Messages)-1].Role)
	assert.Contains(t, writer.types(), "text_delta")
	assert.Contains(t, writer.types(), "tool_call_started")
	assert.Contains(t, writer.types(), "tool_call_completed")
	assert.Equal(t, "model_call_completed", writer.types()[len(writer.types())-1])
}

func TestLoopStopsRepeatedToolCalls(t *testing.T) {
	steps := make([]llm.FakeStep, 5)
	for index := range steps {
		steps[index] = llm.FakeStep{Completion: toolCompletion("same", `{"path":"same"}`)}
	}
	provider := llm.NewFakeProvider(steps...)
	tools := &fakeTools{result: domain.ToolResult{Content: "same result"}}
	writer := &memoryWriter{}
	loop := &Loop{Provider: provider, Tools: tools, Events: writer, MaxIterations: 10}
	result, err := loop.Run(context.Background(), RunInput{RunID: "run-stuck", Model: "fake"})
	assert.ErrorIs(t, err, ErrStuckToolLoop)
	assert.Equal(t, 5, result.Iterations)
	assert.Len(t, tools.calls, 4, "fifth identical batch must be stopped before side effects")
	assert.Contains(t, writer.types(), "stuck_tool_loop")
}

func TestLoopEnforcesMaxIterations(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: toolCompletion("one", `{"path":"one"}`)},
		llm.FakeStep{Completion: toolCompletion("two", `{"path":"two"}`)},
	)
	loop := &Loop{
		Provider: provider, Tools: &fakeTools{result: domain.ToolResult{Content: "ok"}},
		Events: &memoryWriter{}, MaxIterations: 2,
	}
	result, err := loop.Run(context.Background(), RunInput{RunID: "run-max", Model: "fake"})
	assert.ErrorIs(t, err, ErrMaxIterations)
	assert.Equal(t, 2, result.Iterations)
}

func TestLoopPropagatesToolCancellation(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: toolCompletion("cancel", `{}`)})
	started := make(chan struct{})
	tools := &fakeTools{execute: func(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
		close(started)
		<-ctx.Done()
		return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: ctx.Err().Error(), IsError: true}, nil
	}}
	loop := &Loop{Provider: provider, Tools: tools, Events: &memoryWriter{}}
	ctx, cancel := context.WithCancel(context.Background())
	var runErr error
	done := make(chan struct{})
	go func() {
		_, runErr = loop.Run(ctx, RunInput{RunID: "run-cancel", Model: "fake"})
		close(done)
	}()
	<-started
	cancel()
	<-done
	assert.True(t, errors.Is(runErr, context.Canceled), runErr)
}

func TestLoopStopsWhenDurableEventWriteFails(t *testing.T) {
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{StopReason: "stop"}})
	writer := &memoryWriter{err: errors.New("disk full")}
	loop := &Loop{Provider: provider, Tools: &fakeTools{}, Events: writer}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-fail", Model: "fake"})
	assert.ErrorContains(t, err, "disk full")
	assert.Empty(t, provider.Requests, "provider must not start after the durable start event failed")
}
