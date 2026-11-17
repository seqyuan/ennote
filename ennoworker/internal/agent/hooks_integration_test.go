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
		ToolPolicy:    &preflightOnlyPolicy{},
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	result, err := loop.Run(context.Background(), RunInput{RunID: "run-1", Model: "m"})
	require.NoError(t, err)

	assert.Contains(t, writer.types(), "tool_call_skipped")
	assert.NotContains(t, writer.types(), "tool_call_started")

	// The assistant tool_calls message stays in the transcript, so the blocked call
	// still owes the model a result: skipping the result leaves the request with a
	// tool call nobody answered.
	toolResults := toolResultsOf(t, result.Messages)
	require.Len(t, toolResults, 1)
	assert.Equal(t, "call-1", toolResults[0].ToolCallID)
	assert.True(t, toolResults[0].IsError)
	assert.Contains(t, toolResults[0].Content, "hook_blocked")
	require.NoError(t, llm.ValidateToolMessageSequence(result.Messages))
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
		ToolPolicy:    &preflightOnlyPolicy{},
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
		ToolPolicy:    &preflightOnlyPolicy{},
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
		ToolPolicy:    &preflightOnlyPolicy{},
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	_, err := loop.Run(context.Background(), RunInput{RunID: "run-1", Model: "m"})
	require.NoError(t, err)

	assert.Contains(t, writer.types(), "tool_call_started")
	assert.Contains(t, writer.types(), "tool_call_completed")
	assert.NotContains(t, writer.types(), "tool_call_skipped")
}

// preToolUseBashBlock installs the one PreToolUse matcher these cases need: it
// blocks bash and leaves every unmatched tool name allowed.
func preToolUseBashBlock(t *testing.T) *HookLifecycle {
	t.Helper()
	dir := t.TempDir()
	blockScript := filepath.Join(dir, "block-bash.sh")
	require.NoError(t, os.WriteFile(blockScript, []byte("#!/bin/sh\necho blocked >&2\nexit 2\n"), 0o700))

	hookSet := hooks.HookSet{
		"PreToolUse": {Matchers: []hooks.HookMatcherConfig{
			{ID: "m1", Matcher: "bash", Hooks: []hooks.HookConfig{
				{ID: "block-bash", Command: blockScript, TimeoutSeconds: intPtr(5)},
			}},
		}},
	}
	encoded, err := json.Marshal(hookSet)
	require.NoError(t, err)
	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded, WorkspaceRoot: dir, WorkspaceID: "ws-test",
	}).WithRun("run-blocks", "session-blocks")
	require.NotNil(t, life)
	return life
}

func hookBlockTools() *fakeTools {
	return &fakeTools{
		defs: toolDefs("read", "bash"),
		classes: map[string]domain.ExecutionClass{
			"read": domain.ExecutionReadOnly, "bash": domain.ExecutionWorkspaceWrite,
		},
	}
}

// toolResultsOf lists the tool results a run put in front of the model, in
// transcript order.
func toolResultsOf(t *testing.T, messages []domain.ChatMessage) []domain.ToolResult {
	t.Helper()
	var results []domain.ToolResult
	for _, message := range messages {
		if message.Role != domain.RoleTool {
			continue
		}
		for _, block := range message.Content {
			require.Equal(t, domain.ContentToolResult, block.Kind)
			require.NotNil(t, block.ToolResult)
			results = append(results, *block.ToolResult)
		}
	}
	return results
}

