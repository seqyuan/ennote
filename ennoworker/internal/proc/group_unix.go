//go:build linux || darwin

package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// RunGroup starts cmd in its own process group and waits for it, terminating the
// whole group on context cancellation: SIGTERM first, then SIGKILL after a bounded
// grace period, so a child that spawns helpers cannot outlive the Run that started
// it.
//
// This lived in two packages as byte-identical copies plus two divergent
// non-unix variants; the non-unix copy that kept the timeout distinction is the
// one promoted here, because "timed out" and "cancelled" are different answers for
// a Run and losing one of them silently is how a deadline becomes a cancellation.
func RunGroup(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("command timed out: %w", ctx.Err())
		}
		return fmt.Errorf("command cancelled: %w", ctx.Err())
	}
}
