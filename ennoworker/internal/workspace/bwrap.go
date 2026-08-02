package workspace

import (
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
	if executable == "" {
		return nil, fmt.Errorf("executable is required")
	}
	if m.Mode == SandboxNone {
		cmd := exec.Command(executable, commandArgs...)
		cmd.Dir = m.Jail.Root()
		return cmd, nil
	}
	// Join args for shell -lc
	quoted := executable
	for _, a := range commandArgs {
		quoted += " " + a
	}
	return m.bwrapCommand("/bin/sh", quoted, false)
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
	args := []string{
		"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
	}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}

	// Bind workspace
	args = append(args, "--bind", m.Jail.Root(), "/workspace")

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
