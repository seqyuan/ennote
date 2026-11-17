package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/hooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tool settlement parity. A tool outcome must settle the same way whichever
// dispatch mode obtained it: "sequential" runs calls one at a time, while
// "safe_parallel" runs adjacent read-only calls concurrently but still settles
// them in callIndex order. Every case below runs in both modes and asserts the
// same facts — per-callIndex event sequence, PostToolUse call count, the batch
// error code, and the returned results — so a mode-dependent terminal state is a
// failure here instead of a field report.

const (
	modeSequential   = "sequential"
	modeSafeParallel = "safe_parallel"
)

// readOnlyFakeTools reports every tool as read-only so safe_parallel really
// routes its calls through the concurrent read group instead of falling back to
// the sequential dispatcher. Without this the parallel half of every case would
// silently exercise the sequential path.
type readOnlyFakeTools struct {
	*fakeTools
}

func (t *readOnlyFakeTools) ExecutionClass(string) domain.ExecutionClass {
	return domain.ExecutionReadOnly
}

// readOnlyRetryFakeTools adds a retry policy; cancellation is only observable
// as a ToolCancelled outcome when the tool opted into retries (the attempt loop
// re-checks the context between attempts).
type readOnlyRetryFakeTools struct {
	*readOnlyFakeTools
	policy domain.ToolRetryPolicy
}

func (t *readOnlyRetryFakeTools) RetryPolicy(string) domain.ToolRetryPolicy {
	return t.policy
}

func readOnlyTools(execute func(context.Context, domain.ToolCall) (domain.ToolResult, error)) *readOnlyFakeTools {
	return &readOnlyFakeTools{fakeTools: &fakeTools{execute: execute}}
}

// failingPostPolicy fails the post-execution policy hook so settlement has to
// record a failed call and surface domain.ErrorToolPolicyFailed.
type failingPostPolicy struct{ preflightOnlyPolicy }

func (p *failingPostPolicy) AfterToolCall(context.Context, ToolCallContext, domain.ToolCall, domain.ToolResult) (AfterToolDecision, error) {
	return AfterToolDecision{}, errors.New("post policy hook failed")
}

// settlementProbe carries the per-run observations a case reasons about: how
// many calls actually reached Execute, whether one is in flight, and where the
// PostToolUse hook logged its invocations.
type settlementProbe struct {
	executed atomic.Int32
	once     sync.Once
	started  chan struct{}
	hookLog  string
}

func newSettlementProbe() *settlementProbe {
	return &settlementProbe{started: make(chan struct{})}
}

func (p *settlementProbe) markStarted() {
	p.once.Do(func() { close(p.started) })
}

// settlementRun is everything a case may assert about one mode's execution.
type settlementRun struct {
	mode           string
	results        []domain.ToolResult
	err            error
	writer         *memoryWriter
	postToolUseRan int
}

// eventsFor lists the durable event types recorded for one callIndex, in append
// order. callIndex is the only stable identity: execution order is not.
func (r settlementRun) eventsFor(callIndex int) []string {
	var types []string
	for _, event := range r.writer.payloadsOfEveryType() {
		if event.callIndex != callIndex {
			continue
		}
		types = append(types, event.eventType)
	}
	return types
}

func (r settlementRun) payloadsFor(eventType string, callIndex int) []map[string]any {
	var matched []map[string]any
	for _, payload := range r.writer.payloads(eventType) {
		if payload["callIndex"] == float64(callIndex) {
			matched = append(matched, payload)
		}
	}
	return matched
}

type recordedEvent struct {
	eventType string
	callIndex int
}

// payloadsOfEveryType decodes the event stream once, keeping the order the
// recorder used. memoryWriter.payloads filters by type, which is not enough to
// observe the sequence a single callIndex went through.
func (w *memoryWriter) payloadsOfEveryType() []recordedEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	events := make([]recordedEvent, 0, len(w.events))
	for _, event := range w.events {
		var decoded map[string]any
		if err := json.Unmarshal(event.Payload, &decoded); err != nil {
			continue
		}
		index, ok := decoded["callIndex"].(float64)
		if !ok {
			continue
		}
		events = append(events, recordedEvent{eventType: event.EventType, callIndex: int(index)})
	}
	return events
}

