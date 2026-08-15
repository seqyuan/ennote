package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSkillRootRepoUsesSettingsFile(t *testing.T) {
	settings := &fileconfig.SettingsStore{Path: filepath.Join(t.TempDir(), "config", "settings.json")}
	repo := &store.SkillRootRepo{Settings: settings}
	rootPath := filepath.Join(t.TempDir(), "team-skills")
	root, err := repo.Create(context.Background(), store.CreateSkillRootInput{
		Name: "Team", Path: rootPath, AgentKind: "generic", Enabled: true,
	})
	require.NoError(t, err)
	assert.True(t, root.Enabled)

	roots, err := repo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, roots, 1)
	assert.Equal(t, rootPath, roots[0].Path)
	document, err := settings.Read()
	require.NoError(t, err)
	assert.Equal(t, []string{rootPath}, document.SkillRoots)

	disabled := false
	updated, err := repo.Update(context.Background(), roots[0].ID, struct {
		Name      *string
		Path      *string
		AgentKind *string
		Priority  *int
		Enabled   *bool
	}{Enabled: &disabled})
	require.NoError(t, err)
	assert.False(t, updated.Enabled)
	roots, err = repo.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, roots)
}
