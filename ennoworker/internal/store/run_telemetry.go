package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// buildRunTelemetryTx aggregates run statistics within the terminal-transition
// transaction. Best-effort: an error returns a partial payload rather than
// failing the terminal event.
func (r *RunRepo) buildRunTelemetryTx(ctx context.Context, tx *sql.Tx, runID, timestamp string) (domain.RunTelemetryPayload, error) {
	telemetry := domain.RunTelemetryPayload{
		ToolTimings: make(map[string]domain.ToolTiming),
	}

	// Iterations = max completed iteration in tool_calls / model_calls.
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(iteration), 0) FROM model_calls WHERE run_id = ?`, runID).Scan(&telemetry.Iterations)

	// Model call + usage aggregation.
	if err := tx.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COALESCE(SUM(uncached_input_tokens + cache_read_tokens + cache_write_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0)
		FROM model_calls WHERE run_id = ?`, runID).Scan(
		&telemetry.ModelCalls, &telemetry.InputTokens, &telemetry.OutputTokens, &telemetry.CachedTokens); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return telemetry, err
	}

	// Per-tool aggregation: count, error count, total duration.
	rows, err := tx.QueryContext(ctx, `SELECT tool_name,
		COUNT(*),
		COALESCE(SUM(CASE WHEN is_error THEN 1 ELSE 0 END), 0),
		COALESCE(SUM((julianday(finished_at) - julianday(started_at)) * 86400000), 0)
		FROM tool_calls WHERE run_id = ? GROUP BY tool_name`, runID)
	if err != nil {
		return telemetry, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var timing domain.ToolTiming
		// julianday arithmetic is REAL, so the duration aggregate arrives as a
		// fractional millisecond count and cannot be scanned into the int64 domain
		// field. Round, never truncate: CAST would lose the sub-millisecond part.
		var totalMS float64
		if err := rows.Scan(&name, &timing.Count, &timing.ErrorCount, &totalMS); err != nil {
			return telemetry, err
		}
		timing.TotalMS = int64(math.Round(totalMS))
		telemetry.ToolTimings[name] = timing
	}
	if err := rows.Err(); err != nil {
		return telemetry, err
	}

	// Max context utilization: parse the frozen effective config's context
	// window and use peak billed input / window. Best-effort; 0 when unknown.
	var windowText string
	if err := tx.QueryRowContext(ctx, `SELECT effective_config_json FROM agent_runs WHERE id = ?`, runID).Scan(&windowText); err == nil && windowText != "" {
		var effective domain.EffectiveRunConfig
		if jsonErr := json.Unmarshal([]byte(windowText), &effective); jsonErr == nil {
			window := effective.ContextTokens
			if window > 0 {
				var peak int
				_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(uncached_input_tokens + cache_read_tokens + cache_write_tokens), 0) FROM model_calls WHERE run_id = ?`, runID).Scan(&peak)
				telemetry.MaxContextUtilization = float64(peak) / float64(window)
			}
		}
	}

	return telemetry, nil
}
