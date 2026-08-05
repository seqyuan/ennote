package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// TodoTool is the stateful todo-list tool. It writes the submitted list into
// Store, replacing any previous list, and returns a rendered progress view.
// The model submits the WHOLE list each call (not incremental edits). Mirrors
// Claude Code's TodoWrite.
type TodoTool struct {
	// Store holds the run task list. Must be non-nil.
	Store *domain.TodoStore
}

// Definition implements Tool.
func (t *TodoTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name: "todo",
		Description: "Record and update a structured task list to plan and track multi-step " +
			"work. Submit the ENTIRE list every call; it replaces the previous list. " +
			"Each item has a content string and a status of pending, in_progress, or " +
			"completed. Keep exactly one item in_progress at a time and mark items " +
			"completed as soon as they are done.",
		RiskClass: domain.RiskReadOnly,
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "todos": {
      "type": "array",
      "description": "The full task list, replacing any previous list.",
      "maxItems": 50,
      "items": {
        "type": "object",
        "properties": {
          "content": {"type": "string", "minLength": 1, "maxLength": 500, "description": "Task description."},
          "status":  {"type": "string", "enum": ["pending", "in_progress", "completed"], "description": "Task lifecycle state."}
        },
        "required": ["content", "status"],
        "additionalProperties": false
      }
    }
  },
  "required": ["todos"],
  "additionalProperties": false
}`),
	}
}

// ExecutionClass implements ClassifiedTool. Updating the shared list mutates
// run state → exclusive so a batch cannot interleave two list writes or run it
// alongside read-only tools.
func (t *TodoTool) ExecutionClass() domain.ExecutionClass {
	return domain.ExecutionExclusive
}

// Execute implements Tool. It validates every item before storing and returns
// the rendered progress as the result content.
func (t *TodoTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	if t.Store == nil {
		return errorResult(call, fmt.Errorf("todo store is not configured")), nil
	}

	var args struct {
		Todos []domain.TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid todo arguments: %w", err)), nil
	}

	if len(args.Todos) > domain.MaxTodoItems {
		return errorResult(call, fmt.Errorf("at most %d todo items allowed", domain.MaxTodoItems)), nil
	}

	inProgress := 0
	for i, item := range args.Todos {
		if strings.TrimSpace(item.Content) == "" {
			return errorResult(call, fmt.Errorf("todo item %d has empty content", i+1)), nil
		}
		if utf8.RuneCountInString(item.Content) > domain.MaxTodoContentRunes {
			return errorResult(call, fmt.Errorf("todo item %d content exceeds %d characters", i+1, domain.MaxTodoContentRunes)), nil
		}
		if !domain.ValidTodoStatus(item.Status) {
			return errorResult(call, fmt.Errorf("todo item %d has invalid status %q (want pending|in_progress|completed)", i+1, item.Status)), nil
		}
		if item.Status == domain.TodoInProgress {
			inProgress++
		}
	}
	if inProgress > 1 {
		return errorResult(call, fmt.Errorf("at most one todo item can be in_progress, found %d", inProgress)), nil
	}

	t.Store.Set(args.Todos)

	return domain.ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    domain.RenderTodoList(args.Todos),
	}, nil
}
