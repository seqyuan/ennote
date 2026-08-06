package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSkillRootRepo(t *testing.T) (*SkillRootRepo, func()) {
	t.Helper()
	db, err := OpenMemory()
	require.NoError(t, err)
	require.NoError(t, Migrate(db))
	return &SkillRootRepo{DB: db}, func() { _ = db.Close() }
}

func TestSkillRootCRUD(t *testing.T) {
	repo, close := newSkillRootRepo(t)
	defer close()
	ctx := context.Background()

	root, err := repo.Create(ctx, CreateSkillRootInput{Name: "pi", Path: "/home/u/.pi/agent/skills", AgentKind: "pi", Priority: 10, Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, "/home/u/.pi/agent/skills", root.Path)
	assert.Equal(t, "pi", root.AgentKind)
	assert.Equal(t, 10, root.Priority)

	// Default priority normalization.
	root2, err := repo.Create(ctx, CreateSkillRootInput{Name: "claude", Path: "/home/u/.claude/skills", AgentKind: "claude"})
	require.NoError(t, err)
	assert.Equal(t, 10, root2.Priority)

	// Duplicate path rejected.
	_, err = repo.Create(ctx, CreateSkillRootInput{Name: "dup", Path: "/home/u/.pi/agent/skills", AgentKind: "generic"})
	assert.ErrorContains(t, err, "already exists")

	// List ordered by priority then created.
	roots, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, roots, 2)

	// Update toggle.
	disabled := false
	updated, err := repo.Update(ctx, root.ID, struct {
		Name      *string
		Path      *string
		AgentKind *string
		Priority  *int
		Enabled   *bool
	}{Enabled: &disabled})
	require.NoError(t, err)
	assert.False(t, updated.Enabled)

	enabled := true
	priority := 25
	_, err = repo.Update(ctx, root.ID, struct {
		Name      *string
		Path      *string
		AgentKind *string
		Priority  *int
		Enabled   *bool
	}{Enabled: &enabled, Priority: &priority})
	require.NoError(t, err)

	// EnabledPaths only returns enabled roots, sorted by priority.
	enabledRoots, err := repo.EnabledPaths(ctx)
	require.NoError(t, err)
	require.Len(t, enabledRoots, 1)
	assert.Equal(t, 25, enabledRoots[0].Priority)

	// Delete + not-found.
	require.NoError(t, repo.Delete(ctx, root.ID))
	_, err = repo.Get(ctx, root.ID)
	assert.ErrorIs(t, err, ErrSkillRootNotFound)
	assert.ErrorIs(t, repo.Delete(ctx, root.ID), ErrSkillRootNotFound)
}
