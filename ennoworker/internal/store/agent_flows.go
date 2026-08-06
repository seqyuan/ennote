package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// AgentFlowProfileRepo persists Agent Flow profiles and immutable versions.
// The lifecycle mirrors MCP server profiles: authoring is open, runtime is
// frozen, and project files only ever produce candidates.
type AgentFlowProfileRepo struct{ DB *sql.DB }

// AgentFlowBindingRepo persists per-Project Agent Flow bindings.
type AgentFlowBindingRepo struct{ DB *sql.DB }

// CreateAgentFlowProfileInput is the secret-free definition of a flow profile.
type CreateAgentFlowProfileInput struct {
	Name          string
	Slug          string
	SourceKind    string
	ProjectScope  *string
	SourceLocator string
}

func (r *AgentFlowProfileRepo) CreateProfile(ctx context.Context, input CreateAgentFlowProfileInput) (*domain.AgentFlowProfile, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = strings.TrimSpace(input.Slug)
	if input.Name == "" || input.Slug == "" {
		return nil, fmt.Errorf("flow profile name and slug are required")
	}
	if err := validateAgentFlowSlug(input.Slug); err != nil {
		return nil, err
	}
	if input.SourceKind != domain.FlowSourceManaged && input.SourceKind != domain.FlowSourceProjectFile {
		return nil, fmt.Errorf("invalid flow source kind: %s", input.SourceKind)
	}
	now := time.Now().UTC()
	profile := &domain.AgentFlowProfile{
		ID: uuid.NewString(), Name: input.Name, Slug: input.Slug,
		SourceKind: input.SourceKind, ProjectScope: input.ProjectScope,
		SourceLocator: input.SourceLocator, Lifecycle: "active",
		CreatedAt: now, UpdatedAt: now, LatestVersion: 0,
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO agent_flow_profiles
		(id, name, slug, source_kind, project_scope, source_locator, lifecycle_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		profile.ID, profile.Name, profile.Slug, profile.SourceKind,
		nullableString(input.ProjectScope), profile.SourceLocator, roleTime(now), roleTime(now))
	if err != nil {
		return nil, fmt.Errorf("insert flow profile: %w", err)
	}
	return profile, nil
}

// CreateVersion publishes the next immutable version from a parsed definition.
// The config digest is computed from the definition alone (never secrets) and
// is unique per profile so identical definitions reuse one version.
func (r *AgentFlowProfileRepo) CreateVersion(ctx context.Context, profileID string, def *domain.FlowDefinition) (*domain.AgentFlowVersion, error) {
	if def == nil {
		return nil, fmt.Errorf("flow definition is required")
	}
	digest, err := agentflow.ConfigDigest(def)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		return nil, fmt.Errorf("encode flow definition: %w", err)
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var nextVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM agent_flow_versions WHERE profile_id=?`,
		profileID).Scan(&nextVersion); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	version := &domain.AgentFlowVersion{
		ID: uuid.NewString(), ProfileID: profileID, Version: nextVersion,
		ConfigDigest: digest, DefinitionJSON: encoded, PublishedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_flow_versions
		(id, profile_id, version, config_digest, definition_json, published_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		version.ID, profileID, version.Version, version.ConfigDigest, string(encoded), roleTime(now)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, fmt.Errorf("flow version with the same config digest already exists")
		}
		return nil, fmt.Errorf("insert flow version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_flow_profiles SET latest_version=?, updated_at=?
		WHERE id=?`, nextVersion, roleTime(now), profileID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return version, nil
}

// ListProfiles lists active flow profiles with their latest version number.
func (r *AgentFlowProfileRepo) ListProfiles(ctx context.Context) ([]*domain.AgentFlowProfile, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT p.id, p.name, p.slug, p.source_kind, p.project_scope,
		p.source_locator, p.lifecycle_status, p.created_at, p.updated_at,
		COALESCE(MAX(v.version), 0) AS latest
		FROM agent_flow_profiles p
		LEFT JOIN agent_flow_versions v ON v.profile_id = p.id
		WHERE p.lifecycle_status = 'active'
		GROUP BY p.id ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []*domain.AgentFlowProfile
	for rows.Next() {
		p := &domain.AgentFlowProfile{}
		var scope sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.SourceKind, &scope,
			&p.SourceLocator, &p.Lifecycle, &createdAt, &updatedAt, &p.LatestVersion); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		if scope.Valid {
			p.ProjectScope = &scope.String
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// GetProfile fetches a single active profile.
func (r *AgentFlowProfileRepo) GetProfile(ctx context.Context, profileID string) (*domain.AgentFlowProfile, error) {
	var p domain.AgentFlowProfile
	var scope sql.NullString
	var draftJSON, draftYAML sql.NullString
	var createdAt, updatedAt string
	err := r.DB.QueryRowContext(ctx, `SELECT p.id, p.name, p.slug, p.source_kind, p.project_scope,
		p.source_locator, p.lifecycle_status, p.created_at, p.updated_at,
		COALESCE(MAX(v.version), 0) AS latest, p.draft_json, p.draft_yaml, p.draft_revision
		FROM agent_flow_profiles p
		LEFT JOIN agent_flow_versions v ON v.profile_id = p.id
		WHERE p.id = ? GROUP BY p.id`, profileID).
		Scan(&p.ID, &p.Name, &p.Slug, &p.SourceKind, &scope, &p.SourceLocator,
			&p.Lifecycle, &createdAt, &updatedAt, &p.LatestVersion, &draftJSON, &draftYAML, &p.DraftRevision)
	if err != nil {
		return nil, err
	}
	if scope.Valid {
		p.ProjectScope = &scope.String
	}
	if draftJSON.Valid {
		p.DraftJSON = json.RawMessage(draftJSON.String)
	}
	if draftYAML.Valid {
		p.DraftYAML = draftYAML.String
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &p, nil
}

// Archive marks a profile inactive. Existing versions, bindings, and run
// snapshots stay untouched.
func (r *AgentFlowProfileRepo) Archive(ctx context.Context, profileID string) error {
	res, err := r.DB.ExecContext(ctx,
		`UPDATE agent_flow_profiles SET lifecycle_status='archived', updated_at=? WHERE id=? AND lifecycle_status='active'`,
		roleTime(time.Now().UTC()), profileID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetVersion fetches one immutable flow version.
func (r *AgentFlowProfileRepo) GetVersion(ctx context.Context, versionID string) (*domain.AgentFlowVersion, error) {
	versions, err := r.queryVersions(ctx, `WHERE v.id = ?`, versionID)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, sql.ErrNoRows
	}
	return versions[0], nil
}

// ListVersions returns all versions of a profile ordered by version number.
func (r *AgentFlowProfileRepo) ListVersions(ctx context.Context, profileID string) ([]*domain.AgentFlowVersion, error) {
	return r.queryVersions(ctx, `WHERE v.profile_id = ? ORDER BY v.version`, profileID)
}

func (r *AgentFlowProfileRepo) queryVersions(ctx context.Context, where string, args ...any) ([]*domain.AgentFlowVersion, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT v.id, v.profile_id, v.version, v.config_digest,
		v.definition_json, v.published_at FROM agent_flow_versions v `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []*domain.AgentFlowVersion
	for rows.Next() {
		v := &domain.AgentFlowVersion{}
		var definitionJSON, publishedAt string
		if err := rows.Scan(&v.ID, &v.ProfileID, &v.Version, &v.ConfigDigest,
			&definitionJSON, &publishedAt); err != nil {
			return nil, err
		}
		v.DefinitionJSON = json.RawMessage(definitionJSON)
		v.PublishedAt, _ = time.Parse(time.RFC3339Nano, publishedAt)
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// FindProfileBySource locates an active profile by slug + source kind +
// project scope, mirroring MCP candidate matching.
func (r *AgentFlowProfileRepo) FindProfileBySource(ctx context.Context, slug, sourceKind string, projectScope *string) (*domain.AgentFlowProfile, error) {
	rows, err := r.queryProfiles(ctx, `WHERE p.slug = ? AND p.source_kind = ?
		AND (p.project_scope IS ? OR (? IS NULL AND p.project_scope IS NULL))
		AND p.lifecycle_status = 'active'`, slug, sourceKind, nullableString(projectScope), nullableString(projectScope))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

func (r *AgentFlowProfileRepo) queryProfiles(ctx context.Context, where string, args ...any) ([]*domain.AgentFlowProfile, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT p.id, p.name, p.slug, p.source_kind, p.project_scope,
		p.source_locator, p.lifecycle_status, p.created_at, p.updated_at,
		COALESCE(MAX(v.version), 0) AS latest
		FROM agent_flow_profiles p
		LEFT JOIN agent_flow_versions v ON v.profile_id = p.id `+where+`
		GROUP BY p.id ORDER BY p.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []*domain.AgentFlowProfile
	for rows.Next() {
		p := &domain.AgentFlowProfile{}
		var scope sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.SourceKind, &scope,
			&p.SourceLocator, &p.Lifecycle, &createdAt, &updatedAt, &p.LatestVersion); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		if scope.Valid {
			p.ProjectScope = &scope.String
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// FindVersionByDigest locates a version of a profile with the exact config
// digest so candidate discovery can reuse an existing immutable version.
func (r *AgentFlowProfileRepo) FindVersionByDigest(ctx context.Context, profileID, configDigest string) (*domain.AgentFlowVersion, error) {
	versions, err := r.queryVersions(ctx, `WHERE v.profile_id = ? AND v.config_digest = ?`, profileID, configDigest)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, sql.ErrNoRows
	}
	return versions[0], nil
}

func validateAgentFlowSlug(slug string) error {
	if len(slug) > 64 {
		return fmt.Errorf("slug too long (max 64)")
	}
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("slug may only contain lowercase letters, digits, '_' and '-'")
	}
	return nil
}

// --- Project bindings ---

// EnsureBindingExists creates a disabled binding if none exists for the pair.
func (r *AgentFlowBindingRepo) EnsureBindingExists(ctx context.Context, projectID, flowVersionID string) (*domain.ProjectAgentFlowBinding, error) {
	existing, err := r.GetByProjectVersion(ctx, projectID, flowVersionID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now := time.Now().UTC()
	b := &domain.ProjectAgentFlowBinding{
		ID: uuid.NewString(), ProjectID: projectID, FlowVersionID: flowVersionID,
		DesiredEnabled: false, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO project_agent_flow_bindings
		(id, project_id, flow_version_id, desired_enabled, revision, created_at, updated_at)
		VALUES (?, ?, ?, 0, 1, ?, ?)`,
		b.ID, b.ProjectID, b.FlowVersionID, roleTime(now), roleTime(now))
	if err != nil {
		return nil, fmt.Errorf("insert flow binding: %w", err)
	}
	return b, nil
}

// GetByProjectVersion returns the binding for a project + flow version.
func (r *AgentFlowBindingRepo) GetByProjectVersion(ctx context.Context, projectID, flowVersionID string) (*domain.ProjectAgentFlowBinding, error) {
	rows, err := r.queryBindings(ctx, `WHERE project_id = ? AND flow_version_id = ?`, projectID, flowVersionID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

// ListByProject returns all bindings for a project.
func (r *AgentFlowBindingRepo) ListByProject(ctx context.Context, projectID string) ([]*domain.ProjectAgentFlowBinding, error) {
	return r.queryBindings(ctx, `WHERE project_id = ? ORDER BY created_at`, projectID)
}

// Update applies a desired-state mutation and bumps the revision.
func (r *AgentFlowBindingRepo) Update(ctx context.Context, bindingID string, desiredEnabled bool) (*domain.ProjectAgentFlowBinding, error) {
	res, err := r.DB.ExecContext(ctx, `UPDATE project_agent_flow_bindings
		SET desired_enabled=?, revision=revision+1, updated_at=? WHERE id=?`,
		desiredEnabled, roleTime(time.Now().UTC()), bindingID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return r.Get(ctx, bindingID)
}

// Get fetches a binding by id.
func (r *AgentFlowBindingRepo) Get(ctx context.Context, bindingID string) (*domain.ProjectAgentFlowBinding, error) {
	rows, err := r.queryBindings(ctx, `WHERE id = ?`, bindingID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

// Delete removes a binding.
func (r *AgentFlowBindingRepo) Delete(ctx context.Context, bindingID string) error {
	res, err := r.DB.ExecContext(ctx, `DELETE FROM project_agent_flow_bindings WHERE id=?`, bindingID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *AgentFlowBindingRepo) queryBindings(ctx context.Context, where string, args ...any) ([]*domain.ProjectAgentFlowBinding, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, project_id, flow_version_id, desired_enabled, revision,
		created_at, updated_at FROM project_agent_flow_bindings `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []*domain.ProjectAgentFlowBinding
	for rows.Next() {
		b := &domain.ProjectAgentFlowBinding{}
		var createdAt, updatedAt string
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.FlowVersionID, &b.DesiredEnabled,
			&b.Revision, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}
