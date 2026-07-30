package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	stores "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateProjectWithWorkspace(t *testing.T) {
	db := stores.SetupDB(t)
	repo := &stores.ProjectRepo{DB: db}
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
	db := stores.SetupDB(t)
	repo := &stores.ProjectRepo{DB: db}
	ctx := context.Background()

	_, _, err := repo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Missing", HostPath: "/nonexistent/path/12345",
	})
	assert.Error(t, err)
}

func TestFindProjectByID(t *testing.T) {
	db := stores.SetupDB(t)
	repo := &stores.ProjectRepo{DB: db}
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

	// Non-existent returns nil
	notFound, err := repo.FindByID(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, notFound)
}

func TestCreateSession(t *testing.T) {
	db := stores.SetupDB(t)
	projectRepo := &stores.ProjectRepo{DB: db}
	sessionRepo := &stores.SessionRepo{DB: db}
	ctx := context.Background()

	dir := t.TempDir()
	p, _, err := projectRepo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Test", HostPath: dir,
	})
	require.NoError(t, err)

	s, err := sessionRepo.Create(ctx, domain.CreateSessionInput{
		ProjectID: p.ID, Title: "My First Chat",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, s.ID)
	assert.Equal(t, p.ID, s.ProjectID)
	assert.Equal(t, "My First Chat", s.Title)

	sessions, err := sessionRepo.ListByProject(ctx, p.ID)
	require.NoError(t, err)
	assert.Len(t, sessions, 1)
}

func TestMessageLineage(t *testing.T) {
	db := stores.SetupDB(t)
	projectRepo := &stores.ProjectRepo{DB: db}
	sessionRepo := &stores.SessionRepo{DB: db}
	msgRepo := &stores.MessageRepo{DB: db}
	ctx := context.Background()

	dir := t.TempDir()
	p, _, err := projectRepo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Test", HostPath: dir,
	})
	require.NoError(t, err)
	s, err := sessionRepo.Create(ctx, domain.CreateSessionInput{ProjectID: p.ID, Title: "Test"})
	require.NoError(t, err)

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
	db := stores.SetupDB(t)
	repo := &stores.ProjectRepo{DB: db}
	ctx := context.Background()

	dir := t.TempDir()
	p, _, err := repo.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "Archivable", HostPath: dir,
	})
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `UPDATE projects SET status = 'archived' WHERE id = ?`, p.ID)
	require.NoError(t, err)

	list, err := repo.List(ctx)
	require.NoError(t, err)
	for _, lp := range list {
		assert.NotEqual(t, "archived", lp.Status)
	}
}

func TestMessageParentMustBeSameSession(t *testing.T) {
	db := stores.SetupDB(t)
	msgRepo := &stores.MessageRepo{DB: db}
	projectRepo := &stores.ProjectRepo{DB: db}
	sessionRepo := &stores.SessionRepo{DB: db}
	ctx := context.Background()

	dir := t.TempDir()
	p, _, _ := projectRepo.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Test", HostPath: dir})
	s1, _ := sessionRepo.Create(ctx, domain.CreateSessionInput{ProjectID: p.ID})
	s2, _ := sessionRepo.Create(ctx, domain.CreateSessionInput{ProjectID: p.ID})
	msgS2, _ := msgRepo.CreateUserMessage(ctx, s2.ID, "", "Other session")

	_, err := msgRepo.CreateUserMessage(ctx, s1.ID, msgS2.ID, "Invalid cross-session")
	assert.ErrorContains(t, err, "does not belong to session")
}

func TestSwitchActiveLeaf(t *testing.T) {
	db := stores.SetupDB(t)
	msgRepo := &stores.MessageRepo{DB: db}
	projectRepo := &stores.ProjectRepo{DB: db}
	sessionRepo := &stores.SessionRepo{DB: db}
	ctx := context.Background()

	dir := t.TempDir()
	p, _, _ := projectRepo.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Test", HostPath: dir})
	s, _ := sessionRepo.Create(ctx, domain.CreateSessionInput{ProjectID: p.ID})

	a, _ := msgRepo.CreateUserMessage(ctx, s.ID, "", "A")
	b, _ := msgRepo.CreateUserMessage(ctx, s.ID, a.ID, "B")

	_ = sessionRepo.ActivateLeaf(ctx, s.ID, b.ID)
	err := sessionRepo.ActivateLeaf(ctx, s.ID, a.ID)
	require.NoError(t, err)

	found, _ := sessionRepo.FindByID(ctx, s.ID)
	assert.Equal(t, a.ID, *found.ActiveLeafMessageID)

	other, _ := sessionRepo.Create(ctx, domain.CreateSessionInput{ProjectID: p.ID})
	otherMessage, _ := msgRepo.CreateUserMessage(ctx, other.ID, "", "Other")
	err = sessionRepo.ActivateLeaf(ctx, s.ID, otherMessage.ID)
	assert.ErrorContains(t, err, "session or message not found")
}
