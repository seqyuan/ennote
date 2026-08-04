package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var (
	ErrRoleNotFound        = errors.New("role not found")
	ErrRoleDraftConflict   = errors.New("role draft revision conflict")
	ErrRoleValidation      = errors.New("role validation failed")
	ErrRoleVersionNotFound = errors.New("role version not found")
)

type RoleRepo struct {
	DB          *sql.DB
	KnownTools  map[string]bool
	KnownSkills map[string]bool
}

type CreateRoleInput struct {
	Handle      string
	Name        string
	Description string
	Positioning string
	Icon        string
	Color       string
	Scope       domain.RoleScope
	ProjectID   *string
	Definition  domain.RoleDefinition
}

type UpdateRoleDraftInput struct {
	ExpectedRevision int
	Handle           *string
	Name             *string
	Description      *string
	Positioning      *string
	Icon             *string
	Color            *string
	Definition       domain.RoleDefinition
}

type ListRolesInput struct {
	Query     string
	Scope     domain.RoleScope
	ProjectID *string
	Status    string
	Limit     int
	Cursor    string
}

func (r *RoleRepo) Create(ctx context.Context, input CreateRoleInput) (*domain.RoleIdentity, error) {
	input.Handle = strings.TrimSpace(input.Handle)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Positioning = strings.TrimSpace(input.Positioning)
	input.Icon = strings.TrimSpace(input.Icon)
	input.Color = strings.TrimSpace(input.Color)
	if input.Name == "" || input.Handle == "" {
		return nil, fmt.Errorf("role name and handle are required")
	}
	if input.Scope == "" {
		input.Scope = domain.RoleScopeGlobal
	}
	if input.Icon == "" {
		input.Icon = "bot"
	}
	if input.Color == "" {
		input.Color = "neutral"
	}
	definition := normalizeRoleDefinition(input.Definition)
	encoded, err := json.Marshal(definition)
	if err != nil {
		return nil, fmt.Errorf("encode role draft: %w", err)
	}
	now := time.Now().UTC()
	role := &domain.RoleIdentity{
		ID: uuid.NewString(), Handle: input.Handle, Name: input.Name, Description: input.Description,
		Positioning: input.Positioning, Icon: input.Icon, Color: input.Color, Scope: input.Scope,
		ProjectID: input.ProjectID, Status: "active", Draft: encoded, DraftRevision: 0,
		DelegationEnabled: true, CreatedAt: now, UpdatedAt: now,
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO agent_profiles
		(id,name,description,system_prompt,tool_policy,status,created_at,updated_at,
		 object_kind,handle,scope,project_id,icon,color,positioning,draft_json,draft_revision,
		 delegation_enabled,delegation_revocation_epoch)
		VALUES(?,?,?,'','default','active',?,?, 'role',?,?,?,?,?,?,?,0,1,0)`,
		role.ID, role.Name, role.Description, roleTime(now), roleTime(now), role.Handle,
		role.Scope, nullableString(role.ProjectID), role.Icon, role.Color, role.Positioning, string(encoded))
	if err != nil {
		return nil, fmt.Errorf("create role: %w", err)
	}
	return role, nil
}

func (r *RoleRepo) Get(ctx context.Context, roleID string) (*domain.RoleIdentity, error) {
	role, err := scanRoleIdentity(r.DB.QueryRowContext(ctx, roleIdentitySelect+` WHERE p.id=? AND p.object_kind='role'`, strings.TrimSpace(roleID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRoleNotFound
	}
	if err != nil {
		return nil, err
	}
	return &role, nil
}

func (r *RoleRepo) List(ctx context.Context, input ListRolesInput) ([]domain.RoleSummary, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = "active"
	}
	query := `%` + strings.ToLower(strings.TrimSpace(input.Query)) + `%`
	projectID := ""
	if input.ProjectID != nil {
		projectID = *input.ProjectID
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT p.id,p.handle,p.name,p.description,p.positioning,p.icon,p.color,p.scope,p.project_id,
		p.status,p.current_version_id,COALESCE(v.version,0),p.updated_at
		FROM agent_profiles p LEFT JOIN agent_profile_versions v ON v.id=p.current_version_id
		WHERE p.object_kind='role' AND p.status=?
		  AND (?='' OR lower(p.handle) LIKE ? OR lower(p.name) LIKE ? OR lower(p.positioning) LIKE ?)
		  AND (?='' OR p.scope=?)
		  AND (p.scope IN ('builtin','global') OR p.project_id=?)
		  AND (?='' OR p.id>?)
		ORDER BY p.id LIMIT ?`, status, strings.TrimSpace(input.Query), query, query, query,
		string(input.Scope), string(input.Scope), projectID, input.Cursor, input.Cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.RoleSummary, 0)
	for rows.Next() {
		var item domain.RoleSummary
		var project, current sql.NullString
		var updated string
		if err := rows.Scan(&item.ID, &item.Handle, &item.Name, &item.Description, &item.Positioning,
			&item.Icon, &item.Color, &item.Scope, &project, &item.Status, &current, &item.CurrentVersion, &updated); err != nil {
			return nil, err
		}
		if project.Valid {
			item.ProjectID = &project.String
		}
		if current.Valid {
			item.CurrentVersionID = &current.String
		}
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *RoleRepo) UpdateDraft(ctx context.Context, roleID string, input UpdateRoleDraftInput) (*domain.RoleIdentity, error) {
	current, err := r.Get(ctx, roleID)
	if err != nil {
		return nil, err
	}
	definition := normalizeRoleDefinition(input.Definition)
	encoded, err := json.Marshal(definition)
	if err != nil {
		return nil, fmt.Errorf("encode role draft: %w", err)
	}
	handle, name := current.Handle, current.Name
	description, positioning, icon, color := current.Description, current.Positioning, current.Icon, current.Color
	if input.Handle != nil {
		handle = strings.TrimSpace(*input.Handle)
	}
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if input.Description != nil {
		description = strings.TrimSpace(*input.Description)
	}
	if input.Positioning != nil {
		positioning = strings.TrimSpace(*input.Positioning)
	}
	if input.Icon != nil {
		icon = strings.TrimSpace(*input.Icon)
	}
	if input.Color != nil {
		color = strings.TrimSpace(*input.Color)
	}
	if name == "" || handle == "" || icon == "" || color == "" {
		return nil, fmt.Errorf("role name, handle, icon, and color are required")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE agent_profiles SET handle=?,name=?,description=?,positioning=?,icon=?,color=?,
		draft_json=?,draft_revision=draft_revision+1,updated_at=?
		WHERE id=? AND object_kind='role' AND status='active' AND draft_revision=?`,
		handle, name, description, positioning, icon, color, string(encoded), roleTime(time.Now().UTC()), roleID, input.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		var exists int
		_ = r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_profiles WHERE id=? AND object_kind='role'`, roleID).Scan(&exists)
		if exists == 0 {
			return nil, ErrRoleNotFound
		}
		return nil, ErrRoleDraftConflict
	}
	return r.Get(ctx, roleID)
}