// settlementCase is one scenario expressed once and run in both modes.
type settlementCase struct {
	name  string
	calls []domain.ToolCall
	// onlyModes restricts a case to the modes where its trigger exists at all
	// (a semaphore queue only exists in safe_parallel).
	onlyModes []string
	// tools builds the tool set; cancel is the batch context's own canceller so
	// a case can order cancellation against tool execution.
	tools  func(t *testing.T, cancel context.CancelFunc, probe *settlementProbe) domain.ToolRunner
	hooks  func(t *testing.T, probe *settlementProbe) *HookLifecycle
	policy ToolPolicy
	// maxConcurrent is the read-group semaphore size; zero keeps the default.
	maxConcurrent int
	// writerFailTypes makes the durable writer refuse these event types, so a
	// case can observe settlement while a record cannot be persisted.
	writerFailTypes map[string]error
	// terminalWriteFails relaxes the "settles exactly once" assertion: a refused
	// tool_call_completed leaves its call with no terminal event at all, so the
	// guard there is that no callIndex ever records two.
	terminalWriteFails bool
	// wantEvents is the exact event sequence owed to each callIndex.
	wantEvents map[int][]string
	// wantPostToolUse is the number of PostToolUse hook invocations.
	wantPostToolUse int
	// wantBatchError declares the batch error code for each mode the case runs
	// in; an empty code means "no error". A nil map means no error in any mode.
	wantBatchError map[string]domain.ErrorCode
	// check holds the case's own mode-independent assertions.
	check func(t *testing.T, probe *settlementProbe, run settlementRun)
}

func parityCall(id string) domain.ToolCall {
	return domain.ToolCall{ID: id, Name: "read", Arguments: json.RawMessage(`{}`)}
}

func TestToolSettlementParity(t *testing.T) {
	for _, tc := range settlementCases() {
		t.Run(tc.name, func(t *testing.T) {
			runs := make(map[string]settlementRun, 2)
			for _, mode := range []string{modeSequential, modeSafeParallel} {
				if !runsInMode(tc, mode) {
					continue
				}
				probe := newSettlementProbe()
				run := runSettlementCase(t, tc, mode, probe)
				runs[mode] = run
				t.Run(mode, func(t *testing.T) {
					assertSettlement(t, tc, run, probe)
					if tc.check != nil {
						tc.check(t, probe, run)
					}
				})
			}
			if len(runs) == 2 {
				assertSettlementParityAcrossModes(t, runs[modeSequential], runs[modeSafeParallel])
			}
		})
	}
}

// TestToolSettlementTerminationCodeBeatsApprovalDecisionCode pins invariant 2 for
// the case it was only conditionally true for: an approved call that is then
// cancelled must record tool_cancelled, not the approval_approved code that let it
// run. A panic reaches the same branch and gets the same override.
func TestToolSettlementTerminationCodeBeatsApprovalDecisionCode(t *testing.T) {
	writer := &memoryWriter{}
	loop := &Loop{Events: writer}
	plan := plannedToolCall{
		original: parityCall("call-0"), effective: parityCall("call-0"), allowed: true,
		decision: ToolDecision{Action: ToolAllow, Code: "approval_approved", RiskClass: domain.RiskExternal},
	}
	outcome := domain.ToolExecutionOutcome{
		Result: domain.ToolResult{ToolCallID: "call-0", ToolName: "read",
			Content: "tool execution cancelled: context canceled", IsError: true},
		AttemptCount: 1, Kind: domain.ToolCancelled, Cause: context.Canceled,
	}

	_, stop, err := loop.settleToolPlan(context.Background(), "run-approval-cancel", 1, "record-0", plan, outcome)

	require.NoError(t, err)
	assert.False(t, stop)
	failed := writer.payloads("tool_call_failed")
	require.Len(t, failed, 1)
	assert.Equal(t, "tool_cancelled", failed[0]["category"],
		"the termination code wins over the approval code that allowed the call")
}

func runsInMode(tc settlementCase, mode string) bool {
	if len(tc.onlyModes) == 0 {
		return true
	}
	for _, allowed := range tc.onlyModes {
		if allowed == mode {
			return true
		}
	}
	return false
}

func runSettlementCase(t *testing.T, tc settlementCase, mode string, probe *settlementProbe) settlementRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := &memoryWriter{failTypes: tc.writerFailTypes}
	loop := &Loop{
		Tools: tc.tools(t, cancel, probe), Events: writer,
		ToolExecution: domain.ToolExecutionConfig{Mode: mode, MaxConcurrentReadTools: tc.maxConcurrent},
	}
	loop.ToolPolicy = tc.policy
	if tc.hooks != nil {
		loop.HookLife = tc.hooks(t, probe)
	}

	results, err := loop.executeToolBatch(ctx, "run-settlement-parity", 1, tc.calls)
	return settlementRun{
		mode: mode, results: results, err: err, writer: writer,
		postToolUseRan: countHookInvocations(t, probe.hookLog),
	}
}

