package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var (
	ErrDelegationGroupNotFound   = errors.New("delegation group not found")
	ErrDelegationItemNotFound    = errors.New("delegation item not found")
	ErrDelegationGroupExists     = errors.New("delegation group already exists for parent tool call")
	ErrDelegationConflict        = errors.New("delegation state conflict")
	ErrDelegationBudgetExceeded  = errors.New("delegation budget exceeded")
	ErrDelegationBudgetReserved  = errors.New("delegation budget already reserved")
	ErrDelegationNoOwnedChildren = errors.New("parent owns no non-terminal children")
	ErrDelegationNotAuthorized   = errors.New("delegation is not authorized")
	ErrDelegationRoleUnavailable = errors.New("delegation Role is unavailable")
)

type DelegationRepo struct{ DB *sql.DB }

type CreateDelegationItemInput struct {
	Name           string
	RoleVersionID  string
	AssignmentJSON json.RawMessage
	OutputContract string
	Budget         domain.BudgetCeilingJSON
}

type CreateDelegationGroupInput struct {
	ParentRunID      string
	ParentToolCallID string
	Strategy         domain.DelegationStrategy
	Items            []CreateDelegationItemInput
	// AdmissionApproved is true only when an approved tool checkpoint contains
	// this exact parent Provider-visible tool call id.
	AdmissionApproved bool
}

type DelegationRoleSnapshot struct {
	RoleID            string
	VersionID         string
	Scope             domain.RoleScope
	ProjectID         *string
	Definition        domain.RoleDefinition
	DelegationEnabled bool
}