func (r *RoleRepo) Validate(ctx context.Context, roleID string) (domain.RoleValidationResult, error) {
	role, err := r.Get(ctx, roleID)
	if err != nil {
		return domain.RoleValidationResult{}, err
	}
	var definition domain.RoleDefinition
	if err := json.Unmarshal(role.Draft, &definition); err != nil {
		return domain.RoleValidationResult{}, fmt.Errorf("decode role draft: %w", err)
	}
	return r.validateDefinition(ctx, definition), nil
}

func (r *RoleRepo) Publish(ctx context.Context, roleID string, expectedRevision int) (*domain.RoleVersion, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var draftJSON string
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT draft_json,draft_revision FROM agent_profiles
		WHERE id=? AND object_kind='role' AND status='active'`, roleID).Scan(&draftJSON, &revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRoleNotFound
		}
		return nil, err
	}
	if revision != expectedRevision {
		return nil, ErrRoleDraftConflict
	}
	var definition domain.RoleDefinition
	if err := json.Unmarshal([]byte(draftJSON), &definition); err != nil {
		return nil, err
	}
	definition = normalizeRoleDefinition(definition)
	validation := r.validateDefinitionWithQuery(ctx, tx, definition)
	if !validation.Valid {
		return nil, fmt.Errorf("%w: %s", ErrRoleValidation, validationMessage(validation.Diagnostics))
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	var nextVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM agent_profile_versions WHERE agent_profile_id=?`, roleID).Scan(&nextVersion); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	version := &domain.RoleVersion{ID: uuid.NewString(), RoleID: roleID, Version: nextVersion,
		Definition: definition, ConfigDigest: roleDefinitionDigest(encoded), Status: "published", CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_profile_versions
		(id,agent_profile_id,version,definition_json,config_digest,status,created_at)
		VALUES(?,?,?,?,?,'published',?)`, version.ID, roleID, version.Version, string(encoded),
		version.ConfigDigest, roleTime(now)); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_profiles SET current_version_id=?,updated_at=?
		WHERE id=? AND draft_revision=?`, version.ID, roleTime(now), roleID, expectedRevision)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, ErrRoleDraftConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return version, nil
}

