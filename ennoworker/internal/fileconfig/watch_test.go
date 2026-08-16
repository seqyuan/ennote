package fileconfig

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWatchFileInvokesCallbackAfterDebounce verifies the watcher fires the
// callback once a watched file changes, after the debounce window.
func TestWatchFileInvokesCallbackAfterDebounce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"v":0}`), 0o600))

	var mu sync.Mutex
	count := 0
	stop := watchFile(path, 40*time.Millisecond, func() {
		mu.Lock()
		count++
		mu.Unlock()
	})
	defer stop()

	// Let the watcher establish before the first write.
	time.Sleep(80 * time.Millisecond)
	require.NoError(t, os.WriteFile(path, []byte(`{"v":1}`), 0o600))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return count >= 1
	}, 3*time.Second, 20*time.Millisecond, "watcher should fire after a change")
}

// TestWatchFileStopStopsDelivery verifies the returned stop function halts
// future callbacks.
func TestWatchFileStopStopsDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"v":0}`), 0o600))

	var mu sync.Mutex
	count := 0
	stop := watchFile(path, 20*time.Millisecond, func() {
		mu.Lock()
		count++
		mu.Unlock()
	})
	time.Sleep(80 * time.Millisecond)
	stop()

	require.NoError(t, os.WriteFile(path, []byte(`{"v":1}`), 0o600))
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 0, count, "stopped watcher must not deliver events")
}
