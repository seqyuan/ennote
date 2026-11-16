package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shell tools record the exit status of the process they ran, so
// tool_calls.exit_code stops being a column nobody writes.

// exitCode is the pointer form the tools record, kept short for tables.
func exitCode(value int) *int { return &value }

func exitStatusRegistry(t *testing.T) (*workspace.Manager, *Registry) {
	t.Helper()
	manager, err := workspace.NewManager(t.TempDir(), t.TempDir(), t.TempDir(), workspace.SandboxNone)
	require.NoError(t, err)
	registry, err := NewDefaultRegistry(manager)
	require.NoError(t, err)
	return manager, registry
}

func TestBashRecordsProcessExitStatus(t *testing.T) {
	_, registry := exitStatusRegistry(t)
	ctx := context.Background()
	cases := []struct {
		name    string
		command string
		want    *int
	}{
		{name: "exit 0", command: "exit 0", want: exitCode(0)},
		{name: "exit 1", command: "exit 1", want: exitCode(1)},
		// A process killed by a signal has no exit status of its own; the shell
		// convention (128 + signal number) is what every runner and CI reports,
		// so SIGKILL is 137 rather than a fabricated 0.
		{name: "killed by SIGKILL", command: "kill -9 0", want: exitCode(137)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := registry.Execute(ctx,
				call("call-bash", "bash", fmt.Sprintf(`{"command":%q}`, testCase.command)))
			require.NoError(t, err)
			require.NotNil(t, result.ExitStatus, "the process ran, so its status is known")
			assert.Equal(t, *testCase.want, *result.ExitStatus)
		})
	}
}

func TestExecRecordsProcessExitStatus(t *testing.T) {
	manager, _ := exitStatusRegistry(t)
	ctx := context.Background()
	cases := []struct {
		name string
		argv []string
		want *int
	}{
		{name: "exit 0", argv: []string{"/bin/sh", "-c", "exit 0"}, want: exitCode(0)},
		{name: "exit 1", argv: []string{"/bin/sh", "-c", "exit 1"}, want: exitCode(1)},
		{name: "killed by SIGKILL", argv: []string{"/bin/sh", "-c", "kill -9 0"}, want: exitCode(137)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tool := &ExecTool{Workspace: manager}
			encoded, err := json.Marshal(map[string]any{"argv": testCase.argv})
			require.NoError(t, err)
			result, err := tool.Execute(ctx, domain.ToolCall{ID: "call-exec", Name: "exec", Arguments: encoded})
			require.NoError(t, err)
			require.NotNil(t, result.ExitStatus, "the process ran, so its status is known")
			assert.Equal(t, *testCase.want, *result.ExitStatus)
		})
	}
}

// A run that never completed is not a run that exited 0: the deadline is its own
// documented status, in both shell tools.
func TestShellToolsRecordTimeoutAsItsOwnStatus(t *testing.T) {
	manager, _ := exitStatusRegistry(t)
	ctx := context.Background()
	bash := &BashTool{Workspace: manager, Timeout: 100 * time.Millisecond}
	result, err := bash.Execute(ctx, call("call-timeout", "bash", `{"command":"sleep 5"}`))
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.NotNil(t, result.ExitStatus)
	assert.Equal(t, domain.ExitCodeTimedOut, *result.ExitStatus)

	exec := &ExecTool{Workspace: manager, Timeout: 100 * time.Millisecond}
	result, err = exec.Execute(ctx, domain.ToolCall{ID: "call-timeout", Name: "exec",
		Arguments: json.RawMessage(`{"argv":["/bin/sh","-c","sleep 5"]}`)})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.NotNil(t, result.ExitStatus)
	assert.Equal(t, domain.ExitCodeTimedOut, *result.ExitStatus)
}

// A cancelled Run stops the process for a different reason than the deadline, so
// it records the other documented status instead of collapsing the two.
func TestShellToolsRecordCancellationAsItsOwnStatus(t *testing.T) {
	manager, _ := exitStatusRegistry(t)
	cancelled, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	result, err := (&ExecTool{Workspace: manager}).Execute(cancelled,
		domain.ToolCall{ID: "call-cancel", Name: "exec",
			Arguments: json.RawMessage(`{"argv":["/bin/sh","-c","sleep 5"]}`)})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.NotNil(t, result.ExitStatus)
	assert.Equal(t, domain.ExitCodeCancelled, *result.ExitStatus)
}

// A call that never ran a process has no exit status, and nil must stay nil: a
// fabricated 0 here would read as "the command succeeded".
func TestCallsThatNeverRanAProcessLeaveExitStatusUnrecorded(t *testing.T) {
	manager, registry := exitStatusRegistry(t)
	ctx := context.Background()

	refusals := map[string]domain.ToolResult{}
	emptyCommand, err := (&BashTool{Workspace: manager}).Execute(ctx, call("call-empty", "bash", `{"command":"  "}`))
	require.NoError(t, err)
	refusals["bash without a command"] = emptyCommand

	missingBinary, err := (&ExecTool{Workspace: manager}).Execute(ctx,
		domain.ToolCall{ID: "call-missing", Name: "exec", Arguments: json.RawMessage(`{"argv":["ennote-does-not-exist"]}`)})
	require.NoError(t, err)
	refusals["exec of a missing binary"] = missingBinary

	for name, result := range refusals {
		t.Run(name, func(t *testing.T) {
			require.True(t, result.IsError, "these calls are refusals or failures, not successes")
			assert.Nil(t, result.ExitStatus, "no process ran to completion, so no status may be recorded")
		})
	}

	// A tool that runs no process at all succeeds here; nil is still the right
	// answer, and the store writes NULL for it, never a fabricated 0.
	read, err := registry.Execute(ctx, call("call-read", "ls", `{"path":"."}`))
	require.NoError(t, err)
	require.False(t, read.IsError, read.Content)
	assert.Nil(t, read.ExitStatus)

}

// The status is Worker-internal bookkeeping: it must not change the tool result
// the Provider and the Web client read.
func TestExitStatusIsNotPartOfTheToolResultWireShape(t *testing.T) {
	encoded, err := json.Marshal(domain.ToolResult{ToolCallID: "call-1", ToolName: "bash",
		Content: "ok", ExitStatus: exitCode(1)})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "exitStatus")
	assert.NotContains(t, string(encoded), "ExitStatus")
}
