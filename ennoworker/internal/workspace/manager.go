package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type SandboxMode string

const (
	SandboxBubblewrap SandboxMode = "bwrap"
	SandboxNone       SandboxMode = "none"
)

type Manager struct {
	Jail       *Jail
	RuntimeDir string
	SkillsDir  string
	Mode       SandboxMode
}

func NewManager(root, runtimeDir, skillsDir string, mode SandboxMode) (*Manager, error) {
	jail, err := NewJail(root)
	if err != nil {
		return nil, err
	}
	if mode != SandboxBubblewrap && mode != SandboxNone {
		return nil, fmt.Errorf("unsupported sandbox mode: %s", mode)
	}
	for _, dir := range []string{runtimeDir, skillsDir} {
		if dir == "" {
			continue
		}
		absolute, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return nil, err
		}
	}
	return &Manager{Jail: jail, RuntimeDir: runtimeDir, SkillsDir: skillsDir, Mode: mode}, nil
}

func (m *Manager) Degraded() bool { return m.Mode == SandboxNone }

func (m *Manager) Probe(ctx context.Context) error {
	if m.Mode == SandboxNone {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd, err := m.Command("/bin/sh", "test \"$(pwd)\" = /workspace")
	if err != nil {
		return err
	}
	output := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, err := cmd.CombinedOutput()
		output <- struct {
			data []byte
			err  error
		}{data: data, err: err}
	}()
	select {
	case result := <-output:
		if result.err != nil {
			return fmt.Errorf("bubblewrap probe failed: %w: %s", result.err, string(result.data))
		}
		return nil
	case <-probeCtx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return fmt.Errorf("bubblewrap probe timed out: %w", probeCtx.Err())
	}
}
