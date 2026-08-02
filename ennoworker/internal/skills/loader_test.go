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

func TestLoadRejectsInvalidPrompt(t *testing.T) {
	dir := t.TempDir()

	t.Run("other filename", func(t *testing.T) {
		d := filepath.Join(dir, "other")
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "skill.json"),
			[]byte(`{"id":"s","prompt":"other.md"}`), 0o644))
		_, err := Load(d)
		assert.ErrorContains(t, err, "must be empty or \"SKILL.md\"")
	})

	t.Run("absolute path", func(t *testing.T) {
		d := filepath.Join(dir, "abs")
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "skill.json"),
			[]byte(`{"id":"s","prompt":"/etc/passwd"}`), 0o644))
		_, err := Load(d)
		assert.ErrorContains(t, err, "must be empty or \"SKILL.md\"")
	})

	t.Run("dotdot", func(t *testing.T) {
		d := filepath.Join(dir, "dotdot")
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "skill.json"),
			[]byte(`{"id":"s","prompt":"../x.md"}`), 0o644))
		_, err := Load(d)
		assert.ErrorContains(t, err, "must be empty or \"SKILL.md\"")
	})
}

func TestLoadEmptyPromptDefaultsToSkillMD(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"id":"s","prompt":""}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("default prompt"), 0o644))

	skill, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "default prompt", skill.PromptText)
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

func TestIsSkillLeaf(t *testing.T) {
	dir := t.TempDir()

	t.Run("valid skill leaf", func(t *testing.T) {
		d := filepath.Join(dir, "s1")
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "skill.json"),
			[]byte(`{"id":"s1"}`), 0o644))
		ok, err := IsSkillLeaf(d)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("not a skill leaf", func(t *testing.T) {
		d := filepath.Join(dir, "empty")
		require.NoError(t, os.MkdirAll(d, 0o755))
		ok, err := IsSkillLeaf(d)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("symlink skill.json", func(t *testing.T) {
		d := filepath.Join(dir, "sym")
		require.NoError(t, os.MkdirAll(d, 0o755))
		target := filepath.Join(dir, "real.json")
		require.NoError(t, os.WriteFile(target, []byte(`{"id":"sym"}`), 0o644))
		require.NoError(t, os.Symlink(target, filepath.Join(d, "skill.json")))
		ok, err := IsSkillLeaf(d)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("both skill.json and category.md", func(t *testing.T) {
		d := filepath.Join(dir, "both")
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "skill.json"),
			[]byte(`{"id":"both"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(d, "category.md"),
			[]byte("---\ndescription: cat\n---\n# Cat"), 0o644))
		_, err := IsSkillLeaf(d)
		assert.Error(t, err)
	})
}

func TestIsCategoryDir(t *testing.T) {
	dir := t.TempDir()

	t.Run("valid category", func(t *testing.T) {
		d := filepath.Join(dir, "cat")
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "category.md"),
			[]byte("---\ndescription: Test\n---\n# Test"), 0o644))
		ok, err := IsCategoryDir(d)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("not a category", func(t *testing.T) {
		d := filepath.Join(dir, "empty")
		require.NoError(t, os.MkdirAll(d, 0o755))
		ok, err := IsCategoryDir(d)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("category with skill.json", func(t *testing.T) {
		d := filepath.Join(dir, "mixed")
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "category.md"),
			[]byte("---\ndescription: Test\n---\n# Test"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(d, "skill.json"),
			[]byte(`{"id":"mixed"}`), 0o644))
		_, err := IsCategoryDir(d)
		assert.Error(t, err)
	})

	t.Run("category with SKILL.md", func(t *testing.T) {
		d := filepath.Join(dir, "mixed2")
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "category.md"),
			[]byte("---\ndescription: Test\n---\n# Test"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(d, "SKILL.md"),
			[]byte("# Nope"), 0o644))
		_, err := IsCategoryDir(d)
		assert.Error(t, err)
	})
}
