package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// migratedTemplate is the once-built, fully-migrated SQLite file every
// SetupDB test copies from. Running all 41 migrations per test is the
// dominant cost of the store test suite (~90% of wall time): modernc sqlite's
// parser is a C->Go translation that is ~30x slower under -race, and the
// driver serializes concurrent connections on a global mutex, so hundreds of
// concurrent per-test migrations degrade pathologically (up to ~25x a single
// migration, worse under -race). A raw file copy is ~100x cheaper and immune
// to the global-mutex serialization.
var (
	migratedTemplateOnce  sync.Once
	migratedTemplatePath  string
	migratedTemplateError error
)

// ensureTemplate builds the migrated file template exactly once (lazily, on
// the first SetupDB). The WAL is folded into the main file
// (wal_checkpoint(TRUNCATE)) so the raw copy is complete: a template with
// pending WAL rows would silently lose them on copy.
func ensureTemplate() (string, error) {
	migratedTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "enstore-tpl-")
		if err != nil {
			migratedTemplateError = err
			return
		}
		migratedTemplatePath = filepath.Join(dir, "template.db")
		db, err := Open(migratedTemplatePath)
		if err != nil {
			migratedTemplateError = err
			return
		}
		if err := Migrate(db); err != nil {
			_ = db.Close()
			migratedTemplateError = err
			return
		}
		if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			_ = db.Close()
			migratedTemplateError = err
			return
		}
		_ = db.Close()
	})
	return migratedTemplatePath, migratedTemplateError
}

// copyFileBytes copies src into a fresh dst file. A template copy is pure
// filesystem IO (no sqlite involvement), so per-test cost stays ~1-3ms and is
// unaffected by the sqlite global mutex even when hundreds of tests copy in
// parallel.
func copyFileBytes(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// SetupDB creates a temporary SQLite database with the full migrated schema
// by copying the package-level migrated template (see ensureTemplate), and
// returns the *sql.DB. The caller is responsible for closing it. Migration
// semantics themselves are still covered by migrate_test.go / *_migration_test.go,
// which run the real Migrate against fresh OpenMemory databases.
func SetupDB(t testing.TB) *sql.DB {
	t.Helper()
	template, err := ensureTemplate()
	if err != nil {
		t.Fatalf("prepare migrated template: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := copyFileBytes(template, dbPath); err != nil {
		t.Fatalf("copy template: %v", err)
	}
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// SetupDBFile creates a file-backed test database by running the real
// migrations (used by migration-focused tests that must exercise the
// migration path itself, not the template copy).
func SetupDBFile(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	t.Cleanup(func() { os.Remove(dbPath + "-wal"); os.Remove(dbPath + "-shm") })
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}
