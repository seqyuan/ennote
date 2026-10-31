package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	// arbitrary host file.
	if denied, blocked := deniedGitArgs(args.Args); blocked {
		return errorResult(call, fmt.Errorf("git option %s is not allowed", denied)), nil
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
		if err := t.Workspace.ValidateArgv(append([]string{subcommand}, args.Args...)); err != nil {
			return errorResult(call, err), nil
		}
		cmd, commandErr = t.Workspace.CommandArgsReadOnlyContext(runCtx, binary, argv...)
	} else {
		cmd = exec.CommandContext(runCtx, binary, argv...)
		cmd.Dir = t.WorkingDir
	}
	if commandErr != nil {
		return errorResult(call, commandErr), nil
	}
	home := t.WorkingDir
	if t.Workspace != nil {
		home = t.Workspace.RuntimeVisibleDir
	}
	cmd.Env = gitReadonlyEnv(home)
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

// gitDeniedLongOptions lists options that can run repository-configured
// programs (ext-diff, textconv, pager, upload-pack), redirect output, switch
// the object/work tree, or make a nominally read-only command consume an
// arbitrary host file.
var gitDeniedLongOptions = []string{"--output", "--work-tree", "--git-dir", "--config-env",
	"--ext-diff", "--textconv", "--no-index", "--contents", "--ignore-revs-file",
	"--exclude-from", "--pathspec-from-file", "--upload-pack", "--exec",
	"--open-files-in-pager"}

// gitDeniedShortOptions lists the single-letter forms. Git also accepts an
// attached value (-Oorderfile, -cuser.email=x), so a longer argument starting
// with a denied short option is denied too. A bare -c/-cc is not denied here:
// for diff/log/show it selects a combined diff, and the global config form
// (-c key=value) is caught by deniedGitArgs.
var gitDeniedShortOptions = []string{"-o", "-c", "-O"}

// deniedGitArgs applies deniedGitOption to every argument and additionally
// rejects the "-c <key>=<value>" pair, which git reads as a global config
// override rather than the combined-diff flag.
func deniedGitArgs(arguments []string) (string, bool) {
	for index, argument := range arguments {
		if denied, blocked := deniedGitOption(argument); blocked {
			return denied, true
		}
		if argument == "-c" && index+1 < len(arguments) && gitConfigAssignment(arguments[index+1]) {
			return "-c <key>=<value>", true
		}
	}
	return "", false
}

// gitConfigAssignment reports an argument shaped like the "section.key=value"
// that git requires after a global -c.
func gitConfigAssignment(argument string) bool {
	key, _, found := strings.Cut(argument, "=")
	if !found || key == "" || strings.HasPrefix(key, "-") {
		return false
	}
	for _, char := range key {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '.' || char == '-' || char == '_':
		default:
			return false
		}
	}
	return true
}

// deniedGitOption reports the denied option an argument reaches, or false when
// it is allowed. Git accepts any unambiguous prefix of a long option, so
// --ext-d, --textcon, --no-in and --git-di=/x all select the denied option; the
// comparison is therefore symmetric (the argument may spell a prefix of the
// denied option, and the denied option may extend a prefix the argument names)
// after splitting off any "=value". --exclude and the bare combined-diff
// -c/-cc are the two exceptions carved out below; their dangerous siblings
// (--exclude-from, -c<key>=<value>) stay denied. Residual limitation: like
// git, a caller may still reach a denied option through a response file (@file)
// or a subcommand alias; the env/config overrides below cover config-driven
// execution instead.
func deniedGitOption(argument string) (string, bool) {
	name := argument
	if index := strings.IndexByte(name, '='); index >= 0 {
		name = name[:index]
	}
	switch {
	case name == "--" || name == "-":
		return "", false
	case strings.HasPrefix(name, "--"):
		// --exclude filters paths (ls-files, status) and is safe; only its
		// --exclude-from sibling reads a host file, and that stays denied
		// together with every abbreviation of it.
		if name == "--exclude" {
			return "", false
		}
		for _, denied := range gitDeniedLongOptions {
			if strings.HasPrefix(name, denied) || strings.HasPrefix(denied, name) {
				return denied, true
			}
		}
	case strings.HasPrefix(name, "-"):
		// A bare -c/-cc selects a combined diff; any attached value
		// (-cuser.email=x) is still the global config form and is denied below.
		if name == "-c" || name == "-cc" {
			return "", false
		}
		for _, denied := range gitDeniedShortOptions {
			if strings.HasPrefix(name, denied) {
				return denied, true
			}
		}
	}
	return "", false
}

// gitReadonlyEnv keeps the bash/exec allowlist and drops the host environment,
// including provider credentials that live in the user environment.
func gitReadonlyEnv(home string) []string {
	return append(safeEnvironment(home),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1", "GIT_EXTERNAL_DIFF=", "GIT_PAGER=cat")
}