func assertSettlement(t *testing.T, tc settlementCase, run settlementRun, probe *settlementProbe) {
	t.Helper()
	// Every call settles exactly once whatever mode settled it: one started event
	// (no lingering start) and, unless the writer refused it, one terminal event
	// (never both completed and failed).
	for callIndex := range tc.calls {
		assert.Equal(t, 1, countEvent(run.eventsFor(callIndex), "tool_call_started"),
			"callIndex %d must start exactly once", callIndex)
		terminal := countEvent(run.eventsFor(callIndex), "tool_call_completed") +
			countEvent(run.eventsFor(callIndex), "tool_call_failed")
		if tc.terminalWriteFails {
			assert.LessOrEqual(t, terminal, 1, "callIndex %d must never settle twice", callIndex)
		} else {
			assert.Equal(t, 1, terminal, "callIndex %d must settle exactly once", callIndex)
		}
		if want, ok := tc.wantEvents[callIndex]; ok {
			assert.Equal(t, want, run.eventsFor(callIndex),
				"callIndex %d must record this event sequence", callIndex)
		}
	}
	assert.Equal(t, tc.wantPostToolUse, run.postToolUseRan, "PostToolUse invocations")

	wantCode, declared := tc.wantBatchError[run.mode]
	require.True(t, declared || tc.wantBatchError == nil,
		"case declares no expectation for mode %s", run.mode)
	if wantCode == "" {
		require.NoError(t, run.err)
		return
	}
	require.Error(t, run.err)
	assert.Equal(t, wantCode, domain.ErrorCodeOf(run.err))
}

// assertSettlementParityAcrossModes is the parity claim itself: the same case
// must produce the same results, the same per-callIndex event sequences and the
// same number of PostToolUse invocations in both dispatch modes.
func assertSettlementParityAcrossModes(t *testing.T, sequential, parallel settlementRun) {
	t.Helper()
	require.Len(t, parallel.results, len(sequential.results))
	for index := range sequential.results {
		assert.Equal(t, sequential.results[index].ToolCallID, parallel.results[index].ToolCallID,
			"callIndex %d toolCallId", index)
		assert.Equal(t, sequential.results[index].Content, parallel.results[index].Content,
			"callIndex %d content", index)
		assert.Equal(t, sequential.results[index].IsError, parallel.results[index].IsError,
			"callIndex %d isError", index)
		assert.Equal(t, sequential.eventsFor(index), parallel.eventsFor(index),
			"callIndex %d event sequence", index)
	}
	assert.Equal(t, sequential.postToolUseRan, parallel.postToolUseRan, "PostToolUse invocations")
}

// postToolUseHook installs a PostToolUse hook that logs every invocation and
// answers with fixed feedback context, so a case can assert both the call count
// and that the feedback was appended exactly once.
func postToolUseHook(t *testing.T, probe *settlementProbe) *HookLifecycle {
	t.Helper()
	dir := t.TempDir()
	probe.hookLog = filepath.Join(dir, "post_tool_use.log")
	script := filepath.Join(dir, "post_tool_use.sh")
	body := fmt.Sprintf("#!/bin/sh\necho called >> %q\necho '{\"additionalContext\":\"hook feedback\"}'\n", probe.hookLog)
	require.NoError(t, os.WriteFile(script, []byte(body), 0o700))

	hookSet := hooks.HookSet{
		"PostToolUse": {Matchers: []hooks.HookMatcherConfig{
			{ID: "parity", Matcher: "*", Hooks: []hooks.HookConfig{
				{ID: "feedback", Command: script, TimeoutSeconds: intPtr(5)},
			}},
		}},
	}
	encoded, err := json.Marshal(hookSet)
	require.NoError(t, err)
	life := NewHookLifecycle(domain.EffectiveHookConfig{
		ResolvedHookSet: encoded, WorkspaceRoot: dir, WorkspaceID: "ws-settlement-parity",
	}).WithRun("run-settlement-parity", "session-settlement-parity")
	require.NotNil(t, life)
	return life
}

