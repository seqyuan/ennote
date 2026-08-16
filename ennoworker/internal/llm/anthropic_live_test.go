//go:build integration

package llm

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/require"
)

func anthropicLiveProvider(t *testing.T) (*AnthropicProvider, string) {
	t.Helper()
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_ANTHROPIC_API_KEY"))
	if apiKey == "" {
		t.Skip("ENNOTE_ANTHROPIC_API_KEY is required")
	}
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_ANTHROPIC_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	model := strings.TrimSpace(os.Getenv("ENNOTE_ANTHROPIC_MODEL"))
	if model == "" {
		model = "claude-haiku-4-5"
	}
	provider, err := NewAnthropicProvider(AnthropicConfig{
		BaseURL: baseURL, APIKey: NewSecret(apiKey), Model: model, MaxTokens: 256,
	})
	require.NoError(t, err)
	return provider, model
}

func TestLiveAnthropicText(t *testing.T) {
	provider, model := anthropicLiveProvider(t)
	sink := &liveStreamSink{}
	completion, err := provider.Stream(context.Background(), domain.CompletionRequest{
		Model: model,
		Messages: []domain.ChatMessage{
			{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Reply with exactly: OK"}}},
		},
		MaxTokens: 64,
	}, sink)
	require.NoError(t, err)
	require.NotEmpty(t, sink.text.String())
	require.Equal(t, domain.StopReasonStop, completion.StopReason)
	require.NotEmpty(t, completion.ActualModel)
	require.True(t, sink.usage.OutputTokens > 0)
}

func TestLiveAnthropicToolUse(t *testing.T) {
	provider, model := anthropicLiveProvider(t)
	sink := &liveStreamSink{}
	completion, err := provider.Stream(context.Background(), domain.CompletionRequest{
		Model: model,
		Messages: []domain.ChatMessage{
			{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Call the echo tool with text 'hello'."}}},
		},
		Tools: []domain.ToolDefinition{{
			Name:        "echo",
			Description: "Echo text back",
			Parameters:  nil,
		}},
		MaxTokens: 256,
	}, sink)
	require.NoError(t, err)
	require.Equal(t, domain.StopReasonToolCalls, completion.StopReason)
	require.Len(t, completion.ToolCalls, 1)
	require.Equal(t, "echo", completion.ToolCalls[0].Name)
	require.NotEmpty(t, completion.ToolCalls[0].ID)
}
