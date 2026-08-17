package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type CallRepo struct {
	DB        *sql.DB
	Publisher EventPublisher
}

func (r *CallRepo) ModelStarted(ctx context.Context, call domain.ModelCallStart) error {
	if call.Purpose == "" {
		call.Purpose = domain.ModelCallAgentTurn
	}
	eventType := "model_call_started"
	if call.Purpose == domain.ModelCallImageDescription {
		eventType = "vision_fallback_started"
	}
	payload, _ := json.Marshal(map[string]any{
		"callId": call.ID, "iteration": call.Iteration, "attempt": call.Attempt,
		"requestGeneration": call.RequestGeneration, "modelProfileId": call.ModelProfileID, "purpose": call.Purpose,
		"compactionId":     call.CompactionID,
		"sourceArtifactId": call.SourceArtifactID, "routeReason": call.RouteReason,
	})
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: eventType, Payload: payload}, func(tx *sql.Tx, now string) error {
		var seq int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM model_calls WHERE run_id = ?`, call.RunID).Scan(&seq); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO model_calls
			(id, run_id, seq, provider_profile_id, model_profile_id, requested_config_json,
			effective_config_json, started_at, iteration, attempt, status, purpose, route_reason,
			parent_iteration, source_artifact_id, request_generation, compaction_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'started', ?, ?, ?, ?, ?, ?)`, call.ID, call.RunID, seq,
			nullableStr(call.ProviderProfileID), nullableStr(call.ModelProfileID), validJSONOrEmpty(call.RequestedConfig),
			validJSONOrEmpty(call.EffectiveConfig), now, call.Iteration, call.Attempt, call.Purpose,
			call.RouteReason, call.ParentIteration, call.SourceArtifactID, call.RequestGeneration, nullableStr(call.CompactionID))
		return err
	})
}

func (r *CallRepo) ModelUsage(ctx context.Context, call domain.ModelCallFinish) error {
	payload, _ := json.Marshal(map[string]any{
		"callId": call.ID, "iteration": call.Iteration, "attempt": call.Attempt,
		"requestGeneration": call.RequestGeneration, "usage": call.Usage,
	})
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: "usage_updated", Payload: payload}, func(tx *sql.Tx, now string) error {
		if err := updateModelUsage(ctx, tx, call.ID, call.Usage); err != nil {
			return err
		}
		return upsertUsage(ctx, tx, call.RunID, call.ID, call.Usage, now)
	})
}

func (r *CallRepo) ModelCompleted(ctx context.Context, call domain.ModelCallFinish) error {
	eventType := "model_call_completed"
	if call.Purpose == domain.ModelCallImageDescription {
		eventType = "vision_fallback_completed"
	}
	payload, _ := json.Marshal(map[string]any{
		"callId": call.ID, "iteration": call.Iteration, "attempt": call.Attempt,
		"requestGeneration": call.RequestGeneration, "stopReason": call.StopReason,
		"actualModel": call.ActualModel, "usage": call.Usage,
	})
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: eventType, Payload: payload}, func(tx *sql.Tx, now string) error {
		result, err := tx.ExecContext(ctx, `UPDATE model_calls SET status = 'completed', actual_model = ?,
			stop_reason = ?, uncached_input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?,
			reasoning_tokens = ?, finished_at = ?, first_token_at = ? WHERE id = ? AND run_id = ? AND status = 'started'`,
			call.ActualModel, call.StopReason, call.Usage.UncachedInputTokens, call.Usage.OutputTokens,
			call.Usage.CacheReadTokens, call.Usage.CacheWriteTokens, call.Usage.ReasoningTokens,
			now, nullableTime(call.FirstTokenAt), call.ID, call.RunID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("model call is not started: %s", call.ID)
		}
		return upsertUsage(ctx, tx, call.RunID, call.ID, call.Usage, now)
	})
}

func (r *CallRepo) ModelFailed(ctx context.Context, call domain.ModelCallFinish) error {
	eventType := "model_call_attempt_failed"
	if call.Final {
		eventType = "model_call_failed"
	}
	if call.Purpose == domain.ModelCallImageDescription {
		eventType = "vision_fallback_attempt_failed"
		if call.Final {
			eventType = "vision_fallback_failed"
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"callId": call.ID, "iteration": call.Iteration, "attempt": call.Attempt,
		"requestGeneration": call.RequestGeneration, "category": call.ErrorCode,
		"error": call.Error, "retryable": call.Retryable,
	})
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: eventType, Payload: payload}, func(tx *sql.Tx, now string) error {
		result, err := tx.ExecContext(ctx, `UPDATE model_calls SET status = 'failed', error_code = ?,
			http_status = NULLIF(?, 0), finished_at = ? WHERE id = ? AND run_id = ? AND status = 'started'`,
			call.ErrorCode, call.HTTPStatus, now, call.ID, call.RunID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("model call is not started: %s", call.ID)
		}
		return nil
	})
}

