package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type ReadTool struct {
	Jail     *workspace.Jail
	MaxBytes int64
}

func (t *ReadTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionReadOnly }

func (t *ReadTool) RetryPolicy() domain.ToolRetryPolicy {
	return domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 2}
}

func (t *ReadTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "read", Description: "Read a UTF-8 text file inside /workspace or the read-only /skills snapshot", Parameters: schema(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`)}
}

func (t *ReadTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int64  `json:"offset"`
		Limit  int64  `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid read arguments: %w", err)), nil
	}
	path, err := t.Jail.ResolveExisting(args.Path)
	if err != nil {
		return errorResult(call, err), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return errorResult(call, err), nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return errorResult(call, err), nil
	}
	if !info.Mode().IsRegular() {
		return errorResult(call, fmt.Errorf("read path is not a regular file")), nil
	}
	if _, err := file.Seek(args.Offset, io.SeekStart); err != nil {
		return errorResult(call, err), nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = t.MaxBytes
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	if t.MaxBytes > 0 && limit > t.MaxBytes {
		limit = t.MaxBytes
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return errorResult(call, err), nil
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	content := string(data)
	if truncated {
		content += fmt.Sprintf("\n[truncated after %d bytes]", limit)
	}
	select {
	case <-ctx.Done():
		return errorResult(call, ctx.Err()), nil
	default:
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: content}, nil
}
