package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTemplate builds a parsed template with the given tier.
func makeTemplate(name, body string, tier Tier) Template {
	t := Template{
		Name: name, Description: name, Body: body,
		Tier: tier, Source: tier.String(),
	}
	return t
}

func TestRegistry_FourTierPriority(t *testing.T) {
	reg := NewRegistry()
	reg.Add(makeTemplate("x", "builtin", TierBuiltin))
	reg.Add(makeTemplate("x", "settings", TierSettings))
	reg.Add(makeTemplate("x", "global", TierGlobal))
	reg.Add(makeTemplate("x", "project", TierProject))

	list := reg.List()
	require.Len(t, list, 1)
	assert.Equal(t, "x", list[0].Name)
	assert.Equal(t, "project", list[0].Source)

	// Project template body wins.
	res, err := reg.ExpandParsedInvocation("x", "")
	require.NoError(t, err)
	assert.Equal(t, "project", res.Text)
}

func TestRegistry_LowerTierShadowed(t *testing.T) {
	reg := NewRegistry()
	reg.Add(makeTemplate("y", "builtin", TierBuiltin))
	reg.Add(makeTemplate("y", "global", TierGlobal))

	diags := reg.Diagnostics()
	assert.True(t, containsCode(diags, "shadowed_template"), "expected shadowed_template diagnostic")
}

func TestRegistry_SameTierLastWins(t *testing.T) {
	reg := NewRegistry()
	reg.Add(makeTemplate("z", "first", TierGlobal))
	reg.Add(makeTemplate("z", "second", TierGlobal))

	res, err := reg.ExpandParsedInvocation("z", "")
	require.NoError(t, err)
	assert.Equal(t, "second", res.Text)
	// Same-tier override is not a shadow.
	assert.False(t, containsCode(reg.Diagnostics(), "shadowed_template"))
}

func TestRegistry_DeterministicSort(t *testing.T) {
	reg := NewRegistry()
	reg.Add(makeTemplate("banana", "b", TierGlobal))
	reg.Add(makeTemplate("apple", "a", TierGlobal))
	reg.Add(makeTemplate("cherry", "c", TierGlobal))

	list := reg.List()
	assert.Equal(t, []string{"apple", "banana", "cherry"}, []string{list[0].Name, list[1].Name, list[2].Name})
}

func TestRegistry_ExpandNotFound(t *testing.T) {
	reg := NewRegistry()
	res, err := reg.ExpandParsedInvocation("nope", "")
	require.NoError(t, err)
	assert.Equal(t, ExpandCaseNotFound, res.Case)
	assert.Equal(t, "nope", res.Name)
	assert.Empty(t, res.Text)
}

func TestRegistry_ExpandArgsFallback(t *testing.T) {
	reg := NewRegistry()
	reg.Add(makeTemplate("cmd", "$1", TierGlobal))

	// Unterminated quote → arguments_fallback, single raw arg.
	res, err := reg.ExpandParsedInvocation("cmd", `hello "unterminated`)
	require.NoError(t, err)
	assert.Equal(t, ExpandCaseMatched, res.Case)
	assert.Equal(t, `hello "unterminated`, res.Text)
	assert.True(t, containsCode(res.Diagnostics, "arguments_fallback"))
}

func containsCode(diags []Diagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}

// ——— integration: Resolve with real dirs ———

func setupTierDirs(t *testing.T, layout map[string]map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for dir, files := range layout {
		for name, body := range files {
			path := filepath.Join(root, dir, name)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
			require.NoError(t, os.WriteFile(path, []byte(body), 0644))
		}
	}
	return root
}

func TestResolve_FourTierWithDirs(t *testing.T) {
	root := setupTierDirs(t, map[string]map[string]string{
		"project/.ennote/prompts": {"p.md": "---\nname: shared\n---\nproject-body"},
		"settings":                {"s.md": "---\nname: shared\n---\nsettings-body"},
	})

	ctx := ResolveContext{
		CanonicalRoot: filepath.Join(root, "project"),
		Trusted:       true,
		SettingsPaths: []string{filepath.Join(root, "settings")},
	}
	builtins := []Template{makeTemplate("shared", "builtin-body", TierBuiltin)}

	reg, err := Resolve(ctx, builtins, nil, NewResolveBudget())
	require.NoError(t, err)

	// project > settings > builtin.
	res, err := reg.ExpandParsedInvocation("shared", "")
	require.NoError(t, err)
	assert.Equal(t, "project-body", res.Text)
}

func TestResolve_UntrustedSkipsProject(t *testing.T) {
	root := setupTierDirs(t, map[string]map[string]string{
		"project/.ennote/prompts": {"only.md": "---\nname: onlyp\n---\nbody"},
	})

	ctx := ResolveContext{
		CanonicalRoot: filepath.Join(root, "project"),
		Trusted:       false,
	}
	reg, err := Resolve(ctx, nil, nil, NewResolveBudget())
	require.NoError(t, err)
	assert.Len(t, reg.List(), 0)
	assert.True(t, containsCode(reg.Diagnostics(), "project_templates_untrusted"))
}

func TestResolve_ProjectSymlinkedPromptsSkipped(t *testing.T) {
	// .ennote/prompts is a symlink pointing OUTSIDE the workspace.
	root := t.TempDir()
	escapeDir := filepath.Join(root, "escape")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "project", ".ennote"), 0755))
	require.NoError(t, os.MkdirAll(escapeDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(escapeDir, "evil.md"),
		[]byte("---\nname: evil\n---\nevil-body"), 0644))
	require.NoError(t, os.Symlink(escapeDir, filepath.Join(root, "project", ".ennote", "prompts")))

	ctx := ResolveContext{
		CanonicalRoot: filepath.Join(root, "project"),
		Trusted:       true,
	}
	reg, err := Resolve(ctx, nil, nil, NewResolveBudget())
	require.NoError(t, err)
	// The symlinked prompts dir must NOT be followed.
	assert.Len(t, reg.List(), 0, "symlinked prompts directory must not be read")
}

func TestResolve_BudgetHardLimitReturnsError(t *testing.T) {
	root := setupTierDirs(t, map[string]map[string]string{
		"project/.ennote/prompts": {"a.md": "---\nname: a\n---\n" + string(make([]byte, 100))},
		"settings":                {"b.md": "---\nname: b\n---\nbody"},
	})

	ctx := ResolveContext{
		CanonicalRoot: filepath.Join(root, "project"),
		Trusted:       true,
		SettingsPaths: []string{filepath.Join(root, "settings")},
	}

	// Zero file budget → hard exhaustion must surface as an error.
	budget := NewResolveBudget()
	budget.TemplateFilesRemaining = 0
	_, err := Resolve(ctx, nil, nil, budget)
	assert.ErrorIs(t, err, ErrPromptResourceLimit)
}

func TestResolve_Over2000EntriesSkipsSource(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project", ".ennote", "prompts")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep.md"), []byte("---\nname: keep\n---\nbody"), 0644))
	// 2001 non-md files.
	for i := 0; i < 2001; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("junk-%04d.txt", i)), []byte("x"), 0644))
	}

	ctx := ResolveContext{
		CanonicalRoot: filepath.Join(root, "project"),
		Trusted:       true,
	}
	reg, err := Resolve(ctx, nil, nil, NewResolveBudget())
	require.NoError(t, err)
	assert.Len(t, reg.List(), 0, "over-2000-entry source must be skipped")
	assert.True(t, containsCode(reg.Diagnostics(), "source_skipped"))
}
