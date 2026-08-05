package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type ListTool struct {
	Jail       *workspace.Jail
	MaxEntries int
}

func (t *ListTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionReadOnly }

func (t *ListTool) RetryPolicy() domain.ToolRetryPolicy {
	return domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 2}
}

func (t *ListTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "ls", Description: "List a directory inside /workspace or the read-only /skills snapshot", Parameters: schema(pathSchema), RiskClass: domain.RiskReadOnly}
}

func (t *ListTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid ls arguments: %w", err)), nil
	}
	path, err := t.Jail.ResolveExisting(args.Path)
	if err != nil {
		return errorResult(call, err), nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return errorResult(call, err), nil
	}
	limit := t.MaxEntries
	if limit <= 0 {
		limit = 1000
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var lines []string
	for index, entry := range entries {
		if index >= limit {
			lines = append(lines, fmt.Sprintf("[truncated after %d entries]", limit))
			break
		}
		suffix := ""
		if entry.IsDir() {
			suffix = "/"
		} else if entry.Type()&os.ModeSymlink != 0 {
			suffix = "@"
		}
		lines = append(lines, entry.Name()+suffix)
	}
	if err := ctx.Err(); err != nil {
		return errorResult(call, err), nil
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: strings.Join(lines, "\n")}, nil
}
