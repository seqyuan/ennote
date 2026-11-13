// Package sqlmigrations is the single applier for the Worker's embedded SQLite
// schema histories.
//
// Every history lives in its own database and names itself with a prefix, so a
// version recorded by one history can never satisfy another, and each applied
// version keeps the checksum of the SQL that produced it, so editing a migration
// that already ran fails closed on the next start instead of leaving production
// and tests on different schemas.
package sqlmigrations

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// historyTable records which version of which history a database carries.
const historyTable = "migration_history"

// Migration is one embedded schema step.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

// Set is one embedded migration history applied to one database.
type Set struct {
	// Prefix identifies this history in migration_history. Two sets may share a
	// database file only if their prefixes differ; the applier refuses to run a
	// history against a database that already holds a different one.
	Prefix string
	// FS holds the `<version>_<name>.sql` files of this history.
	FS fs.FS
	// LegacyVersionTable names a version table written by an applier that kept no
	// prefix and no checksum. When this database has no migration_history row for
	// Prefix but does carry that table, its recorded versions are adopted as
	// applied instead of being replayed.
	LegacyVersionTable string
	// InitialSchemaTables are the tables version 1 creates. A database with no
	// version table that already carries every one of them was created by an
	// applier that ran InitialSchema without recording it; version 1 is adopted
	// for it. A database carrying only some of them is left alone so the
	// migration fails loudly instead of adopting a half-created schema.
	InitialSchemaTables []string
}

// Sorted returns the embedded migrations ordered by version. Invalid file names
// and duplicate versions are hard errors: silently skipping a migration file
// would leave production databases on a different schema than tests.
func Sorted(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
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
			return nil, fmt.Errorf("invalid migration file name %q", entry.Name())
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if previous, ok := seen[version]; ok {
			return nil, fmt.Errorf("duplicate migration version %d: %q and %q", version, previous, entry.Name())
		}
		contents, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		seen[version] = entry.Name()
		migrations = append(migrations, Migration{
			Version: version, Name: parts[1], SQL: string(contents), Checksum: checksum(contents),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

func checksum(contents []byte) string {
	sum := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Apply brings one database to the current schema of set. Migrations run in
// version order, each in its own transaction, and the applied set must be a
// prefix of the embedded history: a lower-numbered migration discovered after a
// higher one has been recorded is an out-of-order history and fails instead of
// being silently skipped.
func Apply(db *sql.DB, set Set) error {
	if set.Prefix == "" {
		return errors.New("migration set has no prefix")
	}
	migrations, err := Sorted(set.FS)
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return fmt.Errorf("%s migration set is empty", set.Prefix)
	}
	known := make(map[int]Migration, len(migrations))
	for _, migration := range migrations {
		known[migration.Version] = migration
	}
	if err := ensureHistoryTable(db); err != nil {
		return err
	}
	if err := rejectForeignHistory(db, set.Prefix); err != nil {
		return err
	}
	applied, err := loadApplied(db, set.Prefix)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		if err := adoptLegacy(db, set, known); err != nil {
			return err
		}
		if applied, err = loadApplied(db, set.Prefix); err != nil {
			return err
		}
	}

	maxApplied := 0
	for version := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf("%s database records migration %d, which is newer than this build carries: this binary is older than the database, and a downgrade requires restoring the database from a backup", set.Prefix, version)
		}
		if applied[version] != known[version].Checksum {
			return fmt.Errorf("%s migration %d changed after it was applied: recorded %s, embedded %s",
				set.Prefix, version, applied[version], known[version].Checksum)
		}
		if version > maxApplied {
			maxApplied = version
		}
	}

	for _, migration := range migrations {
		if _, ok := applied[migration.Version]; ok {
			continue
		}
		if migration.Version <= maxApplied {
			return fmt.Errorf("%s migration %d is out of order: version %d is already applied",
				set.Prefix, migration.Version, maxApplied)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin %s migration %d: %w", set.Prefix, migration.Version, err)
		}
		if _, err := tx.Exec(migration.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s migration %d: %w", set.Prefix, migration.Version, err)
		}
		if err := record(tx, set.Prefix, migration); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s migration %d: %w", set.Prefix, migration.Version, err)
		}
		applied[migration.Version] = migration.Checksum
		maxApplied = migration.Version
	}
	return nil
}

