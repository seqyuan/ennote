package store_test

import (
	"context"
	"testing"

	"encoding/json"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	stores "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
)

func TestCreateProjectWithWorkspace(t *testing.T) {
	repo := newFileProjects(t)
	ctx := context.Background()

	dir := t.TempDir()
	project, ws, err := repo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Lung Cancer Analysis", Description: "scRNA-seq", HostPath: dir,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, project.ID)
	assert.Equal(t, "Lung Cancer Analysis", project.Name)
	assert.Equal(t, "active", project.Status)

	assert.NotEmpty(t, ws.ID)
	assert.Equal(t, project.ID, ws.ProjectID)
	assert.Equal(t, "/workspace", ws.VirtualPath)
	assert.Equal(t, "local", ws.Kind)
	assert.NotEmpty(t, ws.PathFingerprint)

	projects, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, projects, 1)
	assert.Equal(t, project.ID, projects[0].ID)
}

func TestCreateProjectRejectsNonExistentPath(t *testing.T) {
	repo := newFileProjects(t)
	ctx := context.Background()

	_, _, err := repo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Missing", HostPath: "/nonexistent/path/12345",
	})
	assert.Error(t, err)
}

func TestFindProjectByID(t *testing.T) {
	repo := newFileProjects(t)
	ctx := context.Background()

	dir := t.TempDir()
	created, _, err := repo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Test", HostPath: dir,
	})
	require.NoError(t, err)

	found, err := repo.FindByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, created.Name, found.Name)

	// A well-formed but non-existent id returns nil (V2: invalid ids error).
	notFound, err := repo.FindByID(ctx, "00000000-0000-4000-8000-000000000000")
	require.NoError(t, err)
	assert.Nil(t, notFound)
}

func TestCreateSession(t *testing.T) {
	ctx := context.Background()
	projects := &projectstore.Store{Root: t.TempDir()}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Test", HostPath: t.TempDir()})
	require.NoError(t, err)
	manager := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	sessionRepo := &stores.SessionRepo{Files: manager}

	s, err := sessionRepo.Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "My First Chat",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, s.ID)
	assert.Equal(t, project.ID, s.ProjectID)
	assert.Equal(t, "My First Chat", s.Title)

	sessions, err := sessionRepo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	assert.Len(t, sessions, 1)
}

func TestMessageLineage(t *testing.T) {
	db, manager, s := newSessionDB(t)
	sessionRepo := &stores.SessionRepo{Files: manager}
	msgRepo := &stores.MessageRepo{DB: db}
	ctx := context.Background()

	root, err := msgRepo.CreateUserMessage(ctx, s.ID, "", "Hello")
	require.NoError(t, err)
	child1, err := msgRepo.CreateUserMessage(ctx, s.ID, root.ID, "Follow-up 1")
	require.NoError(t, err)
	child2, err := msgRepo.CreateUserMessage(ctx, s.ID, child1.ID, "Follow-up 2")
	require.NoError(t, err)

	err = sessionRepo.ActivateLeaf(ctx, s.ID, child2.ID)
	require.NoError(t, err)

	sess, err := sessionRepo.FindByID(ctx, s.ID)
	require.NoError(t, err)
	msgs, err := msgRepo.Lineage(ctx, s.ID, *sess.ActiveLeafMessageID)
	require.NoError(t, err)
	assert.Len(t, msgs, 3)
	assert.Equal(t, root.ID, msgs[0].ID)
	assert.Equal(t, child1.ID, msgs[1].ID)
	assert.Equal(t, child2.ID, msgs[2].ID)
}

func TestArchiveProject(t *testing.T) {
	repo := newFileProjects(t)
	ctx := context.Background()

	dir := t.TempDir()
	p, _, err := repo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Archivable", HostPath: dir,
	})
	require.NoError(t, err)

	// V2: archive is a manifest status; archived manifests are hidden from
	// List. The legacy projects SQL table was removed.
	manifest, err := repo.Files.ReadManifest(p.ID)
	require.NoError(t, err)
	manifest.Project.Status = "archived"
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repo.Files.Root, p.ID, "project.json"), encoded, 0o600))

	list, err := repo.List(ctx)
	require.NoError(t, err)
	for _, lp := range list {
		assert.NotEqual(t, "archived", lp.Status)
	}
}

func TestMessageParentMustBeSameSession(t *testing.T) {
	db, _, _ := newSessionDB(t)
	msgRepo := &stores.MessageRepo{DB: db}
	ctx := context.Background()
	s1 := sqlCreateSession(t, db, "00000000-0000-4000-8000-000000000002")
	s2 := sqlCreateSession(t, db, "00000000-0000-4000-8000-000000000002")
	msgS2, _ := msgRepo.CreateUserMessage(ctx, s2.ID, "", "Other session")

	_, err := msgRepo.CreateUserMessage(ctx, s1.ID, msgS2.ID, "Invalid cross-session")
	assert.ErrorContains(t, err, "does not belong to session")
}

func TestSwitchActiveLeaf(t *testing.T) {
	db, manager, s := newSessionDB(t)
	msgRepo := &stores.MessageRepo{DB: db}
	ctx := context.Background()
	sessionRepo := &stores.SessionRepo{Files: manager}

	a, _ := msgRepo.CreateUserMessage(ctx, s.ID, "", "A")
	b, _ := msgRepo.CreateUserMessage(ctx, s.ID, a.ID, "B")

	_ = sessionRepo.ActivateLeaf(ctx, s.ID, b.ID)
	err := sessionRepo.ActivateLeaf(ctx, s.ID, a.ID)
	require.NoError(t, err)

	found, _ := sessionRepo.FindByID(ctx, s.ID)
	assert.Equal(t, a.ID, *found.ActiveLeafMessageID)

	other := sqlCreateSession(t, db, "00000000-0000-4000-8000-000000000003")
	otherMessage, _ := msgRepo.CreateUserMessage(ctx, other.ID, "", "Other")
	err = sessionRepo.ActivateLeaf(ctx, s.ID, otherMessage.ID)
	assert.ErrorContains(t, err, "session or message not found")
}
