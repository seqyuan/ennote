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

func (t *ReadTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "read", Description: "Read a UTF-8 text file inside /workspace", Parameters: schema(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`)}
}

func (t *ReadTool) Execute(ctx context.Context, call domain.ToolCall) domain.ToolResult {
	var args struct {
		Path   string `json:"path"`
		Offset int64  `json:"offset"`
		Limit  int64  `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid read arguments: %w", err))
	}
	path, err := t.Jail.ResolveExisting(args.Path)
	if err != nil {
		return errorResult(call, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return errorResult(call, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return errorResult(call, err)
	}
	if !info.Mode().IsRegular() {
		return errorResult(call, fmt.Errorf("read path is not a regular file"))
	}
	if _, err := file.Seek(args.Offset, io.SeekStart); err != nil {
		return errorResult(call, err)
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
		return errorResult(call, err)
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
		return errorResult(call, ctx.Err())
	default:
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: content}
}
