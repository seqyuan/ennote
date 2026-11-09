// Package sessionstoretest gives test binaries a Session database that is
// already migrated.
//
// Every Session lives in its own SQLite file, and opening a new one replays the
// whole migration chain. Under the race detector that costs seconds per
// database — modernc sqlite's C-to-Go translation is what the profiler shows —
// and a package that creates one Session per test pays it dozens of times. That
// is enough to blow the per-package test timeout on a suite that would otherwise
// finish in a minute.
//
// Use builds the migrated file once per process and points
// sessionstore.Manager at it, so every Session after the first is a byte copy.
// Migration behavior itself stays covered by the sessionmigrations tests, which
// run the real chain against fresh databases.
package sessionstoretest

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/sessionmigrations"
	_ "modernc.org/sqlite"
)

// Use installs a migrated Session database template for this test process and
// returns a cleanup that removes it.
func Use() (func(), error) {
	dir, err := os.MkdirTemp("", "ennote-session-template-")
	if err != nil {
		return nil, fmt.Errorf("create session template directory: %w", err)
	}
	cleanup := func() {
		sessionstore.SetMigratedTemplate("")
		_ = os.RemoveAll(dir)
	}
	path := filepath.Join(dir, "session.db")
	// store.Open is the production opener: one connection, foreign keys on. The
	// module guard test insists every database handle goes through it, and a
	// template built any other way would not match what Sessions are opened with.
	db, err := store.Open(path)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open session template: %w", err)
	}
	if err := sessionmigrations.Apply(db); err != nil {
		_ = db.Close()
		cleanup()
		return nil, fmt.Errorf("migrate session template: %w", err)
	}
	// Fold the WAL into the main file: the Manager copies bytes, so a template
	// with pending WAL frames would hand every Session an incomplete database.
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = db.Close()
		cleanup()
		return nil, fmt.Errorf("checkpoint session template: %w", err)
	}
	if err := db.Close(); err != nil {
		cleanup()
		return nil, fmt.Errorf("close session template: %w", err)
	}
	sessionstore.SetMigratedTemplate(path)
	return cleanup, nil
}

// OpenFixture returns a private copy of the migrated template, opened the way
// production opens a Session database. Tests that need a migrated database of
// their own get one for a file copy instead of a migration replay.
func OpenFixture(t *testing.T) *sql.DB {
	t.Helper()
	template := sessionstore.MigratedTemplatePath()
	if template == "" {
		t.Fatal("session database template is not installed; call sessionstoretest.Use from TestMain")
	}
	data, err := os.ReadFile(template)
	if err != nil {
		t.Fatalf("read session database template: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fixture.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("copy session database template: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
