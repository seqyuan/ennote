package sessionstore

import (
	"context"
	"database/sql"
	"fmt"
)

// scanTail removes torn-tail rows left by a crash in a run that has already
// reached a terminal state. A terminal run can no longer have in-flight rows,
// so any status='started' model/tool call is a logically torn row (its begin
// was committed but its completion never was) and is deleted. Active runs are
// never touched.
//
// run_events and run_messages are not cleaned here: they are append-only facts
// whose integrity is already guaranteed by SQLite transactions (and, for
// run_messages, a BEFORE INSERT trigger that rejects invalid payloads). A torn
// tail in those tables is a seq/ordinal gap, not a half-written fact, and gaps
// are a normal consequence of retries, not corruption.
//
// The cleanup is idempotent: a second pass deletes nothing. It runs on every
// session open, before any business read, so a crash never leaves a session
// whose replay sees a started call that can never complete.
func scanTail(ctx context.Context, db *sql.DB) error {
	const terminalRuns = `SELECT id FROM agent_runs WHERE status IN ('succeeded','failed','cancelled','interrupted')`

	stmts := []string{
		`DELETE FROM model_calls WHERE run_id IN (` + terminalRuns + `) AND status = 'started'`,
		`DELETE FROM tool_calls WHERE run_id IN (` + terminalRuns + `) AND status = 'started'`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("scan session tail: %w", err)
		}
	}
	return nil
}
