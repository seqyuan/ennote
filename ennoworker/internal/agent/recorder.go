package agent

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

func (l *Loop) appendEvent(ctx context.Context, runID, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.NewCodedError(domain.ErrorEventPersistence, err)
	}
	if _, err := l.Events.Append(ctx, runID, domain.PendingEvent{EventType: eventType, Payload: encoded}); err != nil {
		return domain.NewCodedError(domain.ErrorEventPersistence, err)
	}
	return nil
}

func (l *Loop) recordModelStarted(ctx context.Context, call domain.ModelCallStart) error {
	if l.Recorder != nil {
		if err := l.Recorder.ModelStarted(ctx, call); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	return l.appendEvent(ctx, call.RunID, "model_call_started", map[string]any{
		"callId": call.ID, "iteration": call.Iteration, "attempt": call.Attempt,
		"requestGeneration": call.RequestGeneration, "modelProfileId": call.ModelProfileID,
	})
}

func (l *Loop) recordModelUsage(ctx context.Context, call domain.ModelCallFinish) error {
	if l.Recorder != nil {
		if err := l.Recorder.ModelUsage(ctx, call); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	return l.appendEvent(ctx, call.RunID, "usage_updated", map[string]any{
		"callId": call.ID, "iteration": call.Iteration, "attempt": call.Attempt,
		"requestGeneration": call.RequestGeneration, "usage": call.Usage,
	})
}

func (l *Loop) recordModelCompleted(ctx context.Context, call domain.ModelCallFinish) error {
	if l.Recorder != nil {
		if err := l.Recorder.ModelCompleted(ctx, call); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	return l.appendEvent(ctx, call.RunID, "model_call_completed", map[string]any{
		"callId": call.ID, "iteration": call.Iteration, "attempt": call.Attempt,
		"requestGeneration": call.RequestGeneration, "stopReason": call.StopReason,
		"actualModel": call.ActualModel, "usage": call.Usage,
	})
}

func (l *Loop) recordModelFailed(ctx context.Context, call domain.ModelCallFinish) error {
	if l.Recorder != nil {
		if err := l.Recorder.ModelFailed(ctx, call); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	eventType := "model_call_attempt_failed"
	if call.Final {
		eventType = "model_call_failed"
	}
	return l.appendEvent(ctx, call.RunID, eventType, map[string]any{
		"callId": call.ID, "iteration": call.Iteration, "attempt": call.Attempt,
		"requestGeneration": call.RequestGeneration, "category": call.ErrorCode,
		"error": call.Error, "retryable": call.Retryable,
	})
}

func (l *Loop) recordToolStarted(ctx context.Context, call domain.ToolCallStart) error {
	if l.Recorder != nil {
		if err := l.Recorder.ToolStarted(ctx, call); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	return l.appendEvent(ctx, call.RunID, "tool_call_started", map[string]any{
		"recordId": call.ID, "iteration": call.Iteration, "callIndex": call.CallIndex,
		"toolCallId": call.Call.ID, "toolName": call.Call.Name, "arguments": call.Call.Arguments,
	})
}

func (l *Loop) recordToolCompleted(ctx context.Context, call domain.ToolCallFinish) error {
	if l.Recorder != nil {
		if err := l.Recorder.ToolCompleted(ctx, call); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	return l.appendEvent(ctx, call.RunID, "tool_call_completed", map[string]any{
		"recordId": call.ID, "iteration": call.Iteration, "callIndex": call.CallIndex,
		"toolCallId": call.Result.ToolCallID, "toolName": call.Result.ToolName,
		"content": call.Result.Content, "isError": call.Result.IsError, "artifacts": call.Result.Artifacts,
		"attemptCount": call.AttemptCount,
	})
}

func (l *Loop) recordToolFailed(ctx context.Context, call domain.ToolCallFinish) error {
	if l.Recorder != nil {
		if err := l.Recorder.ToolFailed(ctx, call); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	return l.appendEvent(ctx, call.RunID, "tool_call_failed", map[string]any{
		"recordId": call.ID, "iteration": call.Iteration, "callIndex": call.CallIndex,
		"toolCallId": call.Call.ID, "toolName": call.Call.Name, "category": call.Policy.Code,
	})
}

func (l *Loop) recordModelRouteSelected(ctx context.Context, runID string, iteration int, runtime domain.ModelRuntimeSnapshot, reason string) error {
	if l.Recorder != nil {
		if err := l.Recorder.ModelRouteSelected(ctx, runID, iteration, runtime, reason); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	return l.appendEvent(ctx, runID, "model_route_selected", map[string]any{
		"iteration": iteration, "providerProfileId": runtime.ProviderProfileID,
		"modelProfileId": runtime.ModelProfileID, "apiModel": runtime.APIModel, "reason": reason,
	})
}

func (l *Loop) recordToolSkipped(ctx context.Context, runID string, iteration, callIndex int, call domain.ToolCall, result domain.ToolResult, reason string, policies ...domain.ToolPolicyMetadata) error {
	var policy domain.ToolPolicyMetadata
	if len(policies) > 0 {
		policy = policies[0]
	}
	finish := domain.ToolCallFinish{
		ID: uuid.NewString(), RunID: runID, Iteration: iteration, CallIndex: callIndex,
		Call: call, RawResult: result, Result: result, Status: "skipped", Reason: reason, Policy: policy,
	}
	if l.Recorder != nil {
		if err := l.Recorder.ToolSkipped(ctx, finish); err != nil {
			return domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
		return nil
	}
	return l.appendEvent(ctx, runID, "tool_call_skipped", map[string]any{
		"recordId": finish.ID, "iteration": iteration, "callIndex": callIndex,
		"toolCallId": call.ID, "toolName": call.Name, "reason": reason,
		"argumentsFragment": call.ArgumentsFragment,
	})
}
