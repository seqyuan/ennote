package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingSink struct {
	mu        sync.Mutex
	text      strings.Builder
	thinking  strings.Builder
	toolCalls []ToolCallDelta
	usage     []domain.Usage
}

func (s *recordingSink) TextDelta(value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text.WriteString(value)
	return nil
}
func (s *recordingSink) ThinkingDelta(value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.thinking.WriteString(value)
	return nil
}
func (s *recordingSink) ToolCallDelta(value ToolCallDelta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls = append(s.toolCalls, value)
	return nil
}
func (s *recordingSink) Usage(value domain.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = append(s.usage, value)
	return nil
}

func TestOpenAIStreamTextThinkingToolsAndUsage(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&requestBody))
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"model":"wire-model","choices":[{"delta":{"reasoning_content":"think "},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"你好"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"read","arguments":"{\"pa"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.txt\"}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider, err := NewOpenAIProvider(OpenAIConfig{
		BaseURL: server.URL + "/v1", APIKey: NewSecret("sk-test"), Model: "configured-model", MaxTokens: 2048,
	})
	require.NoError(t, err)
	sink := &recordingSink{}
	completion, err := provider.Stream(context.Background(), domain.CompletionRequest{
		Model:    "effective-api-model",
		Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "inspect"}}}},
		Tools:    []domain.ToolDefinition{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}, sink)
	require.NoError(t, err)

	assert.Equal(t, "你好", sink.text.String())
	assert.Equal(t, "think ", sink.thinking.String())
	assert.Len(t, sink.toolCalls, 2)
	assert.Equal(t, "tool_calls", completion.StopReason)
	assert.Equal(t, "wire-model", completion.ActualModel)
	require.Len(t, completion.ToolCalls, 1)
	assert.Equal(t, "call-1", completion.ToolCalls[0].ID)
	assert.Equal(t, "read", completion.ToolCalls[0].Name)
	assert.JSONEq(t, `{"path":"a.txt"}`, string(completion.ToolCalls[0].Arguments))
	assert.Equal(t, int64(11), completion.Usage.InputTokens)
	assert.Equal(t, int64(3), completion.Usage.CachedTokens)
	assert.Equal(t, int64(2), completion.Usage.ReasoningTokens)
	assert.Equal(t, true, requestBody["stream"])
	assert.Equal(t, "effective-api-model", requestBody["model"])
}

func TestOpenAIIncompleteStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
	}))
	defer server.Close()
	provider, _ := NewOpenAIProvider(OpenAIConfig{BaseURL: server.URL, Model: "m"})
	_, err := provider.Stream(context.Background(), domain.CompletionRequest{}, &recordingSink{})
	assert.ErrorIs(t, err, ErrIncompleteStream)
}

func TestOpenAIProviderErrorIsRetryableAndRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"key sk-sensitive exceeded quota","type":"rate_limit","code":"rate_limit"}}`)
	}))
	defer server.Close()
	provider, _ := NewOpenAIProvider(OpenAIConfig{BaseURL: server.URL, APIKey: NewSecret("sk-sensitive"), Model: "m"})
	_, err := provider.Stream(context.Background(), domain.CompletionRequest{}, NopSink{})
	require.Error(t, err)
	assert.True(t, IsRetryable(err))
	assert.NotContains(t, err.Error(), "sk-sensitive")
	assert.Contains(t, err.Error(), "[REDACTED]")
}

func TestOpenAIStreamHonorsCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	provider, _ := NewOpenAIProvider(OpenAIConfig{BaseURL: server.URL, Model: "m"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := provider.Stream(ctx, domain.CompletionRequest{}, NopSink{})
	close(release)
	server.Close()
	assert.True(t, errors.Is(err, ErrCancelled), err)
}

func TestOpenAIParserReturnsPartialToolCallOnLength(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"read","arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`, "",
	}, "\n\n")
	completion, err := parseOpenAIStream(context.Background(), strings.NewReader(stream), NopSink{}, "m")
	require.NoError(t, err)
	assert.Equal(t, domain.StopReasonLength, completion.StopReason)
	require.Len(t, completion.ToolCalls, 1)
	assert.True(t, completion.ToolCalls[0].Partial)
	assert.JSONEq(t, `{}`, string(completion.ToolCalls[0].Arguments))
	assert.NotEmpty(t, completion.ToolCalls[0].ArgumentsFragment)
}

