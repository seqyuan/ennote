package prompts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(home, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.json"), []byte(content), 0644))
}

func TestLoadConfigPaths_HappyPath(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"hooks": {}, "prompts": {"paths": ["packs/team", "/abs/path.md"]}}`)

	paths, err := LoadConfigPaths(home)
	require.NoError(t, err)
	require.Len(t, paths, 2)
	// Relative resolved against home.
	assert.Equal(t, filepath.Join(home, "packs", "team"), paths[0])
	// Absolute kept.
	assert.Equal(t, "/abs/path.md", paths[1])
}

func TestLoadConfigPaths_MissingFile(t *testing.T) {
	home := t.TempDir()
	paths, err := LoadConfigPaths(home)
	require.NoError(t, err)
	assert.Nil(t, paths)
}

func TestLoadConfigPaths_NoPrompts(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"hooks": {}}`)
	paths, err := LoadConfigPaths(home)
	require.NoError(t, err)
	assert.Nil(t, paths)
}

func TestLoadConfigPaths_InvalidJSON(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{not json`)
	_, err := LoadConfigPaths(home)
	assert.Error(t, err)
}

func TestLoadConfigPaths_TooManyPaths(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"prompts": {"paths": [`+stringsJoinQuoted(33)+`]}}`)
	_, err := LoadConfigPaths(home)
	assert.Error(t, err)
}

func TestLoadConfigPaths_PathTooLong(t *testing.T) {
	home := t.TempDir()
	longPath := "/" + string(make([]byte, maxSettingsPath+1))
	writeConfig(t, home, `{"prompts": {"paths": ["`+longPath+`"]}}`)
	_, err := LoadConfigPaths(home)
	assert.Error(t, err)
}

func TestLoadConfigPaths_ReadLimit(t *testing.T) {
	home := t.TempDir()
	// Config larger than 1 MiB.
	big := `{"prompts": {"paths": ["/a"]}, "pad": "` + string(make([]byte, maxConfigBytes)) + `"}`
	writeConfig(t, home, big)
	_, err := LoadConfigPaths(home)
	assert.Error(t, err)
}

func stringsJoinQuoted(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += `"/p"`
	}
	return out
}
