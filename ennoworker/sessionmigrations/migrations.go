package sessionmigrations

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/sqlmigrations"
)

//go:embed *.sql
var FS embed.FS

// Migration is one embedded Session schema step.
type Migration = sqlmigrations.Migration

// Sorted returns the embedded Session migrations ordered by version.
func Sorted() ([]Migration, error) {
	return sqlmigrations.Sorted(FS)
}

// Set is the Session database history. Sessions written before versioning
// existed recorded what they had applied in schema_migrations without a prefix
// or a checksum; those rows are adopted on the first start of this build, and
// the old table is left in place so a rollback to the previous binary still
// sees its own bookkeeping.
func Set() sqlmigrations.Set {
	return sqlmigrations.Set{Prefix: "session", FS: FS, LegacyVersionTable: "schema_migrations"}
}

// Apply brings one Session database to the current schema, then runs the data
// backfills that the schema steps do not cover.
func Apply(db *sql.DB) error {
	if err := sqlmigrations.Apply(db, Set()); err != nil {
		return err
	}
	return BackfillMessageSeq(db)
}

// BackfillMessageSeq assigns session-monotonic seq values to messages created
// before the seq column existed (seq = 0). It runs once per session database
// (guarded by the seq=0 probe) and orders by created_at + id, which is
// monotonic along every lineage (a parent is always inserted before its
// child), so the client's consecutive-assertion over seq holds. New inserts
// continue from MAX(seq).
func BackfillMessageSeq(db *sql.DB) error {
	var unsequenced int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE seq = 0`).Scan(&unsequenced); err != nil {
		return fmt.Errorf("probe unsequenced messages: %w", err)
	}
	if unsequenced == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM messages ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("enumerate messages for seq backfill: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan message id for backfill: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close backfill rows: %w", err)
	}
	for index, id := range ids {
		if _, err := tx.Exec(`UPDATE messages SET seq = ? WHERE id = ?`, index+1, id); err != nil {
			return fmt.Errorf("backfill message seq: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seq backfill: %w", err)
	}
	return nil
}
