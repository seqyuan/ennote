package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
)

var (
	ErrFlowDraftConflict = errors.New("flow draft revision conflict")
	ErrFlowValidation    = errors.New("flow validation failed")
)

// --- Publish resolver wiring ---

// FlowPublishOptions wires the publish-time validator. When Sources/Models
// are set, role references resolve from the file-native Role catalog (V2);
// otherwise the legacy global role SQL is used.
type FlowPublishOptions struct {
	DB             *sql.DB
	ProjectID      string // optional project scope (project_file flows)
	FlowID         string // optional owning flow (flow-local role references)
	Skills         map[string]bool
	CheckAllowlist []string
	Sources        *globalsource.Store
	Models         *ModelRepo
}

// NewFlowValidator builds the publish validator.
func NewFlowValidator(opts FlowPublishOptions) *agentflow.Validator {
	return &agentflow.Validator{
		Resolver: &flowPublishResolver{
			db: opts.DB, projectID: opts.ProjectID, flowID: opts.FlowID, skills: opts.Skills,
			sources: opts.Sources, models: opts.Models,
		},
		CheckAllowlist: opts.CheckAllowlist,
		MaxFanOut:      64,
		MaxRounds:      100,
	}
}

type flowPublishResolver struct {
	db        *sql.DB
	projectID string
	flowID    string
	skills    map[string]bool
	sources   *globalsource.Store
	models    *ModelRepo
}

