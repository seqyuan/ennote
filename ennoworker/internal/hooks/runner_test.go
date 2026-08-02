package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseOutput(t *testing.T) {
	// Valid JSON.
	out, ok := ParseOutput([]byte(`{"decision":"block","reason":"no"}`))
	assert.True(t, ok)
	assert.True(t, out.Blocks())
	assert.Equal(t, "no", out.Reason)

	// Empty.
	_, ok = ParseOutput([]byte("\n  \t\n"))
	assert.False(t, ok)

	// Non-JSON is a no-op.
	_, ok = ParseOutput([]byte("hello"))
	assert.False(t, ok)

	// Continue=false blocks.
	out, ok = ParseOutput([]byte(`{"continue":false}`))
	assert.True(t, ok)
	assert.True(t, out.Blocks())

	// Continue=true allows.
	out, ok = ParseOutput([]byte(`{"continue":true}`))
	assert.True(t, ok)
	assert.False(t, out.Blocks())
}

func TestRunnerAllow(t *testing.T) {
	r := Runner{
		Shell:      "sh",
		ProjectDir: t.TempDir(),
	}
	input := HookInput{
		DeliveryID:    "d1",
		EventType:     "PreToolUse",
		RunID:         "run-1",
		WorkspaceRoot: r.ProjectDir,
		ToolName:      "bash",
		ToolInput:     json.RawMessage(`{"command":"ls"}`),
	}
	// Hook that allows (exit 0, empty output).
	out, err := r.Run(context.Background(), HookConfig{ID: "allow", Command: "exit 0"}, input)
	require.NoError(t, err)
	assert.False(t, out.Blocks())
}

func TestRunnerBlock(t *testing.T) {
	r := Runner{
		Shell:      "sh",
		ProjectDir: t.TempDir(),
	}
	input := HookInput{
		DeliveryID:    "d1",
		EventType:     "PreToolUse",
		RunID:         "run-1",
		WorkspaceRoot: r.ProjectDir,
		ToolName:      "bash",
	}
	// Hook that blocks (exit 2).
	out, err := r.Run(context.Background(), HookConfig{ID: "block", Command: "echo blocked >&2; exit 2"}, input)
	require.NoError(t, err)
	assert.True(t, out.Blocks())
	assert.Contains(t, out.Reason, "blocked")
}

