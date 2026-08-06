package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var (
	ErrFlowDraftConflict = errors.New("flow draft revision conflict")
	ErrFlowValidation    = errors.New("flow validation failed")
)

// UpdateDraft stores the canonical draft (definition JSON + YAML text) and
// bumps the revision with optimistic locking. The draft is the authoring
// transport; nothing here publishes or executes.
func (r *AgentFlowProfileRepo) UpdateDraft(ctx context.Context, profileID string,
	def *domain.FlowDefinition, yamlText string, expectedRevision int) (*domain.AgentFlowProfile, error) {
	encoded, err := json.Marshal(def)
	if err != nil {
		return nil, fmt.Errorf("encode flow draft: %w", err)
	}
	res, err := r.DB.ExecContext(ctx, `UPDATE agent_flow_profiles SET
		draft_json=?, draft_yaml=?, draft_revision=draft_revision+1, updated_at=?
		WHERE id=? AND lifecycle_status='active' AND draft_revision=?`,
		string(encoded), yamlText, roleTime(time.Now().UTC()), profileID, expectedRevision)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		_ = r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_flow_profiles WHERE id=?`, profileID).Scan(&exists)
		if exists == 0 {
			return nil, sql.ErrNoRows
		}
		return nil, ErrFlowDraftConflict
	}
	return r.GetProfile(ctx, profileID)
}

// FlowDraft is the stored authoring draft.
type FlowDraft struct {
	Definition domain.FlowDefinition
	YAML       string
	Revision   int
}

// GetDraft loads the stored draft of a profile.
func (r *AgentFlowProfileRepo) GetDraft(ctx context.Context, profileID string) (*FlowDraft, error) {
	var definitionJSON, yamlText sql.NullString
	var revision int
	if err := r.DB.QueryRowContext(ctx, `SELECT draft_json, draft_yaml, draft_revision
		FROM agent_flow_profiles WHERE id=? AND lifecycle_status='active'`, profileID).
		Scan(&definitionJSON, &yamlText, &revision); err != nil {
		return nil, err
	}
	if !definitionJSON.Valid {
		return nil, sql.ErrNoRows
	}
	draft := &FlowDraft{Revision: revision}
	if yamlText.Valid {
		draft.YAML = yamlText.String
	}
	if err := json.Unmarshal([]byte(definitionJSON.String), &draft.Definition); err != nil {
		return nil, fmt.Errorf("decode flow draft: %w", err)
	}
	return draft, nil
}

// ValidateDraft runs the full publish validation against the stored draft.
func (r *AgentFlowProfileRepo) ValidateDraft(ctx context.Context, profileID string,
	validator *agentflow.Validator) (*agentflow.ValidationResult, error) {
	draft, err := r.GetDraft(ctx, profileID)
	if err != nil {
		return nil, err
	}
	result := validator.Validate(ctx, &draft.Definition)
	return result, nil
}

// Publish validates the stored draft and creates the next immutable version.
// The draft is the transport; the published version is the frozen contract.
func (r *AgentFlowProfileRepo) Publish(ctx context.Context, profileID string, expectedRevision int,
	validator *agentflow.Validator) (*domain.AgentFlowVersion, error) {
	draft, err := r.GetDraft(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if draft.Revision != expectedRevision {
		return nil, ErrFlowDraftConflict
	}
	result := validator.Validate(ctx, &draft.Definition)
	if !result.Valid {
		if len(result.Diagnostics) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrFlowValidation, "flow definition is invalid")
		}
		return nil, fmt.Errorf("%w: %s", ErrFlowValidation, result.Diagnostics[0].Code)
	}
	version, err := r.CreateVersion(ctx, profileID, &draft.Definition)
	if err != nil {
		return nil, err
	}
	return version, nil
}

// --- Publish resolver wiring ---

// FlowPublishOptions wires the publish-time validator against live SQL.
type FlowPublishOptions struct {
	DB             *sql.DB
	ProjectID      string // optional project scope (project_file flows)
	Skills         map[string]bool
	CheckAllowlist []string
}

// NewFlowValidator builds the publish validator with a SQL-backed resolver.
func NewFlowValidator(opts FlowPublishOptions) *agentflow.Validator {
	return &agentflow.Validator{
		Resolver:       &flowPublishResolver{db: opts.DB, projectID: opts.ProjectID, skills: opts.Skills},
		CheckAllowlist: opts.CheckAllowlist,
		MaxFanOut:      64,
		MaxRounds:      100,
	}
}

type flowPublishResolver struct {
	db        *sql.DB
	projectID string
	skills    map[string]bool
}

func (f *flowPublishResolver) ResolveRole(ctx context.Context, roleRef string) (*agentflow.RoleInfo, error) {
	handle, versionText, ok := strings.Cut(strings.TrimSpace(roleRef), "@")
	if !ok || strings.TrimSpace(handle) == "" || strings.TrimSpace(versionText) == "" {
		return nil, fmt.Errorf("role reference %q must be handle@version", roleRef)
	}
	var versionNumber int
	if _, err := fmt.Sscanf(versionText, "%d", &versionNumber); err != nil || versionNumber < 1 {
		return nil, fmt.Errorf("role reference %q has an invalid version", roleRef)
	}
	var definitionJSON string
	var args []any
	query := `SELECT v.definition_json FROM agent_profiles p
		JOIN agent_profile_versions v ON v.agent_profile_id=p.id
		WHERE p.object_kind='role' AND p.handle=? AND v.version=? AND v.status='published'
		  AND p.status='active' AND (p.project_id IS NULL OR p.project_id=?)
		ORDER BY CASE WHEN p.project_id=? THEN 0 WHEN p.scope='builtin' THEN 1 ELSE 2 END LIMIT 1`
	args = []any{handle, versionNumber, f.projectID, f.projectID}
	if f.projectID == "" {
		// Managed (global) flows may only reference global/builtin Roles.
		query = `SELECT v.definition_json FROM agent_profiles p
			JOIN agent_profile_versions v ON v.agent_profile_id=p.id
			WHERE p.object_kind='role' AND p.handle=? AND v.version=? AND v.status='published'
			  AND p.status='active' AND p.project_id IS NULL LIMIT 1`
		args = []any{handle, versionNumber}
	}
	err := f.db.QueryRowContext(ctx, query, args...).Scan(&definitionJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("role %q is not published", roleRef)
	}
	if err != nil {
		return nil, err
	}
	var roleDef domain.RoleDefinition
	if err := json.Unmarshal([]byte(definitionJSON), &roleDef); err != nil {
		return nil, fmt.Errorf("decode Role definition: %w", err)
	}
	return &agentflow.RoleInfo{Definition: roleDef}, nil
}

func (f *flowPublishResolver) KnownSkill(ctx context.Context, name string) bool {
	return f.skills[name]
}

// ToolReadOnly resolves a tool's fan_out safety. MCP remote tools and unknown
// tools fail closed (not read-only).
func (f *flowPublishResolver) ToolReadOnly(ctx context.Context, tool string) bool {
	switch tool {
	case "read", "ls", "grep", "find", "git_readonly", "search_compacted_history", "todo":
		return true
	default:
		return false
	}
}

// CheckFlowDependencies resolves a flow definition's dependency manifest
// against the target environment (global or project scope). Missing
// dependencies are reported with reasons and never installed.
func CheckFlowDependencies(ctx context.Context, opts FlowPublishOptions, def *domain.FlowDefinition) ([]agentflow.DependencyStatus, error) {
	resolver := &flowPublishResolver{db: opts.DB, projectID: opts.ProjectID, skills: opts.Skills}
	return agentflow.CheckDependencies(ctx, resolver, def), nil
}
