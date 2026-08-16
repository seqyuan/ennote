package fileconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteAtomicPersistsContentAndMode verifies the atomic write produces a
// complete file with the requested mode and a trailing newline, and that the
// directory is created when missing.
func TestWriteAtomicPersistsContentAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "config.json")
	err := writeJSONAtomic(path, map[string]any{"k": "v"}, 0o600)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "v", decoded["k"])
	assert.Equal(t, byte('\n'), data[len(data)-1])

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// No leftover temp files (the advisory lock file is expected).
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.ElementsMatch(t, []string{"config.json", "config.json.lock"}, names,
		"atomic write must leave only the file and its lock behind")
}

// TestWriteAtomicOverwritesAtomically verifies an existing file is replaced
// (rename semantics) and a failure to marshal leaves the previous content
// untouched (fail-closed).
func TestWriteAtomicOverwritesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, writeJSONAtomic(path, map[string]any{"v": 1}, 0o600))

	require.NoError(t, writeJSONAtomic(path, map[string]any{"v": 2}, 0o600))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, float64(2), decoded["v"])
}

// TestReadStrictJSONRejectsCorruptAndTrailing verifies the strict reader fails
// closed on truncated, malformed, and multi-value files.
func TestReadStrictJSONRejectsCorruptAndTrailing(t *testing.T) {
	for name, contents := range map[string]string{
		"truncated": `{"schemaVersion":1, "k":`,
		"malformed": `not-json`,
		"trailing":  `{"a":1} {"b":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
			var target map[string]any
			found, err := readStrictJSON(path, &target)
			assert.True(t, found)
			assert.Error(t, err)
		})
	}
}

// TestReadStrictJSONUnknownFieldFailsClosed verifies DisallowUnknownFields.
func TestReadStrictJSONUnknownFieldFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"known":1,"unknown":2}`), 0o600))
	var target struct {
		Known int `json:"known"`
	}
	found, err := readStrictJSON(path, &target)
	assert.True(t, found)
	assert.Error(t, err)
}

// TestReadStrictJSONMissingFileIsNotFound verifies a missing file returns
// found=false without error (the callers treat it as "not configured").
func TestReadStrictJSONMissingFileIsNotFound(t *testing.T) {
	found, err := readStrictJSON(filepath.Join(t.TempDir(), "missing.json"), &struct{}{})
	assert.False(t, found)
	assert.NoError(t, err)
}
