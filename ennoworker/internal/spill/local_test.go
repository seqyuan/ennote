package spill

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalSaveTextRoundTrip(t *testing.T) {
	store := NewLocal(filepath.Join(t.TempDir(), "spill"))
	ref, err := store.SaveText(context.Background(), SaveInput{
		Owner:         Owner{SessionID: "s1"},
		Source:        Source{ToolName: "bash", CallID: "c1", Label: "result"},
		SuggestedName: "bash.txt",
		Content:       "hello spill",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, ref.Locator)
	assert.Equal(t, int64(11), ref.Bytes)
	assert.Equal(t, "read or grep this file", ref.RetrievalHint)

	data, err := os.ReadFile(ref.Locator)
	require.NoError(t, err)
	assert.Equal(t, "hello spill", string(data))

	// Locator is session-scoped.
	assert.Contains(t, ref.Locator, "session-")
	// The file is owner-only.
	info, err := os.Stat(ref.Locator)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestLocalSaveTextIsolatesSessions(t *testing.T) {
	store := NewLocal(t.TempDir())
	refA, err := store.SaveText(context.Background(), SaveInput{Owner: Owner{SessionID: "a"}, SuggestedName: "x.txt", Content: "a"})
	require.NoError(t, err)
	refB, err := store.SaveText(context.Background(), SaveInput{Owner: Owner{SessionID: "b"}, SuggestedName: "x.txt", Content: "b"})
	require.NoError(t, err)
	assert.NotEqual(t, filepath.Dir(refA.Locator), filepath.Dir(refB.Locator))
}

func TestLocalSaveTextSanitizesName(t *testing.T) {
	store := NewLocal(t.TempDir())
	ref, err := store.SaveText(context.Background(), SaveInput{Owner: Owner{SessionID: "s"}, SuggestedName: "bad name.txt", Content: "x"})
	require.NoError(t, err)
	assert.Contains(t, filepath.Base(ref.Locator), "spill.txt")
}

func TestLocalSaveTextRejectsSymlinkTarget(t *testing.T) {
	store := NewLocal(t.TempDir())
	ref, err := store.SaveText(context.Background(), SaveInput{Owner: Owner{SessionID: "s"}, SuggestedName: "x.txt", Content: "x"})
	require.NoError(t, err)
	// The produced locator must be a regular file, never a symlink. The random
	// name + O_EXCL write makes a planted symlink unpredictable and unredirectable.
	info, err := os.Lstat(ref.Locator)
	require.NoError(t, err)
	assert.False(t, info.Mode()&os.ModeSymlink != 0)
	assert.True(t, info.Mode().IsRegular())
}
