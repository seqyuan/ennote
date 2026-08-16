package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const anthropicVersion = "2023-06-01"

type AnthropicConfig struct {
	BaseURL    string
	APIKey     Secret
	Model      string
	MaxTokens  int
	HTTPClient *http.Client
}

type AnthropicProvider struct {
	config AnthropicConfig
	client *http.Client
}

func NewAnthropicProvider(config AnthropicConfig) (*AnthropicProvider, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("Anthropic base URL is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &AnthropicProvider{config: config, client: client}, nil
}

func (p *AnthropicProvider) Capabilities() ModelCapabilities {
	return ModelCapabilities{Streaming: true, ToolUse: true, Thinking: false}
}

func (p *AnthropicProvider) Stream(ctx context.Context, request domain.CompletionRequest, sink StreamSink) (domain.Completion, error) {
	wireRequest, err := p.buildRequest(request)
	if err != nil {
		return domain.Completion{}, &ProtocolError{Message: "build provider request", Cause: err}
	}
	body, err := json.Marshal(wireRequest)
	if err != nil {
		return domain.Completion{}, &ProtocolError{Message: "encode provider request", Cause: err}
	}

	endpoint := strings.TrimRight(p.config.BaseURL, "/") + "/v1/messages"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return domain.Completion{}, &ProtocolError{Message: "create provider request", Cause: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("anthropic-version", anthropicVersion)
	if p.config.APIKey.Reveal() != "" {
		httpRequest.Header.Set("x-api-key", p.config.APIKey.Reveal())
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
	completion, err := parseAnthropicStream(ctx, response.Body, sink, requestedModel)
	if errors.Is(ctx.Err(), context.Canceled) {
		return domain.Completion{}, ErrCancelled
	}
	return completion, err
}

func (p *AnthropicProvider) buildRequest(request domain.CompletionRequest) (anthropicRequest, error) {
	system, messages, err := toAnthropicMessages(request.Messages)
	if err != nil {
		return anthropicRequest{}, err
	}
	model := request.Model
	if model == "" {
		model = p.config.Model
	}
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = p.config.MaxTokens
	}
	wire := anthropicRequest{
		Model: model, Messages: messages, System: system,
		MaxTokens: maxTokens, Stream: true,
	}
	if request.Temperature != nil {
		wire.Temperature = request.Temperature
	}
	if request.Reasoning != nil && request.Reasoning.Dialect != domain.ThinkingDialectNone {
		return anthropicRequest{}, fmt.Errorf("anthropic-messages does not support thinking dialect %q", request.Reasoning.Dialect)
	}
	for _, tool := range request.Tools {
		parameters := tool.Parameters
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		wire.Tools = append(wire.Tools, anthropicTool{
			Name: tool.Name, Description: tool.Description, InputSchema: parameters,
		})
	}
	return wire, nil
}

func (p *AnthropicProvider) decodeError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
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
	code := strings.TrimSpace(payload.Error.Type)
	if code == "" {
		code = "http_error"
	}
	return &ProviderError{
		StatusCode: response.StatusCode,
		Code:       code,
		Message:    message,
		Retryable:  isRetryableStatus(response.StatusCode),
		RequestID:  firstHeader(response.Header, "request-id", "x-request-id", "x-amzn-requestid"),
	}
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string                `json:"type"`
	Text      string                `json:"text,omitempty"`
	ID        string                `json:"id,omitempty"`          // tool_use
	Name      string                `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage       `json:"input,omitempty"`       // tool_use
	ToolUseID string                `json:"tool_use_id,omitempty"` // tool_result
	Content   string                `json:"content,omitempty"`     // tool_result
	IsError   bool                  `json:"is_error,omitempty"`    // tool_result
	Source    *anthropicImageSource `json:"source,omitempty"`      // image
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// toAnthropicMessages translates domain chat messages into the Anthropic
// Messages wire shape. The system role is folded into the top-level system
// field; tool results become user messages carrying tool_result blocks.
func toAnthropicMessages(messages []domain.ChatMessage) (string, []anthropicMessage, error) {
	var system strings.Builder
	result := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == domain.RoleSystem {
			for _, block := range message.Content {
				if block.Kind == domain.ContentText || block.Kind == domain.ContentContextSummary {
					system.WriteString(block.Text)
				}
			}
			continue
		}
		wire := anthropicMessage{Role: string(message.Role)}
		for _, block := range message.Content {
			switch block.Kind {
			case domain.ContentText, domain.ContentContextSummary:
				wire.Content = append(wire.Content, anthropicContent{Type: "text", Text: block.Text})
			case domain.ContentThinking:
				// Thinking is not replayed to provider APIs as visible content.
			case domain.ContentToolCall:
				if block.ToolCall == nil {
					return "", nil, fmt.Errorf("tool call block is missing payload")
				}
				input := block.ToolCall.Arguments
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				wire.Content = append(wire.Content, anthropicContent{
					Type: "tool_use", ID: block.ToolCall.ID, Name: block.ToolCall.Name, Input: input,
				})
			case domain.ContentToolResult:
				if block.ToolResult == nil {
					return "", nil, fmt.Errorf("tool result block is missing payload")
				}
				result = append(result, anthropicMessage{Role: "user", Content: []anthropicContent{{
					Type: "tool_result", ToolUseID: block.ToolResult.ToolCallID,
					Content: block.ToolResult.Content, IsError: block.ToolResult.IsError,
				}}})
			case domain.ContentImage:
				if block.Image == nil || len(block.Image.Data) == 0 {
					return "", nil, fmt.Errorf("image block is unresolved")
				}
				wire.Content = append(wire.Content, anthropicContent{
					Type: "image", Source: &anthropicImageSource{
						Type: "base64", MediaType: block.Image.MIMEType,
						Data: base64.StdEncoding.EncodeToString(block.Image.Data),
					},
				})
			case domain.ContentImageDescription:
				if block.ImageDescription == nil {
					return "", nil, fmt.Errorf("image description block is missing payload")
				}
				wire.Content = append(wire.Content, anthropicContent{
					Type: "text",
					Text: "[Automatically generated description of attached image: " + block.ImageDescription.Text + "]",
				})
			default:
				return "", nil, fmt.Errorf("unsupported content kind: %s", block.Kind)
			}
		}
		if len(wire.Content) > 0 {
			result = append(result, wire)
		}
	}
	return system.String(), result, nil
}

// parseAnthropicStream consumes the Anthropic Messages SSE stream and folds
// text, thinking, and tool_use blocks into a domain.Completion.
func parseAnthropicStream(ctx context.Context, reader io.Reader, sink StreamSink, requestedModel string) (domain.Completion, error) {
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var completion domain.Completion
	completion.ActualModel = requestedModel
	var text, thinking strings.Builder
	toolCalls := make(map[int]*toolCallBuilder)
	sawStop := false

	for {
		line, err := buffered.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return domain.Completion{}, fmt.Errorf("%w: read provider stream: %v", ErrIncompleteStream, err)
		}
		line = strings.TrimRight(line, "\r\n")

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data != "" {
				var event anthropicStreamEvent
				if decodeErr := json.Unmarshal([]byte(data), &event); decodeErr != nil {
					return domain.Completion{}, &ProtocolError{Message: "decode provider stream chunk", Cause: decodeErr}
				}
				switch event.Type {
				case "message_start":
					if event.Message != nil {
						if event.Message.Model != "" {
							completion.ActualModel = event.Message.Model
						}
						completion.Usage = event.Message.Usage.domainUsage()
						if sinkErr := sink.Usage(completion.Usage); sinkErr != nil {
							return domain.Completion{}, sinkErr
						}
					}
				case "content_block_start":
					if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
						toolCalls[event.Index] = &toolCallBuilder{
							id: event.ContentBlock.ID, name: event.ContentBlock.Name,
						}
					}
				case "content_block_delta":
					if event.Delta == nil {
						continue
					}
					switch event.Delta.Type {
					case "text_delta":
						text.WriteString(event.Delta.Text)
						if sinkErr := sink.TextDelta(event.Delta.Text); sinkErr != nil {
							return domain.Completion{}, sinkErr
						}
					case "thinking_delta":
						thinking.WriteString(event.Delta.Thinking)
						if sinkErr := sink.ThinkingDelta(event.Delta.Thinking); sinkErr != nil {
							return domain.Completion{}, sinkErr
						}
					case "input_json_delta":
						builder := toolCalls[event.Index]
						if builder == nil {
							builder = &toolCallBuilder{}
							toolCalls[event.Index] = builder
						}
						builder.arguments.WriteString(event.Delta.PartialJSON)
						if sinkErr := sink.ToolCallDelta(ToolCallDelta{
							Index: event.Index, ID: builder.id, Name: builder.name,
							ArgumentsFragment: event.Delta.PartialJSON,
						}); sinkErr != nil {
							return domain.Completion{}, sinkErr
						}
					}
				case "message_delta":
					if event.Delta != nil && event.Delta.StopReason != "" {
						completion.StopReason = mapAnthropicStopReason(event.Delta.StopReason)
					}
					if event.Usage != nil {
						completion.Usage.OutputTokens = event.Usage.OutputTokens
						if sinkErr := sink.Usage(completion.Usage); sinkErr != nil {
							return domain.Completion{}, sinkErr
						}
					}
				case "message_stop":
					sawStop = true
				}
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		select {
		case <-ctx.Done():
			return domain.Completion{}, ErrCancelled
		default:
		}
	}

	if !sawStop || completion.StopReason == "" {
		return domain.Completion{}, ErrIncompleteStream
	}
	if thinking.Len() > 0 {
		completion.Content = append(completion.Content, domain.ContentBlock{Kind: domain.ContentThinking, Text: thinking.String()})
	}
	if text.Len() > 0 {
		completion.Content = append(completion.Content, domain.ContentBlock{Kind: domain.ContentText, Text: text.String()})
	}
	for index, builder := range toolCalls {
		fragment := strings.TrimSpace(builder.arguments.String())
		arguments := fragment
		if arguments == "" {
			arguments = "{}"
		}
		if !json.Valid([]byte(arguments)) {
			if completion.StopReason != domain.StopReasonLength {
				return domain.Completion{}, &ProtocolError{Message: fmt.Sprintf("tool call %d has invalid JSON arguments", index)}
			}
			arguments = "{}"
		}
		if completion.StopReason != domain.StopReasonLength && (builder.id == "" || builder.name == "") {
			return domain.Completion{}, &ProtocolError{Message: fmt.Sprintf("tool call %d is missing id or name", index)}
		}
		completion.ToolCalls = append(completion.ToolCalls, domain.ToolCall{
			ID: builder.id, Name: builder.name, Arguments: json.RawMessage(arguments),
			ArgumentsFragment: fragment, Partial: completion.StopReason == domain.StopReasonLength,
		})
	}
	return completion, nil
}

func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "tool_use":
		return domain.StopReasonToolCalls
	case "max_tokens":
		return domain.StopReasonLength
	case "end_turn", "stop_sequence":
		return domain.StopReasonStop
	default:
		return domain.StopReasonStop
	}
}

type anthropicStreamEvent struct {
	Type         string                 `json:"type"`
	Message      *anthropicStreamMsg    `json:"message"`
	Index        int                    `json:"index"`
	ContentBlock *anthropicContentBlock `json:"content_block"`
	Delta        *anthropicDelta        `json:"delta"`
	Usage        *anthropicUsage        `json:"usage"`
}

type anthropicStreamMsg struct {
	ID    string         `json:"id"`
	Model string         `json:"model"`
	Usage anthropicUsage `json:"usage"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	Thinking    string `json:"thinking"`
	StopReason  string `json:"stop_reason"`
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

func (u anthropicUsage) domainUsage() domain.Usage {
	return domain.Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CachedTokens: u.CacheReadInputTokens + u.CacheCreationInputTokens,
	}
}