// ResolveRoleForDelegation resolves a handle inside the Session's project
// boundary. Project-scoped Roles override global/builtin Roles with the same
// handle; Roles from other projects are never candidates.
func (r *DelegationRepo) ResolveRoleForDelegation(ctx context.Context, sessionID, handle string) (*DelegationRoleSnapshot, error) {
	var projectID string
	if err := r.DB.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id=?`, sessionID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	var snapshot DelegationRoleSnapshot
	var roleProject, definitionJSON sql.NullString
	var enabled int
	err := r.DB.QueryRowContext(ctx, `SELECT p.id,p.current_version_id,p.scope,p.project_id,v.definition_json,p.delegation_enabled
		FROM agent_profiles p JOIN agent_profile_versions v ON v.id=p.current_version_id
		WHERE p.object_kind='role' AND p.handle=? AND p.status='active'
		  AND p.current_version_id IS NOT NULL AND (p.project_id=? OR p.project_id IS NULL)
		ORDER BY CASE WHEN p.project_id=? THEN 0 WHEN p.scope='builtin' THEN 1 ELSE 2 END LIMIT 1`,
		strings.TrimSpace(handle), projectID, projectID).Scan(&snapshot.RoleID, &snapshot.VersionID,
		&snapshot.Scope, &roleProject, &definitionJSON, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDelegationRoleUnavailable
	}
	if err != nil {
		return nil, err
	}
	if roleProject.Valid {
		snapshot.ProjectID = &roleProject.String
	}
	snapshot.DelegationEnabled = enabled == 1
	if err := json.Unmarshal([]byte(definitionJSON.String), &snapshot.Definition); err != nil {
		return nil, fmt.Errorf("decode published Role definition: %w", err)
	}
	return &snapshot, nil
}

// DelegationToolCallApproved reports whether user approval covered this exact
// Provider-visible delegate_roles call. A previous approval for another call
// or Run is never reusable.
func (r *DelegationRepo) DelegationToolCallApproved(ctx context.Context, runID, toolCallID string) (bool, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT items_json FROM tool_approval_requests
		WHERE run_id=? AND status='approved' ORDER BY resolved_at DESC`, runID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return false, err
		}
		var items []domain.ApprovalItem
		if err := json.Unmarshal([]byte(encoded), &items); err != nil {
			return false, err
		}
		for _, item := range items {
			if item.ToolName == "delegate_roles" && item.ToolCallID == toolCallID {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

// CreateGroup persists a delegation group and its items in one transaction.
// The parent must be a running top-level Run; each item carries the frozen Role
// version and its own budget ceiling snapshot.
func (r *DelegationRepo) CreateGroup(ctx context.Context, input CreateDelegationGroupInput) (*domain.DelegationGroup, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	group, _, err := createDelegationGroupTx(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return group, nil
}

func createDelegationGroupTx(ctx context.Context, tx *sql.Tx,
	input CreateDelegationGroupInput) (*domain.DelegationGroup, []domain.DelegationItem, error) {
	if strings.TrimSpace(input.ParentRunID) == "" || strings.TrimSpace(input.ParentToolCallID) == "" {
		return nil, nil, fmt.Errorf("parent run and tool call ids are required")
	}
	if input.Strategy != domain.DelegationStrategySingle && input.Strategy != domain.DelegationStrategyParallel {
		return nil, nil, fmt.Errorf("unsupported delegation strategy %q", input.Strategy)
	}
	if len(input.Items) == 0 {
		return nil, nil, fmt.Errorf("delegation group requires at least one item")
	}
	var parentKind, parentStatus, parentSessionID, speakerJSON string
	var parentDepth int
	if err := tx.QueryRowContext(ctx, `SELECT run_kind,status,execution_depth,session_id,COALESCE(speaker_snapshot_json,'{}')
		FROM agent_runs WHERE id=?`, input.ParentRunID).
		Scan(&parentKind, &parentStatus, &parentDepth, &parentSessionID, &speakerJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrRunNotFound
		}
		return nil, nil, err
	}
	if parentKind != string(domain.RunKindAgent) {
		return nil, nil, fmt.Errorf("delegation parent must be an agent run")
	}
	if parentStatus != string(domain.RunRunning) {
		return nil, nil, fmt.Errorf("delegation parent must be running")
	}
	if parentDepth != 0 {
		return nil, nil, fmt.Errorf("V1 delegation is depth-one only (parent must be top-level)")
	}
	var speaker struct {
		Kind string `json:"kind"`
	}
	if speakerJSON != "" && speakerJSON != "{}" {
		if err := json.Unmarshal([]byte(speakerJSON), &speaker); err != nil {
			return nil, nil, fmt.Errorf("decode parent speaker snapshot: %w", err)
		}
	}
	if speaker.Kind == "role" {
		return nil, nil, fmt.Errorf("%w: V1 only permits Host callers", ErrDelegationNotAuthorized)
	}
	var parentProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id=?`, parentSessionID).Scan(&parentProjectID); err != nil {
		return nil, nil, err
	}

	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	groupID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_groups
		(id,parent_run_id,parent_tool_call_id,strategy,status,created_at)
		VALUES(?,?,?,?, 'pending', ?)`, groupID, input.ParentRunID, input.ParentToolCallID,
		string(input.Strategy), timestamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, nil, ErrDelegationGroupExists
		}
		return nil, nil, fmt.Errorf("create delegation group: %w", err)
	}
	items := make([]domain.DelegationItem, 0, len(input.Items))
	proposedByRole := make(map[string]int)
	for ordinal, item := range input.Items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, nil, fmt.Errorf("delegation item name is required")
		}
		if strings.TrimSpace(item.RoleVersionID) == "" {
			return nil, nil, fmt.Errorf("delegation item %s requires a Role version", name)
		}
		if err := validateDelegationRoleTx(ctx, tx, input, &item, parentProjectID, proposedByRole); err != nil {
			return nil, nil, fmt.Errorf("delegation item %s: %w", name, err)
		}
		if len(item.AssignmentJSON) == 0 {
			item.AssignmentJSON = json.RawMessage(`{}`)
		}
		if !json.Valid(item.AssignmentJSON) {
			return nil, nil, fmt.Errorf("delegation item %s assignment is not valid JSON", name)
		}
		outputContract := item.OutputContract
		if outputContract == "" {
			outputContract = "text-v1"
		}
		budgetJSON, err := json.Marshal(item.Budget)
		if err != nil {
			return nil, nil, err
		}
		itemID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_items
			(id,group_id,child_run_id,name,role_version_id,assignment_json,output_contract,budget_json,result_json,status,ordinal,created_at)
			VALUES(?,?,NULL,?,?,?,?,?,NULL,'pending',?,?)`,
			itemID, groupID, name, item.RoleVersionID, string(item.AssignmentJSON),
			outputContract, string(budgetJSON), ordinal, timestamp); err != nil {
			return nil, nil, fmt.Errorf("create delegation item %s: %w", name, err)
		}
		items = append(items, domain.DelegationItem{
			ID: itemID, GroupID: groupID, Name: name, RoleVersionID: item.RoleVersionID,
			AssignmentJSON: append(json.RawMessage(nil), item.AssignmentJSON...), OutputContract: outputContract,
			BudgetJSON: budgetJSON, Status: domain.DelegationItemPending, Ordinal: ordinal, CreatedAt: now,
		})
	}
	group := &domain.DelegationGroup{ID: groupID, ParentRunID: input.ParentRunID,
		ParentToolCallID: input.ParentToolCallID, Strategy: input.Strategy,
		Status: domain.DelegationGroupPending, CreatedAt: now}
	return group, items, nil
}

func validateDelegationRoleTx(ctx context.Context, tx *sql.Tx, input CreateDelegationGroupInput,
	item *CreateDelegationItemInput, parentProjectID string, proposedByRole map[string]int) error {
	var roleID, currentVersionID, scope, definitionJSON, versionStatus string
	var roleProject sql.NullString
	var enabled int
	err := tx.QueryRowContext(ctx, `SELECT p.id,COALESCE(p.current_version_id,''),p.scope,p.project_id,
		p.delegation_enabled,v.definition_json,v.status
		FROM agent_profile_versions v JOIN agent_profiles p ON p.id=v.agent_profile_id
		WHERE v.id=? AND p.object_kind='role' AND p.status='active'`, item.RoleVersionID).Scan(
		&roleID, &currentVersionID, &scope, &roleProject, &enabled, &definitionJSON, &versionStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDelegationRoleUnavailable
	}
	if err != nil {
		return err
	}
	if enabled != 1 || versionStatus != "published" || currentVersionID != item.RoleVersionID {
		return fmt.Errorf("%w: Role is disabled, unpublished, or no longer current", ErrDelegationNotAuthorized)
	}
	if (roleProject.Valid && roleProject.String != parentProjectID) ||
		(scope == string(domain.RoleScopeProject) && !roleProject.Valid) ||
		((scope == string(domain.RoleScopeGlobal) || scope == string(domain.RoleScopeBuiltin)) && roleProject.Valid) {
		return fmt.Errorf("%w: Role is outside the parent project", ErrDelegationNotAuthorized)
	}
	var definition domain.RoleDefinition
	if err := json.Unmarshal([]byte(definitionJSON), &definition); err != nil {
		return fmt.Errorf("decode Role policy: %w", err)
	}
	callerAllowed := false
	for _, caller := range definition.DelegationPolicy.AllowedCallerKinds {
		callerAllowed = callerAllowed || caller == "host"
	}
	if !callerAllowed {
		return fmt.Errorf("%w: Role does not allow Host callers", ErrDelegationNotAuthorized)
	}
	switch definition.DelegationPolicy.Admission {
	case domain.DelegationDenied:
		return fmt.Errorf("%w: Role denies Host delegation", ErrDelegationNotAuthorized)
	case domain.DelegationApprovalRequired:
		if !input.AdmissionApproved {
			return fmt.Errorf("%w: Role requires explicit delegation approval", ErrDelegationNotAuthorized)
		}
	case domain.DelegationAutoWithinBudget:
	default:
		return fmt.Errorf("%w: Role has an invalid admission policy", ErrDelegationNotAuthorized)
	}
	strategyAllowed := false
	for _, strategy := range definition.DelegationPolicy.AllowedStrategies {
		strategyAllowed = strategyAllowed || strategy == string(input.Strategy)
	}
	if !strategyAllowed {
		return fmt.Errorf("%w: Role does not allow %s strategy", ErrDelegationNotAuthorized, input.Strategy)
	}
	if definition.Authority == domain.RoleAuthorityMutation && input.Strategy != domain.DelegationStrategySingle {
		return fmt.Errorf("%w: mutation Roles require the single mutation lane", ErrDelegationNotAuthorized)
	}
	if item.OutputContract == "" {
		item.OutputContract = definition.OutputContract
	}
	if item.OutputContract != definition.OutputContract {
		return fmt.Errorf("%w: requested output contract exceeds the frozen Role contract", ErrDelegationNotAuthorized)
	}
	if err := validateDelegationBudget(item.Budget, definition.DelegationPolicy.BudgetCeiling); err != nil {
		return fmt.Errorf("%w: %v", ErrDelegationNotAuthorized, err)
	}

	proposedByRole[roleID]++
	var priorInvocations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM delegation_items i
		JOIN delegation_groups g ON g.id=i.group_id
		JOIN agent_profile_versions v ON v.id=i.role_version_id
		WHERE g.parent_run_id=? AND g.parent_tool_call_id<>? AND v.agent_profile_id=?`,
		input.ParentRunID, input.ParentToolCallID, roleID).Scan(&priorInvocations); err != nil {
		return err
	}
	if limit := definition.DelegationPolicy.MaxInvocationsPerParentRun; limit < 1 ||
		priorInvocations+proposedByRole[roleID] > limit {
		return fmt.Errorf("%w: Role invocation limit exceeded", ErrDelegationNotAuthorized)
	}
	var activeInstances int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs ar
		JOIN delegation_items i ON i.child_run_id=ar.id
		JOIN agent_profile_versions v ON v.id=i.role_version_id
		WHERE v.agent_profile_id=? AND ar.status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children')`,
		roleID).Scan(&activeInstances); err != nil {
		return err
	}
	if limit := definition.DelegationPolicy.MaxConcurrentInstances; limit < 1 ||
		activeInstances+proposedByRole[roleID] > limit {
		return fmt.Errorf("%w: Role concurrent instance limit exceeded", ErrDelegationNotAuthorized)
	}
	return nil
}

func validateDelegationBudget(request domain.BudgetCeilingJSON, ceiling domain.DelegationBudgetCeiling) error {
	if request.MaxModelCalls < 1 || request.MaxModelCalls > ceiling.MaxModelCalls ||
		request.MaxToolCalls < 1 || request.MaxToolCalls > ceiling.MaxToolCalls ||
		request.MaxTotalTokens < 1 || request.MaxTotalTokens > ceiling.MaxTotalTokens ||
		request.MaxOutputTokens < 1 || request.MaxOutputTokens > ceiling.MaxOutputTokens ||
		request.MaxWallTimeMS < 1 || request.MaxWallTimeMS > ceiling.MaxWallTimeMS ||
		request.MaxCostMicros < 0 || (ceiling.MaxCostUSDMicros > 0 && request.MaxCostMicros > ceiling.MaxCostUSDMicros) {
		return errors.New("requested budget exceeds the frozen Role ceiling")
	}
	return nil
}

