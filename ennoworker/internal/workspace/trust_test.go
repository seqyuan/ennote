package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrustStoreBasicOperations(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTrustStore(dir)
	require.NoError(t, err)

	// Initially not trusted.
	trusted, err := store.IsTrusted("ws-1", "/data/project-a")
	require.NoError(t, err)
	assert.False(t, trusted)

	// Trust.
	require.NoError(t, store.Trust("ws-1", "/data/project-a"))
	trusted, err = store.IsTrusted("ws-1", "/data/project-a")
	require.NoError(t, err)
	assert.True(t, trusted)

	// Root mismatch -> not trusted.
	trusted, err = store.IsTrusted("ws-1", "/data/project-b")
	require.NoError(t, err)
	assert.False(t, trusted)

	// ID mismatch -> not trusted.
	trusted, err = store.IsTrusted("ws-2", "/data/project-a")
	require.NoError(t, err)
	assert.False(t, trusted)
}

func TestTrustStoreRevoke(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTrustStore(dir)
	require.NoError(t, err)

	require.NoError(t, store.Trust("ws-1", "/data/a"))
	require.NoError(t, store.Trust("ws-2", "/data/b"))

	list, err := store.List()
	require.NoError(t, err)
	assert.Len(t, list, 2)

	require.NoError(t, store.Revoke("ws-1"))
	list, err = store.List()
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "ws-2", list[0].WorkspaceID)

	trusted, err := store.IsTrusted("ws-1", "/data/a")
	require.NoError(t, err)
	assert.False(t, trusted)
}

func TestTrustStoreUpdateCanonicalRoot(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTrustStore(dir)
	require.NoError(t, err)

	require.NoError(t, store.Trust("ws-1", "/data/old"))
	require.NoError(t, store.Trust("ws-1", "/data/new"))

	// Old root is no longer trusted.
	trusted, err := store.IsTrusted("ws-1", "/data/old")
	require.NoError(t, err)
	assert.False(t, trusted)

	// New root is trusted.
	trusted, err = store.IsTrusted("ws-1", "/data/new")
	require.NoError(t, err)
	assert.True(t, trusted)

	// Only one record exists.
	list, err := store.List()
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestTrustStorePersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTrustStore(dir)
	require.NoError(t, err)
	require.NoError(t, store.Trust("ws-1", "/data/a"))

	// Reopen and verify.
	store2, err := NewTrustStore(dir)
	require.NoError(t, err)
	trusted, err := store2.IsTrusted("ws-1", "/data/a")
	require.NoError(t, err)
	assert.True(t, trusted)
}
