package main

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// sigpipeHelperEnv selects a mode for the re-executed helper process: "armed"
// calls ignoreSIGPIPE before logging, "default" leaves the disposition alone so
// the test can prove the mechanism is real.
const sigpipeHelperEnv = "ENNOTE_SIGPIPE_HELPER"

// TestClosedStderrPipeDoesNotKillWorker is the regression test for the release
// blocker: ennogate reads the Worker's stderr through a pipe, so killing the
// supervisor closes the read end and the next log write raised SIGPIPE, which
// killed the Worker. The helper writes to a stderr that is exactly that broken
// pipe; with ignoreSIGPIPE it must survive and report an EPIPE, and without it
// the same write must kill the process (the negative control).
func TestClosedStderrPipeDoesNotKillWorker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no SIGPIPE; a broken pipe is already a write error")
	}

	runHelper := func(t *testing.T, mode string) (stdout string, err error) {
		t.Helper()
		readEnd, writeEnd, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatalf("create pipe: %v", pipeErr)
		}
		// Closing the read end while the helper still holds the write end is
		// precisely the supervisor-crash state.
		if err := readEnd.Close(); err != nil {
			t.Fatalf("close pipe read end: %v", err)
		}
		command := exec.Command(os.Args[0], "-test.run=^TestSigPipeHelperProcess$")
		command.Env = append(os.Environ(), sigpipeHelperEnv+"="+mode)
		command.Stderr = writeEnd
		var out bytes.Buffer
		command.Stdout = &out
		if err := command.Start(); err != nil {
			_ = writeEnd.Close()
			t.Fatalf("start helper: %v", err)
		}
		_ = writeEnd.Close()
		err = command.Wait()
		return out.String(), err
	}

	t.Run("default disposition dies", func(t *testing.T) {
		_, err := runHelper(t, "default")
		if err == nil {
			t.Fatal("writing to a closed stderr pipe did not kill the process; the guard is untested")
		}
		if !strings.Contains(err.Error(), "broken pipe") {
			t.Fatalf("expected a SIGPIPE/broken-pipe exit, got %v", err)
		}
	})

	t.Run("worker survives", func(t *testing.T) {
		stdout, err := runHelper(t, "armed")
		if err != nil {
			t.Fatalf("worker died writing to a closed stderr pipe: %v (stdout %q)", err, stdout)
		}
		if !strings.Contains(stdout, "log-write-survived") {
			t.Fatalf("helper stdout %q does not prove the write was attempted", stdout)
		}
	})
}

// TestSigPipeHelperProcess is the helper process body, not a real test. It runs
// only when the parent sets sigpipeHelperEnv.
func TestSigPipeHelperProcess(t *testing.T) {
	mode := os.Getenv(sigpipeHelperEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	if mode == "armed" {
		ignoreSIGPIPE()
	}
	// os.Stderr is the closed pipe handed over by the parent. With the signal
	// disarmed this reports EPIPE; with the default disposition it kills us.
	if _, err := os.Stderr.Write([]byte("2026-01-01 ennoworker routine log line\n")); err == nil {
		_, _ = os.Stdout.WriteString("log-write-reportedly-succeeded\n")
	}
	_, _ = os.Stdout.WriteString("log-write-survived\n")
	os.Exit(0)
}
