package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalWorkspaceRoot_Normal(t *testing.T) {
	dir := t.TempDir()
	result, err := CanonicalWorkspaceRoot(dir)
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	assert.DirExists(t, result)
}

func TestCanonicalWorkspaceRoot_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.MkdirAll(target, 0o755))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(target, link))

	result, err := CanonicalWorkspaceRoot(link)
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(target), result)
}

func TestCanonicalWorkspaceRoot_NotDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(file, []byte("data"), 0o644))
	_, err := CanonicalWorkspaceRoot(file)
	assert.Error(t, err)
}

func TestCanonicalWorkspaceRoot_DoesNotExist(t *testing.T) {
	_, err := CanonicalWorkspaceRoot("/tmp/doesnotexist-xyzzy")
	assert.Error(t, err)
}
