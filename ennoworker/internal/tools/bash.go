package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type BashTool struct {
	Workspace           *workspace.Manager
	Shell               string
	Timeout             time.Duration
	OutputLimit         int
	OutputArtifactLimit int64
	Artifacts           *ArtifactSink
}

func (t *BashTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionExclusive }

func (t *BashTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "bash", Description: "Execute a shell command in the current workspace", Parameters: schema(`{"type":"object","properties":{"command":{"type":"string"},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":3600}},"required":["command"],"additionalProperties":false}`)}
}

func (t *BashTool) Execute(ctx context.Context, call domain.ToolCall) domain.ToolResult {
	var args struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid bash arguments: %w", err))
	}
	if strings.TrimSpace(args.Command) == "" {
		return errorResult(call, fmt.Errorf("command must not be empty"))
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
	cmd, err := t.Workspace.Command(t.Shell, args.Command)
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

func safeEnvironment(runtimeDir string) []string {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true, "TZ": true}
	var environment []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if allowed[name] {
			environment = append(environment, entry)
		}
	}
	if runtimeDir != "" {
		environment = append(environment, "HOME="+runtimeDir, "ENNOTE_RUNTIME="+runtimeDir)
	}
	return environment
}

func formatCommandOutput(stdout, stderr string) string {
	switch {
	case stdout != "" && stderr != "":
		return stdout + "\n[stderr]\n" + stderr
	case stdout != "":
		return stdout
	default:
		return stderr
	}
}
