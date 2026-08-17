package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
)

// ErrProjectNotFound reports an unknown or already-deleted project id.
var ErrProjectNotFound = errors.New("project not found")

// ProjectRepo persists Project manifests in the file-native project store
// (V2). The legacy global projects/project_workspaces SQL tables were removed.
type ProjectRepo struct {
	Files *projectstore.Store
}

// expandHostPath resolves a user-supplied host path: a leading "~/" (or a
// bare "~") is expanded to the current user's home directory before the
// path is absolutized. Relative paths resolve against the Worker's cwd.
func expandHostPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Abs(path)
}

func (r *ProjectRepo) CreateWithWorkspace(ctx context.Context, input domain.CreateProjectInput) (*domain.Project, *domain.ProjectWorkspace, error) {
	if r == nil || r.Files == nil {
		return nil, nil, ErrFileBackedStoreRequired
	}
	return r.Files.CreateWithWorkspace(ctx, input)
}

func (r *ProjectRepo) List(ctx context.Context) ([]domain.Project, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.List(ctx)
}

func (r *ProjectRepo) FindWorkspaceByProjectID(ctx context.Context, projectID string) (*domain.ProjectWorkspace, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.FindWorkspaceByProjectID(ctx, projectID)
}

func (r *ProjectRepo) FindByID(ctx context.Context, id string) (*domain.Project, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.FindByID(ctx, id)
}

// Rename updates a project's display name without touching its workspace path.
func (r *ProjectRepo) Rename(ctx context.Context, id, name string) (*domain.Project, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	project, err := r.Files.Rename(ctx, id, name)
	if errors.Is(err, projectstore.ErrNotFound) {
		return nil, ErrProjectNotFound
	}
	return project, err
}

// Delete soft-deletes a project (status "deleted"); List stops returning it.
func (r *ProjectRepo) Delete(ctx context.Context, id string) (*domain.Project, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	project, err := r.Files.Delete(ctx, id)
	if errors.Is(err, projectstore.ErrNotFound) {
		return nil, ErrProjectNotFound
	}
	return project, err
}
