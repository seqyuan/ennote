package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrSkillRootNotFound is returned when a skill root does not exist.
var ErrSkillRootNotFound = errors.New("skill root not found")

// SkillRoot is a user-configured additional skills directory (pi, claude code,
// codex, cursor ecosystems or a custom path).
type SkillRoot struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	AgentKind string    `json:"agentKind"`
	Priority  int       `json:"priority"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateSkillRootInput is the client-provided definition of a new root.
type CreateSkillRootInput struct {
	Name      string
	Path      string
	AgentKind string
	Priority  int
	Enabled   bool
}

// SkillRootRepo persists additional skill roots.
type SkillRootRepo struct{ DB *sql.DB }

// List returns all roots ordered by priority ascending (lower wins).
func (r *SkillRootRepo) List(ctx context.Context) ([]SkillRoot, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT id, name, path, agent_kind, priority, enabled, created_at, updated_at
		FROM skill_roots ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roots []SkillRoot
	for rows.Next() {
		var root SkillRoot
		var enabled int
		var createdAt, updatedAt string
		if err := rows.Scan(&root.ID, &root.Name, &root.Path, &root.AgentKind, &root.Priority, &enabled,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		root.Enabled = enabled == 1
		root.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		root.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

// Create inserts a new root. Priority <= 0 is normalized to 10; duplicate
// paths are rejected with a descriptive error.
func (r *SkillRootRepo) Create(ctx context.Context, input CreateSkillRootInput) (*SkillRoot, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Path = strings.TrimSpace(input.Path)
	if input.Name == "" || input.Path == "" {
		return nil, fmt.Errorf("name and path are required")
	}
	if input.AgentKind == "" {
		input.AgentKind = "generic"
	}
	if input.Priority <= 0 {
		input.Priority = 10
	}
	now := time.Now().UTC()
	root := &SkillRoot{
		ID:        uuid.NewString(),
		Name:      input.Name,
		Path:      input.Path,
		AgentKind: input.AgentKind,
		Priority:  input.Priority,
		Enabled:   input.Enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO skill_roots (id, name, path, agent_kind, priority, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		root.ID, root.Name, root.Path, root.AgentKind, root.Priority, boolInt(root.Enabled), now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("a skill root for %q already exists", root.Path)
		}
		return nil, err
	}
	return root, nil
}

// Update patches name, path, agent_kind, priority, or enabled.
func (r *SkillRootRepo) Update(ctx context.Context, id string, patch struct {
	Name      *string
	Path      *string
	AgentKind *string
	Priority  *int
	Enabled   *bool
}) (*SkillRoot, error) {
	current, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if patch.Name != nil {
		current.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Path != nil {
		current.Path = strings.TrimSpace(*patch.Path)
	}
	if patch.AgentKind != nil {
		current.AgentKind = *patch.AgentKind
	}
	if patch.Priority != nil {
		current.Priority = *patch.Priority
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	current.UpdatedAt = time.Now().UTC()
	if _, err := r.DB.ExecContext(ctx, `
		UPDATE skill_roots SET name = ?, path = ?, agent_kind = ?, priority = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		current.Name, current.Path, current.AgentKind, current.Priority, boolInt(current.Enabled), current.UpdatedAt, id); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("a skill root for %q already exists", current.Path)
		}
		return nil, err
	}
	return current, nil
}

// Delete removes a root.
func (r *SkillRootRepo) Delete(ctx context.Context, id string) error {
	res, err := r.DB.ExecContext(ctx, `DELETE FROM skill_roots WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSkillRootNotFound
	}
	return nil
}

// Get loads one root.
func (r *SkillRootRepo) Get(ctx context.Context, id string) (*SkillRoot, error) {
	row := r.DB.QueryRowContext(ctx, `
		SELECT id, name, path, agent_kind, priority, enabled, created_at, updated_at
		FROM skill_roots WHERE id = ?`, id)
	var root SkillRoot
	var enabled int
	var createdAt, updatedAt string
	if err := row.Scan(&root.ID, &root.Name, &root.Path, &root.AgentKind, &root.Priority, &enabled,
		&createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSkillRootNotFound
		}
		return nil, err
	}
	root.Enabled = enabled == 1
	root.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	root.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &root, nil
}

// EnabledPaths returns the paths of enabled roots sorted by priority.
func (r *SkillRootRepo) EnabledPaths(ctx context.Context) ([]SkillRoot, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT id, name, path, agent_kind, priority, enabled, created_at, updated_at
		FROM skill_roots WHERE enabled = 1 ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roots []SkillRoot
	for rows.Next() {
		var root SkillRoot
		var enabled int
		var createdAt, updatedAt string
		if err := rows.Scan(&root.ID, &root.Name, &root.Path, &root.AgentKind, &root.Priority, &enabled,
			&createdAt, &updatedAt); err != nil {
			return nil, err
		}
		root.Enabled = enabled == 1
		root.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		root.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