func (r *RoleRepo) GetVersion(ctx context.Context, roleID, versionID string) (*domain.RoleVersion, error) {
	return scanRoleVersion(r.DB.QueryRowContext(ctx, roleVersionSelect+` WHERE v.id=? AND v.agent_profile_id=?`, versionID, roleID))
}

func (r *RoleRepo) ListVersions(ctx context.Context, roleID string) ([]domain.RoleVersion, error) {
	rows, err := r.DB.QueryContext(ctx, roleVersionSelect+` WHERE v.agent_profile_id=? ORDER BY v.version DESC`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := make([]domain.RoleVersion, 0)
	for rows.Next() {
		version, err := scanRoleVersion(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, *version)
	}
	return versions, rows.Err()
}

func (r *RoleRepo) Archive(ctx context.Context, roleID string) error {
	result, err := r.DB.ExecContext(ctx, `UPDATE agent_profiles SET status='archived',updated_at=?
		WHERE id=? AND object_kind='role' AND status='active'`, roleTime(time.Now().UTC()), roleID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrRoleNotFound
	}
	return nil
}

type roleQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *RoleRepo) validateDefinition(ctx context.Context, definition domain.RoleDefinition) domain.RoleValidationResult {
	return r.validateDefinitionWithQuery(ctx, r.DB, normalizeRoleDefinition(definition))
}

