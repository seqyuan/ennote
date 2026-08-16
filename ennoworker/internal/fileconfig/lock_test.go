package fileconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithFileLockSerializesConcurrentWrites verifies the advisory lock keeps
// concurrent writes from corrupting the file: after N racing writers the file
// must be a complete JSON value from exactly one writer, never a torn mix.
func TestWithFileLockSerializesConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = writeJSONAtomic(path, map[string]any{"v": n}, 0o600)
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded map[string]int
	require.NoError(t, json.Unmarshal(data, &decoded), "concurrent writes must not tear the file")
	v, ok := decoded["v"]
	require.True(t, ok)
	assert.GreaterOrEqual(t, v, 0)
	assert.Less(t, v, 20)
}

// TestWithFileLockReentrantInSameProcess verifies the flock is advisory per
// file descriptor, so sequential calls in one process do not deadlock.
func TestWithFileLockReentrantInSameProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, writeJSONAtomic(path, map[string]any{"v": 1}, 0o600))
	require.NoError(t, writeJSONAtomic(path, map[string]any{"v": 2}, 0o600))
}
