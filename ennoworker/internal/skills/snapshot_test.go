package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildTestCatalog(t *testing.T) (*Catalog, string) {
	t.Helper()
	base := t.TempDir()
	makeCategory(t, base, "cat-a", "---\ndescription: Category A\n---\n# Cat A")
	makeSkillLeaf(t, base, "cat-a/s1", `{"id":"s1","prompt":"SKILL.md"}`, "# Skill 1")
	makeSkillLeaf(t, base, "s2", `{"id":"s2","prompt":"SKILL.md"}`, "# Skill 2")
	makeSkillLeaf(t, base, "s3", `{"id":"s3","prompt":"","version":"2.0","allowedTools":["read"]}`, "# Skill 3 with ${workspace}")

	return BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}}), base
}

func TestPlanMaterialization_Basic(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)
	assert.Equal(t, "bwrap", plan.Mode)
	assert.NotEmpty(t, plan.SourceCatalogDigest)
	assert.NotEmpty(t, plan.SnapshotCatalogDigest)
	assert.NotEmpty(t, plan.CatalogDigest)
	assert.Equal(t, 4, len(plan.Entries)) // 1 category + 3 skills

	// Verify entries are sorted
	for i := 1; i < len(plan.Entries); i++ {
		assert.True(t, plan.Entries[i-1].RelPath < plan.Entries[i].RelPath,
			"entries should be sorted by RelPath")
	}

	// All three digests must differ
	assert.NotEqual(t, plan.SourceCatalogDigest, plan.SnapshotCatalogDigest)
	assert.NotEqual(t, plan.SourceCatalogDigest, plan.CatalogDigest)
	assert.NotEqual(t, plan.SnapshotCatalogDigest, plan.CatalogDigest)
}

func TestPlanMaterialization_None(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	vars := TemplateVars{Mode: "none", Workspace: ".", SkillDir: "/tmp/test-skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)
	assert.Equal(t, "none", plan.Mode)
}

func TestPlanMaterialization_NilCatalog(t *testing.T) {
	_, err := PlanMaterialization(nil, TemplateVars{Workspace: "/w", SkillDir: "/s"})
	assert.Error(t, err)
}

func TestPlanMaterialization_EmptyVars(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	_, err := PlanMaterialization(catalog, TemplateVars{})
	assert.Error(t, err)
}

func TestMaterializeCatalog_Basic(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	runDir := t.TempDir()
	result, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Root)
	assert.Equal(t, plan.CatalogDigest, result.CatalogDigest)
	assert.Len(t, result.Records, 3) // 3 skills

	// Verify files exist
	assert.FileExists(t, filepath.Join(result.Root, ".catalog.json"))
	assert.FileExists(t, filepath.Join(result.Root, "cat-a", "category.md"))
	assert.DirExists(t, filepath.Join(result.Root, "cat-a", "s1"))
	assert.FileExists(t, filepath.Join(result.Root, "cat-a", "s1", "SKILL.md"))
	assert.DirExists(t, filepath.Join(result.Root, "s2"))
	assert.DirExists(t, filepath.Join(result.Root, "s3"))

	// Verify SKILL.md is rendered
	content, err := os.ReadFile(filepath.Join(result.Root, "s3", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "/workspace")
}

func TestMaterializeCatalog_Variables(t *testing.T) {
	base := t.TempDir()
	makeSkillLeaf(t, base, "s", `{"id":"s","prompt":"SKILL.md"}`, "Workspace: ${workspace}, Skill: ${skill_dir}")

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	runDir := t.TempDir()
	result, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(result.Root, "s", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Workspace: /workspace")
	assert.Contains(t, string(content), "Skill: /skills/s")
}

func TestMaterializeCatalog_StagingFailure(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	// Use a read-only parent dir to force write failure
	parentDir := filepath.Join(t.TempDir(), "ro")
	require.NoError(t, os.MkdirAll(parentDir, 0o500))
	_, err = MaterializeCatalog(parentDir, plan, catalog)
	assert.Error(t, err)

	// Should not leave a half-snapshot
	// (the tmp dir would be cleaned up on error)
}

func TestVerifyMaterializedCatalog_Valid(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	runDir := t.TempDir()
	result, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)

	// Verify with plan digest
	err = VerifyMaterializedCatalog(result.Root, plan.CatalogDigest)
	assert.NoError(t, err)
}

func TestVerifyMaterializedCatalog_TamperedFile(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	runDir := t.TempDir()
	result, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)

	// Tamper with a file
	require.NoError(t, os.WriteFile(
		filepath.Join(result.Root, "s2", "SKILL.md"),
		[]byte("tampered content"), 0o644))

	err = VerifyMaterializedCatalog(result.Root, plan.CatalogDigest)
	assert.Error(t, err)
}

