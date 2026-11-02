package projection

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/seqyuan/ennote/ennoworker/catalogmigrations"
	"github.com/seqyuan/ennote/ennoworker/usagemigrations"
	_ "modernc.org/sqlite"
)

type Stores struct {
	Catalog *sql.DB
	Usage   *sql.DB
}

func Open(catalogPath, usagePath string) (*Stores, error) {
	catalog, err := openRebuildable(catalogPath, catalogmigrations.Apply)
	if err != nil {
		return nil, fmt.Errorf("open catalog projection: %w", err)
	}
	usage, err := openRebuildable(usagePath, usagemigrations.Apply)
	if err != nil {
		catalog.Close()
		return nil, fmt.Errorf("open usage projection: %w", err)
	}
	return &Stores{Catalog: catalog, Usage: usage}, nil
}

func (s *Stores) Close() error {
	var first error
	if s.Catalog != nil {
		first = s.Catalog.Close()
	}
	if s.Usage != nil {
		if err := s.Usage.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// openRebuildable opens a projection database and migrates it. A projection is
// derived data: when migration fails the file is quarantined and rebuilt from
// the Session store rather than leaving the Worker unable to start.
func openRebuildable(path string, migrate func(*sql.DB) error) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	open := func() (*sql.DB, error) {
		db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_busy_timeout=5000")
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, err
		}
		if err := migrate(db); err != nil {
			db.Close()
			return nil, err
		}
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, err
		}
		return db, nil
	}
	db, err := open()
	if err == nil {
		return db, nil
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if _, statErr := os.Stat(candidate); statErr == nil {
			if renameErr := os.Rename(candidate, candidate+".corrupt-"+stamp); renameErr != nil {
				return nil, fmt.Errorf("quarantine corrupt projection %s: %w", candidate, renameErr)
			}
		}
	}
	return open()
}
