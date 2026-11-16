package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf8"

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
			exit_code = ?, finished_at = ? WHERE id = ? AND run_id = ? AND status = 'started'`,
			call.Result.Content, raw.Content, call.Result.Content, string(rawArtifacts), string(projectedArtifacts),
			boolInt(call.Result.IsError), boolInt(call.Policy.StopAfterBatch), call.Policy.Code,
			exitStatusColumn(call), now, call.ID, call.RunID)
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
		"content": call.Result.Content, "isError": call.Result.IsError, "attemptCount": call.AttemptCount,
	})
	return r.transact(ctx, call.RunID, domain.PendingEvent{EventType: "tool_call_failed", Payload: payload}, func(tx *sql.Tx, now string) error {
		// result_preview is the RAW text here, not a projection: the failed paths either
		// never ran policy post-projection or the projection itself threw. A future reader
		// must not assume this column is redacted on a failed row the way it is on a
		// completed one.
		result, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status='failed',raw_result_preview=?,
			raw_artifact_refs_json=?,policy_code=?,exit_code=?,is_error=?,result_preview=?,
			finished_at=? WHERE id=? AND run_id=? AND status='started'`,
			call.RawResult.Content, string(rawArtifacts), call.Policy.Code, exitStatusColumn(call),
			boolInt(call.Result.IsError), call.Result.Content, now, call.ID, call.RunID)
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

