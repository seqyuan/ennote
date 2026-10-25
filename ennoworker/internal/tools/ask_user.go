package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// AskUserTool is a marker tool. The dispatcher intercepts it before execution
// and turns the call into an approval-checkpoint question; Execute is only a
// fail-closed guard for a caller that bypassed that path.
type AskUserTool struct{}

func (t *AskUserTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name:        "ask_user",
		Description: "Ask the user one to four structured questions and wait for the answers. Use only when the answer changes the next step. The call must be the only mutating call in its batch.",
		Parameters: json.RawMessage(`{
			"type":"object",
			"additionalProperties":false,
			"required":["questions"],
			"properties":{
				"questions":{
					"type":"array",
					"minItems":1,
					"maxItems":4,
					"items":{
						"type":"object",
						"additionalProperties":false,
						"required":["id","prompt","options"],
						"properties":{
							"id":{"type":"string","pattern":"^[a-zA-Z0-9_-]{1,32}$"},
							"prompt":{"type":"string","minLength":1,"maxLength":500},
							"options":{
								"type":"array",
								"minItems":2,
								"maxItems":8,
								"items":{
									"type":"object",
									"additionalProperties":false,
									"required":["label"],
									"properties":{
										"id":{"type":"string"},
										"label":{"type":"string","minLength":1,"maxLength":120},
										"description":{"type":"string","maxLength":300}
									}
								}
							},
							"multiSelect":{"type":"boolean"},
							"allowOther":{"type":"boolean"}
						}
					}
				}
			}
		}`),
		RiskClass: domain.RiskReadOnly,
	}
}

func (t *AskUserTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionReadOnly }

func (t *AskUserTool) Execute(context.Context, domain.ToolCall) (domain.ToolResult, error) {
	return domain.ToolResult{}, domain.NewCodedError(domain.ErrorToolPolicyFailed,
		fmt.Errorf("ask_user must be handled by the approval dispatcher"))
}
