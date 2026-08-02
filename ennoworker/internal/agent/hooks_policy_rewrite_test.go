package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/hooks"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingPolicy captures the effective arguments ToolPolicy saw, proving that
// PreToolUse rewrites flow through the full policy gate.
type recordingPolicy struct {
	mu       sync.Mutex
	observed []domain.ToolCall
}

func (p *recordingPolicy) BeforeToolBatch(_ context.Context, _ ToolBatchContext, calls []domain.ToolCall) ([]ToolDecision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observed = append(p.observed, calls...)
	decisions := make([]ToolDecision, len(calls))
	for i := range decisions {
		decisions[i] = ToolDecision{Action: ToolAllow}
	}
	return decisions, nil
}

func (p *recordingPolicy) AfterToolCall(_ context.Context, _ ToolCallContext, _ domain.ToolCall, result domain.ToolResult) (AfterToolDecision, error) {
	return AfterToolDecision{Result: result}, nil
}

func (p *recordingPolicy) seen() []domain.ToolCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]domain.ToolCall(nil), p.observed...)
}

func TestPreToolUseRewriteFeedsPolicy(t *testing.T) {
	dir := t.TempDir()
	rewriteScript := filepath.Join(dir, "rewrite.sh")
	// Rewrites the command to a safe one.
	require.NoError(t, os.WriteFile(rewriteScript,
		[]byte("#!/bin/sh\necho '{\"updatedInput\":{\"command\":\"safe-wrapper clean\",\"path\":\"x\"}}'\n"), 0o700))

	hookSet := hooks.HookSet{
		"PreToolUse": {
			Matchers: []hooks.HookMatcherConfig{
				{ID: "m1", Matcher: "bash", Hooks: []hooks.HookConfig{
					{ID: "rewrite", Command: rewriteScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	encoded, _ := json.Marshal(hookSet)
	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded, WorkspaceRoot: dir, WorkspaceID: "ws-test",
	}).WithRun("run-1", "session-1")

	policy := &recordingPolicy{}
	tools := &fakeTools{
		defs:    toolDefs("bash"),
		classes: map[string]domain.ExecutionClass{"bash": domain.ExecutionWorkspaceWrite},
	}
	writer := &memoryWriter{}
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: domain.Completion{
			ToolCalls:  []domain.ToolCall{{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /"}`)}},
			StopReason: "tool_calls",
		}},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
	)

	loop := &Loop{
		Provider: provider, Tools: tools, Events: writer, HookLife: life, ToolPolicy: policy,
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-1", Model: "m"})
	require.NoError(t, err)

	// The policy must have seen the REWRITTEN (safe) arguments, not the original.
	seen := policy.seen()
	require.Len(t, seen, 1, "policy should observe the rewritten call")
	assert.Contains(t, string(seen[0].Arguments), "safe-wrapper")
	assert.NotContains(t, string(seen[0].Arguments), "rm -rf")
}
