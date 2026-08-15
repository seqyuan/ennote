package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/mcpclient"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
)

// MCPProfileRepo persists MCP server profiles and immutable versions.
type MCPProfileRepo struct {
	DB       *sql.DB
	FilePath string
}

// MCPBindingRepo persists per-Project MCP bindings.
type MCPBindingRepo struct {
	DB       *sql.DB
	Projects *projectstore.Store
}

// CreateMCPProfileInput is the secret-free definition of a server profile.
type CreateMCPProfileInput struct {
	DisplayName   string
	Slug          string
	SourceKind    string
	ProjectScope  *string
	SourceLocator string
}

func (r *MCPProfileRepo) CreateProfile(ctx context.Context, input CreateMCPProfileInput) (*domain.MCPServerProfile, error) {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Slug = strings.TrimSpace(input.Slug)
	if input.DisplayName == "" || input.Slug == "" {
		return nil, fmt.Errorf("profile display name and slug are required")
	}
	if err := validateMCPSlug(input.Slug); err != nil {
		return nil, err
	}
	if input.SourceKind != domain.MCPSourceManaged && input.SourceKind != domain.MCPSourceProjectFile && input.SourceKind != domain.MCPSourceBundled {
		return nil, fmt.Errorf("invalid source kind: %s", input.SourceKind)
	}
	if r.FilePath != "" {
		return r.fileCreateProfile(input)
	}
	now := time.Now().UTC()
	profile := &domain.MCPServerProfile{
		ID: uuid.NewString(), DisplayName: input.DisplayName, Slug: input.Slug,
		SourceKind: input.SourceKind, ProjectScope: input.ProjectScope,
		SourceLocator: input.SourceLocator, Lifecycle: "active",
		CreatedAt: now, UpdatedAt: now, LatestVersion: 0,
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO mcp_server_profiles
		(id, display_name, slug, source_kind, project_scope, source_locator, lifecycle_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		profile.ID, profile.DisplayName, profile.Slug, profile.SourceKind,
		nullableString(input.ProjectScope), profile.SourceLocator, roleTime(now), roleTime(now))
	if err != nil {
		return nil, fmt.Errorf("insert mcp profile: %w", err)
	}
	return profile, nil
}

// CreateVersion creates the next immutable version for a profile.
func (r *MCPProfileRepo) CreateVersion(ctx context.Context, profileID string, v *domain.MCPServerProfileVersion) error {
	if v == nil {
		return fmt.Errorf("version is required")
	}
	if r.FilePath != "" {
		return r.fileCreateVersion(profileID, v)
	}
	version := v.Version
	if version <= 0 {
		// Auto-assign next version.
		var current int
		err := r.DB.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(version), 0) FROM mcp_server_profile_versions WHERE profile_id = ?`,
			profileID).Scan(&current)
		if err != nil {
			return fmt.Errorf("query max version: %w", err)
		}
		version = current + 1
	}
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	if v.TimeoutMS <= 0 {
		v.TimeoutMS = 15000
	}
	if v.NetworkPolicy == "" {
		v.NetworkPolicy = "default"
	}
	argvJSON, err := json.Marshal(v.Argv)
	if err != nil {
		return err
	}
	envLitJSON, err := json.Marshal(v.EnvLiterals)
	if err != nil {
		return err
	}
	envCredJSON, err := json.Marshal(v.EnvCredentials)
	if err != nil {
		return err
	}
	headerLitJSON, err := json.Marshal(v.HeaderLiterals)
	if err != nil {
		return err
	}
	headerCredJSON, err := json.Marshal(v.HeaderCreds)
	if err != nil {
		return err
	}
	if v.ConfigDigest == "" {
		v.ConfigDigest = mcpConfigDigest(v)
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO mcp_server_profile_versions
		(id, profile_id, version, transport, executable, argv_json, endpoint,
		 env_literals_json, env_credentials_json, header_literals_json, header_credentials_json,
		 cwd, timeout_ms, network_policy, config_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, profileID, version, v.Transport, v.Executable, string(argvJSON), v.Endpoint,
		string(envLitJSON), string(envCredJSON), string(headerLitJSON), string(headerCredJSON),
		v.CWD, v.TimeoutMS, v.NetworkPolicy, v.ConfigDigest, roleTime(v.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert mcp profile version: %w", err)
	}
	v.ProfileID = profileID
	v.Version = version
	_, err = r.DB.ExecContext(ctx, `UPDATE mcp_server_profiles
		SET latest_version = ?, updated_at = ? WHERE id = ?`, version, roleTime(time.Now().UTC()), profileID)
	return err
}

// ListVersions returns all versions of a profile ordered by version number.
func (r *MCPProfileRepo) ListVersions(ctx context.Context, profileID string) ([]*domain.MCPServerProfileVersion, error) {
	if r.FilePath != "" {
		return r.fileListVersions(profileID)
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT id, profile_id, version, transport, executable, argv_json,
		endpoint, env_literals_json, env_credentials_json, header_literals_json, header_credentials_json,
		cwd, timeout_ms, network_policy, config_digest, created_at
		FROM mcp_server_profile_versions WHERE profile_id = ? ORDER BY version`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []*domain.MCPServerProfileVersion
	for rows.Next() {
		v := &domain.MCPServerProfileVersion{}
		var argvJSON, envLit, envCred, hdrLit, hdrCred string
		var createdAt string
		if err := rows.Scan(&v.ID, &v.ProfileID, &v.Version, &v.Transport, &v.Executable, &argvJSON,
			&v.Endpoint, &envLit, &envCred, &hdrLit, &hdrCred, &v.CWD, &v.TimeoutMS,
			&v.NetworkPolicy, &v.ConfigDigest, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if err := json.Unmarshal([]byte(argvJSON), &v.Argv); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(envLit), &v.EnvLiterals); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(envCred), &v.EnvCredentials); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(hdrLit), &v.HeaderLiterals); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(hdrCred), &v.HeaderCreds); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// GetVersion fetches a specific immutable version.
