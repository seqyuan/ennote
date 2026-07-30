package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAndSnapshotSkill(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"id":"test-skill","version":"0.2.0","prompt":"SKILL.md","allowedTools":["read","bash"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("# Test Skill\nDo the thing."), 0o644))

	skill, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "test-skill", skill.Manifest.ID)
	assert.Equal(t, "0.2.0", skill.Manifest.Version)
	assert.NotEmpty(t, skill.ManifestHash)
	assert.NotEmpty(t, skill.ContentHash)
	assert.Contains(t, skill.PromptText, "Do the thing")

	runDir := t.TempDir()
	dest, err := skill.CopyToSnapshot(runDir)
	require.NoError(t, err)
	assert.Contains(t, dest, "test-skill")

	snapshotData, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(snapshotData), "Do the thing")

	originalHash := skill.ContentHash
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("changed"), 0o644))
	changed, err := Load(dir)
	require.NoError(t, err)
	assert.NotEqual(t, originalHash, changed.ContentHash, "content hash must change when files change")
}

func TestLoadRejectsMissingId(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"), []byte(`{}`), 0o644))
	_, err := Load(dir)
	assert.ErrorContains(t, err, "skill id is required")
}

func TestDiscoverSkipsInvalidDirs(t *testing.T) {
	base := t.TempDir()
	valid := filepath.Join(base, "valid")
	require.NoError(t, os.MkdirAll(valid, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(valid, "skill.json"),
		[]byte(`{"id":"valid","prompt":"SKILL.md"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(valid, "SKILL.md"), []byte("prompt"), 0o644))

	invalid := filepath.Join(base, "invalid")
	require.NoError(t, os.MkdirAll(invalid, 0o755))

	skills := Discover(base)
	require.Len(t, skills, 1)
	assert.Equal(t, "valid", skills[0].Manifest.ID)
}
