//go:build !linux && !darwin

package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// RunGroup has no process group to signal on this platform, so it kills the one
// child it started. See the unix variant for the contract it keeps.
func RunGroup(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("command timed out: %w", ctx.Err())
		}
		return fmt.Errorf("command cancelled: %w", ctx.Err())
	}
}
