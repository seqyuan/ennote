package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogScale_500Skills25Categories(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scale test in short mode")
	}
	base := t.TempDir()

	// Create 25 top-level categories
	for i := 0; i < 25; i++ {
		catName := "category-" + string(rune('a'+i))
		catDir := filepath.Join(base, catName)
		require.NoError(t, os.MkdirAll(catDir, 0o755))
		desc := "Category " + catName + " description for testing"
		require.NoError(t, os.WriteFile(filepath.Join(catDir, "category.md"),
			[]byte("---\ndescription: "+desc+"\n---\n# "+catName+"\n\nRouting information."), 0o644))

		// 20 skills per category
		for j := 0; j < 20; j++ {
			skillName := "skill-" + string(rune('a'+j))
			skillDir := filepath.Join(catDir, skillName)
			require.NoError(t, os.MkdirAll(skillDir, 0o755))
			id := catName + "/" + skillName
			require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.json"),
				[]byte(`{"id":"`+id+`","prompt":"SKILL.md"}`), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
				[]byte("# "+skillName+"\n\nThis is skill "+id), 0o644))
		}
	}

	// Build catalog
	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	t.Logf("Catalog: %d roots, %d skills, %d diagnostics",
		len(catalog.Roots), len(catalog.Skills), len(catalog.Diagnostics))

	// Verify root count (should be 25)
	assert.Len(t, catalog.Roots, 25)
	assert.Len(t, catalog.Skills, 500)

	// Build catalog prompt
	prompt := BuildCatalogPrompt(catalog, 16*1024)
	require.NotEmpty(t, prompt, "catalog prompt must not be empty for valid catalog")
	assert.LessOrEqual(t, len(prompt), 16*1024, "catalog prompt must be within 16 KiB")

	// Verify no leaf SKILL.md body in prompt
	assert.NotContains(t, prompt, "This is skill", "prompt must not contain SKILL.md body text")

	// Materialize and verify
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}
	plan, err := PlanMaterialization(catalog, vars)
	require.NoError(t, err)

	runDir := t.TempDir()
	result, err := MaterializeCatalog(runDir, plan, catalog)
	require.NoError(t, err)

	// Verify catalog
	err = VerifyMaterializedCatalog(result.Root, plan.CatalogDigest)
	assert.NoError(t, err)

	// Verify records
	assert.Len(t, result.Records, 500)
}

func TestCatalogScale_500SkillsSingleCategoryExceedsLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scale test in short mode")
	}
	base := t.TempDir()

	// Create 1 category with 800 skills → should exceed 16 KiB
	makeCategory(t, base, "big", "---\ndescription: Big category\n---\n# Big\n\nToo many skills here.")
	for i := 0; i < 800; i++ {
		id := "s" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+(i/676)%10))
		skillDir := filepath.Join(base, "big", id)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.json"),
			[]byte(`{"id":"`+id+`","prompt":"SKILL.md"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
			[]byte("# "+id), 0o644))
	}

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	_, err := PlanMaterialization(catalog, vars)
	assert.Error(t, err, "should fail because category.md exceeds 16 KiB")
	assert.Contains(t, err.Error(), "16 KiB")
}

func BenchmarkBuildCatalog(b *testing.B) {
	base := b.TempDir()
	// Create 100 skills across 5 categories
	for i := 0; i < 5; i++ {
		catName := "cat-" + string(rune('a'+i))
		catDir := filepath.Join(base, catName)
		os.MkdirAll(catDir, 0o755)
		os.WriteFile(filepath.Join(catDir, "category.md"),
			[]byte("---\ndescription: Cat "+catName+"\n---\n# "+catName+"\n\nRouting."), 0o644)
		for j := 0; j < 20; j++ {
			skillName := "skill-" + string(rune('a'+j))
			skillDir := filepath.Join(catDir, skillName)
			os.MkdirAll(skillDir, 0o755)
			os.WriteFile(filepath.Join(skillDir, "skill.json"),
				[]byte(`{"id":"`+catName+`/`+skillName+`","prompt":"SKILL.md"}`), 0o644)
			os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
				[]byte("# Skill\n\nContent for benchmark"), 0o644)
		}
	}

	sources := []SourceRoot{{Name: "user", Path: base, Priority: 0}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildCatalog(sources)
	}
}

func BenchmarkMaterializeCatalog(b *testing.B) {
	base := b.TempDir()
	for i := 0; i < 3; i++ {
		catName := "cat-" + string(rune('a'+i))
		catDir := filepath.Join(base, catName)
		os.MkdirAll(catDir, 0o755)
		os.WriteFile(filepath.Join(catDir, "category.md"),
			[]byte("---\ndescription: Cat "+catName+"\n---\n# "+catName+"\n\nRouting."), 0o644)
		for j := 0; j < 10; j++ {
			skillDir := filepath.Join(catDir, "skill-"+string(rune('a'+j)))
			os.MkdirAll(skillDir, 0o755)
			os.WriteFile(filepath.Join(skillDir, "skill.json"),
				[]byte(`{"id":"`+"skill-"+string(rune('a'+j))+`","prompt":"SKILL.md"}`), 0o644)
			os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
				[]byte("# Skill\n\nContent"), 0o644)
		}
	}

	catalog := BuildCatalog([]SourceRoot{{Name: "user", Path: base, Priority: 0}})
	vars := TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		plan, _ := PlanMaterialization(catalog, vars)
		runDir := b.TempDir()
		_, _ = MaterializeCatalog(runDir, plan, catalog)
	}
}