// ensureRootDelegationBudgetTx lazily creates the root budget ledger row for
// the top-level root of a parent Run. It is idempotent and safe to call from
// every materialization path, including Runs that never froze an effective
// config snapshot (tests and legacy recovery paths).
func ensureRootDelegationBudgetTx(ctx context.Context, tx *sql.Tx, parentRunID string) error {
	var rootRunID, sessionID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(root_run_id,id),session_id FROM agent_runs WHERE id=?`,
		parentRunID).Scan(&rootRunID, &sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM delegation_root_budgets WHERE root_run_id=?`,
		rootRunID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	_, err := freezeDelegationPolicyTx(ctx, tx, rootRunID, sessionID)
	return err
}

// reserveRootBudgetTx CAS-reserves one delegation group's total ceiling and
// concurrent-child count against the root ledger. The UPDATE only affects one
// row when every dimension stays within the frozen maximum, so concurrent
// groups contend on the same envelope and the loser creates nothing.
func reserveRootBudgetTx(ctx context.Context, tx *sql.Tx, parentRunID string, itemCount int, total domain.BudgetCeilingJSON) error {
	var rootRunID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(root_run_id,id) FROM agent_runs WHERE id=?`,
		parentRunID).Scan(&rootRunID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE delegation_root_budgets SET
		reserved_model_calls=reserved_model_calls+?,
		reserved_tool_calls=reserved_tool_calls+?,
		reserved_total_tokens=reserved_total_tokens+?,
		reserved_output_tokens=reserved_output_tokens+?,
		reserved_cost_usd_micros=reserved_cost_usd_micros+?,
		active_children=active_children+?,
		version=version+1,
		updated_at=?
		WHERE root_run_id=?
		  AND consumed_model_calls+reserved_model_calls+?<=max_model_calls
		  AND consumed_tool_calls+reserved_tool_calls+?<=max_tool_calls
		  AND consumed_total_tokens+reserved_total_tokens+?<=max_total_tokens
		  AND consumed_output_tokens+reserved_output_tokens+?<=max_output_tokens
		  AND (max_cost_usd_micros=0 OR consumed_cost_usd_micros+reserved_cost_usd_micros+?<=max_cost_usd_micros)
		  AND active_children+?<=max_concurrent_children`,
		total.MaxModelCalls, total.MaxToolCalls, total.MaxTotalTokens, total.MaxOutputTokens, total.MaxCostMicros,
		itemCount, now, rootRunID,
		total.MaxModelCalls, total.MaxToolCalls, total.MaxTotalTokens, total.MaxOutputTokens, total.MaxCostMicros,
		itemCount)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("%w: root budget cannot admit the requested delegation", ErrDelegationBudgetExceeded)
	}
	return nil
}

// reconcileRootBudgetTx releases one child's reservation and folds its actual
// usage into the root ledger at most once. The child's run_budgets row is the
// idempotency key: replaying reconciliation never double-charges.
func reconcileRootBudgetTx(ctx context.Context, tx *sql.Tx, childRunID string) error {
	var reconciled sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT root_reconciled_at FROM run_budgets WHERE run_id=?`,
		childRunID).Scan(&reconciled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // no child budget row: nothing was reserved
		}
		return err
	}
	if reconciled.Valid {
		return nil
	}
	var rootRunID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(root_run_id,parent_run_id) FROM agent_runs WHERE id=?`,
		childRunID).Scan(&rootRunID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	}
	var maxModel, maxTool, consumedModel, consumedTool int
	var maxTokens, maxOutput, maxCost, consumedTokens, consumedOutput, consumedCost int64
	if err := tx.QueryRowContext(ctx, `SELECT max_model_calls,max_tool_calls,max_total_tokens,max_output_tokens,max_cost_usd_micros,
		consumed_model_calls,consumed_tool_calls,consumed_tokens,consumed_output_tokens,consumed_cost_usd_micros
		FROM run_budgets WHERE run_id=?`, childRunID).Scan(
		&maxModel, &maxTool, &maxTokens, &maxOutput, &maxCost,
		&consumedModel, &consumedTool, &consumedTokens, &consumedOutput, &consumedCost); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE delegation_root_budgets SET
		reserved_model_calls=MAX(0,reserved_model_calls-?),
		reserved_tool_calls=MAX(0,reserved_tool_calls-?),
		reserved_total_tokens=MAX(0,reserved_total_tokens-?),
		reserved_output_tokens=MAX(0,reserved_output_tokens-?),
		reserved_cost_usd_micros=MAX(0,reserved_cost_usd_micros-?),
		consumed_model_calls=consumed_model_calls+?,
		consumed_tool_calls=consumed_tool_calls+?,
		consumed_total_tokens=consumed_total_tokens+?,
		consumed_output_tokens=consumed_output_tokens+?,
		consumed_cost_usd_micros=consumed_cost_usd_micros+?,
		active_children=MAX(0,active_children-1),
		version=version+1,
		updated_at=?
		WHERE root_run_id=?`,
		maxModel, maxTool, maxTokens, maxOutput, maxCost,
		consumedModel, consumedTool, consumedTokens, consumedOutput, consumedCost,
		now, rootRunID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("root budget ledger is missing for %s", rootRunID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_budgets SET root_reconciled_at=? WHERE run_id=? AND root_reconciled_at IS NULL`,
		now, childRunID); err != nil {
		return err
	}
	return nil
}

func (r *DelegationRepo) GetGroup(ctx context.Context, groupID string) (*domain.DelegationGroup, error) {
	var group domain.DelegationGroup
	var createdAt string
	if err := r.DB.QueryRowContext(ctx, `SELECT id,parent_run_id,parent_tool_call_id,strategy,status,created_at
		FROM delegation_groups WHERE id=?`, groupID).Scan(&group.ID, &group.ParentRunID, &group.ParentToolCallID,
		&group.Strategy, &group.Status, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDelegationGroupNotFound
		}
		return nil, err
	}
	var err error
	group.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	return &group, nil
}

