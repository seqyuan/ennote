//go:build integration

package agent

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
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type liveToolRunner struct {
	calls int
}

type liveNoTools struct{}

func (liveNoTools) Definitions() []domain.ToolDefinition { return nil }
func (liveNoTools) Execute(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "unexpected tool call", IsError: true}, nil
}

func (r *liveToolRunner) Definitions() []domain.ToolDefinition {
	return []domain.ToolDefinition{{
		Name: "probe_echo", Description: "Return a deterministic probe result.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
	}}
}

func (r *liveToolRunner) Execute(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	r.calls++
	return domain.ToolResult{
		ToolCallID: call.ID, ToolName: call.Name, Content: "ENNOTE_TOOL_RESULT", IsError: false,
	}, nil
}

type liveEventWriter struct {
	sequence int64
	types    []string
}

func (w *liveEventWriter) Append(_ context.Context, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	events := make([]domain.RunEvent, 0, len(pending))
	for _, event := range pending {
		w.sequence++
		w.types = append(w.types, event.EventType)
		events = append(events, domain.RunEvent{RunID: runID, Seq: w.sequence, EventType: event.EventType})
	}
	return events, nil
}

func TestLiveOpenAICompatibleVisionDescriptorFallback(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	provider, err := llm.NewOpenAIProvider(llm.OpenAIConfig{
		BaseURL: baseURL, APIKey: llm.NewSecret(apiKey), Model: model, MaxTokens: 128,
	})
	require.NoError(t, err)
	var encoded bytes.Buffer
	imageValue := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			imageValue.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	require.NoError(t, png.Encode(&encoded, imageValue))

	textRuntime := domain.ModelRuntimeSnapshot{ModelProfileID: "text", ProviderProfileID: "live",
		APIModel: model, ContextTokens: 200000, MaxOutputTokens: 128}
	visionRuntime := domain.ModelRuntimeSnapshot{ModelProfileID: "vision", ProviderProfileID: "live",
		APIModel: model, ContextTokens: 200000, MaxOutputTokens: 128, SupportsVision: true}
	routing := domain.FrozenRoutingConfig{Candidates: []domain.ModelRuntimeSnapshot{textRuntime, visionRuntime}, Pinned: true}
	visionConfig, err := json.Marshal(domain.VisionPolicyConfig{
		Mode: "describe", DescriptorModelProfileID: visionRuntime.ModelProfileID, PromptVersion: "v1",
	})
	require.NoError(t, err)
	events := &liveEventWriter{}
	loop := &Loop{
		Provider: provider, Tools: liveNoTools{}, Events: events,
		ModelRouter: &SnapshotModelRouter{Factory: func(domain.ModelRuntimeSnapshot) (llm.Provider, error) {
			return provider, nil
		}},
		VisionResolver: &BuiltinVisionResolver{Loader: fakeImageLoader{image: domain.ImageRef{
			ArtifactID: "live-red", MIMEType: "image/png", SHA256: "live-red-sha", Data: encoded.Bytes(),
		}}},
		MaxIterations: 2, ContextTokens: 200000, MaxOutput: 128,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	result, err := loop.Run(ctx, RunInput{
		RunID: "live-vision-descriptor", Model: model, InitialRuntime: textRuntime, Routing: routing,
		VisionPolicy: domain.PolicySnapshot{Config: visionConfig},
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{
			{Kind: domain.ContentText, Text: "Reply with exactly ENNOTE_DESCRIPTOR_OK if the derived image description says the image is red."},
			{Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: "live-red"}},
		}}},
	})
	require.NoError(t, err)
	var finalText strings.Builder
	for _, block := range result.Completion.Content {
		if block.Kind == domain.ContentText {
			finalText.WriteString(block.Text)
		}
	}
	assert.Contains(t, finalText.String(), "ENNOTE_DESCRIPTOR_OK")
	assert.GreaterOrEqual(t, countEventType(events.types, "model_call_started"), 2)
	assert.GreaterOrEqual(t, countEventType(events.types, "model_call_completed"), 2)
}

func countEventType(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func TestLiveDeepSeekAgentToolRoundTrip(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	provider, err := llm.NewOpenAIProvider(llm.OpenAIConfig{
		BaseURL: baseURL, APIKey: llm.NewSecret(apiKey), Model: model, MaxTokens: 256,
	})
	require.NoError(t, err)
	tools := &liveToolRunner{}
	events := &liveEventWriter{}
	loop := &Loop{
		Provider: provider, Tools: tools, Events: events,
		MaxIterations: 3, ContextTokens: 32000, MaxOutput: 256,
		ToolExecution: domain.ToolExecutionConfig{Mode: "sequential", MaxConcurrentReadTools: 1},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	result, err := loop.Run(ctx, RunInput{
		RunID: "deepseek-live-tool-round-trip", Model: model,
		History: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{
			Kind: domain.ContentText,
			Text: "Call probe_echo exactly once with value ENNOTE_TOOL. After receiving the tool result, reply with exactly ENNOTE_DONE.",
		}}}},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, tools.calls)
	assert.Equal(t, domain.StopReasonStop, result.Completion.StopReason)
	assert.Len(t, result.Generated, 3)
	assert.Equal(t, domain.RoleAssistant, result.Generated[0].Role)
	assert.Equal(t, domain.RoleTool, result.Generated[1].Role)
	assert.Equal(t, domain.RoleAssistant, result.Generated[2].Role)
	var finalText strings.Builder
	for _, block := range result.Completion.Content {
		if block.Kind == domain.ContentText {
			finalText.WriteString(block.Text)
		}
	}
	assert.Contains(t, finalText.String(), "ENNOTE_DONE")
	assert.Contains(t, events.types, "model_call_started")
	assert.Contains(t, events.types, "model_call_completed")
	assert.Contains(t, events.types, "tool_call_started")
	assert.Contains(t, events.types, "tool_call_completed")
}
