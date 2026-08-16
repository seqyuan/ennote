package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToAnthropicMessagesFoldsSystemAndToolResults(t *testing.T) {
	system, messages, err := toAnthropicMessages([]domain.ChatMessage{
		{Role: domain.RoleSystem, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "You are helpful."}}},
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Run ls"}}},
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{
			{Kind: domain.ContentToolCall, ToolCall: &domain.ToolCall{ID: "toolu_1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)}},
		}},
		{Role: domain.RoleTool, Content: []domain.ContentBlock{
			{Kind: domain.ContentToolResult, ToolResult: &domain.ToolResult{ToolCallID: "toolu_1", ToolName: "bash", Content: "file.txt"}},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "You are helpful.", system)
	require.Len(t, messages, 3)

	assert.Equal(t, "user", messages[0].Role)
	require.Len(t, messages[0].Content, 1)
	assert.Equal(t, "text", messages[0].Content[0].Type)
	assert.Equal(t, "Run ls", messages[0].Content[0].Text)

	assert.Equal(t, "assistant", messages[1].Role)
	require.Len(t, messages[1].Content, 1)
	assert.Equal(t, "tool_use", messages[1].Content[0].Type)
	assert.Equal(t, "toolu_1", messages[1].Content[0].ID)
	assert.Equal(t, "bash", messages[1].Content[0].Name)

	assert.Equal(t, "user", messages[2].Role)
	require.Len(t, messages[2].Content, 1)
	assert.Equal(t, "tool_result", messages[2].Content[0].Type)
	assert.Equal(t, "toolu_1", messages[2].Content[0].ToolUseID)
	assert.Equal(t, "file.txt", messages[2].Content[0].Content)
}

func TestToAnthropicMessagesRejectsUnknownContentKind(t *testing.T) {
	_, _, err := toAnthropicMessages([]domain.ChatMessage{
		{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentKind("bogus")}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported content kind")
}

func TestParseAnthropicStreamTextAndToolUse(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":15}}

event: message_stop
data: {"type":"message_stop"}
`
	completion, err := parseAnthropicStream(context.Background(), strings.NewReader(stream), NopSink{}, "claude-sonnet-4-5")
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", completion.ActualModel)
	assert.Equal(t, domain.StopReasonToolCalls, completion.StopReason)
	require.Len(t, completion.Content, 1)
	assert.Equal(t, "Hello", completion.Content[0].Text)
	require.Len(t, completion.ToolCalls, 1)
	assert.Equal(t, "toolu_1", completion.ToolCalls[0].ID)
	assert.Equal(t, "bash", completion.ToolCalls[0].Name)
	assert.JSONEq(t, `{"cmd":"ls"}`, string(completion.ToolCalls[0].Arguments))
}

func TestParseAnthropicStreamRequiresStop(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}
`
	_, err := parseAnthropicStream(context.Background(), strings.NewReader(stream), NopSink{}, "m")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompleteStream)
}

func TestParseAnthropicStreamPreservesToolCallOrder(t *testing.T) {
	// Two tool_use blocks arrive out of order (index 2 before index 1); the
	// completion must list tool calls in ascending index order, not map order.
	stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":1,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"second"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"v\":2}"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"first"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"v\":1}"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

event: message_stop
data: {"type":"message_stop"}
`
	completion, err := parseAnthropicStream(context.Background(), strings.NewReader(stream), NopSink{}, "m")
	require.NoError(t, err)
	require.Len(t, completion.ToolCalls, 2)
	assert.Equal(t, "first", completion.ToolCalls[0].Name)
	assert.Equal(t, "second", completion.ToolCalls[1].Name)
}

func TestMapAnthropicStopReason(t *testing.T) {
	assert.Equal(t, domain.StopReasonToolCalls, mapAnthropicStopReason("tool_use"))
	assert.Equal(t, domain.StopReasonLength, mapAnthropicStopReason("max_tokens"))
	assert.Equal(t, domain.StopReasonStop, mapAnthropicStopReason("end_turn"))
	assert.Equal(t, domain.StopReasonStop, mapAnthropicStopReason("stop_sequence"))
}

func TestAnthropicBuildRequestRejectsUnsupportedThinking(t *testing.T) {
	provider, err := NewAnthropicProvider(AnthropicConfig{BaseURL: "https://api.anthropic.com", Model: "claude-sonnet-4-5", MaxTokens: 1024})
	require.NoError(t, err)
	_, err = provider.buildRequest(domain.CompletionRequest{
		Reasoning: &domain.ReasoningConfig{Dialect: domain.ThinkingDialectOpenAIReasoningEffort, Effort: domain.ThinkingHigh},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support thinking dialect")
}
