package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeSkillLeaf(t *testing.T, base, relPath string, manifestJSON, skillMD string) {
	t.Helper()
	dir := filepath.Join(base, filepath.FromSlash(relPath))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"), []byte(manifestJSON), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o644))
}

func makeCategory(t *testing.T, base, relPath, catMD string) {
	t.Helper()
	dir := filepath.Join(base, filepath.FromSlash(relPath))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "category.md"), []byte(catMD), 0o644))
}

func TestCatalog_ArbitraryDepth(t *testing.T) {
	base := t.TempDir()
	makeCategory(t, base, "a", "---\ndescription: Level A\n---\n# A")
	makeCategory(t, base, "a/b", "---\ndescription: Level B\n---\n# B")
	makeSkillLeaf(t, base, "a/b/c", `{"id":"deep-skill","prompt":"SKILL.md"}`, "# Deep Skill")

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	require.Len(t, catalog.Roots, 1)
	assert.Equal(t, NodeCategory, catalog.Roots[0].Kind)
	assert.Equal(t, "a", catalog.Roots[0].RelPath)
	require.Len(t, catalog.Roots[0].Children, 1)
	assert.Equal(t, NodeCategory, catalog.Roots[0].Children[0].Kind)
	assert.Equal(t, "a/b", catalog.Roots[0].Children[0].RelPath)
	require.Len(t, catalog.Roots[0].Children[0].Children, 1)
	assert.Equal(t, NodeSkill, catalog.Roots[0].Children[0].Children[0].Kind)
	assert.Equal(t, "a/b/c", catalog.Roots[0].Children[0].Children[0].RelPath)
	assert.Equal(t, "deep-skill", catalog.Roots[0].Children[0].Children[0].Skill.Manifest.ID)
}

func TestCatalog_RootSkillLeaf(t *testing.T) {
	base := t.TempDir()
	makeSkillLeaf(t, base, "myskill", `{"id":"root-skill","prompt":"SKILL.md"}`, "# Root Skill")

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	require.Len(t, catalog.Roots, 1)
	assert.Equal(t, NodeSkill, catalog.Roots[0].Kind)
	assert.Equal(t, "myskill", catalog.Roots[0].RelPath)
}

func TestCatalog_MissingCategory(t *testing.T) {
	base := t.TempDir()
	// Create an intermediate directory without category.md
	dir := filepath.Join(base, "intermediate")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	makeSkillLeaf(t, base, "intermediate/deep", `{"id":"deep","prompt":"SKILL.md"}`, "# Deep")

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	// Should get a diagnostic about missing category.md
	found := false
	for _, d := range catalog.Diagnostics {
		if d.RelPath == "intermediate" && d.Source == "user" {
			found = true
		}
	}
	assert.True(t, found, "expected diagnostic about missing category.md")

	// Deep skill should still be discovered
	require.Len(t, catalog.Skills, 1)
	assert.Equal(t, "deep", catalog.Skills[0].Manifest.ID)
}

func TestCatalog_SymlinkRejection(t *testing.T) {
	base := t.TempDir()
	symDir := filepath.Join(base, "symtarget")
	require.NoError(t, os.MkdirAll(symDir, 0o755))
	makeSkillLeaf(t, base, "symtarget", `{"id":"sym-skill","prompt":"SKILL.md"}`, "# Sym")

	// Create symlink
	require.NoError(t, os.Symlink(symDir, filepath.Join(base, "symlink")))

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	// symtarget should be discovered as "symtarget"
	assert.Len(t, catalog.Skills, 1)
	assert.Equal(t, "symtarget", catalog.Skills[0].RelPath)
}

