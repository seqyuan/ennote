package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/hooks"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreToolUseHookBlocksDangerousCommand(t *testing.T) {
	dir := t.TempDir()
	blockScript := filepath.Join(dir, "block-bash.sh")
	require.NoError(t, os.WriteFile(blockScript, []byte("#!/bin/sh\necho blocked >&2\nexit 2\n"), 0o700))

	hookSet := hooks.HookSet{
		"PreToolUse": {
			Matchers: []hooks.HookMatcherConfig{
				{ID: "m1", Matcher: "bash", Hooks: []hooks.HookConfig{
					{ID: "block-bash", Command: blockScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	encoded, err := json.Marshal(hookSet)
	require.NoError(t, err)

	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded,
		WorkspaceRoot:   dir,
		WorkspaceID:     "ws-test",
	}).WithRun("run-1", "session-1")
	require.NotNil(t, life)

	tools := &fakeTools{
		defs: toolDefs("bash", "read", "write"),
		classes: map[string]domain.ExecutionClass{
			"bash": domain.ExecutionWorkspaceWrite,
		},
	}
	writer := &memoryWriter{}
	provider := llm.NewFakeProvider(
		llm.FakeStep{
			Completion: domain.Completion{
				ToolCalls:  []domain.ToolCall{{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /"}`)}},
				StopReason: "tool_calls",
			},
		},
		llm.FakeStep{
			Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"},
		},
	)

	loop := &Loop{
		Provider: provider, Tools: tools, Events: writer, HookLife: life,
		ToolPolicy: &preflightOnlyPolicy{},
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	_, err = loop.Run(context.Background(), RunInput{RunID: "run-1", Model: "m"})
	require.NoError(t, err)

	assert.Contains(t, writer.types(), "tool_call_skipped")
	assert.NotContains(t, writer.types(), "tool_call_started")
}

func TestPreToolUseHookAllowsSafeCommand(t *testing.T) {
	dir := t.TempDir()
	allowScript := filepath.Join(dir, "allow.sh")
	require.NoError(t, os.WriteFile(allowScript, []byte("#!/bin/sh\nexit 0\n"), 0o700))

	hookSet := hooks.HookSet{
		"PreToolUse": {
			Matchers: []hooks.HookMatcherConfig{
				{ID: "m1", Matcher: "bash", Hooks: []hooks.HookConfig{
					{ID: "allow-bash", Command: allowScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	encoded, _ := json.Marshal(hookSet)
	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded, WorkspaceRoot: dir, WorkspaceID: "ws-test",
	}).WithRun("run-1", "session-1")

	tools := &fakeTools{
		defs:    toolDefs("bash"),
		classes: map[string]domain.ExecutionClass{"bash": domain.ExecutionWorkspaceWrite},
	}
	writer := &memoryWriter{}
	provider := llm.NewFakeProvider(
		llm.FakeStep{
			Completion: domain.Completion{
				ToolCalls:  []domain.ToolCall{{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)}},
				StopReason: "tool_calls",
			},
		},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
	)

	loop := &Loop{
		Provider: provider, Tools: tools, Events: writer, HookLife: life,
		ToolPolicy: &preflightOnlyPolicy{},
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-1", Model: "m"})
	require.NoError(t, err)

	assert.Contains(t, writer.types(), "tool_call_started")
	assert.Contains(t, writer.types(), "tool_call_completed")
	assert.NotContains(t, writer.types(), "tool_call_skipped")
}

func TestPostToolUseHookAppendsFeedback(t *testing.T) {
	dir := t.TempDir()
	feedbackScript := filepath.Join(dir, "feedback.sh")
	require.NoError(t, os.WriteFile(feedbackScript,
		[]byte("#!/bin/sh\necho '{\"additionalContext\":\"Hook: format check passed.\"}'\n"), 0o700))

	hookSet := hooks.HookSet{
		"PostToolUse": {
			Matchers: []hooks.HookMatcherConfig{
				{ID: "m1", Matcher: "write", Hooks: []hooks.HookConfig{
					{ID: "feedback", Command: feedbackScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	encoded, _ := json.Marshal(hookSet)
	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded, WorkspaceRoot: dir, WorkspaceID: "ws-test",
	}).WithRun("run-1", "session-1")

	tools := &fakeTools{
		defs:    toolDefs("write"),
		classes: map[string]domain.ExecutionClass{"write": domain.ExecutionWorkspaceWrite},
	}
	writer := &memoryWriter{}
	provider := llm.NewFakeProvider(
		llm.FakeStep{
			Completion: domain.Completion{
				ToolCalls:  []domain.ToolCall{{ID: "call-1", Name: "write", Arguments: json.RawMessage(`{"path":"out.txt","content":"ok"}`)}},
				StopReason: "tool_calls",
			},
		},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
	)

	loop := &Loop{
		Provider: provider, Tools: tools, Events: writer, HookLife: life,
		ToolPolicy: &preflightOnlyPolicy{},
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-1", Model: "m"})
	require.NoError(t, err)

	assert.Contains(t, writer.types(), "tool_call_completed")
	for _, ev := range writer.events {
		if ev.EventType == "tool_call_completed" {
			assert.Contains(t, string(ev.Payload), "Hook: format check passed")
		}
	}
}

func TestHookDoesNotFireForUnmatchedTool(t *testing.T) {
	dir := t.TempDir()
	blockScript := filepath.Join(dir, "block.sh")
	require.NoError(t, os.WriteFile(blockScript,
		[]byte("#!/bin/sh\necho blocked >&2\nexit 2\n"), 0o700))

	hookSet := hooks.HookSet{
		"PreToolUse": {
			Matchers: []hooks.HookMatcherConfig{
				{ID: "m1", Matcher: "bash", Hooks: []hooks.HookConfig{
					{ID: "block-bash", Command: blockScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	encoded, _ := json.Marshal(hookSet)
	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded, WorkspaceRoot: dir, WorkspaceID: "ws-test",
	}).WithRun("run-1", "session-1")

	tools := &fakeTools{
		defs:    toolDefs("read", "bash"),
		classes: map[string]domain.ExecutionClass{"read": domain.ExecutionReadOnly, "bash": domain.ExecutionWorkspaceWrite},
	}
	writer := &memoryWriter{}
	provider := llm.NewFakeProvider(
		llm.FakeStep{
			Completion: domain.Completion{
				ToolCalls:  []domain.ToolCall{{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"test.txt"}`)}},
				StopReason: "tool_calls",
			},
		},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
	)

	loop := &Loop{
		Provider: provider, Tools: tools, Events: writer, HookLife: life,
		ToolPolicy: &preflightOnlyPolicy{},
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-1", Model: "m"})
	require.NoError(t, err)

	assert.Contains(t, writer.types(), "tool_call_started")
	assert.Contains(t, writer.types(), "tool_call_completed")
	assert.NotContains(t, writer.types(), "tool_call_skipped")
}

type preflightOnlyPolicy struct{}

func (p *preflightOnlyPolicy) BeforeToolBatch(_ context.Context, batch ToolBatchContext, calls []domain.ToolCall) ([]ToolDecision, error) {
	decisions := make([]ToolDecision, len(calls))
	for i := range decisions {
		decisions[i] = ToolDecision{Action: ToolAllow}
	}
	return decisions, nil
}

func (p *preflightOnlyPolicy) AfterToolCall(_ context.Context, _ ToolCallContext, _ domain.ToolCall, result domain.ToolResult) (AfterToolDecision, error) {
	return AfterToolDecision{Result: result}, nil
}

func toolDefs(names ...string) []domain.ToolDefinition {
	defs := make([]domain.ToolDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, domain.ToolDefinition{
			Name:        name,
			Description: name + " tool",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		})
	}
	return defs
}

func intPtr(i int) *int { return &i }
