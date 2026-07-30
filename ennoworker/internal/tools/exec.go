package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type ExecTool struct {
	Workspace           *workspace.Manager
	Timeout             time.Duration
	OutputLimit         int
	OutputArtifactLimit int64
	Artifacts           *ArtifactSink
}

func (t *ExecTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionExclusive }

func (t *ExecTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "exec", Description: "Execute one program with a structured argument vector in the current workspace",
		Parameters: schema(`{"type":"object","properties":{"argv":{"type":"array","items":{"type":"string"},"minItems":1},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":3600}},"required":["argv"],"additionalProperties":false}`)}
}

func (t *ExecTool) Execute(ctx context.Context, call domain.ToolCall) domain.ToolResult {
	var args struct {
		Argv           []string `json:"argv"`
		TimeoutSeconds int      `json:"timeoutSeconds"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid exec arguments: %w", err))
	}
	if len(args.Argv) == 0 || strings.TrimSpace(args.Argv[0]) == "" {
		return errorResult(call, fmt.Errorf("argv must contain an executable"))
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := t.Workspace.CommandArgs(args.Argv[0], args.Argv[1:]...)
	if err != nil {
		return errorResult(call, err)
	}
	cmd.Env = safeEnvironment(t.Workspace.RuntimeDir)
	stdout, err := newOutputCapture(t.Workspace.RuntimeDir, "stdout", t.OutputLimit, t.OutputArtifactLimit)
	if err != nil {
		return errorResult(call, err)
	}
	defer stdout.Cleanup()
	stderr, err := newOutputCapture(t.Workspace.RuntimeDir, "stderr", t.OutputLimit, t.OutputArtifactLimit)
	if err != nil {
		return errorResult(call, err)
	}
	defer stderr.Cleanup()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	runErr := runProcessGroup(runCtx, cmd)
	references, notices, artifactErr := collectOutputArtifacts(context.WithoutCancel(ctx), t.Artifacts, call.ID, stdout, stderr)
	content := formatCommandOutput(stdout.String(), stderr.String())
	for _, notice := range notices {
		if content != "" {
			content += "\n"
		}
		content += notice
	}
	if runErr != nil {
		if content != "" {
			content += "\n"
		}
		content += runErr.Error()
	}
	if artifactErr != nil {
		if content != "" {
			content += "\n"
		}
		content += artifactErr.Error()
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: content,
		IsError: runErr != nil || artifactErr != nil, Artifacts: references}
}
