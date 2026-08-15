package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

func parseOpenAIStream(ctx context.Context, reader io.Reader, sink StreamSink, requestedModel string) (domain.Completion, error) {
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var completion domain.Completion
	completion.ActualModel = requestedModel
	var text, thinking strings.Builder
	toolCalls := make(map[int]*toolCallBuilder)
	sawTerminal := false

	for {
		line, err := buffered.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return domain.Completion{}, fmt.Errorf("%w: read provider stream: %v", ErrIncompleteStream, err)
		}
		line = strings.TrimRight(line, "\r\n")

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				sawTerminal = true
				break
			}
			if data != "" {
				var chunk openAIChunk
				if decodeErr := json.Unmarshal([]byte(data), &chunk); decodeErr != nil {
					return domain.Completion{}, &ProtocolError{Message: "decode provider stream chunk", Cause: decodeErr}
				}
				if chunk.Model != "" {
					completion.ActualModel = chunk.Model
				}
				if chunk.Usage != nil {
					completion.Usage = chunk.Usage.domainUsage()
					if sinkErr := sink.Usage(completion.Usage); sinkErr != nil {
						return domain.Completion{}, sinkErr
					}
				}
				for _, choice := range chunk.Choices {
					if choice.Delta.Content != "" {
						text.WriteString(choice.Delta.Content)
						if sinkErr := sink.TextDelta(choice.Delta.Content); sinkErr != nil {
							return domain.Completion{}, sinkErr
						}
					}
					reasoningDelta := choice.Delta.ReasoningContent
					if reasoningDelta == "" {
						reasoningDelta = choice.Delta.Reasoning
					}
					if reasoningDelta != "" {
						thinking.WriteString(reasoningDelta)
						if sinkErr := sink.ThinkingDelta(reasoningDelta); sinkErr != nil {
							return domain.Completion{}, sinkErr
						}
					}
					for _, delta := range choice.Delta.ToolCalls {
						builder := toolCalls[delta.Index]
						if builder == nil {
							builder = &toolCallBuilder{}
							toolCalls[delta.Index] = builder
						}
						if delta.ID != "" {
							builder.id = delta.ID
						}
						if delta.Function.Name != "" {
							builder.name = delta.Function.Name
						}
						builder.arguments.WriteString(delta.Function.Arguments)
						if sinkErr := sink.ToolCallDelta(ToolCallDelta{
							Index: delta.Index, ID: delta.ID, Name: delta.Function.Name,
							ArgumentsFragment: delta.Function.Arguments,
						}); sinkErr != nil {
							return domain.Completion{}, sinkErr
						}
					}
					if choice.FinishReason != nil {
						completion.StopReason = domain.StopReason(*choice.FinishReason)
						sawTerminal = true
					}
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

	if !sawTerminal || completion.StopReason == "" {
		return domain.Completion{}, ErrIncompleteStream
	}
	if thinking.Len() > 0 {
		completion.Content = append(completion.Content, domain.ContentBlock{Kind: domain.ContentThinking, Text: thinking.String()})
	}
	if text.Len() > 0 {
		completion.Content = append(completion.Content, domain.ContentBlock{Kind: domain.ContentText, Text: text.String()})
	}
	indexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for position, index := range indexes {
		builder := toolCalls[index]
		fragment := strings.TrimSpace(builder.arguments.String())
		arguments := fragment
		if arguments == "" {
			arguments = "{}"
		}
		truncated := completion.StopReason == domain.StopReasonLength
		if !truncated && index != position {
			return domain.Completion{}, &ProtocolError{Message: fmt.Sprintf("tool call index %d is not contiguous", index)}
		}
		if !truncated && (builder.id == "" || builder.name == "") {
			return domain.Completion{}, &ProtocolError{Message: fmt.Sprintf("tool call %d is missing id or name", index)}
		}
		if !json.Valid([]byte(arguments)) {
			if !truncated {
				return domain.Completion{}, &ProtocolError{Message: fmt.Sprintf("tool call %d has invalid JSON arguments", index)}
			}
			arguments = "{}"
		}
		completion.ToolCalls = append(completion.ToolCalls, domain.ToolCall{
			ID: builder.id, Name: builder.name, Arguments: json.RawMessage(arguments),
			ArgumentsFragment: fragment, Partial: truncated,
		})
	}
	return completion, nil
}

type toolCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

type openAIChunk struct {
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage"`
}

type openAIChoice struct {
	Delta        openAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type openAIDelta struct {
	Content          string                `json:"content"`
	ReasoningContent string                `json:"reasoning_content"`
	Reasoning        string                `json:"reasoning"`
	ToolCalls        []openAIToolCallDelta `json:"tool_calls"`
}

type openAIToolCallDelta struct {
	Index    int                     `json:"index"`
	ID       string                  `json:"id"`
	Function openAIFunctionCallDelta `json:"function"`
}

type openAIFunctionCallDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIUsage struct {
	PromptTokens         int64 `json:"prompt_tokens"`
	CompletionTokens     int64 `json:"completion_tokens"`
	CachedTokens         int64 `json:"cached_tokens"`
	ReasoningTokens      int64 `json:"reasoning_tokens"`
	PromptCacheHitTokens int64 `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u openAIUsage) domainUsage() domain.Usage {
	// Cache-hit precedence: DeepSeek's native prompt_cache_hit_tokens first
	// (what deepseek-chat/deepseek-reasoner return), then the OpenAI-compat
	// prompt_tokens_details.cached_tokens, then the bare cached_tokens alias.
	cached := u.PromptCacheHitTokens
	if cached == 0 {
		cached = u.PromptTokensDetails.CachedTokens
	}
	if cached == 0 {
		cached = u.CachedTokens
	}
	reasoning := u.ReasoningTokens
	if reasoning == 0 {
		reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	return domain.Usage{
		InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens,
		CachedTokens: cached, ReasoningTokens: reasoning,
	}
}
