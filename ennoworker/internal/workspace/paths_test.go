package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJailResolvesWorkspaceAndRelativePaths(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "data", "sample.txt"), []byte("ok"), 0o644))
	jail, err := NewJail(root)
	require.NoError(t, err)

	relative, err := jail.ResolveExisting("data/sample.txt")
	require.NoError(t, err)
	virtual, err := jail.ResolveExisting("/workspace/data/sample.txt")
	require.NoError(t, err)
	assert.Equal(t, relative, virtual)
	display, err := jail.DisplayPath(relative)
	require.NoError(t, err)
	assert.Equal(t, "/workspace/data/sample.txt", display)
}

func TestJailRejectsTraversalAndHostAbsolutePath(t *testing.T) {
	jail, err := NewJail(t.TempDir())
	require.NoError(t, err)
	for _, path := range []string{"../outside", "/etc/passwd", "/workspace/../../etc/passwd"} {
		_, err := jail.ResolveForWrite(path)
		assert.True(t, errors.Is(err, ErrPathEscape), "%s: %v", path, err)
	}
}

func TestJailRejectsExistingAndMissingSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	jail, err := NewJail(root)
	require.NoError(t, err)

	_, err = jail.ResolveExisting("escape/secret")
	assert.ErrorIs(t, err, ErrPathEscape)
	_, err = jail.ResolveForWrite("escape/new/file.txt")
	assert.ErrorIs(t, err, ErrPathEscape)
}

func TestJailAllowsInternalSymlink(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "real"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")))
	jail, err := NewJail(root)
	require.NoError(t, err)
	path, err := jail.ResolveForWrite("link/new.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "real", "new.txt"), path)
}
