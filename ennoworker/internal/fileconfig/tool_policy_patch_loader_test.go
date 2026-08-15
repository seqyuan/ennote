package fileconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeWorkspaceConfig(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, ".ennote")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600))
}

func TestLoadWorkspaceToolPolicyPatchPresent(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceConfig(t, root, `{"toolPolicyPatch":{"id":"policy-1","set":{"mode":"auto","allowedTools":["read","write"]}}}`)

	op, err := LoadWorkspaceToolPolicyPatch(root)
	require.NoError(t, err)
	require.NotNil(t, op)
	assert.Equal(t, "policy-1", op.ID)
	assert.JSONEq(t, `{"mode":"auto","allowedTools":["read","write"]}`, string(op.Set))
}

func TestLoadWorkspaceToolPolicyPatchAbsent(t *testing.T) {
	root := t.TempDir()
	// No .ennote/config.json.
	op, err := LoadWorkspaceToolPolicyPatch(root)
	require.NoError(t, err)
	assert.Nil(t, op)

	// Config without the section.
	writeWorkspaceConfig(t, root, `{"hooks":{}}`)
	op, err = LoadWorkspaceToolPolicyPatch(root)
	require.NoError(t, err)
	assert.Nil(t, op)
}

func TestLoadWorkspaceToolPolicyPatchRejectsMalformed(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceConfig(t, root, `{"toolPolicyPatch":{invalid`)
	_, err := LoadWorkspaceToolPolicyPatch(root)
	assert.Error(t, err)
}
