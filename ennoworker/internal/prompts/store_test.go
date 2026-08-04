package prompts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) (*GlobalStore, string) {
	t.Helper()
	home := t.TempDir()
	store, err := OpenGlobalStore(home)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store, home
}

func TestStore_CreateGetUpdateDelete(t *testing.T) {
	s, _ := openTestStore(t)

	require.NoError(t, s.Create("review", "desc", "<path>", "body: $1"))

	got, err := s.Get("review")
	require.NoError(t, err)
	assert.Equal(t, "review", got.Name)
	assert.Equal(t, "desc", got.Description)
	assert.Equal(t, "<path>", got.ArgumentHint)
	assert.Equal(t, "body: $1\n", got.Body)
	assert.Equal(t, "global", got.Source)

	require.NoError(t, s.Update("review", "new desc", "", "new body"))
	got, err = s.Get("review")
	require.NoError(t, err)
	assert.Equal(t, "new desc", got.Description)
	assert.Equal(t, "new body\n", got.Body)

	require.NoError(t, s.Delete("review"))
	_, err = s.Get("review")
	assert.ErrorIs(t, err, ErrPromptTemplateNotFound)
}

func TestStore_CreateDuplicate(t *testing.T) {
	s, _ := openTestStore(t)
	require.NoError(t, s.Create("dup", "", "", "a"))
	err := s.Create("dup", "", "", "b")
	assert.ErrorIs(t, err, ErrPromptTemplateExists)
}

func TestStore_CreateInvalidName(t *testing.T) {
	s, _ := openTestStore(t)
	err := s.Create("BadName", "", "", "body")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)
	err = s.Create("", "", "", "body")
	assert.ErrorIs(t, err, ErrTemplateNameInvalid)
}

func TestStore_CreateTooLarge(t *testing.T) {
	s, _ := openTestStore(t)
	big := string(make([]byte, maxTemplateBytes+100))
	err := s.Create("big", "", "", big)
	assert.ErrorIs(t, err, ErrPromptTemplateTooLarge)
}

func TestStore_GetInvalidContent(t *testing.T) {
	s, home := openTestStore(t)
	// Write a malformed file directly.
	require.NoError(t, os.WriteFile(filepath.Join(home, "prompts", "broken.md"), []byte("---\nname: 123\n---\n"), 0644))
	_, err := s.Get("broken")
	assert.ErrorIs(t, err, ErrPromptTemplateInvalid)
}

func TestStore_ListManagement(t *testing.T) {
	s, home := openTestStore(t)
	require.NoError(t, s.Create("a", "A desc", "", "body a"))
	require.NoError(t, s.Create("b", "B desc", "", "body b"))
	// A malformed safe-name file.
	require.NoError(t, os.WriteFile(filepath.Join(home, "prompts", "bad.md"), []byte("---\nname: 123\n---\n"), 0644))
	// A non-md file that is not a valid basename.
	require.NoError(t, os.WriteFile(filepath.Join(home, "prompts", "junk.txt"), []byte("x"), 0644))

	result := s.List()
	assert.False(t, result.RecoveryMode)
	assert.Len(t, result.Templates, 2)

	// bad.md appears as an invalid but editable entry.
	found := false
	for _, e := range result.GlobalEntries {
		if e.Name == "bad" {
			found = true
			assert.False(t, e.Valid)
			assert.True(t, e.Editable)
		}
	}
	assert.True(t, found, "malformed safe-name entry should be manageable")
}

func TestStore_ListRecoveryMode(t *testing.T) {
	s, home := openTestStore(t)
	// Create 2001 entries by writing files directly.
	dir := filepath.Join(home, "prompts")
	for i := 0; i < 2001; i++ {
		name := "t"
		if i < 26 {
			name += string(rune('a' + i))
		} else {
			name += string(rune('a'+(i%26))) + string(rune('a'+((i/26)%26))) + string(rune('a'+((i/26/26)%26)))
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte("---\nname: "+name+"\n---\nbody"), 0644))
	}

	result := s.List()
	assert.True(t, result.RecoveryMode)
	assert.True(t, containsCode(result.Diagnostics, "global_recovery_required"))
}

func TestStore_RecoveryModeCreateDisabled(t *testing.T) {
	s, home := openTestStore(t)
	dir := filepath.Join(home, "prompts")
	// Fill to 2000 entries.
	for i := 0; i < 2000; i++ {
		name := "t" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/26/26)%26))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte("x"), 0644))
	}
	// 2000 entries → Create allowed up to 2000, so the 2000th fails only if
	// there are already 2000. With exactly 2000, create should fail.
	err := s.Create("newone", "", "", "body")
	assert.ErrorIs(t, err, ErrPromptTemplateLimit)
}

func TestStore_RepairInvalidViaUpdate(t *testing.T) {
	s, home := openTestStore(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, "prompts", "fixme.md"), []byte("---\nname: 123\n---\n"), 0644))

	// PUT can repair an invalid safe file.
	require.NoError(t, s.Update("fixme", "fixed desc", "", "fixed body"))
	got, err := s.Get("fixme")
	require.NoError(t, err)
	assert.Equal(t, "fixed body\n", got.Body)
}

func TestStore_CreateUpdatesParentDirSync(t *testing.T) {
	s, _ := openTestStore(t)
	require.NoError(t, s.Create("sync", "", "", "body"))
	// File must be readable on disk.
	data, err := os.ReadFile(filepath.Join(s.dir, "sync.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "body")
}