func (r *RoleRepo) validateDefinitionWithQuery(ctx context.Context, queryer roleQueryer, definition domain.RoleDefinition) domain.RoleValidationResult {
	diagnostics := make([]domain.RoleValidationDiagnostic, 0)
	add := func(code, message, field string) {
		diagnostics = append(diagnostics, domain.RoleValidationDiagnostic{Level: "error", Code: code, Message: message, Field: field})
	}
	if definition.SchemaVersion != 1 {
		add("schema_version_unsupported", "Role definition schemaVersion must be 1.", "schemaVersion")
	}
	if strings.TrimSpace(definition.RolePrompt) == "" || len(definition.RolePrompt) > 65536 {
		add("role_prompt_invalid", "Role prompt must contain 1 to 65536 bytes.", "rolePrompt")
	}
	if definition.ModelBinding.Mode != domain.RoleModelFixed {
		add("model_binding_not_enabled", "Stage 2 execution currently requires a fixed model binding.", "modelBinding.mode")
	}
	if definition.ModelBinding.ModelProfileID == "" {
		add("model_required", "A fixed Role requires an active model profile.", "modelBinding.modelProfileId")
	} else {
		validateRoleModel(ctx, queryer, definition.ModelBinding.ModelProfileID, definition.ModelBinding.ThinkingEffort, add)
	}
	for _, fallback := range definition.ModelBinding.FallbackModelProfileIDs {
		validateRoleModel(ctx, queryer, fallback, definition.ModelBinding.ThinkingEffort, add)
	}
	for _, skill := range definition.Skills.Entries {
		if r.KnownSkills == nil {
			add("skill_catalog_unavailable", "Skill references cannot be published until the effective catalog is available.", "skills.entries")
		} else if !r.KnownSkills[skill.SkillID] {
			add("skill_not_found", "Referenced Skill is unavailable.", "skills.entries")
		}
	}
	for _, tool := range definition.AllowedTools {
		if r.KnownTools == nil || !r.KnownTools[tool] {
			add("tool_not_found", "Referenced tool is unavailable.", "allowedTools")
		}
		if definition.Authority == domain.RoleAuthorityReadOnly && roleToolRequiresMutation(tool) {
			add("authority_tool_conflict", "A read-only Role cannot allow mutation tools.", "allowedTools")
		}
	}
	if definition.Authority != domain.RoleAuthorityReadOnly && definition.Authority != domain.RoleAuthorityMutation {
		add("authority_invalid", "Authority must be read_only or mutation.", "authority")
	}
	if definition.PermissionCeiling != domain.PermissionDiscuss && definition.PermissionCeiling != domain.PermissionAsk && definition.PermissionCeiling != domain.PermissionAuto {
		add("permission_ceiling_invalid", "Permission ceiling is invalid.", "permissionCeiling")
	}
	allowedModes := make(map[domain.RoleContextMode]bool)
	for _, mode := range definition.ContextPolicy.AllowedModes {
		if mode != domain.RoleContextRoom && mode != domain.RoleContextReply &&
			mode != domain.RoleContextFresh && mode != domain.RoleContextTask {
			add("context_mode_invalid", "Context mode is invalid.", "contextPolicy.allowedModes")
		}
		allowedModes[mode] = true
	}
	if !allowedModes[definition.ContextPolicy.DefaultMode] {
		add("default_context_not_allowed", "Default context mode must be included in allowedModes.", "contextPolicy.defaultMode")
	}
	if definition.ContextPolicy.OwnExecutionContinuity != domain.RoleContinuityNone {
		add("role_private_continuity_not_enabled", "Stage 2 Roles must use ownExecutionContinuity=none.", "contextPolicy.ownExecutionContinuity")
	}
	if definition.DelegationPolicy.Admission != domain.DelegationDenied &&
		definition.DelegationPolicy.Admission != domain.DelegationApprovalRequired &&
		definition.DelegationPolicy.Admission != domain.DelegationAutoWithinBudget {
		add("delegation_admission_invalid", "Delegation admission is invalid.", "delegationPolicy.admission")
	}
	if len(definition.DelegationPolicy.AllowedCallerKinds) != 1 || definition.DelegationPolicy.AllowedCallerKinds[0] != "host" {
		add("delegation_caller_invalid", "Stage 2 allowedCallerKinds must be exactly [host].", "delegationPolicy.allowedCallerKinds")
	}
	strategies := make(map[string]bool)
	for _, strategy := range definition.DelegationPolicy.AllowedStrategies {
		if strategy != string(domain.DelegationStrategySingle) && strategy != string(domain.DelegationStrategyParallel) {
			add("delegation_strategy_invalid", "Delegation strategy must be single or parallel.", "delegationPolicy.allowedStrategies")
		}
		strategies[strategy] = true
	}
	if definition.DelegationPolicy.Admission != domain.DelegationDenied && len(strategies) == 0 {
		add("delegation_strategy_required", "Delegation-enabled Roles require at least one strategy.", "delegationPolicy.allowedStrategies")
	}
	if definition.Authority == domain.RoleAuthorityMutation && strategies[string(domain.DelegationStrategyParallel)] {
		add("mutation_parallel_forbidden", "Mutation Roles cannot allow parallel delegation in Hosted V1.", "delegationPolicy.allowedStrategies")
	}
	if definition.DelegationPolicy.MaxInvocationsPerParentRun < 1 {
		add("delegation_invocation_limit_invalid", "Delegation invocation limit must be positive.", "delegationPolicy.maxInvocationsPerParentRun")
	}
	if definition.DelegationPolicy.MaxConcurrentInstances < 1 {
		add("delegation_concurrency_limit_invalid", "Delegation concurrency limit must be positive.", "delegationPolicy.maxConcurrentInstances")
	}
	budget := definition.DelegationPolicy.BudgetCeiling
	if budget.MaxModelCalls < 1 || budget.MaxModelCalls > 64 ||
		budget.MaxToolCalls < 1 || budget.MaxToolCalls > 256 ||
		budget.MaxTotalTokens < 1 || budget.MaxTotalTokens > 2_000_000 ||
		budget.MaxOutputTokens < 1 || budget.MaxOutputTokens > 131_072 ||
		budget.MaxWallTimeMS < 1 || budget.MaxWallTimeMS > 1_800_000 ||
		budget.MaxCostUSDMicros < 0 || budget.MaxCostUSDMicros > 100_000_000 {
		add("delegation_budget_invalid", "Delegation model, tool, token, output, and wall ceilings must be positive; cost cannot be negative.", "delegationPolicy.budgetCeiling")
	}
	if definition.MaxLoopIterations < 1 || definition.MaxLoopIterations > 64 {
		add("loop_limit_invalid", "maxLoopIterations must be between 1 and 64.", "maxLoopIterations")
	}
	if definition.OutputContract != "text-v1" && definition.OutputContract != "structured-v1" {
		add("output_contract_invalid", "Output contract must be text-v1 or structured-v1.", "outputContract")
	}
	encoded, _ := json.Marshal(definition)
	result := domain.RoleValidationResult{Valid: len(diagnostics) == 0, Diagnostics: diagnostics}
	if result.Valid {
		result.ConfigDigest = roleDefinitionDigest(encoded)
	}
	return result
}