func TestOpenAIParserPreservesSparsePartialCallButRejectsSparseNormalCall(t *testing.T) {
	buildStream := func(reason string) string {
		return strings.Join([]string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":2,"id":"call-2","function":{"name":"read","arguments":"{"}}]},"finish_reason":null}]}`,
			fmt.Sprintf(`data: {"choices":[{"delta":{},"finish_reason":%q}]}`, reason),
			`data: [DONE]`, "",
		}, "\n\n")
	}
	completion, err := parseOpenAIStream(context.Background(), strings.NewReader(buildStream("length")), NopSink{}, "m")
	require.NoError(t, err)
	require.Len(t, completion.ToolCalls, 1)
	assert.Equal(t, "call-2", completion.ToolCalls[0].ID)
	_, err = parseOpenAIStream(context.Background(), strings.NewReader(buildStream("tool_calls")), NopSink{}, "m")
	var protocol *ProtocolError
	require.ErrorAs(t, err, &protocol)
}

func TestOpenAIParserRejectsInvalidToolJSONOnNormalFinish(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"read","arguments":"{"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`, "",
	}, "\n\n")
	_, err := parseOpenAIStream(context.Background(), strings.NewReader(stream), NopSink{}, "m")
	var protocol *ProtocolError
	require.ErrorAs(t, err, &protocol)
}

func TestOpenAITransportErrorIsNormalizedForRetry(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "temporary failure", Name: "provider.test", IsTemporary: true}
	})}
	provider, err := NewOpenAIProvider(OpenAIConfig{BaseURL: "https://provider.test", Model: "m", HTTPClient: client})
	require.NoError(t, err)
	_, err = provider.Stream(context.Background(), domain.CompletionRequest{}, NopSink{})
	require.Error(t, err)
	var providerErr *ProviderError
	require.ErrorAs(t, err, &providerErr)
	assert.True(t, IsRetryable(err))
	assert.Equal(t, "transport_error", providerErr.Code)

	permanentClient := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("certificate verification failed")
	})}
	permanentProvider, createErr := NewOpenAIProvider(OpenAIConfig{BaseURL: "https://provider.test", Model: "m", HTTPClient: permanentClient})
	require.NoError(t, createErr)
	_, err = permanentProvider.Stream(context.Background(), domain.CompletionRequest{}, NopSink{})
	require.Error(t, err)
	assert.False(t, IsRetryable(err))
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOpenAIMessagesEncodeManagedImageAsMultimodalContent(t *testing.T) {
	messages, err := toOpenAIMessages([]domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{
		{Kind: domain.ContentText, Text: "inspect"},
		{Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: "image", MIMEType: "image/png", Data: []byte{1, 2, 3}}},
	}}})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	parts, ok := messages[0].Content.([]openAIContentPart)
	require.True(t, ok)
	require.Len(t, parts, 2)
	assert.Equal(t, "text", parts[0].Type)
	assert.Equal(t, "image_url", parts[1].Type)
	assert.Equal(t, "data:image/png;base64,AQID", parts[1].ImageURL.URL)
}

func TestOpenAIMessagesRejectUnresolvedImage(t *testing.T) {
	_, err := toOpenAIMessages([]domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{
		{Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: "image", MIMEType: "image/png"}},
	}}})
	assert.ErrorContains(t, err, "unresolved")
}

func TestOpenAIRequestMapsToolResult(t *testing.T) {
	provider, _ := NewOpenAIProvider(OpenAIConfig{BaseURL: "http://localhost", Model: "m"})
	request, err := provider.buildRequest(domain.CompletionRequest{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentToolCall, ToolCall: &domain.ToolCall{ID: "c1", Name: "read", Arguments: json.RawMessage(`{}`)}}}},
		{Role: domain.RoleTool, Content: []domain.ContentBlock{{Kind: domain.ContentToolResult, ToolResult: &domain.ToolResult{ToolCallID: "c1", ToolName: "read", Content: "ok"}}}},
	}})
	require.NoError(t, err)
	require.Len(t, request.Messages, 2)
	assert.Equal(t, "assistant", request.Messages[0].Role)
	assert.Equal(t, "tool", request.Messages[1].Role)
	assert.Equal(t, "c1", request.Messages[1].ToolCallID)
}