func ensureHistoryTable(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + historyTable + ` (
	prefix TEXT NOT NULL,
	version INTEGER NOT NULL,
	checksum TEXT NOT NULL,
	applied_at TEXT NOT NULL,
	PRIMARY KEY (prefix, version)
)`); err != nil {
		return fmt.Errorf("create %s: %w", historyTable, err)
	}
	return nil
}

// rejectForeignHistory keeps a history from adopting or rewriting another
// history's rows when two stores share one database file.
func rejectForeignHistory(db *sql.DB, prefix string) error {
	rows, err := db.Query(`SELECT DISTINCT prefix FROM ` + historyTable)
	if err != nil {
		return fmt.Errorf("read %s prefixes: %w", historyTable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var found string
		if err := rows.Scan(&found); err != nil {
			return fmt.Errorf("scan %s prefix: %w", historyTable, err)
		}
		if found != prefix {
			return fmt.Errorf("database holds the %s migration history, refusing to apply the %s history", found, prefix)
		}
	}
	return rows.Err()
}

func loadApplied(db *sql.DB, prefix string) (map[int]string, error) {
	rows, err := db.Query(`SELECT version, checksum FROM `+historyTable+` WHERE prefix=?`, prefix)
	if err != nil {
		return nil, fmt.Errorf("read applied %s migrations: %w", prefix, err)
	}
	defer rows.Close()
	applied := make(map[int]string)
	for rows.Next() {
		var version int
		var recorded string
		if err := rows.Scan(&version, &recorded); err != nil {
			return nil, fmt.Errorf("scan applied %s migration: %w", prefix, err)
		}
		applied[version] = recorded
	}
	return applied, rows.Err()
}

// adoptLegacy records the versions a pre-versioning applier already applied, so
// an existing database upgrades instead of replaying its InitialSchema. The
// checksum of each adopted version is the embedded one: from here on the rows
// are verified like any other.
func adoptLegacy(db *sql.DB, set Set, known map[int]Migration) error {
	versions, err := legacyVersions(db, set)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin %s migration adoption: %w", set.Prefix, err)
	}
	defer tx.Rollback()
	for _, version := range versions {
		migration, ok := known[version]
		if !ok {
			return fmt.Errorf("%s database records migration %d, which is newer than this build carries: this binary is older than the database, and a downgrade requires restoring the database from a backup", set.Prefix, version)
		}
		if err := record(tx, set.Prefix, migration); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// legacyVersions reports the versions a pre-versioning applier left behind, or
// nothing when this database was created by the current applier.
func legacyVersions(db *sql.DB, set Set) ([]int, error) {
	if set.LegacyVersionTable != "" {
		present, err := tableExists(db, set.LegacyVersionTable)
		if err != nil || !present {
			return nil, err
		}
		rows, err := db.Query(`SELECT version FROM ` + set.LegacyVersionTable + ` ORDER BY version`)
		if err != nil {
			return nil, fmt.Errorf("read legacy %s versions: %w", set.Prefix, err)
		}
		defer rows.Close()
		var versions []int
		for rows.Next() {
			var version int
			if err := rows.Scan(&version); err != nil {
				return nil, fmt.Errorf("scan legacy %s version: %w", set.Prefix, err)
			}
			versions = append(versions, version)
		}
		return versions, rows.Err()
	}
	if len(set.InitialSchemaTables) == 0 {
		return nil, nil
	}
	for _, table := range set.InitialSchemaTables {
		present, err := tableExists(db, table)
		if err != nil {
			return nil, err
		}
		if !present {
			// Either a fresh database or a half-created one: neither is adopted.
			return nil, nil
		}
	}
	return []int{1}, nil
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count); err != nil {
		return false, fmt.Errorf("probe table %s: %w", name, err)
	}
	return count > 0, nil
}

func record(tx *sql.Tx, prefix string, migration Migration) error {
	if _, err := tx.Exec(`INSERT INTO `+historyTable+`(prefix,version,checksum,applied_at) VALUES(?,?,?,?)`,
		prefix, migration.Version, migration.Checksum, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record %s migration %d: %w", prefix, migration.Version, err)
	}
	return nil
}
