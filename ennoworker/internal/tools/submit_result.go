package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// SubmitResultTool is the internal terminal-contract tool exposed only to child
// Runs. The Agent Loop intercepts calls to this tool before tool-policy
// dispatch; Execute is a defensive no-op that should never be reached.
type SubmitResultTool struct{}

func (t *SubmitResultTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionReadOnly }

func (t *SubmitResultTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name: "submit_result",
		Description: "End this delegated Run with a structured result. Call this exactly once as the ONLY tool call of the final turn, " +
			"with status one of completed|blocked|needs_input, a bounded summary, and optional artifact references or a JSON payload. " +
			"The result is returned to the calling parent; it is never published to the conversation.",
		Parameters: schema(`{"type":"object","required":["status","summary"],"properties":{
			"status":{"type":"string","enum":["completed","blocked","needs_input"]},
			"summary":{"type":"string","minLength":1,"maxLength":4096},
			"artifactRefs":{"type":"array","maxItems":32,"items":{"type":"object","required":["artifactId","name","kind","mimeType","sha256"],"properties":{"artifactId":{"type":"string"},"name":{"type":"string"},"kind":{"type":"string"},"mimeType":{"type":"string"},"sha256":{"type":"string"}}}},
			"payload":{"type":"object"}
		},"additionalProperties":false}`),
	}
}

func (t *SubmitResultTool) Execute(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	return errorResult(call, fmt.Errorf("submit_result is intercepted by the Agent Loop and must not be dispatched as a regular tool")), nil
}

// ValidateSubmitResult parses and validates submit_result arguments into the
// structured terminal contract.
func ValidateSubmitResult(arguments json.RawMessage) (*domain.SubmitResult, error) {
	return domain.ValidateSubmitResult(arguments)
}
