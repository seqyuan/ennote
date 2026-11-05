package spill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func seedSpillFile(t *testing.T, root, session, name, content string, modTime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, "session-"+session)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	require.NoError(t, os.Chtimes(path, modTime, modTime))
	return path
}

func TestReclaimDropsFilesPastTheRetentionAge(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old := seedSpillFile(t, root, "a", "old-spill.txt", "old", now.Add(-30*24*time.Hour))
	fresh := seedSpillFile(t, root, "b", "fresh-spill.txt", "fresh", now)

	result, err := NewLocal(root).Reclaim(context.Background(), ReclaimOptions{
		MaxAge: DefaultMaxAge, Now: now,
	})
	require.NoError(t, err)

	assert.Equal(t, 1, result.Removed)
	assert.Equal(t, int64(len("old")), result.RemovedBytes)
	assert.NoFileExists(t, old)
	assert.FileExists(t, fresh)
}

func TestReclaimEnforcesTheByteCapOldestFirst(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	oldest := seedSpillFile(t, root, "a", "one.txt", strings.Repeat("x", 100), now.Add(-3*time.Hour))
	middle := seedSpillFile(t, root, "a", "two.txt", strings.Repeat("y", 100), now.Add(-2*time.Hour))
	newest := seedSpillFile(t, root, "a", "three.txt", strings.Repeat("z", 100), now.Add(-time.Hour))

	result, err := NewLocal(root).Reclaim(context.Background(), ReclaimOptions{MaxBytes: 150, Now: now})
	require.NoError(t, err)

	assert.Equal(t, 2, result.Removed, "the cap needs two files gone to fit")
	assert.NoFileExists(t, oldest)
	assert.NoFileExists(t, middle)
	assert.FileExists(t, newest, "the newest file is the last to go")
}

func TestReclaimNeverFollowsOrRemovesSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("keep me"), 0o600))
	dir := filepath.Join(root, "session-link")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(outside, link))

	_, err := NewLocal(root).Reclaim(context.Background(), ReclaimOptions{
		MaxAge: time.Nanosecond, MaxBytes: 1, Now: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	assert.FileExists(t, outside, "a symlink must not turn cleanup into an arbitrary delete")
	assert.FileExists(t, link, "the link itself is left alone")
}

func TestReclaimRemovesEmptiedSessionDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	seedSpillFile(t, root, "gone", "old.txt", "x", now.Add(-40*24*time.Hour))

	_, err := NewLocal(root).Reclaim(context.Background(), ReclaimOptions{MaxAge: DefaultMaxAge, Now: now})
	require.NoError(t, err)

	assert.NoDirExists(t, filepath.Join(root, "session-gone"))
}

func TestReclaimOnAMissingRootIsANoOp(t *testing.T) {
	result, err := NewLocal(filepath.Join(t.TempDir(), "absent")).Reclaim(context.Background(), ReclaimOptions{
		MaxAge: DefaultMaxAge, MaxBytes: DefaultMaxBytes,
	})
	require.NoError(t, err)
	assert.Zero(t, result.Removed)
}