// ResolveRole resolves a task role reference at publish time. A version-qualified
// handle@version resolves against the shared catalog (project > global/builtin;
// flow-scoped roles never match). A bare handle is a flow-local reference: it
// resolves the owning flow's scope='flow' Role first, then falls back to the
// shared catalog's current version — mirroring FreezeFlowDefinition semantics so
// the same graph validates and freezes identically.
func (f *flowPublishResolver) ResolveRole(ctx context.Context, roleRef string) (*agentflow.RoleInfo, error) {
	handle, versionText, hasVersion := strings.Cut(strings.TrimSpace(roleRef), "@")
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return nil, fmt.Errorf("role reference %q has an empty handle", roleRef)
	}
	if f.sources != nil && f.models != nil {
		// V2: role references resolve from the file-native Role catalog.
		document, _, err := f.sources.ReadRoleRevision(handle, "v000001")
		if err != nil {
			return nil, fmt.Errorf("role %q is not published: %w", handle, err)
		}
		definition, diagnostics := (&RoleDiscovery{Models: f.models}).ResolveDocument(ctx, document)
		if definition == nil {
			if len(diagnostics) > 0 {
				return nil, fmt.Errorf("role %q: %s", handle, diagnostics[0].Message)
			}
			return nil, fmt.Errorf("role %q failed to resolve", handle)
		}
		return &agentflow.RoleInfo{Definition: *definition}, nil
	}
	if !hasVersion {
		// Bare handle: flow-local first, shared current fallback.
		if f.flowID != "" {
			var definitionJSON string
			err := f.db.QueryRowContext(ctx, `SELECT v.definition_json
				FROM agent_profiles p JOIN agent_profile_versions v ON v.id=p.current_version_id
				WHERE p.object_kind='role' AND p.handle=? AND p.scope='flow' AND p.flow_id=?
				  AND p.status='active' AND p.current_version_id IS NOT NULL`,
				handle, f.flowID).Scan(&definitionJSON)
			if err == nil {
				return f.decodeRole(definitionJSON)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		return f.resolveSharedCurrent(ctx, handle)
	}
	versionText = strings.TrimSpace(versionText)
	if versionText == "" {
		return nil, fmt.Errorf("role reference %q must be handle@version", roleRef)
	}
	var versionNumber int
	if _, err := fmt.Sscanf(versionText, "%d", &versionNumber); err != nil || versionNumber < 1 {
		return nil, fmt.Errorf("role reference %q has an invalid version", roleRef)
	}
	return f.resolveSharedVersion(ctx, handle, versionNumber)
}

func (f *flowPublishResolver) resolveSharedCurrent(ctx context.Context, handle string) (*agentflow.RoleInfo, error) {
	var definitionJSON string
	query := `SELECT v.definition_json FROM agent_profiles p
		JOIN agent_profile_versions v ON v.id=p.current_version_id
		WHERE p.object_kind='role' AND p.handle=? AND p.status='active'
		  AND p.scope!='flow' AND p.current_version_id IS NOT NULL
		  AND (p.project_id IS NULL OR p.project_id=?)
		ORDER BY CASE WHEN p.project_id=? THEN 0 WHEN p.scope='builtin' THEN 1 ELSE 2 END LIMIT 1`
	args := []any{handle, f.projectID, f.projectID}
	if f.projectID == "" {
		query = `SELECT v.definition_json FROM agent_profiles p
			JOIN agent_profile_versions v ON v.id=p.current_version_id
			WHERE p.object_kind='role' AND p.handle=? AND p.status='active'
			  AND p.scope!='flow' AND p.project_id IS NULL AND p.current_version_id IS NOT NULL LIMIT 1`
		args = []any{handle}
	}
	err := f.db.QueryRowContext(ctx, query, args...).Scan(&definitionJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("role %q is not published", handle)
	}
	if err != nil {
		return nil, err
	}
	return f.decodeRole(definitionJSON)
}

func (f *flowPublishResolver) resolveSharedVersion(ctx context.Context, handle string, versionNumber int) (*agentflow.RoleInfo, error) {
	var definitionJSON string
	var args []any
	query := `SELECT v.definition_json FROM agent_profiles p
		JOIN agent_profile_versions v ON v.agent_profile_id=p.id
		WHERE p.object_kind='role' AND p.handle=? AND v.version=? AND v.status='published'
		  AND p.status='active' AND p.scope!='flow' AND (p.project_id IS NULL OR p.project_id=?)
		ORDER BY CASE WHEN p.project_id=? THEN 0 WHEN p.scope='builtin' THEN 1 ELSE 2 END LIMIT 1`
	args = []any{handle, versionNumber, f.projectID, f.projectID}
	if f.projectID == "" {
		// Managed (global) flows may only reference global/builtin Roles.
		query = `SELECT v.definition_json FROM agent_profiles p
			JOIN agent_profile_versions v ON v.agent_profile_id=p.id
			WHERE p.object_kind='role' AND p.handle=? AND v.version=? AND v.status='published'
			  AND p.status='active' AND p.scope!='flow' AND p.project_id IS NULL LIMIT 1`
		args = []any{handle, versionNumber}
	}
	err := f.db.QueryRowContext(ctx, query, args...).Scan(&definitionJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("role %q is not published", fmt.Sprintf("%s@%d", handle, versionNumber))
	}
	if err != nil {
		return nil, err
	}
	return f.decodeRole(definitionJSON)
}

func (f *flowPublishResolver) decodeRole(definitionJSON string) (*agentflow.RoleInfo, error) {
	var roleDef domain.RoleDefinition
	if err := json.Unmarshal([]byte(definitionJSON), &roleDef); err != nil {
		return nil, fmt.Errorf("decode Role definition: %w", err)
	}
	return &agentflow.RoleInfo{Definition: roleDef}, nil
}

func (f *flowPublishResolver) KnownSkill(ctx context.Context, name string) bool {
	return f.skills[name]
}

// toolReadOnlyKind reports whether a tool can never mutate the workspace.
// MCP remote tools and unknown tools fail closed (not read-only).
func toolReadOnlyKind(tool string) bool {
	switch tool {
	case "read", "ls", "grep", "find", "git_readonly", "search_compacted_history", "todo":
		return true
	default:
		return false
	}
}

// ToolReadOnly resolves a tool's fan_out safety.
func (f *flowPublishResolver) ToolReadOnly(ctx context.Context, tool string) bool {
	return toolReadOnlyKind(tool)
}

// roleDefinitionIsReadOnly reports whether a frozen Role definition is fully
// read-only (authority + every allowed tool), the same classification the
// publish validator applies to fan_out and to writer-scope accounting.
func roleDefinitionIsReadOnly(roleDef domain.RoleDefinition) bool {
	if roleDef.Authority != domain.RoleAuthorityReadOnly {
		return false
	}
	for _, tool := range roleDef.AllowedTools {
		if !toolReadOnlyKind(tool) {
			return false
		}
	}
	return true
}