// A PreToolUse block removes a call from the batch without removing it from the
// assistant tool_calls message, so the blocked call must keep its own slot in the
// results slice. Writing survivors at their hook-filtered positions instead left a
// zero-value ToolResult (empty toolCallId) at the end of the slice, and the loop
// appended that as a tool message: the next provider request then failed protocol
// validation, and the blocked call and its surviving neighbour both recorded
// callIndex 0.
func TestPreToolUseBlockKeepsResultsInModelOrder(t *testing.T) {
	life := preToolUseBashBlock(t)
	tools := hookBlockTools()
	calls := []domain.ToolCall{
		{ID: "call-read", Name: "read", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
		{ID: "call-bash", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /"}`)},
	}

	writer := &memoryWriter{}
	loop := &Loop{Tools: tools, Events: writer, HookLife: life, ToolPolicy: &preflightOnlyPolicy{}}
	results, err := loop.executeToolBatch(context.Background(), "run-blocks", 1, calls)
	require.NoError(t, err)

	require.Len(t, results, len(calls), "every model tool call gets a result, blocked or not")
	assert.Equal(t, "call-read", results[0].ToolCallID)
	assert.False(t, results[0].IsError)
	assert.Equal(t, "call-bash", results[1].ToolCallID)
	assert.True(t, results[1].IsError)
	assert.Contains(t, results[1].Content, "hook_blocked")

	// One callIndex space: the blocked call keeps the position it had in the
	// model's batch, and the survivor keeps its own.
	started := writer.payloads("tool_call_started")
	require.Len(t, started, 1)
	assert.Equal(t, float64(0), started[0]["callIndex"])
	skipped := writer.payloads("tool_call_skipped")
	require.Len(t, skipped, 1)
	assert.Equal(t, float64(1), skipped[0]["callIndex"])

	// The transcript built from those results must pass the same check the
	// providers run before every request.
	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: domain.Completion{ToolCalls: calls, StopReason: "tool_calls"}},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
	)
	runLoop := &Loop{
		Provider: provider, Tools: hookBlockTools(), Events: &memoryWriter{}, HookLife: life,
		ToolPolicy:    &preflightOnlyPolicy{},
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	run, err := runLoop.Run(context.Background(), RunInput{RunID: "run-blocks", Model: "m"})
	require.NoError(t, err)
	toolResults := toolResultsOf(t, run.Messages)
	require.Len(t, toolResults, 2)
	assert.Equal(t, "call-read", toolResults[0].ToolCallID)
	assert.Equal(t, "call-bash", toolResults[1].ToolCallID)
	require.NoError(t, llm.ValidateToolMessageSequence(run.Messages))
}

// When every call is blocked the batch is empty, but the results are not: the
// model still has to be answered for each tool_call id it produced.
func TestPreToolUseBlockingEveryCallStillReturnsTheirResults(t *testing.T) {
	life := preToolUseBashBlock(t)
	tools := hookBlockTools()
	calls := []domain.ToolCall{
		{ID: "call-bash-1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /"}`)},
		{ID: "call-bash-2", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf ~"}`)},
	}

	loop := &Loop{Tools: tools, Events: &memoryWriter{}, HookLife: life, ToolPolicy: &preflightOnlyPolicy{}}
	results, err := loop.executeToolBatch(context.Background(), "run-blocks", 1, calls)
	require.NoError(t, err)

	require.Len(t, results, len(calls))
	for index, result := range results {
		assert.Equal(t, calls[index].ID, result.ToolCallID, "result %d answers its own tool call", index)
		assert.NotEmpty(t, result.ToolCallID)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "hook_blocked")
	}

	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: domain.Completion{ToolCalls: calls, StopReason: "tool_calls"}},
		llm.FakeStep{Completion: domain.Completion{StopReason: "end_turn", ActualModel: "m"}},
	)
	runLoop := &Loop{
		Provider: provider, Tools: hookBlockTools(), Events: &memoryWriter{}, HookLife: life,
		ToolPolicy:    &preflightOnlyPolicy{},
		MaxIterations: 2, ContextTokens: 8000, MaxOutput: 500,
	}
	run, err := runLoop.Run(context.Background(), RunInput{RunID: "run-blocks", Model: "m"})
	require.NoError(t, err)
	toolResults := toolResultsOf(t, run.Messages)
	require.Len(t, toolResults, 2)
	assert.Equal(t, "call-bash-1", toolResults[0].ToolCallID)
	assert.Equal(t, "call-bash-2", toolResults[1].ToolCallID)
	require.NoError(t, llm.ValidateToolMessageSequence(run.Messages))
}

// The read group runs adjacent plans, not adjacent calls: a blocked call between
// two read-only calls makes a filtered group whose members are not consecutive in
// the model's batch, so the group may only index its events by each plan's own
// originalIndex.
func TestSafeParallelReadGroupUsesOriginalCallIndexesAfterHookFiltering(t *testing.T) {
	life := preToolUseBashBlock(t)
	tools := hookBlockTools()
	calls := []domain.ToolCall{
		{ID: "read-a", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)},
		{ID: "bash-b", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /"}`)},
		{ID: "read-c", Name: "read", Arguments: json.RawMessage(`{"path":"c"}`)},
		{ID: "read-d", Name: "read", Arguments: json.RawMessage(`{"path":"d"}`)},
	}

	writer := &memoryWriter{}
	loop := &Loop{Tools: tools, Events: writer, HookLife: life, ToolPolicy: &preflightOnlyPolicy{},
		ToolExecution: domain.ToolExecutionConfig{Mode: modeSafeParallel, MaxConcurrentReadTools: 2}}
	results, err := loop.executeToolBatch(context.Background(), "run-blocks", 1, calls)
	require.NoError(t, err)

	require.Len(t, results, len(calls))
	recorded := make(map[int][]string, len(calls))
	for _, event := range writer.payloadsOfEveryType() {
		recorded[event.callIndex] = append(recorded[event.callIndex], event.eventType)
	}
	assert.Equal(t, map[int][]string{
		0: {"tool_call_started", "tool_call_completed"},
		1: {"tool_call_skipped"},
		2: {"tool_call_started", "tool_call_completed"},
		3: {"tool_call_started", "tool_call_completed"},
	}, recorded)
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
