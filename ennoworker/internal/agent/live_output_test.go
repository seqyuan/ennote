package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamingTools adds ExecuteStreaming to fakeTools, so the live sink path runs:
// the tool emits one chunk and the dispatcher wraps it in a coalescer scoped to the
// run and to this call.
type streamingTools struct {
	fakeTools
	chunks []domain.ToolOutputUpdate
}

func (s *streamingTools) ExecuteStreaming(_ context.Context, call domain.ToolCall, sink domain.ToolOutputSink) (domain.ToolResult, error) {
	for _, chunk := range s.chunks {
		chunk.ToolCallID = call.ID
		sink.TryEmit(chunk)
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "done"}, nil
}

// TestLiveToolOutputCarriesTheCallIdentity pins the wiring the client depends on:
// the coalescer is created with the *call's* name and the run's id, so the payload
// the browser receives names the same (toolCallId, stream) pair the durable
// tool_call_* events use. Renaming a field, dropping the name, or swapping the two
// adjacent string arguments leaves the server compiling and the client unable to
// place the delta on the right card.
func TestLiveToolOutputCarriesTheCallIdentity(t *testing.T) {
	hub := events.NewHub()
	live, unsubscribe := hub.SubscribeLive("run-live", 16)
	defer unsubscribe()

	tools := &streamingTools{chunks: []domain.ToolOutputUpdate{{Stream: "stdout", Data: []byte("hello")}}}
	loop := &Loop{
		Tools:         tools,
		Events:        &memoryWriter{},
		Hub:           hub,
		LivePublisher: hub,
		ToolExecution: domain.ToolExecutionConfig{Mode: "sequential"},
	}

	_, err := loop.executeToolBatch(context.Background(), "run-live", 1, []domain.ToolCall{
		{ID: "call-1", Name: "bash", Arguments: []byte(`{}`)},
	})
	require.NoError(t, err)

	var event domain.LiveRunEvent
	select {
	case event = <-live:
	case <-time.After(2 * time.Second):
		t.Fatal("no live event was published")
	}

	var payload struct {
		ToolCallID string `json:"toolCallId"`
		ToolName   string `json:"toolName"`
		Stream     string `json:"stream"`
		Text       string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(event.Payload, &payload), string(event.Payload))
	assert.Equal(t, "run-live", event.RunID)
	assert.Equal(t, "call-1:stdout", event.StreamID)
	assert.Equal(t, "call-1", payload.ToolCallID)
	assert.Equal(t, "bash", payload.ToolName, "the name comes from the call, not from the stream id")
	assert.Equal(t, "stdout", payload.Stream)
	assert.Equal(t, "hello", payload.Text)
}