func TestCatalog_ManifestPromptValidation(t *testing.T) {
	t.Run("empty prompt ok", func(t *testing.T) {
		base := t.TempDir()
		makeSkillLeaf(t, base, "s1", `{"id":"s1","prompt":""}`, "# S1")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		require.Len(t, catalog.Skills, 1)
	})

	t.Run("SKILL.md prompt ok", func(t *testing.T) {
		base := t.TempDir()
		makeSkillLeaf(t, base, "s2", `{"id":"s2","prompt":"SKILL.md"}`, "# S2")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		require.Len(t, catalog.Skills, 1)
	})

	t.Run("other prompt rejected", func(t *testing.T) {
		base := t.TempDir()
		makeSkillLeaf(t, base, "s3", `{"id":"s3","prompt":"other.md"}`, "# S3")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		assert.Len(t, catalog.Skills, 0)
		// Should have a diagnostic
		found := false
		for _, d := range catalog.Diagnostics {
			if d.RelPath == "s3" {
				found = true
				break
			}
		}
		assert.True(t, found, "expected diagnostic for unsupported prompt path")
	})

	t.Run("absolute path rejected", func(t *testing.T) {
		base := t.TempDir()
		makeSkillLeaf(t, base, "s4", `{"id":"s4","prompt":"/etc/passwd"}`, "# S4")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		assert.Len(t, catalog.Skills, 0)
	})

	t.Run("dotdot rejected", func(t *testing.T) {
		base := t.TempDir()
		makeSkillLeaf(t, base, "s5", `{"id":"s5","prompt":"../x.md"}`, "# S5")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		assert.Len(t, catalog.Skills, 0)
	})
}

func TestCatalog_CategoryYAMLParsing(t *testing.T) {
	t.Run("valid category", func(t *testing.T) {
		base := t.TempDir()
		makeCategory(t, base, "cat", "---\nname: TestCat\ndescription: A test category\n---\n# Test")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		require.Len(t, catalog.Roots, 1)
		assert.Equal(t, "TestCat", catalog.Roots[0].Name)
		assert.Equal(t, "A test category", catalog.Roots[0].Description)
	})

	t.Run("default name from dir", func(t *testing.T) {
		base := t.TempDir()
		makeCategory(t, base, "auto", "---\ndescription: Auto named\n---\n# Auto")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		require.Len(t, catalog.Roots, 1)
		assert.Equal(t, "auto", catalog.Roots[0].Name)
	})

	t.Run("missing description fails", func(t *testing.T) {
		base := t.TempDir()
		makeCategory(t, base, "nodesc", "---\nname: NoDesc\n---\n# No Desc")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		assert.Len(t, catalog.Roots, 0)
	})

	t.Run("description 240 code points ok", func(t *testing.T) {
		base := t.TempDir()
		desc := ""
		for i := 0; i < 240; i++ {
			desc += "a"
		}
		makeCategory(t, base, "max", "---\ndescription: "+desc+"\n---\n# Max")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		require.Len(t, catalog.Roots, 1)
	})

	t.Run("description over 240 fails", func(t *testing.T) {
		base := t.TempDir()
		desc := ""
		for i := 0; i < 241; i++ {
			desc += "a"
		}
		makeCategory(t, base, "over", "---\ndescription: "+desc+"\n---\n# Over")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		assert.Len(t, catalog.Roots, 0)
	})

	t.Run("no frontmatter fails", func(t *testing.T) {
		base := t.TempDir()
		makeCategory(t, base, "nofm", "# Just markdown")
		catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		assert.Len(t, catalog.Roots, 0)
	})
}

func TestCatalog_MergePriority(t *testing.T) {
	baseUser := t.TempDir()
	baseBuiltin := t.TempDir()

	// Same skill in both, user should win
	makeSkillLeaf(t, baseUser, "common", `{"id":"common-skill","prompt":"SKILL.md"}`, "# User Version")
	makeSkillLeaf(t, baseBuiltin, "common", `{"id":"common-skill","prompt":"SKILL.md"}`, "# Builtin Version")

	catalog := BuildCatalog([]SourceRoot{
		{Name: "user", Path: baseUser, Priority: 0},
		{Name: "builtin", Path: baseBuiltin, Priority: 1},
	})
	require.Len(t, catalog.Skills, 1)
	assert.Equal(t, "user", catalog.Skills[0].SourceRoot)
}

