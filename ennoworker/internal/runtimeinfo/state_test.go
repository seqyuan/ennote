package runtimeinfo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testState(instanceID string) WorkerState {
	return WorkerState{
		Version: StateVersion, URL: "http://127.0.0.1:32123", PID: os.Getpid(),
		InstanceID: instanceID, BootstrapToken: "test-bootstrap-token",
		StartedAt: time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
	}
}

func TestWorkerStateIsAtomicAndOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "worker-state.json")
	state := testState("instance-a")
	require.NoError(t, WriteAtomic(path, state))

	stored, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, state, *stored)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	matches, err := filepath.Glob(path + ".tmp-*")
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestWorkerStateRemovalRequiresMatchingOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-state.json")
	require.NoError(t, WriteAtomic(path, testState("instance-a")))

	require.NoError(t, RemoveIfOwner(path, os.Getpid()+1, "instance-a"))
	_, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, RemoveIfOwner(path, os.Getpid(), "instance-b"))
	_, err = os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, RemoveIfOwner(path, os.Getpid(), "instance-a"))
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestWorkerStateRejectsIncompleteAndUnsupportedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-state.json")
	require.Error(t, WriteAtomic(path, WorkerState{Version: StateVersion}))

	require.NoError(t, os.WriteFile(path, []byte(`{"version":99,"url":"http://127.0.0.1:1","pid":1,"instanceId":"x","bootstrapToken":"secret"}`), 0o600))
	_, err := Load(path)
	assert.ErrorContains(t, err, "incomplete or unsupported")
}
