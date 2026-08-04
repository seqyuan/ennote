package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type OpenAIConfig struct {
	BaseURL    string
	APIKey     Secret
	Model      string
	MaxTokens  int
	HTTPClient *http.Client
}

type OpenAIProvider struct {
	config OpenAIConfig
	client *http.Client
}

func NewOpenAIProvider(config OpenAIConfig) (*OpenAIProvider, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("OpenAI-compatible base URL is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &OpenAIProvider{config: config, client: client}, nil
}

func (p *OpenAIProvider) Capabilities() ModelCapabilities {
	return ModelCapabilities{Streaming: true, ToolUse: true, Thinking: true}
}

func (p *OpenAIProvider) Stream(ctx context.Context, request domain.CompletionRequest, sink StreamSink) (domain.Completion, error) {
	wireRequest, err := p.buildRequest(request)
	if err != nil {
		return domain.Completion{}, &ProtocolError{Message: "build provider request", Cause: err}
	}
	body, err := json.Marshal(wireRequest)
	if err != nil {
		return domain.Completion{}, &ProtocolError{Message: "encode provider request", Cause: err}
	}

	endpoint := strings.TrimRight(p.config.BaseURL, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return domain.Completion{}, &ProtocolError{Message: "create provider request", Cause: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if p.config.APIKey.Reveal() != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.config.APIKey.Reveal())
	}

	response, err := p.client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return domain.Completion{}, ErrCancelled
		}
		return domain.Completion{}, &ProviderError{
			Code: "transport_error", Message: "provider request failed",
			Retryable: isRetryableTransportError(err), Cause: err,
		}
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.Completion{}, p.decodeError(response)
	}

	requestedModel := request.Model
	if requestedModel == "" {
		requestedModel = p.config.Model
	}
	completion, err := parseOpenAIStream(ctx, response.Body, sink, requestedModel)
	if errors.Is(ctx.Err(), context.Canceled) {
		return domain.Completion{}, ErrCancelled
	}
	return completion, err
}

func (p *OpenAIProvider) buildRequest(request domain.CompletionRequest) (openAIRequest, error) {
	messages, err := toOpenAIMessages(request.Messages)
	if err != nil {
		return openAIRequest{}, err
	}
	model := request.Model
	if model == "" {
		model = p.config.Model
	}
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.config.MaxTokens
	}
	wire := openAIRequest{
		Model:         model,
		Messages:      messages,
		Stream:        true,
		MaxTokens:     maxTokens,
		Temperature:   request.Temperature,
		StreamOptions: map[string]bool{"include_usage": true},
	}
	if request.Reasoning != nil {
		if request.Reasoning.Dialect != domain.ThinkingDialectOpenAIReasoningEffort {
			return openAIRequest{}, fmt.Errorf("unsupported thinking dialect %q", request.Reasoning.Dialect)
		}
		switch request.Reasoning.Effort {
		case domain.ThinkingLow, domain.ThinkingMedium, domain.ThinkingHigh:
			effort := string(request.Reasoning.Effort)
			wire.ReasoningEffort = &effort
		default:
			return openAIRequest{}, fmt.Errorf("unsupported thinking effort %q", request.Reasoning.Effort)
		}
	}
	for _, tool := range request.Tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		wire.Tools = append(wire.Tools, openAITool{Type: "function", Function: openAIFunctionDefinition{
			Name: tool.Name, Description: tool.Description, Parameters: parameters,
		}})
	}
	return wire, nil
}

func (p *OpenAIProvider) decodeError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	message := strings.TrimSpace(payload.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	if secret := p.config.APIKey.Reveal(); secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	code := strings.TrimSpace(fmt.Sprint(payload.Error.Code))
	if code == "<nil>" || code == "" {
		code = payload.Error.Type
	}
	if code == "" {
		code = "http_error"
	}
	return &ProviderError{
		StatusCode: response.StatusCode,
		Code:       code,
		Message:    message,
		Retryable:  isRetryableStatus(response.StatusCode),
		RequestID:  firstHeader(response.Header, "x-request-id", "request-id", "x-amzn-requestid"),
	}
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func isRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests ||
		(status >= 500 && status <= 599)
}

