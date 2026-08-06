package skillsmgmt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSkill(t *testing.T, dir, skillJSON, skillMD string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if skillJSON != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"), []byte(skillJSON), 0o644))
	}
	if skillMD != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o644))
	}
	return dir
}

func TestListMergesUserAndBuiltinAndAnnotates(t *testing.T) {
	home := t.TempDir()
	userRoot := filepath.Join(home, ".pi", "agent", "skills")
	builtinRoot := filepath.Join(home, "builtin")

	// User root: one pi-style SKILL.md-only skill + one skill.json skill.
	writeSkill(t, filepath.Join(userRoot, "brave-search"), "",
		"---\nname: brave-search\ndescription: Search the web.\n---\n\nbody\n")
	writeSkill(t, filepath.Join(userRoot, "r-tool"), `{"id":"r-tool","version":"0.2.0","prompt":"SKILL.md"}`, "# R tool\n\nuse it")

	// Builtin root: lower priority, must be overridden only by ID conflicts.
	writeSkill(t, filepath.Join(builtinRoot, "builtin-thing"), `{"id":"builtin-thing"}`, "# builtin\n")

	// Global lock: brave-search installed from a GitHub repo with a folder hash.
	globalLock := filepath.Join(home, ".agents", ".skill-lock.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(globalLock), 0o755))
	require.NoError(t, os.WriteFile(globalLock, []byte(`{
	  "version": 3,
	  "skills": {
	    "brave-search": {
	      "source": "badlogic/pi-skills",
	      "sourceType": "github",
	      "sourceUrl": "https://github.com/badlogic/pi-skills.git",
	      "skillPath": "brave-search/SKILL.md",
	      "skillFolderHash": "079a8cb038b9166f6570a1db1f8b576efbb30b24"
	    }
	  }
	}`), 0o644))

	svc := &Service{UserRoot: userRoot, BuiltinRoot: builtinRoot, HomeDir: home}
	result, err := svc.List("")
	require.NoError(t, err)
	require.Len(t, result.Skills, 3)

	var brave *AnnotatedSkill
	for i := range result.Skills {
		if result.Skills[i].SkillID == "brave-search" {
			brave = &result.Skills[i]
		}
	}
	require.NotNil(t, brave)
	require.NotNil(t, brave.Install)
	assert.Equal(t, "global", brave.Install.Scope)
	assert.Equal(t, "badlogic/pi-skills", brave.Install.Source)
	assert.Equal(t, "badlogic/pi-skills@brave-search", brave.Install.Package)
	assert.True(t, brave.Install.CanCheckForUpdates)
	assert.Equal(t, "https://skills.sh/badlogic/pi-skills/brave-search", brave.Install.SkillsShURL)
	assert.Equal(t, "079a8cb038b9166f6570a1db1f8b576efbb30b24", brave.Install.VersionHash)
}

func TestListProjectLockAnnotation(t *testing.T) {
	home := t.TempDir()
	userRoot := filepath.Join(home, ".pi", "agent", "skills")
	projectRoot := filepath.Join(home, "proj", ".pi", "skills")
	workspace := filepath.Join(home, "proj")

	writeSkill(t, filepath.Join(projectRoot, "local-skill"), "",
		"---\nname: local-skill\n---\n\nbody\n")
	projectLock := filepath.Join(workspace, "skills-lock.json")
	require.NoError(t, os.WriteFile(projectLock, []byte(`{
	  "skills": {
	    "local-skill": {
	      "source": "acme/skills",
	      "sourceType": "github",
	      "skillPath": "local-skill/SKILL.md",
	      "computedHash": "abc123"
	    }
	  }
	}`), 0o644))

	svc := &Service{UserRoot: userRoot, HomeDir: home}
	result, err := svc.List(workspace)
	require.NoError(t, err)
	require.Len(t, result.Skills, 1)
	install := result.Skills[0].Install
	require.NotNil(t, install)
	assert.Equal(t, "project", install.Scope)
	assert.Equal(t, "abc123", install.VersionHash)
	// Project installs with a ref are not checkable; no ref means checkable.
	assert.True(t, install.CanCheckForUpdates)
}

func TestListSkipsNonSkillDirsAndReportsDiagnostics(t *testing.T) {
	home := t.TempDir()
	userRoot := filepath.Join(home, ".pi", "agent", "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(userRoot, "random"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userRoot, "random", "notes.txt"), []byte("hi"), 0o644))

	svc := &Service{UserRoot: userRoot, HomeDir: home}
	result, err := svc.List("")
	require.NoError(t, err)
	assert.Empty(t, result.Skills)
}

func TestToggleDisableModelInvocation(t *testing.T) {
	home := t.TempDir()
	userRoot := filepath.Join(home, "skills")
	dir := writeSkill(t, filepath.Join(userRoot, "web"), "",
		"---\nname: web\nallowed-tools: Bash(curl)\n---\n\n# Web\n\nbody\n")

	svc := &Service{UserRoot: userRoot, HomeDir: home}

	disabled, err := svc.ToggleDisabled(dir, true)
	require.NoError(t, err)
	assert.True(t, disabled)

	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "disable-model-invocation: true")
	assert.Contains(t, text, "allowed-tools: Bash(curl)")
	assert.NotContains(t, text, "disable-model-invocation: true\n---\n")

	// Re-toggle off removes only that key, preserving the rest.
	disabled, err = svc.ToggleDisabled(dir, false)
	require.NoError(t, err)
	assert.False(t, disabled)
	data, err = os.ReadFile(filepath.Join(dir, "SKILL.md"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "disable-model-invocation")
	assert.Contains(t, string(data), "allowed-tools: Bash(curl)")
	assert.Contains(t, string(data), "# Web")

	// No frontmatter: toggle wraps the doc in a new frontmatter block.
	plain := writeSkill(t, filepath.Join(userRoot, "plain"), "", "# Plain\n\nbody\n")
	disabled, err = svc.ToggleDisabled(plain, true)
	require.NoError(t, err)
	assert.True(t, disabled)
	data, err = os.ReadFile(filepath.Join(plain, "SKILL.md"))
	require.NoError(t, err)
	assert.True(t, frontmatterKeyTrue(string(data), "disable-model-invocation"))
	assert.Contains(t, string(data), "# Plain")
}

func TestLockHelpers(t *testing.T) {
	entries := map[string]skillLockEntry{
		"PDF": {Source: "openai/skills", SourceType: "github"},
	}
	// Case-insensitive lookup.
	entry, ok := findLockEntry(entries, "pdf")
	assert.True(t, ok)
	assert.Equal(t, "openai/skills", entry.Source)

	assert.Equal(t, "badlogic/pi-skills", normalizeSource("https://github.com/badlogic/pi-skills.git", "github"))
	assert.Equal(t, "tavily-ai/skills", normalizeSource("tavily-ai/skills", "github"))
	assert.Equal(t, "", buildSkillsShURL("", "x"))
	assert.Equal(t, "https://skills.sh/org/repo/skill%20name", buildSkillsShURL("org/repo", "skill name"))
	assert.True(t, isWithin("/a/b/skills/x", "/a/b/skills"))
	assert.False(t, isWithin("/a/b/other/x", "/a/b/skills"))
}

func TestAnnotatedSkillJSONShape(t *testing.T) {
	home := t.TempDir()
	userRoot := filepath.Join(home, "skills")
	writeSkill(t, filepath.Join(userRoot, "web"), `{"id":"web"}`, "---\nname: web\n---\n\nbody\n")
	svc := &Service{UserRoot: userRoot, HomeDir: home}
	result, err := svc.List("")
	require.NoError(t, err)
	data, err := json.Marshal(result)
	require.NoError(t, err)
	// Frontend-visible keys survive serialization.
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	skills, ok := decoded["skills"].([]any)
	require.True(t, ok)
	require.Len(t, skills, 1)
	first, ok := skills[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "web", first["skillId"])
	assert.Equal(t, "web", first["name"])
}