// GroupForParentToolCall returns the group created for a parent tool call
// (idempotency key for re-dispatch after restart).
func (r *DelegationRepo) GroupForParentToolCall(ctx context.Context, parentRunID, parentToolCallID string) (*domain.DelegationGroup, error) {
	var group domain.DelegationGroup
	var createdAt string
	err := r.DB.QueryRowContext(ctx, `SELECT id,parent_run_id,parent_tool_call_id,strategy,status,created_at
		FROM delegation_groups WHERE parent_run_id=? AND parent_tool_call_id=?`, parentRunID, parentToolCallID).
		Scan(&group.ID, &group.ParentRunID, &group.ParentToolCallID, &group.Strategy, &group.Status, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDelegationGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	group.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func (r *DelegationRepo) ListItems(ctx context.Context, groupID string) ([]domain.DelegationItem, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id,group_id,child_run_id,name,role_version_id,assignment_json,
		output_contract,budget_json,result_json,status,ordinal,created_at
		FROM delegation_items WHERE group_id=? ORDER BY ordinal`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDelegationItems(rows)
}

// ListActivity returns stable parent-visible group and child state without
// exposing private child transcripts or execution configuration.
func (r *DelegationRepo) ListActivity(ctx context.Context, parentRunID string) (*domain.DelegationActivityPage, error) {
	if strings.TrimSpace(parentRunID) == "" {
		return nil, fmt.Errorf("parent run id is required")
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT
		g.id,g.parent_tool_call_id,g.strategy,g.status,g.created_at,
		i.id,COALESCE(i.child_run_id,''),i.name,i.status,COALESCE(i.result_json,''),i.created_at,
		COALESCE(p.handle,''),COALESCE(p.name,''),COALESCE(ar.status,''),
		COALESCE(ar.speaker_snapshot_json,''),COALESCE(ar.error_code,''),COALESCE(ar.error_message,'')
		FROM delegation_groups g
		JOIN delegation_items i ON i.group_id=g.id
		JOIN agent_profile_versions v ON v.id=i.role_version_id
		JOIN agent_profiles p ON p.id=v.agent_profile_id
		LEFT JOIN agent_runs ar ON ar.id=i.child_run_id
		WHERE g.parent_run_id=?
		ORDER BY g.created_at,g.id,i.ordinal`, parentRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	page := &domain.DelegationActivityPage{ParentRunID: parentRunID, Groups: []domain.DelegationActivityGroup{}}
	groupIndexes := make(map[string]int)
	for rows.Next() {
		var groupID, parentToolCallID, groupCreatedAt string
		var strategy domain.DelegationStrategy
		var groupStatus domain.DelegationGroupStatus
		var child domain.DelegationChildActivity
		var itemCreatedAt, resultJSON, fallbackHandle, fallbackName, speakerJSON string
		if err := rows.Scan(&groupID, &parentToolCallID, &strategy, &groupStatus, &groupCreatedAt,
			&child.ItemID, &child.ChildRunID, &child.Name, &child.ItemStatus, &resultJSON, &itemCreatedAt,
			&fallbackHandle, &fallbackName, &child.RunStatus, &speakerJSON, &child.ErrorCode, &child.ErrorMessage); err != nil {
			return nil, err
		}
		child.RoleHandle, child.RoleDisplayName = fallbackHandle, fallbackName
		if speakerJSON != "" && speakerJSON != "{}" {
			var speaker struct {
				Handle      string `json:"handle"`
				DisplayName string `json:"displayName"`
			}
			if err := json.Unmarshal([]byte(speakerJSON), &speaker); err != nil {
				return nil, fmt.Errorf("decode child speaker snapshot for %s: %w", child.ChildRunID, err)
			}
			if speaker.Handle != "" {
				child.RoleHandle = speaker.Handle
			}
			if speaker.DisplayName != "" {
				child.RoleDisplayName = speaker.DisplayName
			}
		}
		if resultJSON != "" {
			var result domain.SubmitResult
			if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
				return nil, fmt.Errorf("decode child terminal result for %s: %w", child.ItemID, err)
			}
			child.Result = &result
		}
		child.CreatedAt, err = time.Parse(time.RFC3339Nano, itemCreatedAt)
		if err != nil {
			return nil, err
		}

		groupIndex, ok := groupIndexes[groupID]
		if !ok {
			createdAt, parseErr := time.Parse(time.RFC3339Nano, groupCreatedAt)
			if parseErr != nil {
				return nil, parseErr
			}
			groupIndex = len(page.Groups)
			groupIndexes[groupID] = groupIndex
			page.Groups = append(page.Groups, domain.DelegationActivityGroup{
				ID: groupID, ParentToolCallID: parentToolCallID, Strategy: strategy,
				Status: groupStatus, Children: []domain.DelegationChildActivity{}, CreatedAt: createdAt,
			})
		}
		page.Groups[groupIndex].Children = append(page.Groups[groupIndex].Children, child)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

func scanDelegationItems(rows *sql.Rows) ([]domain.DelegationItem, error) {
	items := make([]domain.DelegationItem, 0)
	for rows.Next() {
		var item domain.DelegationItem
		var childRun, assignment, budget, result sql.NullString
		var createdAt string
		if err := rows.Scan(&item.ID, &item.GroupID, &childRun, &item.Name, &item.RoleVersionID,
			&assignment, &item.OutputContract, &budget, &result, &item.Status, &item.Ordinal, &createdAt); err != nil {
			return nil, err
		}
		if childRun.Valid {
			item.ChildRunID = &childRun.String
		}
		item.AssignmentJSON = json.RawMessage(assignment.String)
		item.BudgetJSON = json.RawMessage(budget.String)
		if result.Valid {
			item.ResultJSON = json.RawMessage(result.String)
		}
		var err error
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// AssignChild links a created child Run to its delegation item. One child may
// be assigned to exactly one item (UNIQUE(child_run_id) backstop).
func (r *DelegationRepo) AssignChild(ctx context.Context, itemID, childRunID string) error {
	result, err := r.DB.ExecContext(ctx, `UPDATE delegation_items SET child_run_id=?, status='running'
		WHERE id=? AND child_run_id IS NULL AND status='pending'`, childRunID, itemID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("%w: item is already assigned or not pending", ErrDelegationConflict)
	}
	return nil
}

// CreateChildRunInput describes the materialization of one delegation item
// into a queued delegated_agent Run.
type CreateChildRunInput struct {
	ParentRunID string
	ItemID      string
	SessionID   string
}

// CreateChildRun atomically: validates the parent is running and owns the item,
// freezes the child's speaker snapshot from the exact Role version, inserts the
// delegated_agent Run (format 2, private_to_parent, depth=parent+1), assigns
// the item, and reserves the child budget row. All in one transaction.
func (r *DelegationRepo) CreateChildRun(ctx context.Context, input CreateChildRunInput) (*domain.AgentRun, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	child, err := createChildRunTx(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return child, nil
}

// CreateGroupWithChildren commits the group, all items, all child Runs, and
// every child budget reservation together. No partially materialized tree is
// visible if any Role snapshot, Run insert, or budget reservation fails.
func (r *DelegationRepo) CreateGroupWithChildren(ctx context.Context, input CreateDelegationGroupInput,
	sessionID string) (*domain.DelegationGroup, []domain.DelegationItem, []*domain.AgentRun, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()
	group, items, err := createDelegationGroupTx(ctx, tx, input)
	if err != nil {
		return nil, nil, nil, err
	}
	children := make([]*domain.AgentRun, 0, len(items))
	for index := range items {
		item := &items[index]
		child, childErr := createChildRunTx(ctx, tx, CreateChildRunInput{
			ParentRunID: input.ParentRunID, ItemID: item.ID, SessionID: sessionID,
		})
		if childErr != nil {
			return nil, nil, nil, childErr
		}
		item.ChildRunID = &child.ID
		item.Status = domain.DelegationItemRunning
		children = append(children, child)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	group.Status = domain.DelegationGroupWaitingChildren
	return group, items, children, nil
}

func createChildRunTx(ctx context.Context, tx *sql.Tx, input CreateChildRunInput) (*domain.AgentRun, error) {
	if strings.TrimSpace(input.ParentRunID) == "" || strings.TrimSpace(input.ItemID) == "" ||
		strings.TrimSpace(input.SessionID) == "" {
		return nil, fmt.Errorf("parent run, item, and session are required")
	}
	var parentStatus, parentRoot, parentSessionID string
	var parentDepth int
	if err := tx.QueryRowContext(ctx, `SELECT status,COALESCE(root_run_id,id),execution_depth,session_id FROM agent_runs WHERE id=?`,
		input.ParentRunID).Scan(&parentStatus, &parentRoot, &parentDepth, &parentSessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if parentStatus != string(domain.RunRunning) && parentStatus != string(domain.RunWaitingChildren) {
		return nil, fmt.Errorf("delegation parent must be running or materializing children")
	}
	if parentSessionID != input.SessionID {
		return nil, fmt.Errorf("%w: child session differs from parent", ErrDelegationConflict)
	}

	var item struct {
		GroupID        string
		RoleVersionID  string
		AssignmentJSON string
		OutputContract string
		BudgetJSON     string
		CurrentStatus  domain.DelegationItemStatus
	}
	if err := tx.QueryRowContext(ctx, `SELECT group_id,role_version_id,assignment_json,output_contract,budget_json,status
		FROM delegation_items WHERE id=? AND child_run_id IS NULL`, input.ItemID).
		Scan(&item.GroupID, &item.RoleVersionID, &item.AssignmentJSON, &item.OutputContract,
			&item.BudgetJSON, &item.CurrentStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: item is not assignable", ErrDelegationItemNotFound)
		}
		return nil, err
	}
	var groupParent string
	if err := tx.QueryRowContext(ctx, `SELECT parent_run_id FROM delegation_groups WHERE id=?`, item.GroupID).
		Scan(&groupParent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDelegationGroupNotFound
		}
		return nil, err
	}
	if groupParent != input.ParentRunID {
		return nil, fmt.Errorf("%w: item belongs to a different parent", ErrDelegationConflict)
	}

	var objectID, handle, displayName, configDigest string
	if err := tx.QueryRowContext(ctx, `SELECT p.id,p.handle,p.name,v.config_digest
		FROM agent_profile_versions v JOIN agent_profiles p ON p.id=v.agent_profile_id
		WHERE v.id=? AND v.status='published'`, item.RoleVersionID).
		Scan(&objectID, &handle, &displayName, &configDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("Role version is unavailable for delegation")
		}
		return nil, err
	}
	speakerJSON, err := json.Marshal(map[string]string{
		"kind": "role", "objectId": objectID, "versionId": item.RoleVersionID,
		"handle": handle, "displayName": displayName, "configDigest": configDigest,
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	childID := uuid.NewString()
	rootRunID := firstNonEmpty(parentRoot, input.ParentRunID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs
		(id,turn_id,session_id,run_kind,base_message_id,attempt,status,requested_config_json,
		 effective_config_json,speaker_snapshot_json,root_run_id,parent_run_id,execution_depth,publish_mode,
		 commit_format_version,context_snapshot_json,created_at)
		VALUES(?,NULL,?,'delegated_agent',NULL,1,'queued','{}','{}',?,?,?,?,'private_to_parent',2,'{}',?)`,
		childID, input.SessionID, string(speakerJSON), rootRunID, input.ParentRunID, parentDepth+1, timestamp); err != nil {
		return nil, fmt.Errorf("create child run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET child_run_id=?, status='running' WHERE id=?`,
		childID, input.ItemID); err != nil {
		return nil, fmt.Errorf("assign child run: %w", err)
	}
	var ceiling domain.BudgetCeilingJSON
	if err := json.Unmarshal([]byte(item.BudgetJSON), &ceiling); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_budgets
		(run_id,max_model_calls,max_tool_calls,max_total_tokens,max_output_tokens,max_cost_usd_micros,max_wall_time_ms,consumed_model_calls,consumed_tool_calls,consumed_tokens,reserved_at)
		VALUES(?,?,?,?,?,?,?,0,0,0,?)`,
		childID, ceiling.MaxModelCalls, ceiling.MaxToolCalls, ceiling.MaxTotalTokens,
		ceiling.MaxOutputTokens, ceiling.MaxCostMicros, ceiling.MaxWallTimeMS, timestamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrDelegationBudgetReserved
		}
		return nil, fmt.Errorf("reserve child budget: %w", err)
	}
	// Every child reservation contends on the root ledger of the top-level
	// Host Run. Two concurrent groups cannot each fit by accident: the losing
	// CAS rolls back this transaction entirely.
	if err := ensureRootDelegationBudgetTx(ctx, tx, input.ParentRunID); err != nil {
		return nil, err
	}
	if err := reserveRootBudgetTx(ctx, tx, input.ParentRunID, 1, ceiling); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET status='waiting_children' WHERE id=? AND status='pending'`,
		item.GroupID); err != nil {
		return nil, fmt.Errorf("activate delegation group: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='waiting_children'
		WHERE id=? AND status='running'`, input.ParentRunID); err != nil {
		return nil, fmt.Errorf("parent enters waiting_children: %w", err)
	}
	return &domain.AgentRun{
		ID: childID, SessionID: input.SessionID, RunKind: domain.RunKindDelegatedAgent, Attempt: 1,
		Status: domain.RunQueued, CommitFormatVersion: domain.CommitFormatSpeakerV2,
		ParentRunID: input.ParentRunID, RootRunID: rootRunID, ExecutionDepth: parentDepth + 1,
		PublishMode: domain.PublishPrivateToParent, SpeakerSnapshot: speakerJSON,
		ContextSnapshot: json.RawMessage(`{}`), RequestedConfig: json.RawMessage(`{}`),
		EffectiveConfig: json.RawMessage(`{}`), CreatedAt: now,
	}, nil
}

// ReserveChildBudget creates the child run_budgets ledger row exactly once (CAS
// via the primary key) from the item's frozen ceiling.
func (r *DelegationRepo) ReserveChildBudget(ctx context.Context, childRunID, itemID string) (*domain.RunBudget, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var budgetJSON string
	if err := tx.QueryRowContext(ctx, `SELECT budget_json FROM delegation_items WHERE id=?`, itemID).
		Scan(&budgetJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDelegationItemNotFound
		}
		return nil, err
	}
	var ceiling domain.BudgetCeilingJSON
	if err := json.Unmarshal([]byte(budgetJSON), &ceiling); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_budgets
		(run_id,max_model_calls,max_tool_calls,max_total_tokens,max_output_tokens,max_cost_usd_micros,max_wall_time_ms,consumed_model_calls,consumed_tool_calls,consumed_tokens,reserved_at)
		VALUES(?,?,?,?,?,?,?,0,0,0,?)`,
		childRunID, ceiling.MaxModelCalls, ceiling.MaxToolCalls, ceiling.MaxTotalTokens,
		ceiling.MaxOutputTokens, ceiling.MaxCostMicros, ceiling.MaxWallTimeMS, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrDelegationBudgetReserved
		}
		return nil, fmt.Errorf("reserve child budget: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.RunBudget{RunID: childRunID, MaxModelCalls: ceiling.MaxModelCalls,
		MaxToolCalls: ceiling.MaxToolCalls, MaxTotalTokens: ceiling.MaxTotalTokens,
		MaxOutputTokens: ceiling.MaxOutputTokens, MaxCostUSDMicros: ceiling.MaxCostMicros,
		MaxWallTimeMS: ceiling.MaxWallTimeMS, ReservedAt: time.Now().UTC()}, nil
}

// BeginBudget starts (or resumes) the wall clock and returns the remaining hard
// duration. The first child execution, not queue creation, starts wall time.
func (r *DelegationRepo) BeginBudget(ctx context.Context, runID string) (time.Duration, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var maxWall int64
	var started sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT max_wall_time_ms,started_at FROM run_budgets WHERE run_id=?`, runID).
		Scan(&maxWall, &started); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	if !started.Valid {
		started.String, started.Valid = now.Format(time.RFC3339Nano), true
		if _, err := tx.ExecContext(ctx, `UPDATE run_budgets SET started_at=? WHERE run_id=? AND started_at IS NULL`,
			started.String, runID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if maxWall == 0 {
		return 0, nil
	}
	startedAt, err := time.Parse(time.RFC3339Nano, started.String)
	if err != nil {
		return 0, err
	}
	remaining := time.Duration(maxWall)*time.Millisecond - now.Sub(startedAt)
	if remaining <= 0 {
		return 0, ErrDelegationBudgetExceeded
	}
	return remaining, nil
}

func (r *DelegationRepo) AdmitModelCall(ctx context.Context, runID, modelProfileID string,
	estimatedInput int64, requestedMaxOutput int) (int, error) {
	if estimatedInput < 0 || requestedMaxOutput < 1 {
		return 0, ErrDelegationBudgetExceeded
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var maxModel int
	var maxTotal, maxOutput, maxCost, consumedTokens, consumedOutput, consumedCost int64
	var consumedModel int
	var maxWall, inputRate, outputRate int64
	var started sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT b.max_model_calls,b.max_total_tokens,b.max_output_tokens,
		b.max_cost_usd_micros,b.max_wall_time_ms,b.consumed_model_calls,b.consumed_tokens,
		b.consumed_output_tokens,b.consumed_cost_usd_micros,b.started_at,
		m.input_cost_usd_micros_per_million,m.output_cost_usd_micros_per_million
		FROM run_budgets b JOIN model_profiles m ON m.id=? WHERE b.run_id=?`, modelProfileID, runID).Scan(
		&maxModel, &maxTotal, &maxOutput, &maxCost, &maxWall, &consumedModel, &consumedTokens,
		&consumedOutput, &consumedCost, &started, &inputRate, &outputRate); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	if started.Valid && maxWall > 0 {
		startedAt, parseErr := time.Parse(time.RFC3339Nano, started.String)
		if parseErr != nil || now.Sub(startedAt) >= time.Duration(maxWall)*time.Millisecond {
			return 0, ErrDelegationBudgetExceeded
		}
	}
	if consumedModel+1 > maxModel || consumedTokens+estimatedInput >= maxTotal {
		return 0, ErrDelegationBudgetExceeded
	}
	allowed := int64(requestedMaxOutput)
	if remaining := maxTotal - consumedTokens - estimatedInput; allowed > remaining {
		allowed = remaining
	}
	if remaining := maxOutput - consumedOutput; allowed > remaining {
		allowed = remaining
	}
	if maxCost > 0 {
		if inputRate == 0 && outputRate == 0 {
			return 0, fmt.Errorf("%w: model pricing is unavailable", ErrDelegationBudgetExceeded)
		}
		inputCost := tokenCostMicros(estimatedInput, inputRate)
		remainingCost := maxCost - consumedCost - inputCost
		if remainingCost < 0 {
			return 0, ErrDelegationBudgetExceeded
		}
		if outputRate > 0 {
			affordable := remainingCost * 1_000_000 / outputRate
			if allowed > affordable {
				allowed = affordable
			}
		}
	}
	if allowed < 1 {
		return 0, ErrDelegationBudgetExceeded
	}
	startedValue := started.String
	if !started.Valid {
		startedValue = now.Format(time.RFC3339Nano)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE run_budgets SET consumed_model_calls=consumed_model_calls+1,
		started_at=COALESCE(started_at,?) WHERE run_id=? AND consumed_model_calls=?`,
		startedValue, runID, consumedModel)
	if err != nil {
		return 0, err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return 0, ErrDelegationBudgetExceeded
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(allowed), nil
}

func (r *DelegationRepo) CompleteModelCall(ctx context.Context, runID, modelProfileID string, usage domain.Usage) error {
	var inputRate, outputRate int64
	if err := r.DB.QueryRowContext(ctx, `SELECT input_cost_usd_micros_per_million,
		output_cost_usd_micros_per_million FROM model_profiles WHERE id=?`, modelProfileID).
		Scan(&inputRate, &outputRate); err != nil {
		return err
	}
	tokens := usage.InputTokens + usage.OutputTokens
	cost := tokenCostMicros(usage.InputTokens, inputRate) + tokenCostMicros(usage.OutputTokens, outputRate)
	result, err := r.DB.ExecContext(ctx, `UPDATE run_budgets SET consumed_tokens=consumed_tokens+?,
		consumed_output_tokens=consumed_output_tokens+?,consumed_cost_usd_micros=consumed_cost_usd_micros+?
		WHERE run_id=? AND consumed_tokens+?<=max_total_tokens
		  AND consumed_output_tokens+?<=max_output_tokens
		  AND (max_cost_usd_micros=0 OR consumed_cost_usd_micros+?<=max_cost_usd_micros)`,
		tokens, usage.OutputTokens, cost, runID, tokens, usage.OutputTokens, cost)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrDelegationBudgetExceeded
	}
	return nil
}

func (r *DelegationRepo) AdmitToolCalls(ctx context.Context, runID string, count int) error {
	if count < 1 {
		return nil
	}
	now := time.Now().UTC()
	result, err := r.DB.ExecContext(ctx, `UPDATE run_budgets SET consumed_tool_calls=consumed_tool_calls+?,
		started_at=COALESCE(started_at,?) WHERE run_id=?
		  AND consumed_tool_calls+?<=max_tool_calls
		  AND (max_wall_time_ms=0 OR started_at IS NULL OR
			(julianday(?) - julianday(started_at))*86400000 < max_wall_time_ms)`,
		count, now.Format(time.RFC3339Nano), runID, count, now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrDelegationBudgetExceeded
	}
	return nil
}

func tokenCostMicros(tokens, microsPerMillion int64) int64 {
	if tokens <= 0 || microsPerMillion <= 0 {
		return 0
	}
	whole, remainder := tokens/1_000_000, tokens%1_000_000
	if whole > math.MaxInt64/microsPerMillion {
		return math.MaxInt64
	}
	cost := whole * microsPerMillion
	if remainder > (math.MaxInt64-999_999)/microsPerMillion {
		return math.MaxInt64
	}
	partial := (remainder*microsPerMillion + 999_999) / 1_000_000
	if cost > math.MaxInt64-partial {
		return math.MaxInt64
	}
	return cost + partial
}

// RecordBudgetUsage remains for substrate callers that report an aggregate in
// one step. Runtime delegated execution uses the admission methods above.
func (r *DelegationRepo) RecordBudgetUsage(ctx context.Context, runID string, modelCalls, toolCalls int,
	tokens, outputTokens, costMicros int64) error {
	result, err := r.DB.ExecContext(ctx, `UPDATE run_budgets SET
		consumed_model_calls = consumed_model_calls + ?,
		consumed_tool_calls  = consumed_tool_calls + ?,
		consumed_tokens      = consumed_tokens + ?,
		consumed_output_tokens = consumed_output_tokens + ?,
		consumed_cost_usd_micros = consumed_cost_usd_micros + ?
		WHERE run_id = ?
		  AND consumed_model_calls + ? <= max_model_calls
		  AND consumed_tool_calls + ? <= max_tool_calls
		  AND consumed_tokens + ? <= max_total_tokens
		  AND consumed_output_tokens + ? <= max_output_tokens
		  AND (max_cost_usd_micros = 0 OR consumed_cost_usd_micros + ? <= max_cost_usd_micros)`,
		modelCalls, toolCalls, tokens, outputTokens, costMicros, runID,
		modelCalls, toolCalls, tokens, outputTokens, costMicros)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrDelegationBudgetExceeded
	}
	return nil
}

// FinalizeChildSuccess terminalizes a delegated_agent Run: it writes the
// complete private transcript to run_messages (no canonical message), folds the
// submit_result contract into the delegation item, settles the group when all
// items are terminal, and wakes the parent (waiting_children -> queued) so the
// Coordinator re-enqueues it. All in one transaction.
func (r *RunRepo) FinalizeChildSuccess(ctx context.Context, runID string, output domain.RunOutput) error {
	if output.Terminal == nil {
		return domain.NewCodedError(domain.ErrorIncompleteTerminalContract,
			errors.New("child Run finalize requires a submit_result contract"))
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status, kind string
	var parent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status,run_kind,parent_run_id FROM agent_runs WHERE id=?`, runID).
		Scan(&status, &kind, &parent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	}
	if kind != string(domain.RunKindDelegatedAgent) || !parent.Valid {
		return fmt.Errorf("%w: only delegated children can be child-finalized", ErrInvalidRunState)
	}
	if status == string(domain.RunSucceeded) {
		return nil // idempotent terminal replay
	}
	if domain.RunStatus(status).Terminal() {
		// Cancellation/failure won the race. Never append a success transcript or
		// emit success events for a terminal child.
		return nil
	}
	if status != string(domain.RunRunning) {
		return fmt.Errorf("%w: child must be running to finalize success", ErrInvalidRunState)
	}

	timestamp := time.Now().UTC()
	if err := validateChildTerminalArtifactsTx(ctx, tx, runID, output.Terminal.ArtifactRefs); err != nil {
		return err
	}
	if _, _, err := AppendRunMessagesTx(ctx, tx, runID, domain.CommitFormatSpeakerV2, output.Messages, timestamp); err != nil {
		return err
	}
	finishedAt := timestamp.Format(time.RFC3339Nano)
	updated, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='succeeded', finished_at=?, error_code=NULL, error_message=NULL WHERE id=? AND status='running'`,
		finishedAt, runID)
	if err != nil {
		return err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return fmt.Errorf("%w: child is no longer running", ErrInvalidRunState)
	}
	resultJSON, err := json.Marshal(output.Terminal)
	if err != nil {
		return err
	}
	var groupID string
	if err := tx.QueryRowContext(ctx, `SELECT group_id FROM delegation_items WHERE child_run_id=?`, runID).Scan(&groupID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("child Run has no delegation item")
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status='succeeded', result_json=?
		WHERE child_run_id=? AND status IN ('pending','running')`, string(resultJSON), runID); err != nil {
		return err
	}
	// Fold the child's reservation back: release the reserved ceiling and
	// record actual usage exactly once (idempotent via root_reconciled_at).
	if err := reconcileRootBudgetTx(ctx, tx, runID); err != nil {
		return err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM delegation_items WHERE group_id=? AND status IN ('pending','running')`,
		groupID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET status='settled' WHERE id=?`, groupID); err != nil {
			return err
		}
		// Wake the parent: waiting_children -> queued so the Coordinator
		// re-enqueues it with the folded tool result available.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='queued', finished_at=NULL
			WHERE id=? AND status='waiting_children'`, parent.String); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='pending', updated_at=? WHERE id=(SELECT turn_id FROM agent_runs WHERE id=?)`,
			finishedAt, parent.String); err != nil {
			return err
		}

		// Inject folded child results into the parent's delegate_roles tool call
		// result_preview so the parent resume sees real output instead of a placeholder.
		if err := injectFoldedResultsTx(ctx, tx, groupID, parent.String); err != nil {
			return err
		}
	}
	committedEvents, err := appendEventsTx(ctx, tx, runID,
		domain.PendingEvent{EventType: "run_transcript_committed", Payload: json.RawMessage(fmt.Sprintf(
			`{"count":%d,"format":2,"shadow":true}`, len(output.Messages)))},
		domain.PendingEvent{EventType: "child_result_folded", Payload: json.RawMessage(fmt.Sprintf(
			`{"status":"%s","summary":%q}`, output.Terminal.Status, output.Terminal.Summary))},
		domain.PendingEvent{EventType: "run_succeeded", Payload: json.RawMessage(`{"status":"succeeded"}`)},
	)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committedEvents...)
	}
	return nil
}

// FinalizeChildFailure atomically fails a delegated child, folds a bounded
// blocked result, settles its group, and queues the parent when every sibling
// is terminal. The returned wake flag is true only when this transaction moved
// the parent from waiting_children to queued.
func (r *RunRepo) FinalizeChildFailure(ctx context.Context, runID, code, message string) (string, bool, error) {
	if code == "" {
		code = string(domain.ErrorToolBatchFailed)
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	var status, kind string
	var parent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status,run_kind,parent_run_id FROM agent_runs WHERE id=?`, runID).
		Scan(&status, &kind, &parent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, ErrRunNotFound
		}
		return "", false, err
	}
	if kind != string(domain.RunKindDelegatedAgent) || !parent.Valid {
		return "", false, fmt.Errorf("%w: only delegated children can be child-failed", ErrInvalidRunState)
	}
	if status == string(domain.RunFailed) {
		var parentStatus domain.RunStatus
		_ = tx.QueryRowContext(ctx, `SELECT status FROM agent_runs WHERE id=?`, parent.String).Scan(&parentStatus)
		return parent.String, parentStatus == domain.RunQueued, tx.Commit()
	}
	if domain.RunStatus(status).Terminal() {
		return parent.String, false, tx.Commit()
	}
	if status != string(domain.RunRunning) {
		return "", false, fmt.Errorf("%w: child must be running to finalize failure", ErrInvalidRunState)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	updated, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='failed',error_code=?,error_message=?,finished_at=?
		WHERE id=? AND status='running'`, code, message, now, runID)
	if err != nil {
		return "", false, err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return "", false, fmt.Errorf("%w: child is no longer running", ErrInvalidRunState)
	}
	summary := "Delegated child failed: " + message
	if len(summary) > 4096 {
		summary = summary[:4096]
	}
	resultJSON, err := json.Marshal(domain.SubmitResult{Status: domain.SubmitBlocked,
		Summary: summary,
		Payload: json.RawMessage(fmt.Sprintf(`{"errorCode":%q}`, code))})
	if err != nil {
		return "", false, err
	}
	var groupID string
	itemUpdate, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status='failed',result_json=?
		WHERE child_run_id=? AND status IN ('pending','running')`, string(resultJSON), runID)
	if err != nil {
		return "", false, err
	}
	if changed, _ := itemUpdate.RowsAffected(); changed != 1 {
		return "", false, fmt.Errorf("%w: child delegation item is already terminal", ErrDelegationConflict)
	}
	// Fold the child's reservation back: release the reserved ceiling and
	// record actual usage exactly once (idempotent via root_reconciled_at).
	if err := reconcileRootBudgetTx(ctx, tx, runID); err != nil {
		return "", false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT group_id FROM delegation_items WHERE child_run_id=?`, runID).Scan(&groupID); err != nil {
		return "", false, err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM delegation_items WHERE group_id=? AND status IN ('pending','running')`,
		groupID).Scan(&remaining); err != nil {
		return "", false, err
	}
	wakeParent := false
	if remaining == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET status='settled' WHERE id=? AND status='waiting_children'`, groupID); err != nil {
			return "", false, err
		}
		parentUpdate, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='queued',finished_at=NULL
			WHERE id=? AND status='waiting_children'`, parent.String)
		if err != nil {
			return "", false, err
		}
		parentChanged, _ := parentUpdate.RowsAffected()
		wakeParent = parentChanged == 1
		if wakeParent {
			if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='pending',updated_at=?
				WHERE id=(SELECT turn_id FROM agent_runs WHERE id=?)`, now, parent.String); err != nil {
				return "", false, err
			}
			if err := injectFoldedResultsTx(ctx, tx, groupID, parent.String); err != nil {
				return "", false, err
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{"status": "failed", "errorCode": code, "errorMessage": message})
	committed, err := appendEventsTx(ctx, tx, runID,
		domain.PendingEvent{EventType: "child_result_folded", Payload: resultJSON},
		domain.PendingEvent{EventType: "run_failed", Payload: payload})
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return parent.String, wakeParent, nil
}

