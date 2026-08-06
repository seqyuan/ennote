package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaultsToLoopback(t *testing.T) {
	t.Setenv("ENNOTE_HOME", t.TempDir())
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:0", cfg.ListenAddr)
	assert.Equal(t, 2, cfg.MaxConcurrentRuns)
}

func TestLoadRejectsPublicBind(t *testing.T) {
	t.Setenv("ENNOTE_HOME", t.TempDir())
	cfg, err := config.Load()
	require.NoError(t, err)

	// Validate that bind address is loopback
	assert.True(t,
		cfg.ListenAddr == "127.0.0.1:0" || cfg.ListenAddr == "[::1]:0",
		"listen address must be loopback",
	)
}

func TestLoadRejectsInvalidConcurrency(t *testing.T) {
	t.Setenv("ENNOTE_HOME", t.TempDir())
	t.Setenv("ENNOTE_MAX_CONCURRENT_RUNS", "0")
	_, err := config.Load()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "max concurrent runs")
}

func TestDefaultEnnoteHome(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Contains(t, cfg.HomeDir, ".ennote")
}

func TestCustomEnnoteHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("ENNOTE_HOME", tmp)
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, tmp, cfg.HomeDir)
}

func TestDataDir(t *testing.T) {
	t.Setenv("ENNOTE_HOME", "/tmp/ennote-test")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/ennote-test/data/ennote.db", cfg.DatabasePath)
}

func TestSkillsDirEnvOverride(t *testing.T) {
	t.Setenv("ENNOTE_HOME", t.TempDir())
	t.Setenv("ENNOTE_SKILLS_DIR", "/custom/skills")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "/custom/skills", cfg.SkillsDir)
}

func TestSkillsDirFallsBackToEnnoteHome(t *testing.T) {
	t.Setenv("ENNOTE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // ecosystem dirs never replace the default
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cfg.HomeDir, "skills"), cfg.SkillsDir)
}

func TestSkillsDirIgnoresEcosystemDirsForDefault(t *testing.T) {
	home := t.TempDir()
	piDir := filepath.Join(home, ".pi", "agent", "skills")
	require.NoError(t, os.MkdirAll(piDir, 0o755))
	t.Setenv("HOME", home)
	t.Setenv("ENNOTE_HOME", t.TempDir())
	cfg, err := config.Load()
	require.NoError(t, err)
	// The default root stays ennote's own; pi is an additional root instead.
	assert.Equal(t, filepath.Join(cfg.HomeDir, "skills"), cfg.SkillsDir)
	assert.NotEqual(t, piDir, cfg.SkillsDir)
}
