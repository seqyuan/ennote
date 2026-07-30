package domain

import (
	"context"
	"encoding/json"
)

type ExecutionClass string

const (
	ExecutionReadOnly       ExecutionClass = "read_only"
	ExecutionWorkspaceWrite ExecutionClass = "workspace_write"
	ExecutionExclusive      ExecutionClass = "exclusive"
)

type ToolRunner interface {
	Definitions() []ToolDefinition
	Execute(context.Context, ToolCall) ToolResult
}

type ToolExecutionClassifier interface {
	ExecutionClass(toolName string) ExecutionClass
}

type ToolArgumentValidator interface {
	ValidateArguments(toolName string, arguments json.RawMessage) error
}

type ToolExecutionConfig struct {
	Mode                   string `json:"mode"`
	MaxConcurrentReadTools int    `json:"maxConcurrentReadTools"`
}