func countHookInvocations(t *testing.T, path string) int {
	t.Helper()
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	require.NoError(t, err)
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func settlementCases() []settlementCase {
	return []settlementCase{
		{
			name:  "normal_return",
			calls: []domain.ToolCall{parityCall("call-0"), parityCall("call-1")},
			tools: func(_ *testing.T, _ context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
					probe.executed.Add(1)
					return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "result of " + call.ID}, nil
				})
			},
			wantEvents: map[int][]string{
				0: {"tool_call_started", "tool_call_completed"},
				1: {"tool_call_started", "tool_call_completed"},
			},
			check: func(t *testing.T, probe *settlementProbe, run settlementRun) {
				require.Len(t, run.results, 2)
				assert.Equal(t, "result of call-0", run.results[0].Content)
				assert.Equal(t, "result of call-1", run.results[1].Content)
				assert.False(t, run.results[0].IsError)
				assert.Equal(t, int32(2), probe.executed.Load())
			},
		},
		{
			name:  "tool_error_does_not_cancel_sibling",
			calls: []domain.ToolCall{parityCall("call-0"), parityCall("call-1")},
			tools: func(_ *testing.T, _ context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
					probe.executed.Add(1)
					return domain.ToolResult{
						ToolCallID: call.ID, ToolName: call.Name,
						Content: "tool said no: " + call.ID, IsError: call.ID == "call-0",
					}, nil
				})
			},
			wantEvents: map[int][]string{
				0: {"tool_call_started", "tool_call_completed"},
				1: {"tool_call_started", "tool_call_completed"},
			},
			check: func(t *testing.T, probe *settlementProbe, run settlementRun) {
				require.Len(t, run.results, 2)
				assert.True(t, run.results[0].IsError, "a tool-level error is still a completed call")
				assert.False(t, run.results[1].IsError)
				assert.Equal(t, "tool said no: call-1", run.results[1].Content)
				assert.Equal(t, int32(2), probe.executed.Load(), "a failing tool must not cancel its sibling")
			},
		},
		{
			name:  "panic_settles_as_failed_with_tool_panicked",
			calls: []domain.ToolCall{parityCall("call-0")},
			tools: func(_ *testing.T, _ context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(context.Context, domain.ToolCall) (domain.ToolResult, error) {
					probe.executed.Add(1)
					panic("boom")
				})
			},
			wantEvents: map[int][]string{
				0: {"tool_call_started", "tool_call_failed"},
			},
			check: func(t *testing.T, _ *settlementProbe, run settlementRun) {
				require.Len(t, run.results, 1)
				assert.True(t, run.results[0].IsError)
				assert.Contains(t, run.results[0].Content, "tool panic: boom")
				failed := run.payloadsFor("tool_call_failed", 0)
				require.Len(t, failed, 1)
				assert.Equal(t, "tool_panicked", failed[0]["category"],
					"a panicked call must never settle as completed or with a blank category")
				// The failed payload carries the result too: the client renders the
				// content, and without it a panic card falls back to a generic line.
				content, _ := failed[0]["content"].(string)
				assert.Contains(t, content, "tool panic")
				assert.Equal(t, true, failed[0]["isError"])
				assert.Equal(t, float64(1), failed[0]["attemptCount"])
			},
		},
		{
			name:  "external_cancel_of_in_flight_call",
			calls: []domain.ToolCall{parityCall("call-0")},
			tools: func(_ *testing.T, cancel context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				tools := &readOnlyRetryFakeTools{
					readOnlyFakeTools: readOnlyTools(func(ctx context.Context, _ domain.ToolCall) (domain.ToolResult, error) {
						probe.executed.Add(1)
						probe.markStarted()
						<-ctx.Done() // in flight until the run is cancelled
						return domain.ToolResult{}, transientFailure("connection reset")
					}),
					policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 2},
				}
				// The cancellation is external to the tool: it lands while the call
				// is running and the tool only observes it through its context.
				go func() {
					<-probe.started
					cancel()
				}()
				return tools
			},
			wantEvents: map[int][]string{
				0: {"tool_call_started", "tool_call_failed"},
			},
			// A cancelled outcome is a tool-level failure, so the cancelled branch
			// returns no error. Only the read group re-checks ctx at its tail, so
			// safe_parallel also surfaces the cancellation to the caller; that
			// asymmetry predates settlement parity and is reported, not fixed here.
			wantBatchError: map[string]domain.ErrorCode{
				modeSequential:   "",
				modeSafeParallel: domain.ErrorRunCancelled,
			},
			check: func(t *testing.T, probe *settlementProbe, run settlementRun) {
				require.Len(t, run.results, 1)
				assert.True(t, run.results[0].IsError)
				assert.Contains(t, run.results[0].Content, "cancelled")
				failed := run.payloadsFor("tool_call_failed", 0)
				require.Len(t, failed, 1)
				assert.Equal(t, "tool_cancelled", failed[0]["category"])
				assert.Equal(t, float64(1), failed[0]["attemptCount"], "the call had started when it was cancelled")
				assert.Equal(t, int32(1), probe.executed.Load(), "a cancelled call must not be retried")
			},
		},
		{
			name:  "parallel_cancel_before_semaphore_grants_the_call",
			calls: []domain.ToolCall{parityCall("call-0"), parityCall("call-1")},
			// The queue only exists when more calls than slots are dispatched, so
			// this trigger has no sequential counterpart.
			onlyModes:     []string{modeSafeParallel},
			maxConcurrent: 1,
			tools: func(_ *testing.T, cancel context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
					if probe.executed.Add(1) > 1 {
						return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "ran " + call.ID}, nil
					}
					// Whichever call wins the only slot is cancelled in flight and then keeps
					// the slot for a moment: the other call must observe a done batch context
					// while no slot is available, i.e. "cancelled before this call started".
					cancel()
					time.Sleep(100 * time.Millisecond)
					return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "ran " + call.ID}, nil
				})
			},
			// The slot race makes the winning callIndex non-deterministic, so the
			// assertions below are by event role rather than by index.
			wantBatchError: map[string]domain.ErrorCode{modeSafeParallel: domain.ErrorRunCancelled},
			check: func(t *testing.T, probe *settlementProbe, run settlementRun) {
				require.Len(t, run.results, 2)
				assert.Equal(t, int32(1), probe.executed.Load(), "the queued call must never reach Execute")

				completed, failed := 0, 0
				for callIndex := range run.results {
					completed += countEvent(run.eventsFor(callIndex), "tool_call_completed")
					if countEvent(run.eventsFor(callIndex), "tool_call_failed") != 1 {
						continue
					}
					failed++
					queued := run.payloadsFor("tool_call_failed", callIndex)
					require.Len(t, queued, 1)
					assert.Equal(t, "tool_cancelled", queued[0]["category"])
					// The failed call here is the one that never reached Execute, so its
					// attempt count is the documented "never started" zero.
					assert.Equal(t, float64(0), queued[0]["attemptCount"])
					// Two cancellation texts are legitimate: under load the queued call can
					// win the semaphore and be cancelled inside the attempt loop instead.
					assert.Contains(t, run.results[callIndex].Content, "cancelled")
					assert.NotContains(t, run.results[callIndex].Content, "denied",
						"nothing denied this call, so a policy denial would be a fabricated cause")
				}
				assert.Equal(t, 1, completed, "the call that held the slot must settle as completed")
				assert.Equal(t, 1, failed, "the queued call must settle as failed")
			},
		},
		{
			name:  "infrastructure_failure_after_retries_settles_completed",
			calls: []domain.ToolCall{parityCall("call-0")},
			tools: func(_ *testing.T, _ context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return &readOnlyRetryFakeTools{
					readOnlyFakeTools: readOnlyTools(func(_ context.Context, _ domain.ToolCall) (domain.ToolResult, error) {
						probe.executed.Add(1)
						return domain.ToolResult{}, transientFailure("connection reset")
					}),
					policy: domain.ToolRetryPolicy{Mode: domain.ToolRetryTransient, MaxRetries: 1},
				}
			},
			// An exhausted infrastructure failure is still a completed call: the model
			// receives the error as a tool result, so the event must not be "failed".
			wantEvents: map[int][]string{0: {"tool_call_started", "tool_call_completed"}},
			check: func(t *testing.T, probe *settlementProbe, run settlementRun) {
				assert.Equal(t, int32(2), probe.executed.Load(), "1 attempt + 1 retry")
				require.Len(t, run.results, 1)
				assert.True(t, run.results[0].IsError)
				assert.Contains(t, run.results[0].Content, "tool execution failed after 2 attempts")
				completed := run.payloadsFor("tool_call_completed", 0)
				require.Len(t, completed, 1)
				assert.Equal(t, true, completed[0]["isError"])
				assert.Empty(t, run.payloadsFor("tool_call_failed", 0),
					"an infrastructure failure is not a failed call")
			},
		},
		{
			name:  "event_append_failure_on_completed",
			calls: []domain.ToolCall{parityCall("call-0")},
			tools: func(_ *testing.T, _ context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
					probe.executed.Add(1)
					return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "result of " + call.ID}, nil
				})
			},
			writerFailTypes:    map[string]error{"tool_call_completed": errors.New("disk full")},
			terminalWriteFails: true,
			wantBatchError: map[string]domain.ErrorCode{
				modeSequential:   domain.ErrorEventPersistence,
				modeSafeParallel: domain.ErrorEventPersistence,
			},
			check: func(t *testing.T, probe *settlementProbe, run settlementRun) {
				assert.Equal(t, int32(1), probe.executed.Load(), "the tool ran; only its record was refused")
				require.Len(t, run.results, 1)
				assert.Equal(t, 1, countEvent(run.eventsFor(0), "tool_call_started"))
				assert.Zero(t, countEvent(run.eventsFor(0), "tool_call_completed"))
				assert.Zero(t, countEvent(run.eventsFor(0), "tool_call_failed"),
					"a refused completion must not be papered over with a fabricated failure")
			},
		},
		{
			name:  "post_tool_use_feedback_appended_once",
			calls: []domain.ToolCall{parityCall("call-0"), parityCall("call-1")},
			tools: func(_ *testing.T, _ context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
					probe.executed.Add(1)
					return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "result of " + call.ID}, nil
				})
			},
			hooks:           postToolUseHook,
			wantPostToolUse: 2,
			wantEvents: map[int][]string{
				0: {"tool_call_started", "tool_call_completed"},
				1: {"tool_call_started", "tool_call_completed"},
			},
			check: func(t *testing.T, _ *settlementProbe, run settlementRun) {
				require.Len(t, run.results, 2)
				for index, result := range run.results {
					assert.Equal(t, 1, strings.Count(result.Content, "hook feedback"),
						"callIndex %d must append the PostToolUse feedback exactly once", index)
				}
			},
		},
		{
			// Behaviour change S1 introduces on purpose: PostToolUse means "the tool
			// produced a model-visible result", and a panic produced none.
			name:  "post_tool_use_does_not_fire_on_panic",
			calls: []domain.ToolCall{parityCall("call-0")},
			tools: func(_ *testing.T, _ context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(context.Context, domain.ToolCall) (domain.ToolResult, error) {
					probe.executed.Add(1)
					panic("boom")
				})
			},
			hooks:           postToolUseHook,
			wantPostToolUse: 0,
			wantEvents: map[int][]string{
				0: {"tool_call_started", "tool_call_failed"},
			},
			check: func(t *testing.T, _ *settlementProbe, run settlementRun) {
				failed := run.payloadsFor("tool_call_failed", 0)
				require.Len(t, failed, 1)
				assert.Equal(t, "tool_panicked", failed[0]["category"])
			},
		},
		{
			name:  "post_policy_error_fails_the_call",
			calls: []domain.ToolCall{parityCall("call-0")},
			tools: func(_ *testing.T, _ context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
					probe.executed.Add(1)
					return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "result of " + call.ID}, nil
				})
			},
			policy:         &failingPostPolicy{},
			wantEvents:     map[int][]string{0: {"tool_call_started", "tool_call_failed"}},
			wantBatchError: map[string]domain.ErrorCode{modeSequential: domain.ErrorToolPolicyFailed, modeSafeParallel: domain.ErrorToolPolicyFailed},
			check: func(t *testing.T, _ *settlementProbe, run settlementRun) {
				failed := run.payloadsFor("tool_call_failed", 0)
				require.Len(t, failed, 1)
				assert.Equal(t, "policy_hook_failed", failed[0]["category"])
			},
		},
		{
			name:  "cancel_after_the_completed_record_was_written",
			calls: []domain.ToolCall{parityCall("call-0")},
			tools: func(_ *testing.T, cancel context.CancelFunc, probe *settlementProbe) domain.ToolRunner {
				return readOnlyTools(func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
					probe.executed.Add(1)
					// The call finished, then the run is cancelled: settlement must still
					// persist exactly one terminal event and report the cancellation.
					cancel()
					return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "result of " + call.ID}, nil
				})
			},
			wantEvents:     map[int][]string{0: {"tool_call_started", "tool_call_completed"}},
			wantBatchError: map[string]domain.ErrorCode{modeSequential: domain.ErrorRunCancelled, modeSafeParallel: domain.ErrorRunCancelled},
			check: func(t *testing.T, _ *settlementProbe, run settlementRun) {
				require.Len(t, run.results, 1)
				assert.Equal(t, "result of call-0", run.results[0].Content)
			},
		},
	}
}
