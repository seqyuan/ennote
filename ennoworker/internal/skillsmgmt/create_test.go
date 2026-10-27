package skillsmgmt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateWritesSkillJSONAndSkillMD(t *testing.T) {
	home := t.TempDir()
	// Deliberately do not pre-create the user root: Create must create it.
	userRoot := filepath.Join(home, ".pi", "agent", "skills")
	svc := &Service{UserRoot: userRoot, HomeDir: home}

	result, err := svc.Create(CreateInput{
		ID:           "pdf-report",
		Name:         "PDF Report",
		Description:  "Build a PDF report\nfrom a dataset.",
		Prompt:       "Write a report.\n\nUse the template.",
		AllowedTools: []string{"read", " read ", "", "bash"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	dir := filepath.Join(userRoot, "pdf-report")
	assert.Equal(t, "pdf-report", result.ID)
	assert.Equal(t, dir, result.Path)
	assert.Equal(t, "Build a PDF report from a dataset.", result.Description)
	assert.Equal(t, []string{"read", "bash"}, result.AllowedTools)

	manifestData, err := os.ReadFile(filepath.Join(dir, "skill.json"))
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(manifestData, &manifest))
	assert.Equal(t, "pdf-report", manifest["id"])
	assert.Equal(t, "1", manifest["version"])
	assert.Equal(t, "SKILL.md", manifest["prompt"])
	assert.Equal(t, "Build a PDF report from a dataset.", manifest["description"])
	assert.Equal(t, []any{"read", "bash"}, manifest["allowedTools"])

	skillMD, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	require.NoError(t, err)
	text := string(skillMD)
	assert.True(t, strings.HasPrefix(text, "---\nname: PDF Report\n"), text)
	assert.Contains(t, text, "description: Build a PDF report from a dataset.\n")
	assert.Contains(t, text, "Write a report.\n\nUse the template.\n")
	assert.True(t, strings.HasSuffix(text, "\n"))

	// Directory 0700, files 0600.
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	for _, name := range []string{"skill.json", "SKILL.md"} {
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}

	// The real loader reads back exactly what was authored.
	loaded, err := skills.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, "pdf-report", loaded.Manifest.ID)
	assert.Equal(t, "Build a PDF report from a dataset.", loaded.Manifest.Description)
	assert.Equal(t, []string{"read", "bash"}, loaded.Manifest.AllowedTools)
	assert.Contains(t, loaded.PromptText, "Write a report.")
	assert.Contains(t, loaded.PromptText, "Use the template.")

	// No staging leftovers.
	entries, err := os.ReadDir(userRoot)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "pdf-report", entries[0].Name())
}

func TestCreateDefaultsNameToIDAndOmitsEmptyTools(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), "skills")
	svc := &Service{UserRoot: userRoot}

	result, err := svc.Create(CreateInput{ID: "quick-note", Prompt: "Summarize the note."})
	require.NoError(t, err)
	assert.Empty(t, result.Description)
	assert.NotNil(t, result.AllowedTools)
	assert.Empty(t, result.AllowedTools)

	skillMD, err := os.ReadFile(filepath.Join(result.Path, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(skillMD), "name: quick-note\n")
	assert.NotContains(t, string(skillMD), "description:")

	manifestData, err := os.ReadFile(filepath.Join(result.Path, "skill.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(manifestData), "allowedTools")

	loaded, err := skills.Load(result.Path)
	require.NoError(t, err)
	assert.Equal(t, "quick-note", loaded.Manifest.ID)
}

func TestCreateRejectsInvalidIDs(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), "skills")
	svc := &Service{UserRoot: userRoot}
	longID := "a" + strings.Repeat("b", 64) // 65 chars

	for _, id := range []string{
		"",
		"..",
		"../escape",
		"a/b",
		`a\b`,
		"/etc/passwd",
		"Upper",
		"with space",
		"-leading",
		"_leading",
		"1leading",
		".hidden",
		"weird!chars",
		longID,
	} {
		_, err := svc.Create(CreateInput{ID: id, Prompt: "body"})
		require.Error(t, err, id)
		assert.ErrorIs(t, err, ErrSkillIDInvalid, id)
	}
	// Nothing was created, not even the root.
	_, statErr := os.Stat(userRoot)
	assert.True(t, os.IsNotExist(statErr))
}

func TestCreateRejectsEmptyPrompt(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), "skills")
	svc := &Service{UserRoot: userRoot}

	for _, prompt := range []string{"", "   ", "\n\t \r\n"} {
		_, err := svc.Create(CreateInput{ID: "empty-prompt", Prompt: prompt})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSkillPromptRequired)
	}
	_, statErr := os.Stat(filepath.Join(userRoot, "empty-prompt"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestCreateRejectsDuplicateID(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), "skills")
	svc := &Service{UserRoot: userRoot}

	_, err := svc.Create(CreateInput{ID: "dup-skill", Prompt: "first"})
	require.NoError(t, err)

	_, err = svc.Create(CreateInput{ID: "dup-skill", Prompt: "second"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSkillExists)

	// The original Skill is untouched and no staging directory is left behind.
	loaded, err := skills.Load(filepath.Join(userRoot, "dup-skill"))
	require.NoError(t, err)
	assert.Contains(t, loaded.PromptText, "first")
	entries, err := os.ReadDir(userRoot)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// A symlink planted at the target ID must be treated as an existing skill, not
// followed out of the user root.
func TestCreateRejectsSymlinkedTarget(t *testing.T) {
	home := t.TempDir()
	userRoot := filepath.Join(home, "skills")
	outside := filepath.Join(home, "outside")
	require.NoError(t, os.MkdirAll(userRoot, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(userRoot, "linked-skill")))

	svc := &Service{UserRoot: userRoot}
	_, err := svc.Create(CreateInput{ID: "linked-skill", Prompt: "body"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSkillExists)

	contents, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, contents)
}