func validateChildTerminalArtifactsTx(ctx context.Context, tx *sql.Tx, runID string, refs []domain.ArtifactReference) error {
	seen := make(map[string]struct{}, len(refs))
	for _, reference := range refs {
		if reference.ArtifactID == "" || reference.Name == "" || reference.Kind == "" ||
			reference.MIMEType == "" || reference.SHA256 == "" {
			return fmt.Errorf("submit_result artifact reference is incomplete")
		}
		if _, exists := seen[reference.ArtifactID]; exists {
			return fmt.Errorf("artifact %s is referenced more than once", reference.ArtifactID)
		}
		seen[reference.ArtifactID] = struct{}{}
		var stored domain.ArtifactReference
		if err := tx.QueryRowContext(ctx, `SELECT id,name,kind,mime_type,size_bytes,sha256
			FROM artifacts WHERE id=? AND run_id=?`, reference.ArtifactID, runID).Scan(
			&stored.ArtifactID, &stored.Name, &stored.Kind, &stored.MIMEType, &stored.SizeBytes, &stored.SHA256); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("artifact %s was not produced by child Run", reference.ArtifactID)
			}
			return err
		}
		if stored.ArtifactID != reference.ArtifactID || stored.Name != reference.Name || stored.Kind != reference.Kind ||
			stored.MIMEType != reference.MIMEType || stored.SHA256 != reference.SHA256 ||
			(reference.SizeBytes != 0 && stored.SizeBytes != reference.SizeBytes) {
			return fmt.Errorf("artifact %s metadata does not match immutable storage", reference.ArtifactID)
		}
	}
	return nil
}

