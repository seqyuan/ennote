package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedQueue struct {
	mu          sync.Mutex
	steerCalls  int
	followCalls int
	steerAt     int
	followAt    int
	order       *[]string
}

func (q *scriptedQueue) Drain(_ context.Context, runID string, kind domain.QueuedInputKind, mode domain.QueueMode) ([]domain.QueuedInput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if kind == domain.QueuedInputSteer {
		q.steerCalls++
		if q.steerCalls == q.steerAt {
			if q.order != nil {
				*q.order = append(*q.order, "steer")
			}
			return []domain.QueuedInput{{RunID: runID, Kind: kind, Text: "change direction"}}, nil
		}
		return nil, nil
	}
	q.followCalls++
	if q.followCalls == q.followAt {
		if q.order != nil {
			*q.order = append(*q.order, "follow_up")
		}
		return []domain.QueuedInput{{RunID: runID, Kind: kind, Text: "also summarize"}}, nil
	}
	return nil, nil
}

func TestSteerIsInjectedAfterCompleteToolBatchBeforeNextModelCall(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: toolCompletion("read-1", `{"path":"a"}`)},
		llm.FakeStep{Completion: domain.Completion{Content: []domain.ContentBlock{textBlock("redirected")}, StopReason: "stop"}},
	)
	order := []string{}
	tools := &fakeTools{execute: func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
		order = append(order, "tool")
		return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "done"}, nil
	}}
	queue := &scriptedQueue{steerAt: 2, order: &order}
	loop := &Loop{
		Provider: provider, Tools: tools, Events: &memoryWriter{}, QueuedInputs: queue,
		SteeringMode: domain.QueueOneAtATime, FollowUpMode: domain.QueueOneAtATime,
	}
	result, err := loop.Run(context.Background(), RunInput{RunID: "run-steer", Model: "fake"})
	require.NoError(t, err)
	assert.Equal(t, []string{"tool", "steer"}, order)
	assert.Equal(t, 2, result.Iterations)
	require.Len(t, provider.Requests, 2)
	second := provider.Requests[1].Messages
	assert.Equal(t, domain.RoleTool, second[len(second)-2].Role)
	assert.Equal(t, domain.RoleUser, second[len(second)-1].Role)
	assert.Equal(t, "change direction", messageText(second[len(second)-1]))
	assert.Equal(t, 1, queue.followCalls, "follow-up is polled only after the redirected response would stop")
}

func TestFollowUpRunsOnlyAfterAgentWouldOtherwiseStop(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: domain.Completion{Content: []domain.ContentBlock{textBlock("done")}, StopReason: "stop"}},
		llm.FakeStep{Completion: domain.Completion{Content: []domain.ContentBlock{textBlock("summary")}, StopReason: "stop"}},
	)
	queue := &scriptedQueue{steerAt: -1, followAt: 1}
	events := &memoryWriter{}
	loop := &Loop{
		Provider: provider, Tools: &fakeTools{}, Events: events, QueuedInputs: queue,
		SteeringMode: domain.QueueOneAtATime, FollowUpMode: domain.QueueOneAtATime,
	}
	result, err := loop.Run(context.Background(), RunInput{RunID: "run-follow", Model: "fake"})
	require.NoError(t, err)
	assert.Equal(t, 2, result.Iterations)
	require.Len(t, provider.Requests, 2)
	second := provider.Requests[1].Messages
	assert.Equal(t, domain.RoleUser, second[len(second)-1].Role)
	assert.Equal(t, "also summarize", messageText(second[len(second)-1]))
	assert.GreaterOrEqual(t, queue.steerCalls, 2)
	assert.Equal(t, 2, queue.followCalls)
	assert.Contains(t, events.types(), "follow_up_consumed")
}
