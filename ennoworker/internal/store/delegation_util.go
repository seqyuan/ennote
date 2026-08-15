package store

import (
	"context"
	"database/sql"
)

// nullableBackfillString maps a nullable value into an SQL argument.
func nullableBackfillString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

// nullableBackfillJSON maps an empty string to NULL.
func nullableBackfillJSON(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// readChildUsageTx reads a child Run's consumed budget ledger.
func readChildUsageTx(ctx context.Context, tx *sql.Tx, childRunID string) map[string]int64 {
	usage := map[string]int64{"modelCalls": 0, "toolCalls": 0, "tokens": 0, "outputTokens": 0, "costMicros": 0}
	var modelCalls, toolCalls int
	var tokens, outputTokens, costMicros int64
	if err := tx.QueryRowContext(ctx, `SELECT consumed_model_calls,consumed_tool_calls,consumed_tokens,
		consumed_output_tokens,consumed_cost_usd_micros FROM run_budgets WHERE run_id=?`, childRunID).
		Scan(&modelCalls, &toolCalls, &tokens, &outputTokens, &costMicros); err != nil {
		return usage
	}
	usage["modelCalls"] = int64(modelCalls)
	usage["toolCalls"] = int64(toolCalls)
	usage["tokens"] = tokens
	usage["outputTokens"] = outputTokens
	usage["costMicros"] = costMicros
	return usage
}

// nextDeliverySequenceTx allocates the next session-wide delivery sequence.
func nextDeliverySequenceTx(ctx context.Context, tx *sql.Tx, sessionID string) (int, error) {
	var next int
	if err := tx.QueryRowContext(ctx, `SELECT next_sequence FROM session_delivery_sequences
		WHERE session_id=?`, sessionID).Scan(&next); err != nil {
		if err != sql.ErrNoRows {
			return 0, err
		}
		next = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_delivery_sequences (session_id,next_sequence)
		VALUES(?,?) ON CONFLICT(session_id) DO UPDATE SET next_sequence=next_sequence+1`,
		sessionID, next+1); err != nil {
		return 0, err
	}
	return next, nil
}
