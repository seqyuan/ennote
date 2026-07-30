//go:build !linux && !darwin

package tools

import (
	"context"
	"fmt"
	"os/exec"
)

func runProcessGroup(ctx context.Context, cmd *exec.Cmd) error {
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
		return fmt.Errorf("command cancelled: %w", ctx.Err())
	}
}
