// This file integrates hooks into the agent lifecycle. It wraps a Dispatcher
// and provides strongly-typed methods for each of the 9 lifecycle points.
// Decision hooks (PreToolUse, PostToolUse, Stop, UserPromptSubmit) run
// synchronously; observer hooks (RunEnd, PreCompact, ApprovalRequested,
// Notification) queue via outbox.
//
// HookLifecycle is a thin glue layer between the agent loop and the hooks
// engine. It does NOT own the runner or dispatcher lifecycle — those are
// handled by the Dispatcher.
package agent

import (
	"context"
	"encoding/json"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/hooks"
)

// HookLifecycle wraps the hooks Dispatcher and provides per-event dispatch
// methods tailored to each lifecycle point. A nil *HookLifecycle is a valid
// no-op (all methods return zero values).
type HookLifecycle struct {
	d          *hooks.Dispatcher
	runID      string
	sessionID  string
	workspaceID  string
	workspaceRoot string
}

// NewHookLifecycle creates a lifecycle wrapper from a run's frozen hook config.
// Returns nil when no hooks are configured, so the hot path pays nothing.
func NewHookLifecycle(effective domain.EffectiveHookConfig) *HookLifecycle {
	if len(effective.ResolvedHookSet) == 0 || string(effective.ResolvedHookSet) == "null" {
		return nil
	}
	var set hooks.HookSet
	if err := json.Unmarshal(effective.ResolvedHookSet, &set); err != nil {
		return nil
	}
	if set.IsEmpty() {
		return nil
	}
	d := hooks.NewDispatcher(set, effective.WorkspaceRoot, nil)
	if d == nil {
		return nil
	}
	return &HookLifecycle{
		d:             d,
		runID:         "", // set per-event
		sessionID:     "",
		workspaceID:   effective.WorkspaceID,
		workspaceRoot: effective.WorkspaceRoot,
	}
}

// WithRun scopes the lifecycle to a specific run.
func (h *HookLifecycle) WithRun(runID, sessionID string) *HookLifecycle {
	if h == nil {
		return nil
	}
	return &HookLifecycle{
		d:             h.d,
		runID:         runID,
		sessionID:     sessionID,
		workspaceID:   h.workspaceID,
		workspaceRoot: h.workspaceRoot,
	}
}

// PreToolUse dispatches PreToolUse hooks before a tool is executed.
// Returns the merged decision. A blocked decision means the tool should not
// execute; UpdatedInput, when non-empty, replaces the tool's arguments
// (re-validation and policy checks run afterward by the caller).
func (h *HookLifecycle) PreToolUse(ctx context.Context, toolName string, args json.RawMessage) hooks.Decision {
	return h.dispatch(ctx, hooks.EventPreToolUse, toolName, hooks.HookInput{
		EventType:     hooks.EventPreToolUse,
		RunID:         h.runID,
		SessionID:     h.sessionID,
		WorkspaceID:   h.workspaceID,
		WorkspaceRoot: h.workspaceRoot,
		ToolName:      toolName,
		ToolInput:     args,
	})
}

// PostToolUse dispatches PostToolUse hooks after a tool result is finalized.
// Returns feedback context to append to the tool result. PostToolUse cannot
// block an already-executed tool; block becomes feedback-only.
func (h *HookLifecycle) PostToolUse(ctx context.Context, toolName string, args json.RawMessage, result json.RawMessage, isError bool) string {
	dec := h.dispatch(ctx, "PostToolUse", toolName, hooks.HookInput{
		EventType:     "PostToolUse",
		RunID:         h.runID,
		SessionID:     h.sessionID,
		WorkspaceID:   h.workspaceID,
		WorkspaceRoot: h.workspaceRoot,
		ToolName:      toolName,
		ToolInput:     args,
		ToolResponse:  result,
		IsError:       isError,
	})
	return joinFeedback(dec.Reason, dec.AdditionalContext)
}

// Stop dispatches Stop hooks when the agent loop would naturally end.
// Returns a decision: if Block is true, the run continues with the Reason
// as a guidance message. The caller owns the consecutive-block counter.
func (h *HookLifecycle) Stop(ctx context.Context, iteration int, stopReason string) hooks.Decision {
	return h.dispatch(ctx, "Stop", "", hooks.HookInput{
		EventType:     "Stop",
		RunID:         h.runID,
		SessionID:     h.sessionID,
		WorkspaceID:   h.workspaceID,
		WorkspaceRoot: h.workspaceRoot,
		Iteration:     iteration,
		StopReason:    stopReason,
	})
}

// RunStart dispatches RunStart hooks once per run before the first model
// request. Returns the additionalContext to inject (empty when no hook fired).
func (h *HookLifecycle) RunStart(ctx context.Context, source string) string {
	dec := h.dispatch(ctx, "RunStart", "", hooks.HookInput{
		EventType:     "RunStart",
		RunID:         h.runID,
		SessionID:     h.sessionID,
		WorkspaceID:   h.workspaceID,
		WorkspaceRoot: h.workspaceRoot,
		Source:        source,
	})
	return dec.AdditionalContext
}

// PreCompact dispatches PreCompact hooks before a compaction model call.
// Observer-only; the decision is discarded (fail-open).
func (h *HookLifecycle) PreCompact(ctx context.Context, iteration int, trigger string) {
	h.dispatch(ctx, "PreCompact", "", hooks.HookInput{
		EventType:     "PreCompact",
		RunID:         h.runID,
		SessionID:     h.sessionID,
		WorkspaceID:   h.workspaceID,
		WorkspaceRoot: h.workspaceRoot,
		Iteration:     iteration,
		Trigger:       trigger,
	})
}

func (h *HookLifecycle) dispatch(ctx context.Context, eventType, toolName string, input hooks.HookInput) hooks.Decision {
	if h == nil || h.d == nil {
		return hooks.Decision{}
	}
	input.DeliveryID = h.runID + "-" + eventType // stable key per run+event
	return h.d.Dispatch(ctx, eventType, toolName, input)
}

func joinFeedback(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
}