func roleToolRequiresMutation(name string) bool {
	switch name {
	case "write", "edit", "exec", "bash", "publish_artifact":
		return true
	default:
		return false
	}
}

func validateRoleModel(ctx context.Context, queryer roleQueryer, modelID string, effort domain.ThinkingEffort,
	add func(string, string, string)) {
	var effortsJSON, credentialRef string
	var status, providerStatus string
	if err := queryer.QueryRowContext(ctx, `SELECT m.supported_thinking_efforts_json,m.status,p.credential_ref,p.status
		FROM model_profiles m JOIN provider_profiles p ON p.id=m.provider_id WHERE m.id=?`, modelID).
		Scan(&effortsJSON, &status, &credentialRef, &providerStatus); err != nil || status != "active" || providerStatus != "active" {
		add("model_not_found", "Referenced model and Provider must be active.", "modelBinding.modelProfileId")
		return
	}
	if err := validateCredentialReference(credentialRef); err != nil {
		add("model_credential_invalid", "Referenced Provider credential configuration is invalid.", "modelBinding.modelProfileId")
	}
	var efforts []domain.ThinkingEffort
	_ = json.Unmarshal([]byte(effortsJSON), &efforts)
	if effort == "" {
		effort = domain.ThinkingDefault
	}
	found := false
	for _, candidate := range efforts {
		found = found || candidate == effort
	}
	if !found {
		add("thinking_effort_unsupported", "Selected thinking effort is not supported by the model profile.", "modelBinding.thinkingEffort")
	}
}

