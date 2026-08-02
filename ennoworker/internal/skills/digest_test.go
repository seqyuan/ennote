package skills

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDigest_TreeDigest(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world"), 0o600))

	d1, err := treeDigest(dir)
	require.NoError(t, err)
	assert.NotEmpty(t, d1)

	// Same content → same digest
	d2, err := treeDigest(dir)
	require.NoError(t, err)
	assert.Equal(t, d1, d2)

	// Change content → different digest
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello2"), 0o644))
	d3, err := treeDigest(dir)
	require.NoError(t, err)
	assert.NotEqual(t, d1, d3)
}

func TestDigest_ExecutableModeChanges(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "script.sh"), []byte("#!/bin/sh"), 0o644))

	d1, err := treeDigest(dir)
	require.NoError(t, err)

	// Change mode → different digest
	require.NoError(t, os.Chmod(filepath.Join(dir, "script.sh"), 0o755))
	d2, err := treeDigest(dir)
	require.NoError(t, err)
	assert.NotEqual(t, d1, d2, "executable mode change must affect digest")
}

func TestDigest_MtimeIgnored(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("content"), 0o644))

	d1, err := treeDigest(dir)
	require.NoError(t, err)

	// Touch file (change mtime) → same digest
	fi, err := os.Stat(filepath.Join(dir, "f.txt"))
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "f.txt"), fi.ModTime().Add(time.Hour), fi.ModTime().Add(time.Hour)))
	d2, err := treeDigest(dir)
	require.NoError(t, err)
	assert.Equal(t, d1, d2, "mtime change must not affect digest")
}

func TestDigest_NewFileChangesDigest(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644))

	d1, err := treeDigest(dir)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644))
	d2, err := treeDigest(dir)
	require.NoError(t, err)
	assert.NotEqual(t, d1, d2)
}

func TestDigest_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "empty"), 0o755))

	d, err := treeDigest(dir)
	require.NoError(t, err)
	assert.NotEmpty(t, d, "digest must include empty directories")
}

func TestDigest_DifferentDomainTags(t *testing.T) {
	entries := []CatalogManifestEntry{
		{Kind: "skill", RelPath: "s1", SourceName: "user", SourceDigest: "abc", SnapshotDigest: "def", SnapshotMode: "bwrap"},
	}

	src := SourceCatalogDigest(entries)
	snap := SnapshotCatalogDigest(entries, "bwrap")
	cat := CatalogDigest(entries, "bwrap", src, snap)

	// All three must be different
	assert.NotEqual(t, src, snap)
	assert.NotEqual(t, src, cat)
	assert.NotEqual(t, snap, cat)

	// Must not match each other when cross-compared
	assert.NotEqual(t, src, snap)
	assert.NotEqual(t, src, cat)
}

func TestDigest_CategoryDigest(t *testing.T) {
	d1 := ComputeCategoryDigest([]byte("hello"))
	d2 := ComputeCategoryDigest([]byte("hello"))
	d3 := ComputeCategoryDigest([]byte("world"))

	assert.Equal(t, d1, d2)
	assert.NotEqual(t, d1, d3)
}
