package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRepoUsesSessionStoreManager(t *testing.T) {
	projects := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects")}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	manager := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	repo := &store.SessionRepo{Files: manager}

	session, err := repo.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Needle Session"})
	require.NoError(t, err)
	found, err := repo.FindByID(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Equal(t, session.ID, found.ID)
	matches, err := repo.SearchByProject(context.Background(), project.ID, store.SessionStatusActive, "needle")
	require.NoError(t, err)
	require.Len(t, matches, 1)

	updated, err := repo.UpdateTitle(context.Background(), session.ID, "Updated")
	require.NoError(t, err)
	assert.Equal(t, "Updated", updated.Title)
	archived, err := repo.Archive(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionStatusArchived, archived.Status)
	restored, err := repo.Restore(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionStatusActive, restored.Status)
}