// AssignmentForChild returns the frozen task assignment of a child Run.
func (r *DelegationRepo) AssignmentForChild(ctx context.Context, childRunID string) (json.RawMessage, error) {
	var assignment string
	if err := r.DB.QueryRowContext(ctx, `SELECT assignment_json FROM delegation_items WHERE child_run_id=?`,
		childRunID).Scan(&assignment); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDelegationItemNotFound
		}
		return nil, err
	}
	return json.RawMessage(assignment), nil
}

// FoldChildResult terminalizes an item from a child Run and settles the group
// when every item is terminal. All mutations happen in one transaction.
func (r *DelegationRepo) FoldChildResult(ctx context.Context, childRunID string,
	status domain.DelegationItemStatus, resultJSON json.RawMessage) (groupSettled bool, err error) {
	if status != domain.DelegationItemTerminal && status != domain.DelegationItemFailed &&
		status != domain.DelegationItemCancelled && status != domain.DelegationItemNotAuth {
		return false, fmt.Errorf("invalid folded item status %q", status)
	}
	if len(resultJSON) == 0 {
		resultJSON = json.RawMessage(`{}`)
	}
	if !json.Valid(resultJSON) {
		return false, fmt.Errorf("folded result is not valid JSON")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var groupID string
	var currentStatus domain.DelegationItemStatus
	if err := tx.QueryRowContext(ctx, `SELECT group_id,status FROM delegation_items WHERE child_run_id=?`,
		childRunID).Scan(&groupID, &currentStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrDelegationItemNotFound
		}
		return false, err
	}
	if currentStatus != domain.DelegationItemPending && currentStatus != domain.DelegationItemRunning {
		return false, fmt.Errorf("%w: item is already terminal", ErrDelegationConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status=?, result_json=?
		WHERE id=(SELECT id FROM delegation_items WHERE child_run_id=?) AND status IN ('pending','running')`,
		status, string(resultJSON), childRunID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET status='settled', created_at=created_at
		WHERE id=? AND NOT EXISTS (
			SELECT 1 FROM delegation_items WHERE group_id=? AND status NOT IN ('succeeded','failed','cancelled','not_authorized')
		)`, groupID, groupID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	var remaining int
	if err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM delegation_items WHERE group_id=? AND status IN ('pending','running')`,
		groupID).Scan(&remaining); err != nil {
		return false, err
	}
	return remaining == 0, nil
}

// ListOwnedChildren returns every non-terminal child Run of a parent.
func (r *DelegationRepo) ListOwnedChildren(ctx context.Context, parentRunID string) ([]domain.AgentRun, error) {
	rows, err := r.DB.QueryContext(ctx, runSelect+` WHERE parent_run_id=?
		AND status IN ('queued','running','waiting_for_approval') ORDER BY created_at`, parentRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]domain.AgentRun, 0)
	for rows.Next() {
		run, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// ReapOrphans interrupts live children of terminal parents and reconciles every
// terminal child into its delegation item/group. This also cleans children that
// RecoverActive already marked interrupted before this pass.
func (r *DelegationRepo) ReapOrphans(ctx context.Context) ([]string, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `SELECT c.id FROM agent_runs c JOIN agent_runs p ON p.id=c.parent_run_id
		WHERE c.run_kind='delegated_agent'
		  AND c.status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children')
		  AND p.status IN ('succeeded','failed','cancelled','interrupted') ORDER BY c.created_at,c.id`)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='interrupted',error_code='parent_terminal',
			error_message='parent Run ended before child finished',finished_at=? WHERE id=? AND status NOT IN ('succeeded','failed','cancelled','interrupted')`,
			now, id); err != nil {
			return nil, err
		}
	}
	// Map every terminal child fact to a terminal item, including children that
	// RecoverActive interrupted before the orphan scan.
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status=CASE
		WHEN (SELECT status FROM agent_runs WHERE id=child_run_id)='succeeded' THEN 'succeeded'
		WHEN (SELECT status FROM agent_runs WHERE id=child_run_id)='cancelled' THEN 'cancelled'
		ELSE 'failed' END
		WHERE status IN ('pending','running') AND child_run_id IN (
			SELECT id FROM agent_runs WHERE status IN ('succeeded','failed','cancelled','interrupted'))`); err != nil {
		return nil, err
	}
	// Reconcile every terminal child reservation, including children that were
	// never selected by the orphan scan above. Idempotent via root_reconciled_at.
	terminalRows, err := tx.QueryContext(ctx, `SELECT child_run_id FROM delegation_items
		WHERE child_run_id IS NOT NULL AND child_run_id IN (
			SELECT id FROM agent_runs WHERE status IN ('succeeded','failed','cancelled','interrupted'))`)
	if err != nil {
		return nil, err
	}
	terminalChildIDs := make([]string, 0)
	for terminalRows.Next() {
		var id string
		if err := terminalRows.Scan(&id); err != nil {
			terminalRows.Close()
			return nil, err
		}
		terminalChildIDs = append(terminalChildIDs, id)
	}
	if err := terminalRows.Close(); err != nil {
		return nil, err
	}
	for _, id := range terminalChildIDs {
		if err := reconcileRootBudgetTx(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET status=CASE
		WHEN (SELECT status FROM agent_runs WHERE id=parent_run_id)='cancelled' THEN 'cancelled'
		ELSE 'settled' END
		WHERE status IN ('pending','waiting_admission','waiting_children')
		  AND NOT EXISTS (SELECT 1 FROM delegation_items i WHERE i.group_id=delegation_groups.id
			AND i.status IN ('pending','running'))`); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// injectFoldedResultsTx updates the parent's delegate_roles tool_calls row with
