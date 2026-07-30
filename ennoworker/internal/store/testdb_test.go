package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// SetupDB creates a temporary SQLite database, applies migrations,
// and returns the *sql.DB. The caller is responsible for closing it.
func SetupDB(t testing.TB) *sql.DB {
	t.Helper()
	db, err := OpenMemory()
	if err != nil {
		t.Fatalf("open memory: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// SetupDBFile creates a file-backed test database.
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
