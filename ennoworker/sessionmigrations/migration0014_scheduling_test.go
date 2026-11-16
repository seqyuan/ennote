package sessionmigrations

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration 0014 adds the two durable scheduling facts of a Graph task node:
// the retry attempts already spent, and the frozen `optional` declaration.
//
// Pinned here because the Go layer scans both columns unconditionally (every
// node read, not only the scheduling paths), and because `optional` decides
// whether a failed node is a satisfied dependency: a database that skipped this
// step would fail every flow node read, and one that defaulted to anything but 0
// would silently make every old task optional.
func TestMigrationFourteenAddsFlowNodeSchedulingColumns(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, Apply(db))

	for _, column := range []string{"attempts", "optional"} {
		var notNull int
		var defaultValue sql.NullString
		require.NoError(t, db.QueryRow(
			`SELECT "notnull", dflt_value FROM pragma_table_info('run_agent_flow_nodes') WHERE name=?`,
			column).Scan(&notNull, &defaultValue), column)
		assert.Equal(t, 1, notNull, "%s must be NOT NULL so the Go layer scans a plain integer", column)
		require.True(t, defaultValue.Valid, "%s needs a default for pre-existing rows", column)
		assert.Equal(t, "0", defaultValue.String, column)
	}

	// A row written before this migration reads as "no attempts spent, not
	// optional": an old checkpoint is never retroactively optional.
	_, err := db.Exec(`INSERT INTO run_agent_flow_nodes
		(run_id, task_index, handle, terminal_state, created_at)
		VALUES ('run-old', 0, 'producer', 'pending', '2026-11-16T00:00:00Z')`)
	require.NoError(t, err)
	var attempts, optional int
	require.NoError(t, db.QueryRow(`SELECT attempts, optional FROM run_agent_flow_nodes
		WHERE run_id='run-old'`).Scan(&attempts, &optional))
	assert.Equal(t, 0, attempts)
	assert.Equal(t, 0, optional)

	// The exact retry reset the store runs must be accepted: it spends an attempt
	// in the same statement that clears the failed attempt's leftovers.
	_, err = db.Exec(`UPDATE run_agent_flow_nodes SET terminal_state='pending',
		output_ref=NULL, error_code=NULL, acceptance_verdict='', attempts=attempts+1
		WHERE run_id='run-old' AND task_index=0`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT attempts FROM run_agent_flow_nodes
		WHERE run_id='run-old'`).Scan(&attempts))
	assert.Equal(t, 1, attempts)
}
