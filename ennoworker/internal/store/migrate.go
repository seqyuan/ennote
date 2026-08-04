package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/migrations"
)

// Migrate applies all outstanding schema migrations.
func Migrate(db *sql.DB) error {
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

	pending := make([]migrations.Migration, 0)
	rebuildsTables := false
	for _, migration := range migrations.Sorted() {
		if migration.Version <= current {
			continue
		}
		pending = append(pending, migration)
		if migration.Version == 7 || migration.Version == 21 {
			rebuildsTables = true
		}
	}
	if len(pending) == 0 {
		return nil
	}

	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("reserve migration connection: %w", err)
	}
	defer conn.Close()
	if rebuildsTables {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = OFF"); err != nil {
			return fmt.Errorf("disable foreign keys for table rebuild: %w", err)
		}
		defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	}

	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, migration := range pending {
		if _, err := tx.Exec(migration.SQL); err != nil {
			return fmt.Errorf("migration %d: %w", migration.Version, err)
		}
		if migration.Version == 18 {
			if err := backfillHostedPromptSnapshots(context.Background(), tx); err != nil {
				return fmt.Errorf("migration 18 prompt snapshots: %w", err)
			}
		}
		if _, err := tx.Exec(
			"INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)",
			migration.Version, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
	}
	if rebuildsTables {
		rows, err := tx.Query("PRAGMA foreign_key_check")
		if err != nil {
			return fmt.Errorf("check rebuilt foreign keys: %w", err)
		}
		if rows.Next() {
			var table string
			var rowID int64
			var parent string
			var foreignKey int
			_ = rows.Scan(&table, &rowID, &parent, &foreignKey)
			rows.Close()
			return fmt.Errorf("foreign key check failed: table=%s row=%d parent=%s fk=%d", table, rowID, parent, foreignKey)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if rebuildsTables {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
			return fmt.Errorf("re-enable foreign keys: %w", err)
		}
	}
	return nil
}

func backfillHostedPromptSnapshots(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT ar.id,s.default_agent_profile_id,COALESCE(ap.system_prompt,'')
		FROM agent_runs ar JOIN sessions s ON s.id=ar.session_id
		LEFT JOIN agent_profiles ap ON ap.id=s.default_agent_profile_id`)
	if err != nil {
		return err
	}
	type entry struct{ runID, agentID, prompt string }
	entries := make([]entry, 0)
	for rows.Next() {
		var item entry
		var agentID sql.NullString
		if err := rows.Scan(&item.runID, &agentID, &item.prompt); err != nil {
			rows.Close()
			return err
		}
		if agentID.Valid {
			item.agentID = agentID.String
		}
		entries = append(entries, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range entries {
		snapshot, err := newSystemPromptSnapshot(item.agentID, item.prompt)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET system_prompt_snapshot_json=?,system_prompt_digest=? WHERE id=?`,
			string(encoded), snapshot.Digest, item.runID); err != nil {
			return err
		}
	}
	return nil
}
