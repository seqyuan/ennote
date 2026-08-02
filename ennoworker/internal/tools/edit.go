package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type EditTool struct {
	Jail     *workspace.Jail
	MaxBytes int64
}

func (t *EditTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionWorkspaceWrite }

func (t *EditTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "edit", Description: "Replace one unique text occurrence in a file inside /workspace", Parameters: schema(`{"type":"object","properties":{"path":{"type":"string"},"oldText":{"type":"string"},"newText":{"type":"string"}},"required":["path","oldText","newText"],"additionalProperties":false}`)}
}

func (t *EditTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"oldText"`
		NewText string `json:"newText"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid edit arguments: %w", err)), nil
	}
	if args.OldText == "" {
		return errorResult(call, fmt.Errorf("oldText must not be empty")), nil
	}
	path, err := t.Jail.ResolveExisting(args.Path)
	if err != nil {
		return errorResult(call, err), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return errorResult(call, err), nil
	}
	maxBytes := t.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	if info.Size() > maxBytes {
		return errorResult(call, fmt.Errorf("file exceeds edit limit of %d bytes", maxBytes)), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errorResult(call, err), nil
	}
	count := strings.Count(string(data), args.OldText)
	if count != 1 {
		return errorResult(call, fmt.Errorf("oldText must match exactly once; matched %d times", count)), nil
	}
	if err := ctx.Err(); err != nil {
		return errorResult(call, err), nil
	}
	updated := strings.Replace(string(data), args.OldText, args.NewText, 1)
	if err := atomicWrite(path, []byte(updated), info.Mode().Perm()); err != nil {
		return errorResult(call, err), nil
	}
	display, _ := t.Jail.DisplayPath(path)
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "edited " + display}, nil
}
