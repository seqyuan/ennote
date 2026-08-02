package skills

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCatalogPrompt_RootsOnly(t *testing.T) {
	catalog := &Catalog{
		Roots: []*CatalogNode{
			{Kind: NodeCategory, RelPath: "cat-a", Name: "Category A", Description: "Desc A"},
			{Kind: NodeSkill, RelPath: "s1", Name: "skill-1", Description: "Skill 1"},
		},
	}

	prompt := BuildCatalogPrompt(catalog, 16*1024)
	assert.Contains(t, prompt, "<available_skills>")
	assert.Contains(t, prompt, "Category A")
	assert.Contains(t, prompt, "skill-1")
	assert.Contains(t, prompt, "/skills/cat-a/category.md")
	assert.Contains(t, prompt, "/skills/s1/SKILL.md")
	// Must not contain actual skill body content (only references)
	assert.NotContains(t, prompt, "# Test Skill", "prompt must not include SKILL.md body")
}

func TestBuildCatalogPrompt_EmptyCatalog(t *testing.T) {
	assert.Empty(t, BuildCatalogPrompt(nil, 1024))
	assert.Empty(t, BuildCatalogPrompt(&Catalog{}, 1024))
}

func TestBuildCatalogPrompt_16KiBLimit(t *testing.T) {
	// Create a catalog with entries that exceed 16 KiB
	var roots []*CatalogNode
	for i := 0; i < 500; i++ {
		roots = append(roots, &CatalogNode{
			Kind:        NodeSkill,
			RelPath:     "skill-" + string(rune('a'+i%26)),
			Name:        "Long skill name " + strings.Repeat("x", 50),
			Description: "Description " + strings.Repeat("y", 100),
		})
	}
	catalog := &Catalog{Roots: roots}

	prompt := BuildCatalogPrompt(catalog, 16*1024)
	assert.Empty(t, prompt, "prompt exceeding 16 KiB must return empty")
}

func TestBuildCatalogPrompt_DescriptionNormalization(t *testing.T) {
	catalog := &Catalog{
		Roots: []*CatalogNode{
			{Kind: NodeSkill, RelPath: "s1", Name: "test", Description: "line1\nline2\rline3"},
		},
	}
	prompt := BuildCatalogPrompt(catalog, 16*1024)
	// Description itself must not contain raw newlines
	assert.Contains(t, prompt, "line1 line2 line3")
	assert.NotContains(t, prompt, "line1\nline2")
}

func TestSanitizePromptDesc(t *testing.T) {
	require.Equal(t, "hello world", sanitizePromptDesc("hello\nworld"))
	require.Equal(t, "hello world", sanitizePromptDesc("hello\rworld"))
}
