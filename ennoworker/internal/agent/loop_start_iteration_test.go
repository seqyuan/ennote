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

// TestLoopStartIterationContinuesToolCallNumbering pins the fix for a re-claimed
// Run (a parent waking from waiting_children) that has no approval checkpoint.
// tool_calls is unique on (run_id, iteration, call_index) with no request
// generation, so the resumed attempt must record its tools above the iterations
// the Run already used; otherwise the batch collides with the pre-wait batch.
func TestLoopStartIterationContinuesToolCallNumbering(t *testing.T) {
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: toolCompletion("read", `{"path":"notes.txt"}`)},
		llm.FakeStep{Completion: domain.Completion{
			Content: []domain.ContentBlock{textBlock("done")}, StopReason: "stop", ActualModel: "fake-model"}},
	)
	writer := &memoryWriter{}
	loop := &Loop{Provider: provider, Tools: &fakeTools{result: domain.ToolResult{Content: "ok"}},
		Events: writer, MaxIterations: 6}

	result, err := loop.Run(context.Background(), RunInput{
		RunID: "run-resumed", Model: "fake-model", StartIteration: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, 4, result.Iterations, "iterations resume at StartIteration, not 1")

	startedIteration := -1
	for _, event := range writer.events {
		if event.EventType != "tool_call_started" {
			continue
		}
		require.NotNil(t, event.Payload)
		var payload struct {
			Iteration int `json:"iteration"`
		}
		require.NoError(t, json.Unmarshal(event.Payload, &payload))
		startedIteration = payload.Iteration
	}
	assert.Equal(t, 3, startedIteration, "the resumed tool call must not reuse iteration 1")
}