func normalizeRoleDefinition(value domain.RoleDefinition) domain.RoleDefinition {
	if value.ModelBinding.ThinkingEffort == "" {
		value.ModelBinding.ThinkingEffort = domain.ThinkingDefault
	}
	value.ModelBinding.FallbackModelProfileIDs = sortedUnique(value.ModelBinding.FallbackModelProfileIDs)
	value.ModelBinding.OverridableFields = sortedUnique(value.ModelBinding.OverridableFields)
	value.AllowedTools = sortedUnique(value.AllowedTools)
	sort.Slice(value.Skills.Entries, func(i, j int) bool {
		if value.Skills.Entries[i].SkillID == value.Skills.Entries[j].SkillID {
			return value.Skills.Entries[i].Mode < value.Skills.Entries[j].Mode
		}
		return value.Skills.Entries[i].SkillID < value.Skills.Entries[j].SkillID
	})
	sort.Slice(value.ContextPolicy.AllowedModes, func(i, j int) bool { return value.ContextPolicy.AllowedModes[i] < value.ContextPolicy.AllowedModes[j] })
	value.DelegationPolicy.AllowedCallerKinds = sortedUnique(value.DelegationPolicy.AllowedCallerKinds)
	value.DelegationPolicy.AllowedStrategies = sortedUnique(value.DelegationPolicy.AllowedStrategies)
	return value
}

func sortedUnique(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) == 0 {
		return []string{}
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

func roleDefinitionDigest(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validationMessage(values []domain.RoleValidationDiagnostic) string {
	if len(values) == 0 {
		return "invalid role definition"
	}
	return values[0].Code
}

const roleIdentitySelect = `SELECT p.id,p.handle,p.name,p.description,p.positioning,p.icon,p.color,p.scope,p.project_id,
	p.status,p.draft_json,p.draft_revision,p.current_version_id,COALESCE(v.version,0),p.delegation_enabled,
	p.delegation_revocation_epoch,p.delegation_disabled_at,p.created_at,p.updated_at
	FROM agent_profiles p LEFT JOIN agent_profile_versions v ON v.id=p.current_version_id`

func scanRoleIdentity(scanner interface{ Scan(...any) error }) (domain.RoleIdentity, error) {
	var role domain.RoleIdentity
	var projectID, currentVersionID, disabledAt sql.NullString
	var draftJSON, createdAt, updatedAt string
	var delegationEnabled int
	if err := scanner.Scan(&role.ID, &role.Handle, &role.Name, &role.Description, &role.Positioning,
		&role.Icon, &role.Color, &role.Scope, &projectID, &role.Status, &draftJSON, &role.DraftRevision,
		&currentVersionID, &role.CurrentVersion, &delegationEnabled, &role.DelegationRevocationEpoch,
		&disabledAt, &createdAt, &updatedAt); err != nil {
		return role, err
	}
	role.Draft = json.RawMessage(draftJSON)
	if projectID.Valid {
		role.ProjectID = &projectID.String
	}
	if currentVersionID.Valid {
		role.CurrentVersionID = &currentVersionID.String
	}
	if disabledAt.Valid {
		value, err := time.Parse(time.RFC3339Nano, disabledAt.String)
		if err != nil {
			return role, err
		}
		role.DelegationDisabledAt = &value
	}
	role.DelegationEnabled = delegationEnabled != 0
	role.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	role.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return role, nil
}

const roleVersionSelect = `SELECT v.id,v.agent_profile_id,v.version,v.definition_json,v.config_digest,v.status,v.created_at
	FROM agent_profile_versions v`

func scanRoleVersion(scanner interface{ Scan(...any) error }) (*domain.RoleVersion, error) {
	var version domain.RoleVersion
	var definitionJSON, createdAt string
	if err := scanner.Scan(&version.ID, &version.RoleID, &version.Version, &definitionJSON,
		&version.ConfigDigest, &version.Status, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRoleVersionNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(definitionJSON), &version.Definition); err != nil {
		return nil, err
	}
	version.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &version, nil
}

func roleTime(value time.Time) string { return value.Format(time.RFC3339Nano) }

func nullableString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}
