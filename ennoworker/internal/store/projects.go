package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type ProjectRepo struct{ DB *sql.DB }

func (r *ProjectRepo) CreateWithWorkspace(ctx context.Context, input domain.CreateProjectInput) (*domain.Project, *domain.ProjectWorkspace, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	projectID := uuid.NewString()
	wsID := uuid.NewString()

	hostPath, err := filepath.Abs(input.HostPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve host path: %w", err)
	}
	hostPath = filepath.Clean(hostPath)

	info, err := os.Stat(hostPath)
	if err != nil {
		return nil, nil, fmt.Errorf("host path does not exist: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("host path is not a directory: %s", hostPath)
	}
	hostPath, err = filepath.EvalSymlinks(hostPath)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve host path symlinks: %w", err)
	}
	fp := fmt.Sprintf("%x", sha256.Sum256([]byte(hostPath)))[:16]

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO projects (id, name, description, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', ?, ?)`,
		projectID, input.Name, input.Description, now, now,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("insert project: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO project_workspaces (id, project_id, kind, host_path, virtual_path, status, path_fingerprint, created_at)
		 VALUES (?, ?, 'local', ?, '/workspace', 'active', ?, ?)`,
		wsID, projectID, hostPath, fp, now,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("insert workspace: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit: %w", err)
	}

	return &domain.Project{
			ID: projectID, Name: input.Name, Description: input.Description,
			Status: "active", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}, &domain.ProjectWorkspace{
			ID: wsID, ProjectID: projectID, Kind: "local",
			HostPath: hostPath, VirtualPath: "/workspace", Status: "active",
			PathFingerprint: fp, CreatedAt: time.Now().UTC(),
		}, nil
}

func (r *ProjectRepo) List(ctx context.Context) ([]domain.Project, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, name, description, status, created_at, updated_at
		 FROM projects WHERE status NOT IN ('deleted', 'archived') ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projects := make([]domain.Project, 0)
	for rows.Next() {
		var p domain.Project
		var c, u string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Status, &c, &u); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, c)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, u)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (r *ProjectRepo) FindWorkspaceByProjectID(ctx context.Context, projectID string) (*domain.ProjectWorkspace, error) {
	var workspace domain.ProjectWorkspace
	var created string
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, project_id, kind, host_path, virtual_path, status, path_fingerprint, created_at
		 FROM project_workspaces WHERE project_id = ? AND status = 'active'`, projectID,
	).Scan(
		&workspace.ID, &workspace.ProjectID, &workspace.Kind, &workspace.HostPath,
		&workspace.VirtualPath, &workspace.Status, &workspace.PathFingerprint, &created,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	workspace.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &workspace, nil
}

func (r *ProjectRepo) FindByID(ctx context.Context, id string) (*domain.Project, error) {
	var p domain.Project
	var c, u string
	err := r.DB.QueryRowContext(ctx,
		`SELECT id, name, description, status, created_at, updated_at
		 FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Description, &p.Status, &c, &u)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, c)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, u)
	return &p, nil
}
