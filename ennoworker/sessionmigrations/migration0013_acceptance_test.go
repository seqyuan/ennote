package sessionmigrations

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration 0013 freezes a task's acceptance clause onto its checkpoint and
// stores the verdict next to it.
//
// Pinned here because the Go layer reads both columns unconditionally: a
// database that skipped this step fails every flow node read, not only the
// acceptance fold. Both columns are NOT NULL with an empty-string default, and
// that is the point — `ResumeFlowRun` resets a failed node by writing an
// explicit column list, and a NULL write into a NOT NULL column would break
// every resume at runtime instead of at migration time.
func TestMigrationThirteenAddsFlowNodeAcceptanceColumns(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Apply(db))

	for _, column := range []string{"acceptance_json", "acceptance_verdict"} {
		var notNull int
		var defaultValue sql.NullString
		require.NoError(t, db.QueryRow(
			`SELECT "notnull", dflt_value FROM pragma_table_info('run_agent_flow_nodes') WHERE name=?`,
			column).Scan(&notNull, &defaultValue), column)
		assert.Equal(t, 1, notNull, "%s must be NOT NULL so a reset can write '' instead of NULL", column)
		require.True(t, defaultValue.Valid, "%s needs a default for pre-existing rows", column)
		assert.Equal(t, "''", defaultValue.String, column)
	}

	// The exact reset statement ResumeFlowRun runs must be accepted: writing NULL
	// into the NOT NULL verdict column is the failure this migration avoids.
	_, err := db.Exec(`UPDATE run_agent_flow_nodes SET terminal_state='pending',
		error_code=NULL, output_ref=NULL, finished_at=NULL, acceptance_verdict=''
		WHERE run_id='missing-run' AND terminal_state IN ('cancelled','interrupted','failed','blocked')`)
	require.NoError(t, err)
}
