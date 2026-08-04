package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func (m *Manager) Command(shell, command string) (*exec.Cmd, error) {
	if shell == "" {
		shell = "/bin/sh"
	}
	if m.Mode == SandboxNone {
		cmd := exec.Command(shell, "-lc", command)
		cmd.Dir = m.Jail.Root()
		return cmd, nil
	}
	return m.bwrapCommand(shell, command, true)
}

func (m *Manager) CommandArgs(executable string, commandArgs ...string) (*exec.Cmd, error) {
	return m.commandArgs(context.Background(), executable, false, commandArgs...)
}

// CommandArgsReadOnly executes argv with a read-only workspace mount when the
// sandbox is enabled. SandboxNone still relies on the caller's command-level
// validation, but retains direct argv execution with no shell interpolation.
func (m *Manager) CommandArgsReadOnly(executable string, commandArgs ...string) (*exec.Cmd, error) {
	return m.commandArgs(context.Background(), executable, true, commandArgs...)
}

func (m *Manager) CommandArgsReadOnlyContext(ctx context.Context, executable string, commandArgs ...string) (*exec.Cmd, error) {
	return m.commandArgs(ctx, executable, true, commandArgs...)
}

func (m *Manager) commandArgs(ctx context.Context, executable string, readOnly bool, commandArgs ...string) (*exec.Cmd, error) {
	if executable == "" {
		return nil, fmt.Errorf("executable is required")
	}
	if m.Mode == SandboxNone {
		cmd := exec.CommandContext(ctx, executable, commandArgs...)
		cmd.Dir = m.Jail.Root()
		return cmd, nil
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap is required for sandbox mode bwrap: %w", err)
	}
	args := buildBwrapArgsMode(m, readOnly)
	args = append(args, executable)
	args = append(args, commandArgs...)
	return exec.CommandContext(ctx, bwrap, args...), nil
}

func (m *Manager) bwrapCommand(shell, command string, withShell bool) (*exec.Cmd, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap is required for sandbox mode bwrap: %w", err)
	}
	args := buildBwrapArgs(m)
	if withShell {
		args = append(args, shell, "-lc", command)
	} else {
		args = append(args, shell)
	}
	return exec.Command(bwrap, args...), nil
}

// buildBwrapArgs builds the shared bwrap argument list for both Command and CommandArgs.
func buildBwrapArgs(m *Manager) []string {
	return buildBwrapArgsMode(m, false)
}

func buildBwrapArgsMode(m *Manager, readOnlyWorkspace bool) []string {
	args := []string{
		"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
	}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}

	// Bind workspace. Inspection-only tools use a separate read-only command
	// path even when the same Manager also serves mutation-capable tools.
	workspaceBind := "--bind"
	if readOnlyWorkspace {
		workspaceBind = "--ro-bind"
	}
	args = append(args, workspaceBind, m.Jail.Root(), "/workspace")

	// Bind skills read-only
	if m.SkillsDir != "" {
		skills, err := filepath.Abs(m.SkillsDir)
		if err == nil {
			args = append(args, "--ro-bind", skills, "/skills")
		}
	}

	// Bind runtime I/O
	if m.RuntimeHostDir != "" {
		runtimeDir, err := filepath.Abs(m.RuntimeHostDir)
		if err == nil {
			args = append(args, "--bind", runtimeDir, "/runtime")
		}
	}

	args = append(args, "--chdir", "/workspace")
	return args
}