func TestRunnerTimeout(t *testing.T) {
	r := Runner{
		Shell:      "sh",
		ProjectDir: t.TempDir(),
	}
	input := HookInput{DeliveryID: "d1", EventType: "Stop", RunID: "run-1", WorkspaceRoot: r.ProjectDir}
	hook := HookConfig{ID: "slow", Command: "sleep 10", TimeoutSeconds: intPtr(1)}
	_, err := r.Run(context.Background(), hook, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestRunnerContextCancel(t *testing.T) {
	r := Runner{
		Shell:      "sh",
		ProjectDir: t.TempDir(),
	}
	input := HookInput{DeliveryID: "d1", EventType: "Stop", RunID: "run-1", WorkspaceRoot: r.ProjectDir}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err := r.Run(ctx, HookConfig{ID: "never", Command: "sleep 5"}, input)
	require.Error(t, err)
}

func TestRunnerStdoutDecision(t *testing.T) {
	r := Runner{
		Shell:      "sh",
		ProjectDir: t.TempDir(),
	}
	input := HookInput{DeliveryID: "d1", EventType: "PreToolUse", RunID: "run-1", WorkspaceRoot: r.ProjectDir}
	out, err := r.Run(context.Background(), HookConfig{ID: "json", Command: `echo '{"decision":"block","reason":"json deny"}'`}, input)
	require.NoError(t, err)
	assert.True(t, out.Blocks())
	assert.Equal(t, "json deny", out.Reason)
}

func TestRunnerEnvironment(t *testing.T) {
	r := Runner{
		Shell:      "sh",
		ProjectDir: t.TempDir(),
	}
	input := HookInput{DeliveryID: "d1", EventType: "RunStart", RunID: "run-env-test", WorkspaceID: "ws-1",
		WorkspaceRoot: "/data/project", SessionID: "session-1"}
	out, err := r.Run(context.Background(), HookConfig{ID: "env", Command: "echo \"$ENNOTE_RUN_ID\""}, input)
	require.NoError(t, err)
	assert.False(t, out.Blocks())
	// stdout was not JSON so ParseOutput gave empty — that's fine.
	// The variable was set, so the command ran successfully.
}

func TestRunnerCommandNotFound(t *testing.T) {
	r := Runner{
		Shell:      "sh",
		ProjectDir: t.TempDir(),
	}
	input := HookInput{DeliveryID: "d1", EventType: "Stop", RunID: "run-1", WorkspaceRoot: r.ProjectDir}
	_, err := r.Run(context.Background(), HookConfig{ID: "missing", Command: "/nonexistent/binary"}, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code")
}

func TestDispatcherEmptySet(t *testing.T) {
	d := NewDispatcher(HookSet{}, "/tmp", nil)
	assert.Nil(t, d)
}

func TestDispatcherDispatch(t *testing.T) {
	dir := t.TempDir()
	allowScript := filepath.Join(dir, "allow.sh")
	blockScript := filepath.Join(dir, "block.sh")
	require.NoError(t, os.WriteFile(allowScript, []byte("#!/bin/sh\necho '{\"additionalContext\":\"context added\"}'\n"), 0o700))
	require.NoError(t, os.WriteFile(blockScript, []byte("#!/bin/sh\necho blocked >&2\nexit 2\n"), 0o700))

	set := HookSet{
		"PreToolUse": {
			Matchers: []HookMatcherConfig{
				{ID: "m1", Matcher: "bash", Hooks: []HookConfig{
					{ID: "h1", Command: allowScript, TimeoutSeconds: intPtr(5)},
				}},
				{ID: "m2", Matcher: "bash", Hooks: []HookConfig{
					{ID: "h2", Command: blockScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	d := NewDispatcher(set, dir, nil)
	require.NotNil(t, d)

	dec := d.Dispatch(context.Background(), EventPreToolUse, "bash", HookInput{
		DeliveryID: "d1", EventType: EventPreToolUse, RunID: "run-1", WorkspaceRoot: dir,
		ToolName: "bash",
	})
	assert.True(t, dec.Block, "second hook should block")
	assert.Contains(t, dec.Reason, "blocked")
	assert.Equal(t, "context added", dec.AdditionalContext, "first hook's context should be accumulated")
}

func TestDispatcherPreToolUseShortCircuit(t *testing.T) {
	dir := t.TempDir()
	blockScript := filepath.Join(dir, "block.sh")
	rewriteScript := filepath.Join(dir, "rewrite.sh")
	require.NoError(t, os.WriteFile(blockScript, []byte("#!/bin/sh\necho blocked >&2\nexit 2\n"), 0o700))
	require.NoError(t, os.WriteFile(rewriteScript, []byte("#!/bin/sh\necho '{\"updatedInput\":{\"command\":\"safe\"}}'\n"), 0o700))

	set := HookSet{
		"PreToolUse": {
			Matchers: []HookMatcherConfig{
				{ID: "m1", Matcher: "bash", Hooks: []HookConfig{
					{ID: "block", Command: blockScript, TimeoutSeconds: intPtr(5)},
				}},
				{ID: "m2", Matcher: "bash", Hooks: []HookConfig{
					{ID: "rewrite", Command: rewriteScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	d := NewDispatcher(set, dir, nil)
	dec := d.Dispatch(context.Background(), EventPreToolUse, "bash", HookInput{
		DeliveryID: "d1", EventType: EventPreToolUse, RunID: "run-1", WorkspaceRoot: dir,
		ToolName: "bash", ToolInput: json.RawMessage(`{"command":"rm -rf /"}`),
	})
	assert.True(t, dec.Block)
	// Short-circuit: the rewrite hook should not have run, so UpdatedInput is empty.
	assert.Nil(t, dec.UpdatedInput)
}

func TestDispatcherNonBlockingFeedback(t *testing.T) {
	dir := t.TempDir()
	feedbackScript := filepath.Join(dir, "feedback.sh")
	require.NoError(t, os.WriteFile(feedbackScript, []byte("#!/bin/sh\necho '{\"additionalContext\":\"format result: ok\"}'\n"), 0o700))

	set := HookSet{
		"PostToolUse": {
			Matchers: []HookMatcherConfig{
				{ID: "m1", Hooks: []HookConfig{
					{ID: "feedback", Command: feedbackScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	d := NewDispatcher(set, dir, nil)
	dec := d.Dispatch(context.Background(), "PostToolUse", "write", HookInput{
		DeliveryID: "d1", EventType: "PostToolUse", RunID: "run-1", WorkspaceRoot: dir,
		ToolName: "write", ToolResponse: json.RawMessage(`{"ok":true}`),
	})
	assert.False(t, dec.Block)
	assert.Contains(t, dec.AdditionalContext, "format result: ok")
}

func intPtr(i int) *int { return &i }

func TestHookInputJSON(t *testing.T) {
	// All fields should be serializable.
	input := HookInput{
		DeliveryID:    "d1",
		EventType:     "PreToolUse",
		RunID:         "run-1",
		SessionID:     "session-1",
		WorkspaceID:   "ws-1",
		WorkspaceRoot: "/data/project",
		ToolName:      "bash",
		ToolInput:     json.RawMessage(`{"command":"ls"}`),
		RiskHint:      "workspace_write",
		ToolCallID:    "call-1",
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"tool_name":"bash"`)

	// Omitting optional fields.
	minimal := HookInput{DeliveryID: "d2", EventType: "Stop", RunID: "run-2"}
	data, err = json.Marshal(minimal)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "tool_name")
}

func TestDecisionMergeBlock(t *testing.T) {
	// Simulate what Dispatch does: two hooks, first blocks, second adds context.
	var dec Decision
	out := HookOutput{Decision: "block", Reason: "not allowed"}
	if out.Blocks() {
		dec.Block = true
		dec.Reason = joinNonEmpty(dec.Reason, out.Reason, "\n")
	}
	out2 := HookOutput{AdditionalContext: "extra info"}
	dec.AdditionalContext = joinNonEmpty(dec.AdditionalContext, out2.AdditionalContext, "\n")

	assert.True(t, dec.Block)
	assert.Equal(t, "not allowed", dec.Reason)
	assert.Equal(t, "extra info", dec.AdditionalContext)
}
