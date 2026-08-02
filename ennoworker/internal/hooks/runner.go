// This file implements Runner.Run: executing a single hook command via the
// system shell with the event payload on stdin, a per-hook timeout, bounded
// output capture, and exit-code classification. It is the one place that forks
// a process, so all isolation guarantees (timeout kill, output cap, error
// containment) live here.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// MaxOutputBytes caps the bytes captured from a hook's stdout and stderr each.
	// Output beyond this is dropped and the truncation is flagged.
	MaxOutputBytes = 1 << 20 // 1 MB
	// MaxDecisionBytes caps the first line of stdout used for the hook decision.
	MaxDecisionBytes = 64 << 10 // 64 KB
	// blockExitCode signals a block (Claude Code semantics).
	blockExitCode = 2
)

// Runner executes hook commands. Shell defaults to "sh" with a "-c" flag.
// ProjectDir is the working directory hook commands run in (the workspace root).
type Runner struct {
	Shell      string
	ProjectDir string
	WarnLog    io.Writer
}

// Run executes one hook, writing input as a single-line JSON document to the
// command's stdin. It returns the parsed HookOutput and a non-nil error only
// for an execution *failure* (could not start, timed out, or exited non-zero
// and non-2). A clean exit 0 or a block (exit 2) both return err == nil; the
// caller distinguishes a block via HookOutput.Blocks().
func (r *Runner) Run(ctx context.Context, h HookConfig, input HookInput) (HookOutput, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return HookOutput{}, fmt.Errorf("marshal hook input: %w", err)
	}

	timeout := time.Duration(h.Timeout()) * time.Second
	if timeout < 1*time.Second {
		timeout = time.Duration(DefaultTimeoutSeconds) * time.Second
	}
	if timeout > 300*time.Second {
		timeout = 300 * time.Second
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := r.Shell
	if shell == "" {
		shell = "sh"
	}

	cmd := exec.CommandContext(runCtx, shell, "-c", h.Command)
	cmd.Dir = r.ProjectDir
	cmd.Env = r.minimalEnv(input)

	// On Linux, create an independent process group so timeout kills the
	// entire tree, not just the shell.
	if isLinux() {
		cmd.SysProcAttr = linuxProcessGroup()
	}

	cmd.Stdin = bytes.NewReader(payload)
	// WaitDelay bounds the wait after kill: after the process is killed, Go
	// force-closes I/O pipes so Run returns promptly even if a grandchild
	// inherited them.
	cmd.WaitDelay = time.Second

	var stdout, stderr cappedBuffer
	stdout.limit = MaxOutputBytes
	stderr.limit = MaxOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	if stdout.truncated() || stderr.truncated() {
		warnf(r.WarnLog, "hooks: output from command %q exceeded %d bytes and was truncated\n", h.Command, MaxOutputBytes)
	}

	// Timeout: the context deadline fired and the process was killed.
	if runCtx.Err() == context.DeadlineExceeded {
		return HookOutput{}, fmt.Errorf("hook timed out after %s", timeout)
	}

	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			// Could not start (ENOENT etc.) or was killed.
			return HookOutput{}, fmt.Errorf("hook failed to run: %w", runErr)
		}
	}

	switch exitCode {
	case 0:
		out, _ := ParseOutput(stdout.Bytes())
		return out, nil
	case blockExitCode:
		if out, ok := ParseOutput(stdout.Bytes()); ok {
			if out.Reason == "" {
				out.Reason = strings.TrimSpace(string(stderr.Bytes()))
			}
			out.Decision = "block"
			return out, nil
		}
		return HookOutput{Decision: "block", Reason: strings.TrimSpace(string(stderr.Bytes()))}, nil
	default:
		return HookOutput{}, fmt.Errorf("hook exited with code %d: %s", exitCode,
			strings.TrimSpace(string(stderr.Bytes())))
	}
}

func (r *Runner) minimalEnv(input HookInput) []string {
	p := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"USER=" + os.Getenv("USER"),
		"TMPDIR=" + os.Getenv("TMPDIR"),
		"SHELL=" + shellOrDefault(),
		"ENNOTE_RUN_ID=" + input.RunID,
		"ENNOTE_EVENT_TYPE=" + input.EventType,
	}
	if input.WorkspaceID != "" {
		p = append(p, "ENNOTE_WORKSPACE_ID="+input.WorkspaceID)
	}
	if input.WorkspaceRoot != "" {
		p = append(p, "ENNOTE_WORKSPACE_ROOT="+input.WorkspaceRoot)
	}
	if input.SessionID != "" {
		p = append(p, "ENNOTE_SESSION_ID="+input.SessionID)
	}
	return p
}

func shellOrDefault() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

// cappedBuffer is an io.Writer that stores at most limit bytes.
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.limit <= 0 {
		return c.buf.Write(p)
	}
	room := c.limit - c.buf.Len()
	if room <= 0 {
		c.dropped += len(p)
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.dropped += len(p) - room
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) Bytes() []byte    { return c.buf.Bytes() }
func (c *cappedBuffer) truncated() bool   { return c.dropped > 0 }
