package projectstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateProjectWritesManifestOutsideWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	store := &projectstore.Store{Root: filepath.Join(home, "projects"), Now: fixedNow}

	project, binding, err := store.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "RNA Study", Description: "Test project", HostPath: workspace,
	})
	require.NoError(t, err)
	assert.Equal(t, "RNA Study", project.Name)
	assert.Equal(t, workspace, binding.HostPath)
	assert.Equal(t, project.ID, binding.ProjectID)

	projectDir := filepath.Join(home, "projects", project.ID)
	require.FileExists(t, filepath.Join(projectDir, "project.json"))
	require.DirExists(t, filepath.Join(projectDir, "sessions"))
	require.DirExists(t, filepath.Join(projectDir, "artifacts"))
	_, err = os.Stat(filepath.Join(workspace, ".ennote"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestProjectStoreListsAndFindsManifestData(t *testing.T) {
	store := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects"), Now: fixedNow}
	workspace := t.TempDir()
	project, expectedWorkspace, err := store.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Project", HostPath: workspace})
	require.NoError(t, err)

	projects, err := store.List(context.Background())
	require.NoError(t, err)
	require.Len(t, projects, 1)
	assert.Equal(t, project.ID, projects[0].ID)
	found, err := store.FindByID(context.Background(), project.ID)
	require.NoError(t, err)
	assert.Equal(t, project, found)
	foundWorkspace, err := store.FindWorkspaceByProjectID(context.Background(), project.ID)
	require.NoError(t, err)
	assert.Equal(t, expectedWorkspace, foundWorkspace)
}

func TestProjectStoreCanonicalizesSymlinkWorkspace(t *testing.T) {
	store := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects"), Now: fixedNow}
	realWorkspace := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace-link")
	require.NoError(t, os.Symlink(realWorkspace, link))

	_, workspace, err := store.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Project", HostPath: link})
	require.NoError(t, err)
	assert.Equal(t, realWorkspace, workspace.HostPath)
	assert.NotEmpty(t, workspace.PathFingerprint)
}

func TestProjectStoreRejectsManifestIdentityMismatchAndSymlinkProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	store := &projectstore.Store{Root: root, Now: fixedNow}
	project, _, err := store.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	manifest := filepath.Join(root, project.ID, "project.json")
	contents, err := os.ReadFile(manifest)
	require.NoError(t, err)
	modified := []byte(string(contents))
	modified = []byte(replaceOnce(string(modified), project.ID, "00000000-0000-0000-0000-000000000000"))
	require.NoError(t, os.WriteFile(manifest, modified, 0o600))
	_, err = store.FindByID(context.Background(), project.ID)
	assert.ErrorContains(t, err, "identity does not match")

	outside := t.TempDir()
	linkID := "11111111-1111-1111-1111-111111111111"
	require.NoError(t, os.Symlink(outside, filepath.Join(root, linkID)))
	_, err = store.FindByID(context.Background(), linkID)
	assert.ErrorContains(t, err, "regular directory")
}

func fixedNow() time.Time {
	return time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
}

func replaceOnce(value, old, replacement string) string {
	for index := 0; index+len(old) <= len(value); index++ {
		if value[index:index+len(old)] == old {
			return value[:index] + replacement + value[index+len(old):]
		}
	}
	return value
}

func TestProjectStoreRejectsCorruptManifest(t *testing.T) {
	store := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects"), Now: fixedNow}
	project, _, err := store.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Corrupt", HostPath: t.TempDir(),
	})
	require.NoError(t, err)

	manifestPath := filepath.Join(store.Root, project.ID, "project.json")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"schemaVersion":1,"project":`), 0o600))

	_, err = store.FindByID(context.Background(), project.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode project manifest")

	// A corrupt manifest fails the whole catalog closed (never silently drops
	// a project).
	_, err = store.List(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode project manifest")
}
