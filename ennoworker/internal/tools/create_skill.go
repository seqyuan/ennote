package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// CreateSkillInput is the portable request shape for authoring one Skill.
type CreateSkillInput struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Prompt       string   `json:"prompt"`
	AllowedTools []string `json:"allowedTools"`
}

// CreateSkillResult is the compact result returned to the model.
type CreateSkillResult struct {
	ID           string
	Path         string
	Description  string
	AllowedTools []string
}

// CreateSkillTool lets the agent author a reusable Skill in the Ennote user
// skills root. The write happens in the Worker process, not in the workspace
// sandbox, so it works in bwrap mode too. The caller injects the actual
// implementation (skillsmgmt.Service.Create), keeping this package free of a
// dependency on the management service.
type CreateSkillTool struct {
	Create func(CreateSkillInput) (CreateSkillResult, error)
}

func (t *CreateSkillTool) ExecutionClass() domain.ExecutionClass {
	return domain.ExecutionExclusive
}

func (t *CreateSkillTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name: "create_skill",
		Description: "Create a new reusable Skill in the Ennote user skills root. " +
			"Use this only when the user asks to turn a workflow into a Skill; the new Skill is " +
			"available to Flows and Roles immediately without restarting the Worker.",
		Parameters: schema(`{"type":"object","properties":{` +
			`"id":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$"},` +
			`"name":{"type":"string"},` +
			`"description":{"type":"string"},` +
			`"prompt":{"type":"string","minLength":1},` +
			`"allowedTools":{"type":"array","items":{"type":"string"}}` +
			`},"required":["id","prompt"],"additionalProperties":false}`),
		RiskClass: domain.RiskLocalWrite,
	}
}

func (t *CreateSkillTool) Execute(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	if t.Create == nil {
		return errorResult(call, fmt.Errorf("create_skill is not configured")), nil
	}
	var input CreateSkillInput
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		return errorResult(call, fmt.Errorf("invalid create_skill arguments: %w", err)), nil
	}
	result, err := t.Create(input)
	if err != nil {
		return errorResult(call, err), nil
	}
	content := fmt.Sprintf("Created skill %q at %s. It is available to Flows and Roles immediately; no Worker restart is required.", result.ID, result.Path)
	if result.Description != "" {
		content += "\nDescription: " + result.Description
	}
	if len(result.AllowedTools) > 0 {
		content += "\nAllowed tools: " + joinComma(result.AllowedTools)
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: content}, nil
}

func joinComma(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ", "
		}
		result += value
	}
	return result
}
