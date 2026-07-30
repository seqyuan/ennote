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

	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap is required for sandbox mode bwrap: %w", err)
	}
	args := []string{
		"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
	}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	args = append(args, "--bind", m.Jail.Root(), "/workspace")
	if m.SkillsDir != "" {
		skills, err := filepath.Abs(m.SkillsDir)
		if err != nil {
			return nil, err
		}
		args = append(args, "--ro-bind", skills, "/skills")
	}
	if m.RuntimeDir != "" {
		runtimeDir, err := filepath.Abs(m.RuntimeDir)
		if err != nil {
			return nil, err
		}
		args = append(args, "--bind", runtimeDir, "/runtime")
	}
	args = append(args, "--chdir", "/workspace", shell, "-lc", command)
	return exec.Command(bwrap, args...), nil
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
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap is required for sandbox mode bwrap: %w", err)
	}
	args := []string{
		"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
	}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	args = append(args, "--bind", m.Jail.Root(), "/workspace")
	if m.SkillsDir != "" {
		skills, err := filepath.Abs(m.SkillsDir)
		if err != nil {
			return nil, err
		}
		args = append(args, "--ro-bind", skills, "/skills")
	}
	if m.RuntimeDir != "" {
		runtimeDir, err := filepath.Abs(m.RuntimeDir)
		if err != nil {
			return nil, err
		}
		args = append(args, "--bind", runtimeDir, "/runtime")
	}
	args = append(args, "--chdir", "/workspace", executable)
	args = append(args, commandArgs...)
	return exec.Command(bwrap, args...), nil
}