// exitStatusColumn is the tool_calls.exit_code value for one finish: the status
// the shell tool captured, or NULL when the call never ran a process to
// completion. NULL is deliberate - it is what every row written before the
// column had a writer holds, and the reader resolves it to
// domain.ExitCodeUnrecorded. A fabricated 0 here would read as a success.
//
// The RAW result is preferred, because the projected result is the one a
// redaction step may rebuild; the exit status is Worker-level bookkeeping and
// neither step is allowed to lose it.
func exitStatusColumn(call domain.ToolCallFinish) any {
	status := call.Result.ExitStatus
	if call.RawResult.ExitStatus != nil {
		status = call.RawResult.ExitStatus
	}
	if status == nil {
		return nil
	}
	return *status
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

// MaxToolExecutionOutputBytes bounds how much of one projected tool output the
// execution reader pulls into memory, per row. It is a read budget, not a
// semantic limit: a longer output is read up to the budget and reported as
// truncated, so a missing test summary in it is undecidable instead of absent.
//
// The budget is split between the two ends of the preview, because the fact a
// reader decides on is at the end: a test runner prints its summary last. Half of
// it is more than any summary block needs, and the half before it is what a reader
// skims.
const MaxToolExecutionOutputBytes = 256 << 10

// toolExecutionHeadBytes and toolExecutionTailBytes split that one budget: they
// are the first and the last bytes of the recorded preview, never more than
// MaxToolExecutionOutputBytes together.
func toolExecutionHeadBytes() int { return MaxToolExecutionOutputBytes / 2 }

func toolExecutionTailBytes() int { return MaxToolExecutionOutputBytes - toolExecutionHeadBytes() }

// MaxToolExecutionRows bounds how many recorded executions one reader returns.
// The tests-passed leaf decides on the highest matching seq, so the LAST rows of
// a Run are the ones that can still matter: a Run with thousands of shell calls
// must not load all of them (with every preview) into one fold.
const MaxToolExecutionRows = 200

// ListToolExecutions returns one Run's recorded shell executions, oldest first:
// the completed bash and exec calls, their recorded invocation and their
// projected output. It is the execution evidence of the tests-passed acceptance
// leaf, so it reads the PROJECTED result columns only — raw_result_preview and
// raw_result_path hold the pre-redaction text and must never be re-introduced
// into a deterministic check.
//
// The read is bounded twice: the last MaxToolExecutionRows rows (by seq, the
// window the leaf can still decide on) and MaxToolExecutionOutputBytes of
// preview per row, capped in SQL so the driver never materialises a full
// preview. A capped preview is reported as truncated.
//
// projected_result_path is read here but is never written today (the projection
// step populates projected_result_preview only), so the branch below is dead in
// practice. Whatever writer lands must confine the path to the session runtime
// directory before this reader trusts it: an absolute path out of a recorded row
// would otherwise let a run point the leaf at any file on the host.
func (r *CallRepo) ListToolExecutions(ctx context.Context, runID string) ([]domain.ToolExecution, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT seq, tool_call_id, tool_name, arguments_json, status, is_error,
		COALESCE(exit_code, -1),
		substr(CAST(projected_result_preview AS BLOB), 1, ?),
		CASE WHEN length(CAST(projected_result_preview AS BLOB)) > ?
			THEN substr(CAST(projected_result_preview AS BLOB), -?)
			ELSE NULL END,
		COALESCE(length(CAST(projected_result_preview AS BLOB)) > ?, 0),
		projected_result_path
		FROM tool_calls WHERE run_id = ? AND tool_name IN ('bash','exec') AND status = 'completed'
		ORDER BY seq DESC LIMIT ?`,
		toolExecutionHeadBytes(), MaxToolExecutionOutputBytes, toolExecutionTailBytes(),
		MaxToolExecutionOutputBytes, runID, MaxToolExecutionRows)
	if err != nil {
		return nil, fmt.Errorf("list tool executions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var executions []domain.ToolExecution
	for rows.Next() {
		var (
			execution   domain.ToolExecution
			arguments   string
			status      string
			isError     int
			preview     []byte
			tail        []byte
			wasCapped   int
			projectedTo sql.NullString
		)
		if err := rows.Scan(&execution.Seq, &execution.ToolCallID, &execution.Tool, &arguments, &status,
			&isError, &execution.ExitCode, &preview, &tail, &wasCapped, &projectedTo); err != nil {
			return nil, fmt.Errorf("scan tool execution: %w", err)
		}
		if status != "completed" {
			continue
		}
		execution.IsError = isError != 0
		execution.Command, execution.Argv = recordedInvocation(execution.Tool, arguments)
		// The SQL caps are byte-based, so they can cut a multi-byte sequence in
		// half; drop the partial rune at either cut rather than hand invalid UTF-8
		// to the leaf.
		execution.Output = string(trimPartialUTF8Tail(preview))
		if wasCapped != 0 && len(tail) > 0 {
			// Both ends of the preview are retained and its middle is dropped. The
			// seam carries the same marker vocabulary the shell tools use
			// (domain.Output*TruncationMarker), so a reader that decides on the end
			// of an output reads one contract however deeply it was cut. Without it
			// the head and the tail would be glued together and read as one
			// continuous output that never happened.
			execution.Output = execution.Output + "\n" + domain.OutputHeadTruncationMarker + "\n" +
				domain.OutputTailTruncationMarker + "\n" + string(trimPartialUTF8Head(tail))
		}
		execution.Truncated = wasCapped != 0
		if projectedTo.Valid && projectedTo.String != "" {
			// The projection spilled to a file: that file is the whole output, so
			// it replaces the preview. A read that failed is truncated, never
			// "no output": an unreadable file must not read as a passing summary
			// that was not there.
			output, truncated := readProjectedOutput(projectedTo.String, MaxToolExecutionOutputBytes)
			execution.Truncated = truncated
			if !truncated || output != "" {
				execution.Output = output
			}
		}
		executions = append(executions, execution)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tool executions: %w", err)
	}
	// The query reads the newest rows first; the seam promises oldest first.
	for i, j := 0, len(executions)-1; i < j; i, j = i+1, j-1 {
		executions[i], executions[j] = executions[j], executions[i]
	}
	return executions, nil
}

// trimPartialUTF8Tail drops up to three trailing bytes of an incomplete UTF-8
// sequence, so a byte-capped preview does not end in the middle of a rune.
func trimPartialUTF8Tail(data []byte) []byte {
	for n := 0; n < 3 && len(data) > 0; n++ {
		if r, size := utf8.DecodeLastRune(data); r != utf8.RuneError || size > 1 {
			break
		}
		data = data[:len(data)-1]
	}
	return data
}

// trimPartialUTF8Head drops up to three leading bytes of an incomplete UTF-8
// sequence, so a byte-capped tail does not start in the middle of a rune.
func trimPartialUTF8Head(data []byte) []byte {
	for n := 0; n < 3 && len(data) > 0; n++ {
		if r, size := utf8.DecodeRune(data); r != utf8.RuneError || size > 1 {
			break
		}
		data = data[1:]
	}
	return data
}

// recordedInvocation decodes the invocation of one recorded shell call: bash
// records a command string, exec records an argument vector. A row whose
// arguments do not decode keeps both empty, so it matches nothing.
func recordedInvocation(tool, arguments string) (string, []string) {
	switch tool {
	case "bash":
		var recorded struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(arguments), &recorded) == nil {
			return recorded.Command, nil
		}
	case "exec":
		var recorded struct {
			Argv []string `json:"argv"`
		}
		if json.Unmarshal([]byte(arguments), &recorded) == nil {
			return "", recorded.Argv
		}
	}
	return "", nil
}

// readProjectedOutput reads at most limit bytes of a projected output file.
// truncated reports that the file was longer than the budget or could not be
// read to the end: in both cases the caller must not conclude that the output
// lacked a summary.
func readProjectedOutput(path string, limit int64) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", true
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return string(data), true
	}
	if int64(len(data)) > limit {
		return string(data[:limit]), true
	}
	return string(data), false
}
