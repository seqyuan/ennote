package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type WriteTool struct{ Jail *workspace.Jail }

func (t *WriteTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionWorkspaceWrite }

func (t *WriteTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "write", Description: "Atomically write a UTF-8 text file inside /workspace", Parameters: schema(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`), RiskClass: domain.RiskLocalWrite}
}

func (t *WriteTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid write arguments: %w", err)), nil
	}
	path, err := t.Jail.ResolveForWrite(args.Path)
	if err != nil {
		return errorResult(call, err), nil
	}
	if err := ctx.Err(); err != nil {
		return errorResult(call, err), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errorResult(call, err), nil
	}
	if err := atomicWrite(path, []byte(args.Content), 0o644); err != nil {
		return errorResult(call, err), nil
	}
	display, _ := t.Jail.DisplayPath(path)
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), display)}, nil
}

func atomicWrite(path string, content []byte, defaultMode os.FileMode) error {
	mode := defaultMode
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ennote-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
