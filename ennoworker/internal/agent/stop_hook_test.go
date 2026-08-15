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

func TestStopHookBlockLimitForcesEnd(t *testing.T) {
	dir := t.TempDir()
	blockScript := filepath.Join(dir, "block.sh")
	require.NoError(t, os.WriteFile(blockScript, []byte("#!/bin/sh\necho '{\"decision\":\"block\",\"reason\":\"keep going\"}'\n"), 0o700))

	hookSet := hooks.HookSet{
		"Stop": {
			Matchers: []hooks.HookMatcherConfig{
				{ID: "m1", Hooks: []hooks.HookConfig{
					{ID: "always-block", Command: blockScript, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	encoded, _ := json.Marshal(hookSet)
	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded, WorkspaceRoot: dir, WorkspaceID: "ws-test",
	}).WithRun("run-1", "session-1")

	tools := &fakeTools{defs: toolDefs("read")}
	writer := &memoryWriter{}
	// The model keeps returning text (no tool calls), so every iteration ends
	// naturally → Stop hook fires each time. MaxIterations is high so the Stop
	// limit (5) triggers first.
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
	)

	loop := &Loop{
		Provider: provider, Tools: tools, Events: writer, HookLife: life,
		ToolPolicy:    &preflightOnlyPolicy{},
		MaxIterations: 10, ContextTokens: 8000, MaxOutput: 500,
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-1", Model: "m"})
	require.NoError(t, err)

	// After 5 consecutive blocks the run must have ended with the limit event.
	assert.Contains(t, writer.types(), "hook_stop_limit_reached")
}

func TestStopHookAllowResetsCounter(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "toggle.sh")
	// Alternate: block once, allow once. The allow resets the counter, so the
	// run continues past 5 blocks without hitting the limit.
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nif [ -f \"$0.flag\" ]; then rm \"$0.flag\"; echo '{\"decision\":\"block\",\"reason\":\"b\"}'; else touch \"$0.flag\"; exit 0; fi\n"), 0o700))

	hookSet := hooks.HookSet{
		"Stop": {
			Matchers: []hooks.HookMatcherConfig{
				{ID: "m1", Hooks: []hooks.HookConfig{
					{ID: "toggle", Command: script, TimeoutSeconds: intPtr(5)},
				}},
			},
		},
	}
	encoded, _ := json.Marshal(hookSet)
	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded, WorkspaceRoot: dir, WorkspaceID: "ws-test",
	}).WithRun("run-2", "session-1")

	tools := &fakeTools{defs: toolDefs("read")}
	writer := &memoryWriter{}
	// 12 natural-end steps: block/allow alternates 6 pairs, never 5 consecutive.
	steps := make([]llm.FakeStep, 0, 12)
	for i := 0; i < 12; i++ {
		steps = append(steps, llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}})
	}

	loop := &Loop{
		Provider: providerFromSteps(steps), Tools: tools, Events: writer, HookLife: life,
		ToolPolicy:    &preflightOnlyPolicy{},
		MaxIterations: 20, ContextTokens: 8000, MaxOutput: 500,
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-2", Model: "m"})
	require.NoError(t, err)

	// Alternating block/allow never hits the consecutive limit.
	assert.NotContains(t, writer.types(), "hook_stop_limit_reached")
}

// providerFromSteps builds a fake provider that returns the given steps in order.
func providerFromSteps(steps []llm.FakeStep) *llm.FakeProvider {
	return llm.NewFakeProvider(steps...)
}
