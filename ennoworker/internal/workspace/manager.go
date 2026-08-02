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

// Manager controls workspace isolation and skill snapshot access.
type Manager struct {
	Jail             *Jail
	RuntimeHostDir   string // host path for output capture
	RuntimeVisibleDir string // sandbox-visible path for env vars
	SkillsDir        string // host path for skills snapshot
	Mode             SandboxMode
}

// NewManager creates a Manager with workspace-only (backward compatible).
func NewManager(root, runtimeDir, skillsDir string, mode SandboxMode) (*Manager, error) {
	jail, err := NewJail(root)
	if err != nil {
		return nil, err
	}
	return newManagerWithJail(jail, runtimeDir, runtimeDir, skillsDir, mode)
}

// NewManagerWithSkills creates a dual-mount Manager.
func NewManagerWithSkills(workspaceRoot, runtimeHostDir, skillsRoot string, mode SandboxMode) (*Manager, error) {
	jail, err := NewJailWithSkills(workspaceRoot, skillsRoot)
	if err != nil {
		return nil, err
	}
	runtimeVisible := runtimeHostDir
	if mode == SandboxBubblewrap {
		runtimeVisible = "/runtime"
	}
	return newManagerWithJail(jail, runtimeHostDir, runtimeVisible, skillsRoot, mode)
}

func newManagerWithJail(jail *Jail, runtimeHostDir, runtimeVisibleDir, skillsDir string, mode SandboxMode) (*Manager, error) {
	if mode != SandboxBubblewrap && mode != SandboxNone {
		return nil, fmt.Errorf("unsupported sandbox mode: %s", mode)
	}

	// Create host runtime dir
	if runtimeHostDir != "" {
		abs, err := filepath.Abs(runtimeHostDir)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return nil, err
		}
		runtimeHostDir = abs
	}

	// Verify runtime I/O doesn't overlap with mounts
	if runtimeHostDir != "" {
		if err := jail.VerifyNoOverlap(runtimeHostDir); err != nil {
			return nil, fmt.Errorf("runtime I/O overlap: %w", err)
		}
	}

	if skillsDir != "" {
		abs, err := filepath.Abs(skillsDir)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return nil, err
		}
		skillsDir = abs
	}

	m := &Manager{
		Jail:              jail,
		RuntimeHostDir:    runtimeHostDir,
		RuntimeVisibleDir: runtimeVisibleDir,
		SkillsDir:         skillsDir,
		Mode:              mode,
	}

	// Verify runtime I/O
	if err := m.verifyRuntimeIO(); err != nil {
		return nil, err
	}

	return m, nil
}

func (m *Manager) verifyRuntimeIO() error {
	if m.RuntimeHostDir == "" {
		return nil
	}
	abs, err := filepath.Abs(m.RuntimeHostDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return err
	}
	return nil
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
