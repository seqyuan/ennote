package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runMigration25 applies migration 25 and its Go backfill.
func runMigration25(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, migration := range migrations.Sorted() {
		if migration.Version != 25 {
			continue
		}
		_, err := db.Exec(migration.SQL)
		require.NoError(t, err)
	}
	tx, err := db.Begin()
	require.NoError(t, err)
	require.NoError(t, store.BackfillDelegationHandles(context.Background(), tx))
	require.NoError(t, tx.Commit())
}

func TestMigration25BackgroundBackfillCreatesHandlesAndCompletions(t *testing.T) {
	db := seedMigration22DelegationDB(t)
	runMigration24(t, db)
	runMigration25(t, db)

	// Every group gets exactly one blocking handle with mapped status.
	handles := map[string]struct{ mode, status string }{}
	rows, err := db.Query(`SELECT group_id,execution_mode,status FROM delegation_handles`)
	require.NoError(t, err)
	for rows.Next() {
		var groupID, mode, status string
		require.NoError(t, rows.Scan(&groupID, &mode, &status))
		handles[groupID] = struct{ mode, status string }{mode, status}
	}
	require.NoError(t, rows.Close())
	require.Len(t, handles, 3)
	assert.Equal(t, "blocking", handles["g-settled"].mode)
	assert.Equal(t, "completed", handles["g-settled"].status)
	assert.Equal(t, "blocking", handles["g-waiting"].mode)
	assert.Equal(t, "active", handles["g-waiting"].status)
	assert.Equal(t, "blocking", handles["g-cancelled"].mode)
	assert.Equal(t, "cancelled", handles["g-cancelled"].status)

	// Terminal generation 0 gets one completion consumed by the parent.
	var completions int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_completions`).Scan(&completions))
	assert.Equal(t, 2, completions)
	var deliveryStatus, kind string
	require.NoError(t, db.QueryRow(`SELECT delivery_status,kind FROM delegation_completions
		JOIN delegation_handles h ON h.id=delegation_completions.handle_id
		WHERE h.group_id='g-settled'`).Scan(&deliveryStatus, &kind))
	assert.Equal(t, "consumed_by_parent", deliveryStatus)
	assert.Equal(t, "completed", kind)
	require.NoError(t, db.QueryRow(`SELECT delivery_status,kind FROM delegation_completions
		JOIN delegation_handles h ON h.id=delegation_completions.handle_id
		WHERE h.group_id='g-cancelled'`).Scan(&deliveryStatus, &kind))
	assert.Equal(t, "consumed_by_parent", deliveryStatus)
	assert.Equal(t, "cancelled", kind)

	// The waiting group has no completion yet.
	var waitingCompletions int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_completions c
		JOIN delegation_handles h ON h.id=c.handle_id WHERE h.group_id='g-waiting'`).Scan(&waitingCompletions))
	assert.Zero(t, waitingCompletions)

	// Sequences are unique per session.
	var distinct int
	require.NoError(t, db.QueryRow(`SELECT COUNT(DISTINCT sequence) FROM delegation_completions`).Scan(&distinct))
	assert.Equal(t, 2, distinct)

	// Idempotency: rerunning the backfill creates nothing new.
	tx, err := db.Begin()
	require.NoError(t, err)
	require.NoError(t, store.BackfillDelegationHandles(context.Background(), tx))
	require.NoError(t, tx.Commit())
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_handles`).Scan(&distinct))
	assert.Equal(t, 3, distinct)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_completions`).Scan(&distinct))
	assert.Equal(t, 2, distinct)
}