func isRetryableTransportError(err error) bool {
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return true
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

type openAIRequest struct {
	Model           string          `json:"model"`
	Messages        []openAIMessage `json:"messages"`
	Tools           []openAITool    `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	StreamOptions   map[string]bool `json:"stream_options,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ReasoningEffort *string         `json:"reasoning_effort,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAITool struct {
	Type     string                   `json:"type"`
	Function openAIFunctionDefinition `json:"function"`
}

type openAIFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func toOpenAIMessages(messages []domain.ChatMessage) ([]openAIMessage, error) {
	if err := validateOpenAIToolMessageSequence(messages); err != nil {
		return nil, err
	}
	var result []openAIMessage
	for _, message := range messages {
		wire := openAIMessage{Role: string(message.Role)}
		var text strings.Builder
		var contentParts []openAIContentPart
		for _, block := range message.Content {
			switch block.Kind {
			case domain.ContentText, domain.ContentContextSummary:
				text.WriteString(block.Text)
				contentParts = append(contentParts, openAIContentPart{Type: "text", Text: block.Text})
			case domain.ContentThinking:
				// Thinking is not replayed to provider APIs as visible content.
			case domain.ContentToolCall:
				if block.ToolCall == nil {
					return nil, fmt.Errorf("tool call block is missing payload")
				}
				wire.ToolCalls = append(wire.ToolCalls, openAIToolCall{
					ID: block.ToolCall.ID, Type: "function",
					Function: openAIFunctionCall{Name: block.ToolCall.Name, Arguments: string(block.ToolCall.Arguments)},
				})
			case domain.ContentImage:
				if block.Image == nil || len(block.Image.Data) == 0 {
					return nil, fmt.Errorf("image block %q is unresolved", func() string {
						if block.Image == nil {
							return ""
						}
						return block.Image.ArtifactID
					}())
				}
				contentParts = append(contentParts, openAIContentPart{Type: "image_url", ImageURL: &openAIImageURL{
					URL: "data:" + block.Image.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(block.Image.Data),
				}})
			case domain.ContentImageDescription:
				if block.ImageDescription == nil {
					return nil, fmt.Errorf("image description block is missing payload")
				}
				value := "[Automatically generated description of attached image: " + block.ImageDescription.Text + "]"
				text.WriteString(value)
				contentParts = append(contentParts, openAIContentPart{Type: "text", Text: value})
			case domain.ContentToolResult:
				if block.ToolResult == nil {
					return nil, fmt.Errorf("tool result block is missing payload")
				}
				result = append(result, openAIMessage{
					Role: "tool", Content: block.ToolResult.Content,
					ToolCallID: block.ToolResult.ToolCallID, Name: block.ToolResult.ToolName,
				})
			default:
				return nil, fmt.Errorf("unsupported content kind: %s", block.Kind)
			}
		}
		if len(contentParts) > 0 {
			hasImage := false
			for _, part := range contentParts {
				if part.Type == "image_url" {
					hasImage = true
					break
				}
			}
			if hasImage {
				wire.Content = contentParts
			} else {
				wire.Content = text.String()
			}
		}
		if wire.Content != nil || len(wire.ToolCalls) > 0 || message.Role != domain.RoleTool {
			result = append(result, wire)
		}
	}
	return result, nil
}

// validateOpenAIToolMessageSequence enforces the chat-completions protocol
// before making a network request. Every tool result must immediately follow
// the assistant tool_calls message and reference one of its LLM-assigned IDs.
func validateOpenAIToolMessageSequence(messages []domain.ChatMessage) error {
	pending := make(map[string]struct{})
	for messageIndex, message := range messages {
		if message.Role != domain.RoleTool && len(pending) != 0 {
			return fmt.Errorf("message %d has role %q before all preceding tool calls have results",
				messageIndex, message.Role)
		}

		toolCallCount := 0
		toolResultCount := 0
		for _, block := range message.Content {
			switch block.Kind {
			case domain.ContentToolCall:
				if message.Role != domain.RoleAssistant || block.ToolCall == nil || block.ToolCall.ID == "" {
					return fmt.Errorf("message %d has an invalid assistant tool call", messageIndex)
				}
				if _, duplicate := pending[block.ToolCall.ID]; duplicate {
					return fmt.Errorf("message %d repeats tool call id %q", messageIndex, block.ToolCall.ID)
				}
				pending[block.ToolCall.ID] = struct{}{}
				toolCallCount++
			case domain.ContentToolResult:
				if message.Role != domain.RoleTool || block.ToolResult == nil || block.ToolResult.ToolCallID == "" {
					return fmt.Errorf("message %d has an invalid tool result", messageIndex)
				}
				if _, ok := pending[block.ToolResult.ToolCallID]; !ok {
					return fmt.Errorf("message %d tool result references unknown tool call id %q",
						messageIndex, block.ToolResult.ToolCallID)
				}
				delete(pending, block.ToolResult.ToolCallID)
				toolResultCount++
			default:
				if message.Role == domain.RoleTool {
					return fmt.Errorf("message %d with role tool contains %q content", messageIndex, block.Kind)
				}
			}
		}
		if message.Role == domain.RoleTool && toolResultCount == 0 {
			return fmt.Errorf("message %d with role tool has no tool result", messageIndex)
		}
		if message.Role == domain.RoleAssistant && toolCallCount == 0 && len(pending) != 0 {
			return fmt.Errorf("message %d leaves an invalid pending tool call set", messageIndex)
		}
	}
	for toolCallID := range pending {
		return fmt.Errorf("assistant tool call %q has no following tool result", toolCallID)
	}
	return nil
}