// the folded results from all delegation items in the group. It runs inside the
// FinalizeChildSuccess transaction so the parent resume sees the real output.
func injectFoldedResultsTx(ctx context.Context, tx *sql.Tx, groupID, parentRunID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT di.name, di.result_json, di.status
		 FROM delegation_items di
		 WHERE di.group_id=? ORDER BY di.ordinal`, groupID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type childResult struct {
		Name   string          `json:"name"`
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
	}
	var children []childResult
	for rows.Next() {
		var name, status string
		var result sql.NullString
		if err := rows.Scan(&name, &result, &status); err != nil {
			return err
		}
		res := json.RawMessage("null")
		if result.Valid && result.String != "" {
			res = json.RawMessage(result.String)
		}
		children = append(children, childResult{Name: name, Status: status, Result: res})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	folded := map[string]any{"status": "settled", "children": children}
	foldedJSON, err := json.Marshal(folded)
	if err != nil {
		return err
	}

	// Find the delegate_roles tool call for this parent run.
	var parentTCID string
	err = tx.QueryRowContext(ctx,
		`SELECT dg.parent_tool_call_id FROM delegation_groups dg WHERE dg.id=?`,
		groupID).Scan(&parentTCID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // No parent tool call to update.
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tool_calls SET result_preview=? WHERE run_id=? AND tool_call_id=?`,
		string(foldedJSON), parentRunID, parentTCID); err != nil {
		return err
	}
	return nil
}
