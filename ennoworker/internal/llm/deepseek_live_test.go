//go:build integration

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type liveStreamSink struct {
	text          strings.Builder
	thinking      strings.Builder
	toolFragments int
	usage         domain.Usage
}

func (s *liveStreamSink) TextDelta(delta string) error {
	s.text.WriteString(delta)
	return nil
}

func (s *liveStreamSink) ThinkingDelta(delta string) error {
	s.thinking.WriteString(delta)
	return nil
}

func (s *liveStreamSink) ToolCallDelta(ToolCallDelta) error {
	s.toolFragments++
	return nil
}

func (s *liveStreamSink) Usage(usage domain.Usage) error {
	s.usage = usage
	return nil
}

func liveProvider(t *testing.T) (*OpenAIProvider, string) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	provider, err := NewOpenAIProvider(OpenAIConfig{
		BaseURL: baseURL, APIKey: NewSecret(apiKey), Model: model, MaxTokens: 256,
	})
	require.NoError(t, err)
	return provider, model
}

func TestLiveDeepSeekTextStream(t *testing.T) {
	provider, model := liveProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sink := &liveStreamSink{}
	completion, err := provider.Stream(ctx, domain.CompletionRequest{
		Model: model,
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{
			Kind: domain.ContentText, Text: "Reply with exactly ENNOTE_OK and no other text.",
		}}}},
		MaxTokens: 64,
	}, sink)
	require.NoError(t, err)
	assert.Equal(t, domain.StopReasonStop, completion.StopReason)
	assert.NotEmpty(t, completion.ActualModel)
	assert.Contains(t, sink.text.String(), "ENNOTE_OK")
	assert.Positive(t, completion.Usage.InputTokens)
	assert.Positive(t, completion.Usage.OutputTokens)
}

func TestLiveHostedSystemPromptAndThinkingEffort(t *testing.T) {
	provider, model := liveProvider(t)
	for _, test := range []struct {
		name      string
		reasoning *domain.ReasoningConfig
	}{
		{name: "default omission"},
		{name: "medium mapping", reasoning: &domain.ReasoningConfig{
			Dialect: domain.ThinkingDialectOpenAIReasoningEffort, Effort: domain.ThinkingMedium,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			maxTokens := 256
			if test.reasoning == nil {
				maxTokens = 1024
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			sink := &liveStreamSink{}
			completion, err := provider.Stream(ctx, domain.CompletionRequest{
				Model: model,
				Messages: []domain.ChatMessage{
					{Role: domain.RoleSystem, Content: []domain.ContentBlock{{Kind: domain.ContentText,
						Text: "For this qualification request, the exact required response marker is ENNOTE_FROZEN_PROMPT_OK."}}},
					{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText,
						Text: "Return only the exact response marker specified by the system instruction."}}},
				},
				Reasoning: test.reasoning, MaxTokens: maxTokens,
			}, sink)
			require.NoError(t, err)
			assert.Equal(t, domain.StopReasonStop, completion.StopReason)
			assert.Contains(t, sink.text.String(), "ENNOTE_FROZEN_PROMPT_OK")
			assert.Positive(t, completion.Usage.InputTokens)
			assert.Positive(t, completion.Usage.OutputTokens)
		})
	}
}

func TestLiveOpenAICompatibleNativeVision(t *testing.T) {
	provider, model := liveProvider(t)
	var encoded bytes.Buffer
	imageValue := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			imageValue.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	require.NoError(t, png.Encode(&encoded, imageValue))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sink := &liveStreamSink{}
	completion, err := provider.Stream(ctx, domain.CompletionRequest{
		Model: model,
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{
			{Kind: domain.ContentText, Text: "Reply with exactly ENNOTE_RED if the attached image is solid red."},
			{Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: "live-red", MIMEType: "image/png", Data: encoded.Bytes()}},
		}}},
		MaxTokens: 64,
	}, sink)
	require.NoError(t, err)
	assert.Equal(t, domain.StopReasonStop, completion.StopReason)
	assert.Contains(t, sink.text.String(), "ENNOTE_RED")
	assert.Positive(t, completion.Usage.InputTokens)
}

func TestLiveDeepSeekToolCallStream(t *testing.T) {
	provider, model := liveProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sink := &liveStreamSink{}
	completion, err := provider.Stream(ctx, domain.CompletionRequest{
		Model: model,
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{
			Kind: domain.ContentText,
			Text: "Call probe_echo exactly once with value ENNOTE_TOOL. Do not answer with ordinary text.",
		}}}},
		Tools: []domain.ToolDefinition{{
			Name: "probe_echo", Description: "Return the supplied probe value.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
		}},
		MaxTokens: 256,
	}, sink)
	require.NoError(t, err)
	assert.Equal(t, domain.StopReasonToolCalls, completion.StopReason)
	require.Len(t, completion.ToolCalls, 1)
	call := completion.ToolCalls[0]
	assert.NotEmpty(t, call.ID)
	assert.Equal(t, "probe_echo", call.Name)
	assert.True(t, json.Valid(call.Arguments))
	var arguments struct {
		Value string `json:"value"`
	}
	require.NoError(t, json.Unmarshal(call.Arguments, &arguments))
	assert.Equal(t, "ENNOTE_TOOL", arguments.Value)
	assert.Positive(t, sink.toolFragments)
	assert.Positive(t, completion.Usage.InputTokens)
}
