package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type PublishArtifactTool struct {
	Jail *workspace.Jail
	Sink *ArtifactSink
}

func (t *PublishArtifactTool) ExecutionClass() domain.ExecutionClass {
	return domain.ExecutionWorkspaceWrite
}

func (t *PublishArtifactTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name:        "publish_artifact",
		Description: "Publish one completed Workspace file as an immutable conversation artifact. Files are never published automatically.",
		Parameters:  schema(`{"type":"object","properties":{"path":{"type":"string","description":"Workspace path of the completed file"},"name":{"type":"string","description":"Optional download filename"}},"required":["path"],"additionalProperties":false}`),
	}
}

func (t *PublishArtifactTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid publish_artifact arguments: %w", err)), nil
	}
	if strings.TrimSpace(args.Path) == "" {
		return errorResult(call, fmt.Errorf("path must not be empty")), nil
	}
	resolved, err := t.Jail.ResolveWorkspaceExisting(args.Path)
	if err != nil {
		return errorResult(call, err), nil
	}
	file, err := os.Open(resolved)
	if err != nil {
		return errorResult(call, err), nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return errorResult(call, err), nil
	}
	if !info.Mode().IsRegular() {
		return errorResult(call, fmt.Errorf("artifact source must be a regular file")), nil
	}
	displayPath, err := openedWorkspacePath(t.Jail, file)
	if err != nil {
		return errorResult(call, err), nil
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		name = filepath.Base(resolved)
	}
	reference, err := t.Sink.Publish(ctx, call.ID, name, "workspace_publish", displayPath, file)
	if err != nil {
		return errorResult(call, err), nil
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
		Content:   describeArtifacts([]domain.ArtifactReference{reference}),
		Artifacts: []domain.ArtifactReference{reference}}, nil
}

func openedWorkspacePath(jail *workspace.Jail, file *os.File) (string, error) {
	for _, descriptorRoot := range []string{"/proc/self/fd", "/dev/fd"} {
		descriptorPath := filepath.Join(descriptorRoot, fmt.Sprint(file.Fd()))
		canonical, err := filepath.EvalSymlinks(descriptorPath)
		if err != nil {
			continue
		}
		return jail.DisplayWorkspacePath(canonical)
	}
	return "", fmt.Errorf("secure artifact source verification is unavailable on this platform")
}