func (r *MCPProfileRepo) GetVersion(ctx context.Context, versionID string) (*domain.MCPServerProfileVersion, error) {
	if r.FilePath != "" {
		return r.fileGetVersion(versionID)
	}
	versions, err := r.queryVersions(ctx, `WHERE id = ?`, versionID)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, sql.ErrNoRows
	}
	return versions[0], nil
}

func (r *MCPProfileRepo) queryVersions(ctx context.Context, where string, args ...any) ([]*domain.MCPServerProfileVersion, error) {
	query := `SELECT id, profile_id, version, transport, executable, argv_json,
		endpoint, env_literals_json, env_credentials_json, header_literals_json, header_credentials_json,
		cwd, timeout_ms, network_policy, config_digest, created_at
		FROM mcp_server_profile_versions ` + where
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []*domain.MCPServerProfileVersion
	for rows.Next() {
		v := &domain.MCPServerProfileVersion{}
		var argvJSON, envLit, envCred, hdrLit, hdrCred string
		var createdAt string
		if err := rows.Scan(&v.ID, &v.ProfileID, &v.Version, &v.Transport, &v.Executable, &argvJSON,
			&v.Endpoint, &envLit, &envCred, &hdrLit, &hdrCred, &v.CWD, &v.TimeoutMS,
			&v.NetworkPolicy, &v.ConfigDigest, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if err := json.Unmarshal([]byte(argvJSON), &v.Argv); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(envLit), &v.EnvLiterals); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(envCred), &v.EnvCredentials); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(hdrLit), &v.HeaderLiterals); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(hdrCred), &v.HeaderCreds); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// ListProfiles lists non-archived profiles with their latest version.
