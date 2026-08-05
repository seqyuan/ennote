package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

// Read-only git subcommands the Workspace Explorer may invoke. Each entry is a
// prefix match on the subcommand name; arguments are validated per-command.
var gitReadonlySubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true,
	"ls-files": true, "blame": true,
}

// GitReadonlyTool inspects git history/status without mutating the repository
// or the workspace. It is classified ExecutionReadOnly so discuss/ask policy
// gates allow it alongside read-only filesystem tools.
type GitReadonlyTool struct {
	// GitBinary defaults to "git" when empty.
	GitBinary string
	// Workspace is required in production so bwrap mode uses a read-only mount.
	Workspace *workspace.Manager
	// WorkingDir is retained for isolated SandboxNone unit tests.
	WorkingDir string
	Timeout    time.Duration
	MaxOutput  int
}

func (t *GitReadonlyTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionReadOnly }

func (t *GitReadonlyTool) RetryPolicy() domain.ToolRetryPolicy {
	return domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 2}
}

func (t *GitReadonlyTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name: "git_readonly",
		Description: "Run a read-only git subcommand (status, diff, log, show, ls-files, blame) inside /workspace. " +
			"Never mutates the repository, invokes helpers, or reads paths outside the workspace.",
		Parameters: schema(`{"type":"object","properties":{
			"subcommand":{"type":"string","enum":["status","diff","log","show","ls-files","blame"]},
			"args":{"type":"array","items":{"type":"string"},"maxItems":12},
			"maxOutputLines":{"type":"integer","minimum":1,"maximum":500}
		},"required":["subcommand"],"additionalProperties":false}`),
		RiskClass: domain.RiskReadOnly,
	}
}

func (t *GitReadonlyTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	var args struct {
		Subcommand     string   `json:"subcommand"`
		Args           []string `json:"args"`
		MaxOutputLines int      `json:"maxOutputLines"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid git_readonly arguments: %w", err)), nil
	}
	subcommand := strings.TrimSpace(args.Subcommand)
	if !gitReadonlySubcommands[subcommand] {
		return errorResult(call, fmt.Errorf("git subcommand %q is not read-only", subcommand)), nil
	}
	if args.MaxOutputLines <= 0 {
		args.MaxOutputLines = 200
	}
	if args.MaxOutputLines > 500 {
		args.MaxOutputLines = 500
	}

	// Block options that can invoke repository-configured programs, switch the
	// object/work tree, or make a nominally read-only command consume an
	// arbitrary host file. Prefix checks cover both --flag=value and split forms.
	dangerous := []string{"--output", "-o", "--work-tree", "--git-dir", "-c", "--config-env",
		"--ext-diff", "--textconv", "--no-index", "--contents", "--ignore-revs-file",
		"--exclude-from", "--pathspec-from-file", "--upload-pack", "--exec"}
	for _, argument := range args.Args {
		for _, denied := range dangerous {
			if argument == denied || strings.HasPrefix(argument, denied+"=") {
				return errorResult(call, fmt.Errorf("git option %s is not allowed", denied)), nil
			}
		}
	}

	binary := t.GitBinary
	if binary == "" {
		binary = "git"
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Override execution-capable local config and force built-in diff behavior.
	// User arguments come after the subcommand and cannot supply global -c flags.
	argv := []string{"--no-pager", "--no-optional-locks",
		"-c", "core.pager=cat", "-c", "pager." + subcommand + "=false",
		"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"-c", "diff.external=", "-c", "core.attributesFile=/dev/null",
		"-c", "mailmap.file=", "-c", "mailmap.blob=", subcommand}
	if subcommand == "diff" || subcommand == "show" || subcommand == "log" {
		argv = append(argv, "--no-ext-diff", "--no-textconv")
	}
	argv = append(argv, args.Args...)
	var cmd *exec.Cmd
	var commandErr error
	if t.Workspace != nil {
		cmd, commandErr = t.Workspace.CommandArgsReadOnlyContext(runCtx, binary, argv...)
	} else {
		cmd = exec.CommandContext(runCtx, binary, argv...)
		cmd.Dir = t.WorkingDir
	}
	if commandErr != nil {
		return errorResult(call, commandErr), nil
	}
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1", "GIT_EXTERNAL_DIFF=", "GIT_PAGER=cat")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return errorResult(call, fmt.Errorf("git %s timed out", subcommand)), nil
		}
		return errorResult(call, fmt.Errorf("git %s failed: %v: %s", subcommand, err, strings.TrimSpace(stderr.String()))), nil
	}
	output := stdout.String()
	lines := strings.Split(output, "\n")
	if len(lines) > args.MaxOutputLines {
		output = strings.Join(lines[:args.MaxOutputLines], "\n")
		output += fmt.Sprintf("\n[truncated %d more lines]", len(lines)-args.MaxOutputLines)
	}
	if max := t.MaxOutput; max > 0 && len(output) > max {
		output = output[:max]
		output += "\n[truncated]"
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: output, IsError: false}, nil
}
