package fileconfig_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialStoreWrites0600AndReplacesAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "provider-auth.json")
	store := &fileconfig.CredentialStore{Path: path}

	require.NoError(t, store.Put("deepseek-main", "sk-first"))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	value, err := store.Resolve("deepseek-main")
	require.NoError(t, err)
	assert.Equal(t, "sk-first", value)

	require.NoError(t, store.Put("deepseek-main", "sk-second"))
	value, err = store.Resolve("deepseek-main")
	require.NoError(t, err)
	assert.Equal(t, "sk-second", value)
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".provider-auth.json-*"))
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestCredentialStoreRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-auth.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "schemaVersion": 1,
  "credentials": {
    "deepseek-main": {"type": "api_key", "value": "sk-secret"}
  }
}`), 0o644))
	store := &fileconfig.CredentialStore{Path: path}

	_, err := store.Resolve("deepseek-main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permissions must be 0600")
}

func TestCredentialStoreRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider-auth.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "schemaVersion": 1,
  "credentials": {},
  "unexpected": true
}`), 0o600))
	store := &fileconfig.CredentialStore{Path: path}

	_, err := store.Resolve("deepseek-main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestCredentialStoreReportsMissingCredential(t *testing.T) {
	store := &fileconfig.CredentialStore{Path: filepath.Join(t.TempDir(), "provider-auth.json")}
	_, err := store.Resolve("missing-main")
	require.Error(t, err)
	assert.True(t, fileconfig.IsCredentialUnavailable(err))
}