func (r *CallRepo) ToolStarted(ctx context.Context, call domain.ToolCallStart) error {
	arguments := call.Call.Arguments
	if len(arguments) == 0 || !json.Valid(arguments) {
		arguments = json.RawMessage(`{}`)
	}
	original := call.OriginalArguments
	if len(original) == 0 || !json.Valid(original) {
		original = arguments
	}
	effective := call.EffectiveArguments
	if len(effective) == 0 || !json.Valid(effective) {
		effective = arguments
	}
	payload, _ := json.Marshal(map[string]any{
		"recordId": call.ID, "iteration": call.Iteration, "callIndex": call.CallIndex,
		"toolCallId": call.Call.ID, "toolName": call.Call.Name, "arguments": effective,
		"policyId": call.Policy.PolicyID, "policyCode": call.Policy.Code, "riskClass": call.Policy.RiskClass,
		"standingRuleId": call.Policy.StandingRuleID,
	})
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: "tool_call_started", Payload: payload}, func(tx *sql.Tx, now string) error {
		var seq int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM tool_calls WHERE run_id = ?`, call.RunID).Scan(&seq); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO tool_calls
			(id, run_id, seq, tool_call_id, tool_name, arguments_json, status, started_at,
			iteration, call_index, arguments_fragment, original_arguments_json, effective_arguments_json,
			policy_id, policy_version, policy_action, policy_code, risk_class, standing_rule_id)
			VALUES (?, ?, ?, ?, ?, ?, 'started', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, call.ID, call.RunID, seq,
			call.Call.ID, call.Call.Name, string(effective), now, call.Iteration, call.CallIndex,
			nullableStr(call.Call.ArgumentsFragment), string(original), string(effective), call.Policy.PolicyID,
			call.Policy.PolicyVersion, call.Policy.Action, call.Policy.Code, call.Policy.RiskClass,
			call.Policy.StandingRuleID)
		return err
	})
}

func (r *CallRepo) ToolCompleted(ctx context.Context, call domain.ToolCallFinish) error {
	raw := call.RawResult
	if raw.ToolCallID == "" {
		raw = call.Result
	}
	rawArtifacts, _ := json.Marshal(nonNilArtifactReferences(raw.Artifacts))
	projectedArtifacts, _ := json.Marshal(nonNilArtifactReferences(call.Result.Artifacts))
	payload, _ := json.Marshal(map[string]any{
		"recordId": call.ID, "iteration": call.Iteration, "callIndex": call.CallIndex,
		"toolCallId": call.Result.ToolCallID, "toolName": call.Result.ToolName,
		"content": call.Result.Content, "isError": call.Result.IsError,
		"artifacts": nonNilArtifactReferences(call.Result.Artifacts),
		"policyId":  call.Policy.PolicyID, "policyCode": call.Policy.Code, "riskClass": call.Policy.RiskClass,
		"attemptCount": call.AttemptCount,
	})
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: "tool_call_completed", Payload: payload}, func(tx *sql.Tx, now string) error {
		result, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'completed', result_preview = ?,
			raw_result_preview = ?, projected_result_preview = ?, raw_artifact_refs_json = ?,
			projected_artifact_refs_json = ?, is_error = ?, stop_after_batch = ?, policy_code = ?,
			finished_at = ? WHERE id = ? AND run_id = ? AND status = 'started'`,
			call.Result.Content, raw.Content, call.Result.Content, string(rawArtifacts), string(projectedArtifacts),
			boolInt(call.Result.IsError), boolInt(call.Policy.StopAfterBatch), call.Policy.Code, now, call.ID, call.RunID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("tool call is not started: %s", call.ID)
		}
		return nil
	})
}