func TestCatalog_KindConflict(t *testing.T) {
	baseUser := t.TempDir()
	baseBuiltin := t.TempDir()

	// User has category, builtin has skill at same path
	makeCategory(t, baseUser, "conflict", "---\ndescription: A category\n---\n# Cat")
	makeSkillLeaf(t, baseBuiltin, "conflict", `{"id":"conflict-skill","prompt":"SKILL.md"}`, "# Skill")

	catalog := BuildCatalog([]SourceRoot{
		{Name: "user", Path: baseUser, Priority: 0},
		{Name: "builtin", Path: baseBuiltin, Priority: 1},
	})
	// User wins, should be a category
	require.Len(t, catalog.Roots, 1)
	assert.Equal(t, NodeCategory, catalog.Roots[0].Kind)

	// Should have kind shadowed diagnostic
	found := false
	for _, d := range catalog.Diagnostics {
		if stringsContains(d.Message, "node_kind_shadowed") {
			found = true
		}
	}
	assert.True(t, found, "expected node_kind_shadowed diagnostic")
}

func TestCatalog_DuplicateManifestID(t *testing.T) {
	baseUser := t.TempDir()
	makeSkillLeaf(t, baseUser, "path-a", `{"id":"dup-id","prompt":"SKILL.md"}`, "# Path A")
	makeSkillLeaf(t, baseUser, "path-b", `{"id":"dup-id","prompt":"SKILL.md"}`, "# Path B")

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: baseUser, Priority: 0}})
	// Only one should survive
	assert.LessOrEqual(t, len(catalog.Skills), 1)

	found := false
	for _, d := range catalog.Diagnostics {
		if stringsContains(d.Message, "duplicate_skill_id") {
			found = true
		}
	}
	assert.True(t, found, "expected duplicate_skill_id diagnostic")
}

func TestCatalog_DuplicateSourceRoot(t *testing.T) {
	base := t.TempDir()
	makeSkillLeaf(t, base, "s1", `{"id":"s1","prompt":"SKILL.md"}`, "# S1")

	catalog := BuildCatalog([]SourceRoot{
		{Name: "user-a", Path: base, Priority: 0},
		{Name: "user-b", Path: base, Priority: 1},
	})
	require.Len(t, catalog.Skills, 1)

	found := false
	for _, d := range catalog.Diagnostics {
		if stringsContains(d.Message, "duplicate canonical source root") {
			found = true
		}
	}
	assert.True(t, found, "expected duplicate_source_root diagnostic")
}

func TestCatalog_SamePriorityTieBreak(t *testing.T) {
	baseA := t.TempDir()
	baseB := t.TempDir()
	makeSkillLeaf(t, baseA, "dup", `{"id":"dup-id","prompt":"SKILL.md"}`, "# A")
	makeSkillLeaf(t, baseB, "dup", `{"id":"dup-id","prompt":"SKILL.md"}`, "# B")

	// Same priority, different Name, different canonical Path
	// Sort key: (Priority, Name, Path, RelPath)
	// "root-A" < "root-B" alphabetically → root-A wins
	catalog := BuildCatalog([]SourceRoot{
		{Name: "root-A", Path: baseA, Priority: 0},
		{Name: "root-B", Path: baseB, Priority: 0},
	})
	require.Len(t, catalog.Skills, 1)
	assert.Equal(t, "root-A", catalog.Skills[0].SourceRoot)
}

func TestCatalog_CategoryLeafMutualExclusion(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "both")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	// Both skill.json and category.md
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"id":"both","prompt":"SKILL.md"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("# Both"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "category.md"),
		[]byte("---\ndescription: Cat\n---\n# Cat"), 0o644))

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	assert.Len(t, catalog.Skills, 0)
}

