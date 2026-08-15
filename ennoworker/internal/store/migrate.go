package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/sessionmigrations"
)

// MigrateSession applies the Session schema migrations (V2). It is the single
// authority for the per-Session database shape and is used by the Session
// store manager and the store test fixtures. There is exactly one migration:
// the consolidated initial schema.
func MigrateSession(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	var current int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, migration := range sessionmigrations.Sorted() {
		if migration.Version <= current {
			continue
		}
		if _, err := tx.Exec(migration.SQL); err != nil {
			return fmt.Errorf("session migration %d: %w", migration.Version, err)
		}
		if _, err := tx.Exec(
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			migration.Version, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("record session migration %d: %w", migration.Version, err)
		}
	}
	return tx.Commit()
}
