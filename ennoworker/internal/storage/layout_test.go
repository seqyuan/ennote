package storage_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForHomeBuildsV2Paths(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ennote-home")
	layout := storage.ForHome(home)

	assert.Equal(t, home, layout.Home)
	assert.Equal(t, filepath.Join(home, "config", "models.json"), layout.Models)
	assert.Equal(t, filepath.Join(home, "config", "provider-auth.json"), layout.ProviderAuth)
	assert.Equal(t, filepath.Join(home, "agents", "roles"), layout.Roles)
	assert.Equal(t, filepath.Join(home, "agents", "graphs"), layout.Graphs)
	assert.Equal(t, filepath.Join(home, "projects"), layout.Projects)
	assert.Equal(t, filepath.Join(home, "data", "catalog.db"), layout.CatalogDB)
	assert.Equal(t, filepath.Join(home, "data", "usage.db"), layout.UsageDB)
	assert.Equal(t, filepath.Join(home, "runtime", "worker-state.json"), layout.WorkerState)
	assert.Equal(t, filepath.Join(home, "data", "ennote.db"), layout.LegacyDB)
}

func TestBootstrapArchivesLegacyFiles(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "data", "ennote.db")
	writeLegacyFiles(t, legacy)

	layout, err := storage.Bootstrap(home, func(layout storage.Layout) error {
		for _, path := range []string{layout.LegacyDB, layout.LegacyDB + "-wal", layout.LegacyDB + "-shm"} {
			_, statErr := os.Stat(path)
			require.ErrorIs(t, statErr, os.ErrNotExist)
		}
		return nil
	})
	require.NoError(t, err)
	require.FileExists(t, layout.Marker)

	archiveDir := filepath.Join(home, "data", "legacy-v1")
	for _, name := range []string{"ennote.db", "ennote.db-wal", "ennote.db-shm"} {
		contents, readErr := os.ReadFile(filepath.Join(archiveDir, name))
		require.NoError(t, readErr)
		assert.Equal(t, "legacy", string(contents))
	}
}

func TestBootstrapLeavesArchiveUntouchedAfterMarkerExists(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "data", "ennote.db")
	writeLegacyFiles(t, legacy)

	_, err := storage.Bootstrap(home, nil)
	require.NoError(t, err)

	archive := filepath.Join(home, "data", "legacy-v1", "ennote.db")
	before, err := os.Stat(archive)
	require.NoError(t, err)

	// Simulate a user restoring a legacy file after the V2 layout exists.
	writeLegacyFiles(t, legacy)
	_, err = storage.Bootstrap(home, nil)
	require.NoError(t, err)
	require.FileExists(t, legacy)

	after, err := os.Stat(archive)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
	assert.Equal(t, before.Size(), after.Size())
}

func TestBootstrapDoesNotCreateEmptyArchiveDirectory(t *testing.T) {
	home := t.TempDir()
	_, err := storage.Bootstrap(home, nil)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(home, "data", "legacy-v1"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestBootstrapWritesMarkerOnlyAfterInitialization(t *testing.T) {
	home := t.TempDir()
	expected := errors.New("catalog failed")
	_, err := storage.Bootstrap(home, func(storage.Layout) error { return expected })
	require.ErrorIs(t, err, expected)
	_, statErr := os.Stat(filepath.Join(home, "storage-layout.json"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestBootstrapFailsWhenLegacyStoreCannotBeArchived(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "data", "ennote.db")
	writeLegacyFiles(t, legacy)
	// A regular file where the archive directory belongs makes MkdirAll fail.
	require.NoError(t, os.WriteFile(filepath.Join(home, "data", "legacy-v1"), []byte("blocked"), 0o600))

	_, err := storage.Bootstrap(home, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create legacy archive directory")
	_, statErr := os.Stat(filepath.Join(home, "storage-layout.json"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	require.FileExists(t, legacy)
}

func TestBootstrapRejectsUnknownMarkerFields(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "storage-layout.json"), []byte(`{
  "schemaVersion": 2,
  "initializedAt": "2026-08-10T00:00:00Z",
  "unexpected": true
}`), 0o600))

	_, err := storage.Bootstrap(home, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func writeLegacyFiles(t *testing.T, database string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(database), 0o700))
	for _, path := range []string{database, database + "-wal", database + "-shm"} {
		require.NoError(t, os.WriteFile(path, []byte("legacy"), 0o600))
	}
}