func TestCatalog_StableOutput(t *testing.T) {
	base := t.TempDir()
	makeCategory(t, base, "z", "---\ndescription: Z cat\n---\n# Z")
	makeSkillLeaf(t, base, "a", `{"id":"a-skill","prompt":"SKILL.md"}`, "# A")
	makeCategory(t, base, "m", "---\ndescription: M cat\n---\n# M")

	// Run twice, compare
	cat1 := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	cat2 := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})

	assert.Equal(t, len(cat1.Roots), len(cat2.Roots))
	for i := range cat1.Roots {
		assert.Equal(t, cat1.Roots[i].RelPath, cat2.Roots[i].RelPath)
	}
	assert.Equal(t, len(cat1.Skills), len(cat2.Skills))
	for i := range cat1.Skills {
		assert.Equal(t, cat1.Skills[i].RelPath, cat2.Skills[i].RelPath)
	}
}

func TestCatalog_KnownSkills(t *testing.T) {
	base := t.TempDir()
	makeSkillLeaf(t, base, "s1", `{"id":"s1","prompt":"SKILL.md","version":"1.0"}`, "# S1 text")
	makeSkillLeaf(t, base, "s2", `{"id":"s2","prompt":""}`, "# S2 text")
	makeSkillLeaf(t, base, "s3", `{"id":"s3","version":"2.0","prompt":"SKILL.md","allowedTools":["read","bash"]}`, "# Tooled")

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	require.Len(t, catalog.Skills, 3)

	s1 := catalog.Skills[0]
	assert.Equal(t, "s1", s1.Manifest.ID)
	assert.Equal(t, "1.0", s1.Manifest.Version)
	assert.Equal(t, "s1", s1.RelPath)
	assert.Equal(t, "user", s1.SourceRoot)
	assert.NotEmpty(t, s1.ManifestHash)
	assert.NotEmpty(t, s1.ContentHash)

	s2 := catalog.Skills[1]
	assert.Equal(t, "# S2 text", s2.PromptText)

	s3 := catalog.Skills[2]
	assert.Equal(t, "2.0", s3.Manifest.Version)
	assert.Equal(t, []string{"read", "bash"}, s3.Manifest.AllowedTools)
}

func TestCatalog_EmptySourceRoots(t *testing.T) {
	catalog := BuildCatalog(nil)
	assert.Len(t, catalog.Roots, 0)
	assert.Len(t, catalog.Skills, 0)
}

func TestCatalog_DiscoveryLoadFailure(t *testing.T) {
	base := t.TempDir()
	// Skill json without ID
	dir := filepath.Join(base, "badskill")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"prompt":"SKILL.md"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("# Bad"), 0o644))

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	assert.Len(t, catalog.Skills, 0)
}

func stringsContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestCatalog_DuplicateIDDeterministic(t *testing.T) {
	// Regression: duplicate manifest IDs at different RelPaths must resolve
	// deterministically regardless of map iteration order.
	base := t.TempDir()
	makeSkillLeaf(t, base, "a", `{"id":"dup","prompt":"SKILL.md"}`, "# A")
	makeSkillLeaf(t, base, "b", `{"id":"dup","prompt":"SKILL.md"}`, "# B")

	winners := map[string]int{}
	for i := 0; i < 50; i++ {
		c := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
		require.Len(t, c.Skills, 1)
		winners[c.Skills[0].RelPath]++
	}
	assert.Len(t, winners, 1, "duplicate ID resolution must be deterministic")
}

func TestCatalog_DuplicateIDCrossRootUserWins(t *testing.T) {
	// Regression: with user priority < builtin priority, user must win a
	// duplicate ID even when the two live at different RelPaths.
	baseUser := t.TempDir()
	baseBuiltin := t.TempDir()
	makeSkillLeaf(t, baseUser, "user-skill", `{"id":"dup","prompt":"SKILL.md"}`, "# User")
	makeSkillLeaf(t, baseBuiltin, "builtin-skill", `{"id":"dup","prompt":"SKILL.md"}`, "# Builtin")

	c := BuildCatalog([]SourceRoot{
		{Name: "user", Path: baseUser, Priority: 0},
		{Name: "builtin", Path: baseBuiltin, Priority: 1},
	})
	require.Len(t, c.Skills, 1)
	assert.Equal(t, "user-skill", c.Skills[0].RelPath)
}