func (r *CallRepo) ToolFailed(ctx context.Context, call domain.ToolCallFinish) error {
	rawArtifacts, _ := json.Marshal(nonNilArtifactReferences(call.RawResult.Artifacts))
	payload, _ := json.Marshal(map[string]any{
		"recordId": call.ID, "iteration": call.Iteration, "callIndex": call.CallIndex,
		"toolCallId": call.Call.ID, "toolName": call.Call.Name, "category": call.Policy.Code,
	})
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: "tool_call_failed", Payload: payload}, func(tx *sql.Tx, now string) error {
		result, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status='failed',raw_result_preview=?,
			raw_artifact_refs_json=?,policy_code=?,finished_at=? WHERE id=? AND run_id=? AND status='started'`,
			call.RawResult.Content, string(rawArtifacts), call.Policy.Code, now, call.ID, call.RunID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return fmt.Errorf("tool call is not started: %s", call.ID)
		}
		return nil
	})
}

func (r *CallRepo) ModelRouteSelected(ctx context.Context, runID string, iteration int, runtime domain.ModelRuntimeSnapshot, reason string) error {
	payload, _ := json.Marshal(map[string]any{"iteration": iteration, "providerProfileId": runtime.ProviderProfileID,
		"modelProfileId": runtime.ModelProfileID, "apiModel": runtime.APIModel, "reason": reason})
	return r.transact(ctx, runID, domain.PendingEvent{EventType: "model_route_selected", Payload: payload},
		func(*sql.Tx, string) error { return nil })
}

func (r *CallRepo) ToolSkipped(ctx context.Context, call domain.ToolCallFinish) error {
	arguments := call.Call.Arguments
	if len(arguments) == 0 || !json.Valid(arguments) {
		arguments = json.RawMessage(`{}`)
	}
	payload, _ := json.Marshal(map[string]any{
		"recordId": call.ID, "iteration": call.Iteration, "callIndex": call.CallIndex,
		"toolCallId": call.Call.ID, "toolName": call.Call.Name, "reason": call.Reason,
		"argumentsFragment": call.Call.ArgumentsFragment, "policyId": call.Policy.PolicyID,
		"policyCode": call.Policy.Code, "policyAction": call.Policy.Action, "riskClass": call.Policy.RiskClass,
	})
	eventType := "tool_call_skipped"
	if call.Policy.Action == "deny" {
		eventType = "tool_policy_denied"
	} else if call.Policy.Action == "terminate_batch" {
		eventType = "tool_policy_terminated"
	}
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: eventType, Payload: payload}, func(tx *sql.Tx, now string) error {
		var seq int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM tool_calls WHERE run_id = ?`, call.RunID).Scan(&seq); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO tool_calls
			(id, run_id, seq, tool_call_id, tool_name, arguments_json, status, result_preview,
			is_error, started_at, finished_at, iteration, call_index, arguments_fragment,
			original_arguments_json,effective_arguments_json,policy_id,policy_version,policy_action,
			policy_code,risk_class,projected_result_preview,stop_after_batch)
			VALUES (?, ?, ?, ?, ?, ?, 'skipped', ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, call.ID, call.RunID, seq,
			call.Call.ID, call.Call.Name, string(arguments), call.Result.Content, now, now, call.Iteration,
			call.CallIndex, nullableStr(call.Call.ArgumentsFragment), string(arguments), string(arguments),
			call.Policy.PolicyID, call.Policy.PolicyVersion, call.Policy.Action, call.Policy.Code, call.Policy.RiskClass,
			call.Result.Content, boolInt(call.Policy.StopAfterBatch))
		return err
	})
}

func (r *CallRepo) transact(ctx context.Context, runID string, event domain.PendingEvent, mutate func(*sql.Tx, string) error) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := mutate(tx, now); err != nil {
		return err
	}
	committed, err := appendEventsTx(ctx, tx, runID, event)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return nil
}

func updateModelUsage(ctx context.Context, tx *sql.Tx, callID string, usage domain.Usage) error {
	result, err := tx.ExecContext(ctx, `UPDATE model_calls SET uncached_input_tokens = ?, output_tokens = ?,
		cache_read_tokens = ?, cache_write_tokens = ?, reasoning_tokens = ? WHERE id = ? AND status = 'started'`,
		usage.UncachedInputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens, callID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("model call is not started: %s", callID)
	}
	return nil
}

func upsertUsage(ctx context.Context, tx *sql.Tx, runID, callID string, usage domain.Usage, now string) error {
	details, _ := json.Marshal(usage)
	_, err := tx.ExecContext(ctx, `INSERT INTO usage_records (id, run_id, kind, ref_id, details_json, created_at)
		VALUES (?, ?, 'model_call', ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET details_json = excluded.details_json`,
		"usage-"+callID, runID, callID, string(details), now)
	return err
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func validJSONOrEmpty(value json.RawMessage) string {
	if len(value) == 0 || !json.Valid(value) {
		return "{}"
	}
	return string(value)
}

func nonNilArtifactReferences(value []domain.ArtifactReference) []domain.ArtifactReference {
	if value == nil {
		return []domain.ArtifactReference{}
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