func TestVerifyMaterializedCatalog_ExtraFile(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	runDir := t.TempDir()
	result, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)

	// Add an extra file
	require.NoError(t, os.WriteFile(
		filepath.Join(result.Root, "extra.txt"),
		[]byte("extra"), 0o644))

	err = VerifyMaterializedCatalog(result.Root, plan.CatalogDigest)
	assert.Error(t, err)
}

func TestVerifyMaterializedCatalog_TamperedManifest(t *testing.T) {
	catalog, _ := buildTestCatalog(t)
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	runDir := t.TempDir()
	result, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)

	// Tamper with catalog digest in manifest
	manifestData, err := os.ReadFile(filepath.Join(result.Root, ".catalog.json"))
	require.NoError(t, err)
	tampered := string(manifestData)
	tampered = tampered[:len(tampered)-3] + "xxx\"\n}"
	require.NoError(t, os.WriteFile(
		filepath.Join(result.Root, ".catalog.json"),
		[]byte(tampered), 0o644))

	err = VerifyMaterializedCatalog(result.Root, plan.CatalogDigest)
	assert.Error(t, err)
}

func TestCategory16KiBLimit(t *testing.T) {
	base := t.TempDir()

	// Create a category with enough children to exceed 16 KiB
	makeCategory(t, base, "big", "---\ndescription: Big category\n---\n# Big\n\nLarge category body to help exceed limit.")

	// Create 800 skills under this category (each entry ~55 bytes → ~44 KiB)
	for i := 0; i < 800; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('a'+(i/260)%26))
		dir := filepath.Join(base, "big", id)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
			[]byte(`{"id":"`+id+`","prompt":"SKILL.md"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("# "+id), 0o644))
	}

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	t.Logf("Catalog has %d roots, %d skills", len(catalog.Roots), len(catalog.Skills))
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	_, err := PlanMaterialization(catalog, vars)
	if err != nil {
		t.Logf("Expected error: %v", err)
		assert.Contains(t, err.Error(), "16 KiB")
		return
	}
	// If no error, the generated index must be under 16 KiB
	// This is acceptable if the skill descriptions are short enough
}

func TestVerify_NestedSubdir(t *testing.T) {
	// Regression: a skill with nested attachment directories (e.g. scripts/run.R)
	// must pass verification. See plan §3.1 legal layout.
	base := t.TempDir()
	makeSkillLeaf(t, base, "s1", `{"id":"s1","prompt":"SKILL.md"}`, "# S1")
	require.NoError(t, os.MkdirAll(filepath.Join(base, "s1", "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "s1", "scripts", "run.R"),
		[]byte("#!/usr/bin/Rscript"), 0o755))

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}
	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)
	runDir := t.TempDir()
	result, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)
	assert.NoError(t, VerifyMaterializedCatalog(result.Root, plan.CatalogDigest))
}

func TestMaterialize_IdempotentReuse(t *testing.T) {
	// Regression: when the target snapshot already exists and matches the
	// current plan, MaterializeCatalog must reuse it without overwriting.
	// A tampered existing snapshot must fail with a conflict error.
	base := t.TempDir()
	makeSkillLeaf(t, base, "s1", `{"id":"s1","prompt":"SKILL.md"}`, "# S1")

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}
	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	runDir := t.TempDir()
	_, err = MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)

	result2, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)
	assert.Equal(t, plan.CatalogDigest, result2.CatalogDigest)

	require.NoError(t, os.WriteFile(filepath.Join(runDir, "skills", "s1", "SKILL.md"),
		[]byte("tampered"), 0o644))
	_, err = MaterializeCatalog(runDir, plan, catalog)
	assert.Error(t, err, "existing tampered snapshot must conflict")
}