func (r *MCPProfileRepo) ListProfiles(ctx context.Context) ([]*domain.MCPServerProfile, error) {
	if r.FilePath != "" {
		return r.fileListProfiles()
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT p.id, p.display_name, p.slug, p.source_kind, p.project_scope,
		p.source_locator, p.lifecycle_status, p.created_at, p.updated_at,
		COALESCE(MAX(v.version), 0) AS latest
		FROM mcp_server_profiles p
		LEFT JOIN mcp_server_profile_versions v ON v.profile_id = p.id
		WHERE p.lifecycle_status = 'active'
		GROUP BY p.id ORDER BY p.display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []*domain.MCPServerProfile
	for rows.Next() {
		p := &domain.MCPServerProfile{}
		var scope sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.Slug, &p.SourceKind, &scope,
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
func (r *MCPProfileRepo) GetProfile(ctx context.Context, profileID string) (*domain.MCPServerProfile, error) {
	if r.FilePath != "" {
		return r.fileGetProfile(profileID)
	}
	var p domain.MCPServerProfile
	var scope sql.NullString
	var createdAt, updatedAt string
	err := r.DB.QueryRowContext(ctx, `SELECT p.id, p.display_name, p.slug, p.source_kind, p.project_scope,
		p.source_locator, p.lifecycle_status, p.created_at, p.updated_at,
		COALESCE(MAX(v.version), 0) AS latest
		FROM mcp_server_profiles p
		LEFT JOIN mcp_server_profile_versions v ON v.profile_id = p.id
		WHERE p.id = ? GROUP BY p.id`, profileID).
		Scan(&p.ID, &p.DisplayName, &p.Slug, &p.SourceKind, &scope, &p.SourceLocator,
			&p.Lifecycle, &createdAt, &updatedAt, &p.LatestVersion)
	if err != nil {
		return nil, err
	}
	if scope.Valid {
		p.ProjectScope = &scope.String
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &p, nil
}

// Archive marks a profile inactive. Existing bindings and run snapshots stay.
func (r *MCPProfileRepo) Archive(ctx context.Context, profileID string) error {
	if r.FilePath != "" {
		return r.fileArchive(profileID)
	}
	res, err := r.DB.ExecContext(ctx,
		`UPDATE mcp_server_profiles SET lifecycle_status='archived', updated_at=? WHERE id=?`,
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

func validateMCPSlug(slug string) error {
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

// mcpConfigDigest produces a stable digest of a version's non-secret config.
// It delegates to the shared mcpclient implementation so candidate discovery
// and persistence always agree.
func mcpConfigDigest(v *domain.MCPServerProfileVersion) string {
	return mcpclient.ConfigDigest(v)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// EnsureBindingExists creates a disabled binding if none exists for the pair.
// Returns the existing binding when already present.
func (r *MCPBindingRepo) EnsureBindingExists(ctx context.Context, projectID, profileVersionID string) (*domain.MCPProjectBinding, error) {
	if r.Projects != nil {
		return r.fileEnsure(ctx, projectID, profileVersionID)
	}
	existing, err := r.GetByProjectVersion(ctx, projectID, profileVersionID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now := time.Now().UTC()
	b := &domain.MCPProjectBinding{
		ID: uuid.NewString(), ProjectID: projectID, ProfileVersionID: profileVersionID,
		DesiredEnabled: false, Required: true, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO project_mcp_bindings
		(id, project_id, profile_version_id, desired_enabled, required, selected_remote_tool_names_json,
		 credential_refs_json, revision, created_at, updated_at)
		VALUES (?, ?, ?, 0, 1, '[]', '{}', 1, ?, ?)`,
		b.ID, b.ProjectID, b.ProfileVersionID, roleTime(now), roleTime(now))
	if err != nil {
		return nil, fmt.Errorf("insert mcp binding: %w", err)
	}
	return b, nil
}

// GetByProjectVersion returns the binding for a project + profile version.
func (r *MCPBindingRepo) GetByProjectVersion(ctx context.Context, projectID, profileVersionID string) (*domain.MCPProjectBinding, error) {
	if r.Projects != nil {
		bindings, err := r.fileList(ctx, projectID)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			if binding.ProfileVersionID == profileVersionID {
				return binding, nil
			}
		}
		return nil, sql.ErrNoRows
	}
	rows, err := r.queryBindings(ctx, `WHERE project_id = ? AND profile_version_id = ?`, projectID, profileVersionID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

// ListByProject returns all bindings for a project.
func (r *MCPBindingRepo) ListByProject(ctx context.Context, projectID string) ([]*domain.MCPProjectBinding, error) {
	if r.Projects != nil {
		return r.fileList(ctx, projectID)
	}
	return r.queryBindings(ctx, `WHERE project_id = ? ORDER BY created_at`, projectID)
}

// Update applies a desired-state mutation and bumps the revision. selected and
// credential refs are replaced atomically; nil fields keep their current value.
type MCPBindingUpdate struct {
	DesiredEnabled          *bool
	Required                *bool
	SelectedRemoteToolNames []string
	CredentialRefs          map[string]string
}

func (r *MCPBindingRepo) Update(ctx context.Context, bindingID string, upd MCPBindingUpdate) (*domain.MCPProjectBinding, error) {
	if r.Projects != nil {
		return r.fileUpdate(ctx, bindingID, upd)
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Read current values; nil update fields keep their current value.
	var desired bool
	var required bool
	var selectedJSON string
	var credJSON string
	if err := tx.QueryRowContext(ctx, `SELECT desired_enabled, required,
		selected_remote_tool_names_json, credential_refs_json
		FROM project_mcp_bindings WHERE id=?`,
		bindingID).Scan(&desired, &required, &selectedJSON, &credJSON); err != nil {
		return nil, err
	}
	if upd.DesiredEnabled != nil {
		desired = *upd.DesiredEnabled
	}
	if upd.Required != nil {
		required = *upd.Required
	}
	if upd.SelectedRemoteToolNames != nil {
		b, err := json.Marshal(upd.SelectedRemoteToolNames)
		if err != nil {
			return nil, err
		}
		selectedJSON = string(b)
	}
	if upd.CredentialRefs != nil {
		if err := validateCredentialRefMap(upd.CredentialRefs); err != nil {
			return nil, err
		}
		b, err := json.Marshal(upd.CredentialRefs)
		if err != nil {
			return nil, err
		}
		credJSON = string(b)
	}
	_, err = tx.ExecContext(ctx, `UPDATE project_mcp_bindings
		SET desired_enabled=?, required=?, selected_remote_tool_names_json=?,
		    credential_refs_json=?, revision=revision+1, updated_at=?
		WHERE id=?`, desired, required, selectedJSON, credJSON, roleTime(time.Now().UTC()), bindingID)
	if err != nil {
		return nil, err
	}
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM project_mcp_bindings WHERE id=?`, bindingID).Scan(&revision); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	updated, err := r.Get(ctx, bindingID)
	if err != nil {
		return nil, err
	}
	updated.Revision = revision
	return updated, nil
}

// Get fetches a binding by id.
func (r *MCPBindingRepo) Get(ctx context.Context, bindingID string) (*domain.MCPProjectBinding, error) {
	if r.Projects != nil {
		return r.fileGet(ctx, bindingID)
	}
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
func (r *MCPBindingRepo) Delete(ctx context.Context, bindingID string) error {
	if r.Projects != nil {
		return r.fileDelete(ctx, bindingID)
	}
	res, err := r.DB.ExecContext(ctx, `DELETE FROM project_mcp_bindings WHERE id=?`, bindingID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *MCPBindingRepo) queryBindings(ctx context.Context, where string, args ...any) ([]*domain.MCPProjectBinding, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, project_id, profile_version_id, desired_enabled, required,
		selected_remote_tool_names_json, credential_refs_json, revision, created_at, updated_at
		FROM project_mcp_bindings `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []*domain.MCPProjectBinding
	for rows.Next() {
		b := &domain.MCPProjectBinding{}
		var selectedJSON, credJSON string
		var createdAt, updatedAt string
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.ProfileVersionID, &b.DesiredEnabled, &b.Required,
			&selectedJSON, &credJSON, &b.Revision, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		b.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		b.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		if err := json.Unmarshal([]byte(selectedJSON), &b.SelectedRemoteToolNames); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(credJSON), &b.CredentialRefs); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}

func validateCredentialRefMap(refs map[string]string) error {
	for name, ref := range refs {
		if name == "" || ref == "" {
			return fmt.Errorf("credential refs require non-empty env name and reference")
		}
		scheme, value, ok := strings.Cut(strings.TrimSpace(ref), ":")
		if !ok || value == "" || (scheme != "env" && scheme != "file" && scheme != "keyring") {
			return fmt.Errorf("invalid credential reference %q", ref)
		}
	}
	return nil
}

// FindProfileBySource locates an active profile by slug + source kind +
// project scope. Used by candidate discovery to match project-file candidates
// to already-materialized profiles.
func (r *MCPProfileRepo) FindProfileBySource(ctx context.Context, slug, sourceKind string, projectScope *string) (*domain.MCPServerProfile, error) {
	if r.FilePath != "" {
		profiles, err := r.fileListProfiles()
		if err != nil {
			return nil, err
		}
		for _, profile := range profiles {
			sameScope := profile.ProjectScope == nil && projectScope == nil || profile.ProjectScope != nil && projectScope != nil && *profile.ProjectScope == *projectScope
			if profile.Slug == slug && profile.SourceKind == sourceKind && sameScope {
				return profile, nil
			}
		}
		return nil, sql.ErrNoRows
	}
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

func (r *MCPProfileRepo) queryProfiles(ctx context.Context, where string, args ...any) ([]*domain.MCPServerProfile, error) {
	query := `SELECT p.id, p.display_name, p.slug, p.source_kind, p.project_scope,
		p.source_locator, p.lifecycle_status, p.created_at, p.updated_at,
		COALESCE(MAX(v.version), 0) AS latest
		FROM mcp_server_profiles p
		LEFT JOIN mcp_server_profile_versions v ON v.profile_id = p.id ` + where +
		` GROUP BY p.id ORDER BY p.display_name`
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []*domain.MCPServerProfile
	for rows.Next() {
		p := &domain.MCPServerProfile{}
		var scope sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.Slug, &p.SourceKind, &scope,
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
// digest, so candidate discovery can reuse an existing immutable version.
func (r *MCPProfileRepo) FindVersionByDigest(ctx context.Context, profileID, configDigest string) (*domain.MCPServerProfileVersion, error) {
	if r.FilePath != "" {
		versions, err := r.fileListVersions(profileID)
		if err != nil {
			return nil, err
		}
		for _, version := range versions {
			if version.ConfigDigest == configDigest {
				return version, nil
			}
		}
		return nil, sql.ErrNoRows
	}
	versions, err := r.queryVersions(ctx, `WHERE profile_id = ? AND config_digest = ?`, profileID, configDigest)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, sql.ErrNoRows
	}
	return versions[0], nil
}
