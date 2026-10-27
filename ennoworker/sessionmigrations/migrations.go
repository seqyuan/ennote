package sessionmigrations

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed *.sql
var FS embed.FS

type Migration struct {
	Version int
	SQL     string
}

// Sorted returns the embedded migrations ordered by version. Invalid file names
// and duplicate versions are hard errors: silently skipping a migration file
// would leave production databases on a different schema than tests.
func Sorted() ([]Migration, error) {
	return sorted(FS)
}

func sorted(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read session migrations: %w", err)
	}
	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".sql")
		parts := strings.SplitN(name, "_", 2)
		if len(parts) != 2 || parts[1] == "" {
			return nil, fmt.Errorf("invalid session migration file name %q", entry.Name())
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid session migration version in %q", entry.Name())
		}
		if previous, ok := seen[version]; ok {
			return nil, fmt.Errorf("duplicate session migration version %d: %q and %q", version, previous, entry.Name())
		}
		contents, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read session migration %q: %w", entry.Name(), err)
		}
		seen[version] = entry.Name()
		migrations = append(migrations, Migration{Version: version, SQL: string(contents)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// Apply brings one Session database to the current schema. It is the single
// applier for production and test fixtures: migrations run in version order,
// each in its own transaction, and the applied set must be a prefix of the
// embedded history. A lower-numbered migration discovered after a higher one
// has been recorded is an out-of-order history and fails instead of being
// silently skipped by a MAX(version) check.
func Apply(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	migrations, err := Sorted()
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read applied session migrations: %w", err)
	}
	applied := make(map[int]bool)
	maxApplied := 0
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("scan applied session migration: %w", err)
		}
		applied[version] = true
		if version > maxApplied {
			maxApplied = version
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close applied session migrations: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read applied session migrations: %w", err)
	}

	for _, migration := range migrations {
		if applied[migration.Version] {
			continue
		}
		if migration.Version <= maxApplied {
			return fmt.Errorf("session migration %d is out of order: version %d is already applied", migration.Version, maxApplied)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin session migration %d: %w", migration.Version, err)
		}
		if _, err := tx.Exec(migration.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("session migration %d: %w", migration.Version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`,
			migration.Version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record session migration %d: %w", migration.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit session migration %d: %w", migration.Version, err)
		}
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
