package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
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
	ErrDelegationAttemptNotFound = errors.New("delegation attempt not found")
)

// attemptAuthorizationSnapshot freezes the Role facts an attempt runs under.
// It is immutable once the attempt row exists.
type attemptAuthorizationSnapshot struct {
	ItemID         string `json:"itemId"`
	Name           string `json:"name"`
	RoleVersionID  string `json:"roleVersionId"`
	RoleObjectID   string `json:"roleObjectId"`
	Handle         string `json:"handle"`
	DisplayName    string `json:"displayName"`
	ConfigDigest   string `json:"configDigest"`
	OutputContract string `json:"outputContract"`
}

type DelegationRepo struct{ DB *sql.DB }

type CreateDelegationItemInput struct {
	Name           string
	RoleVersionID  string
	AssignmentJSON json.RawMessage
	OutputContract string
	Budget         domain.BudgetCeilingJSON
	// Skills are task-level additive preload Skill IDs. They do not change the
	// frozen Role's tool policy or authority and are persisted for retry/recovery.
	Skills []string
	// Depends declares batch-scoped task dependencies: sibling item names in
	// the same group that must settle before this item is ready to start.
	// Empty means the item is an entry task. The topology is validated in
	// createDelegationGroupTx (no dangling refs, no cycles, one entry minimum).
	Depends []string
}

type CreateDelegationGroupInput struct {
	ParentRunID      string
	ParentToolCallID string
	Strategy         domain.DelegationStrategy
	Items            []CreateDelegationItemInput
	// ExecutionMode and AutoResume freeze delivery semantics. Blocking keeps
	// the Item 5 behavior; background returns a handle and never blocks the
	// parent on waiting_children.
	ExecutionMode domain.DelegationExecutionMode
	AutoResume    bool
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
		  AND p.current_version_id IS NOT NULL AND p.scope!='flow'
		  AND (p.project_id=? OR p.project_id IS NULL)
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
// Provider-visible delegate_tasks call. A previous approval for another call
// or Run is never reusable. Both the current name and the legacy
// delegate_roles alias are matched so approvals recorded before the rename
// stay valid for resumed runs.
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
			if domain.IsDelegationToolName(item.ToolName) && item.ToolCallID == toolCallID {
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
	if input.ExecutionMode == "" {
		input.ExecutionMode = domain.DelegationExecutionBlocking
	}
	if input.ExecutionMode != domain.DelegationExecutionBlocking &&
		input.ExecutionMode != domain.DelegationExecutionBackground {
		return nil, nil, fmt.Errorf("unsupported execution mode %q", input.ExecutionMode)
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
	// Batch topology validation: every depends reference must name a sibling
	// item, the dependency graph must be acyclic, and at least one entry task
	// (indegree 0) must exist. Fail loud before any row is written.
	if err := validateTaskTopologyTx(input.Items); err != nil {
		return nil, nil, err
	}
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
		taskSkills, err := normalizeTaskSkillIDs(item.Skills)
		if err != nil {
			return nil, nil, fmt.Errorf("delegation item %s: %w", name, err)
		}
		skillsJSON, err := json.Marshal(taskSkills)
		if err != nil {
			return nil, nil, err
		}
		itemID := uuid.NewString()
		dependsJSON := []byte("[]")
		if len(item.Depends) > 0 {
			encoded, marshalErr := json.Marshal(item.Depends)
			if marshalErr != nil {
				return nil, nil, marshalErr
			}
			dependsJSON = encoded
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_items
			(id,group_id,child_run_id,name,role_version_id,assignment_json,output_contract,budget_json,result_json,status,ordinal,depends_json,skills_json,created_at)
			VALUES(?,?,NULL,?,?,?,?,?,NULL,'pending',?,?,?,?)`,
			itemID, groupID, name, item.RoleVersionID, string(item.AssignmentJSON),
			outputContract, string(budgetJSON), ordinal, string(dependsJSON), string(skillsJSON), timestamp); err != nil {
			return nil, nil, fmt.Errorf("create delegation item %s: %w", name, err)
		}
		items = append(items, domain.DelegationItem{
			ID: itemID, GroupID: groupID, Name: name, Skills: taskSkills,
			Depends: append([]string(nil), item.Depends...), RoleVersionID: item.RoleVersionID,
			AssignmentJSON: append(json.RawMessage(nil), item.AssignmentJSON...), OutputContract: outputContract,
			BudgetJSON: budgetJSON, Status: domain.DelegationItemPending, Ordinal: ordinal, CreatedAt: now,
		})
	}
	group := &domain.DelegationGroup{ID: groupID, ParentRunID: input.ParentRunID,
		ParentToolCallID: input.ParentToolCallID, Strategy: input.Strategy,
		Status: domain.DelegationGroupPending, CreatedAt: now}
	return group, items, nil
}

// normalizeTaskSkillIDs freezes a stable, de-duplicated task Skill list while
// rejecting blank IDs. Catalog availability is validated by the caller that
// owns the effective Skill roots.
func normalizeTaskSkillIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	normalized := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, fmt.Errorf("task skill id is required")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized, nil
}

// ValidateTaskTopology validates model-authored TaskSpecs before delegation
// admission. The store repeats the same validation during materialization so
// replay/recovery and non-tool callers cannot bypass the invariant.
func ValidateTaskTopology(specs []domain.TaskSpec) error {
	items := make([]CreateDelegationItemInput, len(specs))
	for index := range specs {
		items[index] = CreateDelegationItemInput{Name: specs[index].Name, Depends: specs[index].Depends}
	}
	return validateTaskTopologyTx(items)
}

// validateTaskTopologyTx validates the batch task graph before any row is
// written: item names are unique when dependency edges exist, every depends
// reference names a sibling item, the graph is acyclic, and at least one entry
// task (indegree 0) exists.
func validateTaskTopologyTx(items []CreateDelegationItemInput) error {
	byName := make(map[string]int, len(items))
	dupNames := make(map[string]struct{})
	hasDepends := false
	for index, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return domain.NewCodedError(domain.ErrorDelegationDagInvalid,
				fmt.Errorf("task name is required"))
		}
		if len(item.Depends) > 0 {
			hasDepends = true
		}
		if _, dup := byName[name]; dup {
			dupNames[name] = struct{}{}
		}
		byName[name] = index
	}
	// Duplicate names are permitted for dependency-free parallel batches
	// (multiple instances of the same Role); once any depends declaration
	// exists, names must be unique because depends references by name.
	if !hasDepends {
		return nil // flat parallel batch: legacy semantics, no topology checks
	}
	if len(dupNames) > 0 {
		names := make([]string, 0, len(dupNames))
		for name := range dupNames {
			names = append(names, name)
		}
		sort.Strings(names)
		return domain.NewCodedError(domain.ErrorDelegationDagInvalid,
			fmt.Errorf("duplicate task name(s) %v with depends declared", names))
	}
	if len(items) == 1 {
		// A single task must not depend on itself; an empty depends is valid.
		if len(items[0].Depends) > 0 {
			return domain.NewCodedError(domain.ErrorDelegationDagInvalid,
				fmt.Errorf("task %q depends on %q which is not a sibling task", items[0].Name, items[0].Depends[0]))
		}
		return nil
	}
	indegree := make([]int, len(items))
	adj := make([][]int, len(items))
	seen := make(map[string]struct{})
	for index, item := range items {
		for _, dep := range item.Depends {
			target, ok := byName[dep]
			if !ok {
				return domain.NewCodedError(domain.ErrorDelegationDagInvalid,
					fmt.Errorf("task %q depends on %q which is not a sibling task", item.Name, dep))
			}
			if target == index {
				return domain.NewCodedError(domain.ErrorDelegationDagInvalid,
					fmt.Errorf("task %q depends on itself", item.Name))
			}
			key := fmt.Sprintf("%d>%d", index, target)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			adj[index] = append(adj[index], target)
			indegree[target]++
		}
	}
	// Kahn topological sort: a cycle leaves unvisited nodes.
	queue := make([]int, 0, len(items))
	for index, degree := range indegree {
		if degree == 0 {
			queue = append(queue, index)
		}
	}
	if len(queue) == 0 {
		return domain.NewCodedError(domain.ErrorDelegationDagInvalid,
			fmt.Errorf("task graph has no entry task (all tasks depend on something)"))
	}
	visited := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adj[current] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(items) {
		return domain.NewCodedError(domain.ErrorDelegationDagInvalid,
			fmt.Errorf("task graph contains a dependency cycle"))
	}
	return nil
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
	if input.ExecutionMode == domain.DelegationExecutionBackground && definition.Authority == domain.RoleAuthorityMutation {
		return fmt.Errorf("%w: mutation Roles cannot run in background mode", ErrDelegationNotAuthorized)
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
		output_contract,budget_json,result_json,status,ordinal,depends_json,skills_json,created_at
		FROM delegation_items WHERE group_id=? ORDER BY ordinal`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDelegationItems(rows)
}

// TaskSkillsForChildRun returns the task-level additive preload Skill IDs
// frozen on the delegation item for this concrete attempt. Looking up through
// delegation_item_attempts keeps retry children attached to the same task
// contract without relying on the generation-zero child_run_id substrate.
func (r *DelegationRepo) TaskSkillsForChildRun(ctx context.Context, childRunID string) ([]string, error) {
	var encoded string
	err := r.DB.QueryRowContext(ctx, `SELECT COALESCE(i.skills_json,'[]')
		FROM delegation_item_attempts a JOIN delegation_items i ON i.id=a.item_id
		WHERE a.child_run_id=?`, childRunID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal([]byte(encoded), &ids); err != nil {
		return nil, fmt.Errorf("decode task skills for child run %s: %w", childRunID, err)
	}
	return ids, nil
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
		if child.ItemStatus == domain.DelegationItemBlocked && child.ErrorMessage == "" {
			// A blocked task never started: the terminal reason is always a
			// prerequisite task that failed, cancelled, or was itself blocked.
			// The exact dependency name is rendered from the task graph in the UI.
			child.ErrorCode = string(domain.ErrorDelegationDagInvalid)
			child.ErrorMessage = "Blocked: a prerequisite task did not complete"
		}
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
		var childRun, assignment, budget, result, dependsJSON, skillsJSON sql.NullString
		var createdAt string
		if err := rows.Scan(&item.ID, &item.GroupID, &childRun, &item.Name, &item.RoleVersionID,
			&assignment, &item.OutputContract, &budget, &result, &item.Status, &item.Ordinal,
			&dependsJSON, &skillsJSON, &createdAt); err != nil {
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
		if dependsJSON.Valid && dependsJSON.String != "" && dependsJSON.String != "[]" {
			if err := json.Unmarshal([]byte(dependsJSON.String), &item.Depends); err != nil {
				return nil, fmt.Errorf("decode depends for item %s: %w", item.ID, err)
			}
		}
		if skillsJSON.Valid && skillsJSON.String != "" && skillsJSON.String != "[]" {
			if err := json.Unmarshal([]byte(skillsJSON.String), &item.Skills); err != nil {
				return nil, fmt.Errorf("decode skills for item %s: %w", item.ID, err)
			}
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

// createDelegationHandleTx writes one stable delivery handle for a group in
// the same transaction as its children. The branch is frozen from the Session's
// active branch; auto-resume is part of the frozen handle.
func createDelegationHandleTx(ctx context.Context, tx *sql.Tx, groupID, parentRunID, sessionID string,
	executionMode domain.DelegationExecutionMode, autoResume bool) (string, error) {
	var branchID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(active_branch_id,'') FROM sessions WHERE id=?`,
		sessionID).Scan(&branchID); err != nil {
		return "", err
	}
	if branchID == "" {
		return "", fmt.Errorf("session %s has no active branch for delegation delivery", sessionID)
	}
	handleID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	autoResumeValue := 0
	if autoResume {
		autoResumeValue = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_handles
		(id,group_id,session_id,source_parent_run_id,source_branch_id,execution_mode,auto_resume,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?, 'active',?,?)`,
		handleID, groupID, sessionID, parentRunID, branchID, string(executionMode),
		autoResumeValue, now, now); err != nil {
		return "", fmt.Errorf("create delegation handle: %w", err)
	}
	return handleID, nil
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

// createInitialGenerationTx writes the generation 0 row with explicit ordinal
// selection and frozen authorization/budget snapshots. Selection is never
// inferred by timestamp; reused attempts are empty for the initial round.
func createInitialGenerationTx(ctx context.Context, tx *sql.Tx, groupID, clientRequestID string,
	items []domain.DelegationItem) error {
	selection := make([]map[string]string, 0, len(items))
	var budget domain.BudgetCeilingJSON
	for _, item := range items {
		selection = append(selection, map[string]string{
			"itemId": item.ID, "roleVersionId": item.RoleVersionID,
		})
		var ceiling domain.BudgetCeilingJSON
		if len(item.BudgetJSON) > 0 {
			if err := json.Unmarshal(item.BudgetJSON, &ceiling); err != nil {
				return fmt.Errorf("decode item budget %s: %w", item.ID, err)
			}
		}
		budget.MaxModelCalls += ceiling.MaxModelCalls
		budget.MaxToolCalls += ceiling.MaxToolCalls
		budget.MaxTotalTokens += ceiling.MaxTotalTokens
		budget.MaxOutputTokens += ceiling.MaxOutputTokens
		budget.MaxCostMicros += ceiling.MaxCostMicros
	}
	authJSON, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	authDigest, err := digestJSON(selection)
	if err != nil {
		return err
	}
	budgetJSON, err := json.Marshal(budget)
	if err != nil {
		return err
	}
	budgetDigest, err := digestJSON(budget)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_group_generations
		(id,group_id,generation,kind,status,retry_selection_json,reused_attempts_json,
		 authorization_snapshot_json,authorization_snapshot_digest,budget_snapshot_json,budget_snapshot_digest,
		 client_request_id,created_at)
		VALUES(?,?,0,'initial','running','[]','[]',?,?,?,?,?,?)`,
		uuid.NewString(), groupID, string(authJSON), authDigest, string(budgetJSON), budgetDigest,
		clientRequestID, now); err != nil {
		return fmt.Errorf("create initial generation: %w", err)
	}
	return nil
}

// CreateChildRunInput describes the materialization of one delegation item
// into a queued delegated_agent Run.
type CreateChildRunInput struct {
	ParentRunID string
	ItemID      string
	SessionID   string
	// Generation and RetryOfAttemptID are zero/NULL for generation 0 and set
	// for retry/follow-up generations.
	Generation       int
	RetryOfAttemptID string
	// BudgetOverride, when set, is the authorization-visible budget for a
	// retry/follow-up attempt. The frozen delegation_items.budget_json is never
	// rewritten.
	BudgetOverride *domain.BudgetCeilingJSON
	// AllowTerminalParent permits materialization after the parent Run ended
	// (retry/follow-up). Generation 0 blocking materialization still requires a
	// running parent.
	AllowTerminalParent bool
	// Background skips the waiting_children wake protocol: the parent stays
	// running and delivery happens through the handle/completion projection.
	Background bool
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
	executionMode := input.ExecutionMode
	if executionMode == "" {
		executionMode = domain.DelegationExecutionBlocking
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()
	group, items, err := createDelegationGroupTx(ctx, tx, input)
	if err != nil {
		return nil, nil, nil, err
	}
	// Create generation 0 before any child Run: attempts reference it and the
	// original folding contract stays tied to generation 0.
	if err := createInitialGenerationTx(ctx, tx, group.ID, input.ParentToolCallID, items); err != nil {
		return nil, nil, nil, err
	}
	children := make([]*domain.AgentRun, 0, len(items))
	for index := range items {
		item := &items[index]
		child, childErr := createChildRunTx(ctx, tx, CreateChildRunInput{
			ParentRunID: input.ParentRunID, ItemID: item.ID, SessionID: sessionID,
			Background: executionMode == domain.DelegationExecutionBackground,
		})
		if childErr != nil {
			return nil, nil, nil, childErr
		}
		item.ChildRunID = &child.ID
		item.Status = domain.DelegationItemRunning
		children = append(children, child)
	}
	// Every group gets one stable handle; background mode freezes it with
	// auto-resume in the same transaction as the children.
	handleID, err := createDelegationHandleTx(ctx, tx, group.ID, input.ParentRunID, sessionID,
		executionMode, input.AutoResume)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	group.Status = domain.DelegationGroupWaitingChildren
	if executionMode == domain.DelegationExecutionBackground {
		group.Status = domain.DelegationGroupPending
	}
	_ = handleID
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
	if !input.AllowTerminalParent && parentStatus != string(domain.RunRunning) &&
		parentStatus != string(domain.RunWaitingChildren) {
		return nil, fmt.Errorf("delegation parent must be running or materializing children")
	}
	if parentSessionID != input.SessionID {
		return nil, fmt.Errorf("%w: child session differs from parent", ErrDelegationConflict)
	}

	var item struct {
		GroupID        string
		Name           string
		RoleVersionID  string
		AssignmentJSON string
		OutputContract string
		BudgetJSON     string
		CurrentStatus  domain.DelegationItemStatus
	}
	itemQuery := `SELECT group_id,name,role_version_id,assignment_json,output_contract,budget_json,status
		FROM delegation_items WHERE id=?`
	if input.Generation <= 0 {
		itemQuery += ` AND child_run_id IS NULL`
	}
	if err := tx.QueryRowContext(ctx, itemQuery, input.ItemID).
		Scan(&item.GroupID, &item.Name, &item.RoleVersionID, &item.AssignmentJSON, &item.OutputContract,
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
	if input.Generation <= 0 {
		// Generation 0 owns the substrate columns; later generations never
		// rewrite the frozen child_run_id/status/result of the item.
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET child_run_id=?, status='running' WHERE id=?`,
			childID, input.ItemID); err != nil {
			return nil, fmt.Errorf("assign child run: %w", err)
		}
	}
	var ceiling domain.BudgetCeilingJSON
	reservedBudgetJSON := item.BudgetJSON
	if input.BudgetOverride != nil {
		ceiling = *input.BudgetOverride
		encoded, marshalErr := json.Marshal(ceiling)
		if marshalErr != nil {
			return nil, marshalErr
		}
		reservedBudgetJSON = string(encoded)
	} else if err := json.Unmarshal([]byte(item.BudgetJSON), &ceiling); err != nil {
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
	// Record the immutable attempt row so every terminal path can settle the
	// attempt state machine, not just the substrate columns. Generation 0 keeps
	// the original folding contract; later generations are explicit retries.
	authSnapshot := attemptAuthorizationSnapshot{
		ItemID: input.ItemID, Name: item.Name, RoleVersionID: item.RoleVersionID,
		RoleObjectID: objectID, Handle: handle, DisplayName: displayName,
		ConfigDigest: configDigest, OutputContract: item.OutputContract,
	}
	authJSON, err := json.Marshal(authSnapshot)
	if err != nil {
		return nil, err
	}
	authDigest, err := digestJSON(authSnapshot)
	if err != nil {
		return nil, err
	}
	generation := input.Generation
	if generation < 0 {
		generation = 0
	}
	retryOf := nullableBackfillString(sql.NullString{Valid: input.RetryOfAttemptID != "", String: input.RetryOfAttemptID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_item_attempts
		(id,item_id,generation,retry_of_attempt_id,child_run_id,
		 authorization_snapshot_json,authorization_snapshot_digest,reserved_budget_json,actual_usage_json,
		 status,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		uuid.NewString(), input.ItemID, generation, retryOf, childID, string(authJSON), authDigest,
		reservedBudgetJSON, `{"modelCalls":0,"toolCalls":0,"tokens":0,"outputTokens":0,"costMicros":0}`, "queued", timestamp); err != nil {
		return nil, fmt.Errorf("create delegation attempt: %w", err)
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
	if !input.Background {
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET status='waiting_children' WHERE id=? AND status='pending'`,
			item.GroupID); err != nil {
			return nil, fmt.Errorf("activate delegation group: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='waiting_children'
			WHERE id=? AND status='running'`, input.ParentRunID); err != nil {
			return nil, fmt.Errorf("parent enters waiting_children: %w", err)
		}
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

// settleAttemptTx terminalizes the attempt row of a child Run (idempotent via
// the active-status guard) and settles the owning generation when the whole
// group is terminal. Generation settlement never selects by timestamp.
func settleAttemptTx(ctx context.Context, tx *sql.Tx, childRunID string, status domain.DelegationAttemptStatus,
	resultJSON json.RawMessage, terminalKind, errorCode, errorMessage string) error {
	var attemptID, groupID string
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT a.id,g.id,a.generation FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		JOIN delegation_groups g ON g.id=i.group_id
		WHERE a.child_run_id=?`, childRunID).Scan(&attemptID, &groupID, &generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDelegationAttemptNotFound
		}
		return err
	}
	resultDigest := ""
	if len(resultJSON) > 0 {
		digest, err := digestJSON(json.RawMessage(resultJSON))
		if err != nil {
			return err
		}
		resultDigest = digest
	}
	usage := readChildUsageTx(ctx, tx, childRunID)
	usageJSON, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_item_attempts SET status=?,result_json=?,result_digest=?,
		terminal_kind=?,actual_usage_json=?,root_reconciled_at=?,finished_at=?,error_code=?,error_message=?
		WHERE id=? AND status IN ('queued','running')`,
		string(status), nullableBackfillJSON(string(resultJSON)), resultDigest, terminalKind,
		string(usageJSON), now, now, errorCode, errorMessage, attemptID); err != nil {
		return err
	}
	// Advance the dynamic task graph BEFORE the generation settlement check:
	// dependent tasks whose dependencies failed are marked blocked here so the
	// generation settlement below sees only terminal attempts; dependent tasks
	// whose dependencies all succeeded are reported ready for enqueue. Blocked
	// attempts never consume budget because their child Runs never start.
	if _, err := advanceDagTx(ctx, tx, groupID); err != nil {
		return err
	}
	// Settle the generation when every attempt of THIS generation is terminal.
	// Item substrate columns are frozen at generation 0 and must never gate
	// later generations. The first (and only) transition to settled creates the
	// logical completion and its durable delivery event in the same transaction.
	result, err := tx.ExecContext(ctx, `UPDATE delegation_group_generations SET status='settled',completed_at=?
		WHERE group_id=? AND generation=? AND status IN ('queued','running')
		  AND NOT EXISTS (SELECT 1 FROM delegation_item_attempts a JOIN delegation_items i ON i.id=a.item_id
			WHERE i.group_id=? AND a.generation=? AND a.status IN ('queued','running'))`,
		now, groupID, generation, groupID, generation)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		if _, err := createCompletionTx(ctx, tx, groupID, generation); err != nil {
			return err
		}
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
	// Locate the attempt and its generation. Retry children live only in the
	// attempt table; generation-0 children also own the substrate columns.
	var itemID, groupID string
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT i.id,i.group_id,a.generation FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		WHERE a.child_run_id=?`, runID).Scan(&itemID, &groupID, &generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("child Run has no delegation attempt")
		}
		return err
	}
	if generation == 0 {
		// Generation 0 owns the folded item columns; later generations never
		// rewrite the frozen substrate.
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status='succeeded', result_json=?
			WHERE id=? AND status IN ('pending','running')`, string(resultJSON), itemID); err != nil {
			return err
		}
	}
	// Terminalize the attempt state machine (idempotent) before settling. The
	// attempt status mirrors the submit_result contract so needs_input and
	// blocked attempts are addressable by the continuation commands.
	attemptStatus := domain.DelegationAttemptSucceeded
	switch output.Terminal.Status {
	case domain.SubmitBlocked:
		attemptStatus = domain.DelegationAttemptBlocked
	case domain.SubmitNeedsInput:
		attemptStatus = domain.DelegationAttemptNeedsInput
	}
	if err := settleAttemptTx(ctx, tx, runID, attemptStatus,
		resultJSON, string(output.Terminal.Status), "", ""); err != nil {
		return err
	}
	if attemptStatus == domain.DelegationAttemptNeedsInput {
		var sessionID, projectID string
		if err := tx.QueryRowContext(ctx, `SELECT ar.session_id,s.project_id FROM agent_runs ar
			JOIN sessions s ON s.id=ar.session_id WHERE ar.id=?`, runID).Scan(&sessionID, &projectID); err != nil {
			return err
		}
		if err := ProjectAttentionTx(ctx, tx, projectID, sessionID,
			domain.AttentionSourceDelegationItem, itemID, generation,
			domain.AttentionNeedsInput, true,
			map[string]any{"kind": "needs_input", "generation": generation,
				"summary": boundedAttentionSummary(string(resultJSON))},
			&domain.AttentionAction{Kind: "delegation_input", ItemID: itemID,
				ExpectedGeneration: generation}); err != nil {
			return err
		}
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
	if generation == 0 && remaining == 0 {
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

		// Inject folded child results into the parent's delegate_tasks tool call
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
	var itemID, groupID string
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT i.id,i.group_id,a.generation FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		WHERE a.child_run_id=?`, runID).Scan(&itemID, &groupID, &generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("child Run has no delegation attempt")
		}
		return "", false, err
	}
	if generation == 0 {
		itemUpdate, updateErr := tx.ExecContext(ctx, `UPDATE delegation_items SET status='failed',result_json=?
			WHERE id=? AND status IN ('pending','running')`, string(resultJSON), itemID)
		if updateErr != nil {
			return "", false, updateErr
		}
		if changed, _ := itemUpdate.RowsAffected(); changed != 1 {
			return "", false, fmt.Errorf("%w: child delegation item is already terminal", ErrDelegationConflict)
		}
	}
	// Terminalize the attempt state machine (idempotent) before settling.
	if err := settleAttemptTx(ctx, tx, runID, domain.DelegationAttemptFailed,
		resultJSON, string(domain.SubmitBlocked), code, message); err != nil {
		return "", false, err
	}
	// Fold the child's reservation back: release the reserved ceiling and
	// record actual usage exactly once (idempotent via root_reconciled_at).
	if err := reconcileRootBudgetTx(ctx, tx, runID); err != nil {
		return "", false, err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM delegation_items WHERE group_id=? AND status IN ('pending','running')`,
		groupID).Scan(&remaining); err != nil {
		return "", false, err
	}
	wakeParent := false
	if generation == 0 && remaining == 0 {
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

// dagItemView is one delegation item with its latest attempt status and child
// Run status, used by the dynamic task graph scheduler. For retry generations
// the view follows the newest attempt (the frozen item substrate columns never
// rewrite, so they cannot gate scheduling).
type dagItemView struct {
	ID            string
	Name          string
	Status        string
	AttemptChild  string
	Depends       []string
	AttemptStatus string
	RunStatus     string
}

// loadDagItemsTx loads all items of a group with their latest attempt status
// and child Run status. The latest attempt is the effective execution state
// for scheduling: for generation 0 it mirrors the item substrate columns; for
// retry generations it reflects the newest attempt without rewriting the
// frozen item row.
func loadDagItemsTx(ctx context.Context, tx *sql.Tx, groupID string) ([]dagItemView, error) {
	rows, err := tx.QueryContext(ctx, `SELECT i.id, i.name, i.status, i.depends_json,
		COALESCE((SELECT a.child_run_id FROM delegation_item_attempts a
			WHERE a.item_id=i.id ORDER BY a.generation DESC, a.created_at DESC LIMIT 1), COALESCE(i.child_run_id,'')) AS attempt_child_run_id,
		COALESCE((SELECT a.status FROM delegation_item_attempts a
			WHERE a.item_id=i.id ORDER BY a.generation DESC, a.created_at DESC LIMIT 1), i.status) AS attempt_status,
		COALESCE((SELECT ar.status FROM agent_runs ar WHERE ar.id=(
			SELECT a.child_run_id FROM delegation_item_attempts a
			WHERE a.item_id=i.id ORDER BY a.generation DESC, a.created_at DESC LIMIT 1)), '') AS run_status
		FROM delegation_items i WHERE i.group_id=?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []dagItemView
	for rows.Next() {
		var view dagItemView
		var dependsJSON string
		if err := rows.Scan(&view.ID, &view.Name, &view.Status, &dependsJSON, &view.AttemptChild, &view.AttemptStatus, &view.RunStatus); err != nil {
			return nil, err
		}
		if dependsJSON != "" && dependsJSON != "[]" {
			if err := json.Unmarshal([]byte(dependsJSON), &view.Depends); err != nil {
				return nil, fmt.Errorf("decode depends for task %s: %w", view.Name, err)
			}
		}
		items = append(items, view)
	}
	return items, rows.Err()
}

// advanceDagTx advances the dynamic task graph inside a Finalize transaction:
// dependent tasks whose dependencies reached a terminal failure are marked
// blocked (item + queued attempt), and dependent tasks whose dependencies all
// succeeded are reported ready for enqueue. Blocked state propagates
// transitively (a blocked dependency blocks its descendants) by iterating
// until stable, and ready tasks must still be queued at the Run level so
// already-started successors are never re-reported. A blocked item with a
// fresh retry attempt is unblocked (item substrate reset to running) when its
// dependencies all succeed, so a successful dependency retry lifts its blocked
// descendants. It is idempotent: re-running it after any attempt settles
// re-evaluates every non-terminal item against the latest attempt states.
// Blocked tasks never consume budget because their attempts never start.
func advanceDagTx(ctx context.Context, tx *sql.Tx, groupID string) ([]string, error) {
	items, err := loadDagItemsTx(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	hasDependencies := false
	for index := range items {
		if len(items[index].Depends) > 0 {
			hasDependencies = true
			break
		}
	}
	if !hasDependencies {
		// Legacy flat batches may contain duplicate names. They need no DAG
		// advancement (all children are entry tasks), so avoid constructing an
		// ambiguous name index and preserve their original parallel semantics.
		return nil, nil
	}
	byName := make(map[string]*dagItemView, len(items))
	for index := range items {
		byName[items[index].Name] = &items[index]
	}
	// Phase 1: propagate blocked state transitively until stable. A task is
	// blocked when any of its dependencies is failed/cancelled/blocked/not
	// authorized. Marking updates the in-memory snapshot so descendants of a
	// freshly blocked task are blocked in the same pass.
	changed := true
	for changed {
		changed = false
		for index := range items {
			item := &items[index]
			retryInFlight := item.Status == "blocked" && item.AttemptStatus != "blocked"
			if item.Status != "pending" && item.Status != "running" && !retryInFlight {
				continue
			}
			if len(item.Depends) == 0 {
				continue
			}
			anyFailed := false
			for _, depName := range item.Depends {
				dep, ok := byName[depName]
				if !ok {
					anyFailed = true // dangling reference: treat as unsatisfiable
					continue
				}
				switch dep.AttemptStatus {
				case "failed", "cancelled", "blocked", "not_authorized":
					anyFailed = true
				}
			}
			if !anyFailed {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status='blocked'
				WHERE id=? AND status IN ('pending','running')`, item.ID); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE delegation_item_attempts SET status='blocked'
				WHERE item_id=? AND status='queued'`, item.ID); err != nil {
				return nil, err
			}
			item.Status = "blocked"
			item.AttemptStatus = "blocked"
			changed = true
		}
	}
	// Phase 2: collect ready successors. A task is ready when every dependency
	// succeeded and its current attempt's child Run is still queued. A blocked
	// item with a fresh (non-blocked) attempt is unblocked when its
	// dependencies all succeed: a successful dependency retry lifts it.
	var ready []string
	for index := range items {
		item := &items[index]
		retryInFlight := item.Status == "blocked" && item.AttemptStatus != "blocked"
		if item.Status != "pending" && item.Status != "running" && !retryInFlight {
			continue
		}
		if len(item.Depends) == 0 {
			continue // entry tasks are enqueued at creation
		}
		allSettled := true
		for _, depName := range item.Depends {
			dep, ok := byName[depName]
			if !ok || dep.AttemptStatus != "succeeded" {
				allSettled = false
				break
			}
		}
		if allSettled && item.AttemptChild != "" && item.RunStatus == "queued" {
			if retryInFlight {
				// Unblock the frozen substrate: a successful dependency retry
				// lifts this task. The attempt row stays the source of truth.
				if _, err := tx.ExecContext(ctx, `UPDATE delegation_items SET status='running'
					WHERE id=? AND status='blocked'`, item.ID); err != nil {
					return nil, err
				}
				item.Status = "running"
			}
			ready = append(ready, item.AttemptChild)
		}
	}
	return ready, nil
}

// ReadySuccessorRuns returns the delegated child Runs of a task graph whose
// dependencies have all succeeded and which are therefore ready to start. It
// runs advanceDagTx in its own transaction (idempotent) so the coordinator can
// enqueue ready successors after a child settles, including successors whose
// blocked state was lifted by a successful dependency retry.
func (r *RunRepo) ReadySuccessorRuns(ctx context.Context, childRunID string) ([]string, error) {
	var groupID string
	if err := r.DB.QueryRowContext(ctx, `SELECT i.group_id FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id
		WHERE a.child_run_id=?`, childRunID).Scan(&groupID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // not a delegated child
		}
		return nil, err
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ready, err := advanceDagTx(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ready, nil
}

// ReadyChildrenForEnqueue filters a set of delegated child Runs down to those
// whose task dependencies are all satisfied. It is used at materialization
// time (initial delegate_tasks and retry generations) so dependent tasks stay
// queued until their dependencies settle; ReadySuccessorRuns wakes them later.
// Items are resolved through delegation_item_attempts because retry children
// never own the frozen delegation_items.child_run_id column.
func (r *DelegationRepo) ReadyChildrenForEnqueue(ctx context.Context, runIDs []string) ([]string, error) {
	if len(runIDs) == 0 {
		return nil, nil
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	type cachedDagGroup struct {
		items  []dagItemView
		byName map[string]*dagItemView
		byID   map[string]*dagItemView
	}
	groups := make(map[string]*cachedDagGroup)
	ready := make([]string, 0, len(runIDs))
	for _, runID := range runIDs {
		var groupID, itemID string
		if err := tx.QueryRowContext(ctx, `SELECT i.group_id,i.id FROM delegation_items i
			JOIN delegation_item_attempts a ON a.item_id=i.id
			WHERE a.child_run_id=?`, runID).Scan(&groupID, &itemID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				ready = append(ready, runID) // not a delegation child: keep caller semantics
				continue
			}
			return nil, err
		}
		group := groups[groupID]
		if group == nil {
			items, err := loadDagItemsTx(ctx, tx, groupID)
			if err != nil {
				return nil, err
			}
			group = &cachedDagGroup{
				items: items, byName: make(map[string]*dagItemView, len(items)),
				byID: make(map[string]*dagItemView, len(items)),
			}
			for index := range group.items {
				item := &group.items[index]
				group.byName[item.Name] = item
				group.byID[item.ID] = item
			}
			groups[groupID] = group
		}
		current := group.byID[itemID]
		if current == nil {
			ready = append(ready, runID) // defensive: unknown item keeps caller semantics
			continue
		}
		allSettled := true
		for _, depName := range current.Depends {
			dep, ok := group.byName[depName]
			if !ok || dep.AttemptStatus != "succeeded" {
				allSettled = false
				break
			}
		}
		if allSettled {
			ready = append(ready, runID)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ready, nil
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
	if err := r.DB.QueryRowContext(ctx, `SELECT i.assignment_json FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id WHERE a.child_run_id=?`,
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

// ReapOrphans interrupts generation-0 blocking children of terminal parents
// and reconciles terminal child facts. Background and explicit later-generation
// children are parent-owned but intentionally allowed to outlive the parent.
func (r *DelegationRepo) ReapOrphans(ctx context.Context) ([]string, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `SELECT c.id FROM agent_runs c
		JOIN agent_runs p ON p.id=c.parent_run_id
		LEFT JOIN delegation_item_attempts a ON a.child_run_id=c.id
		LEFT JOIN delegation_items i ON i.id=a.item_id
		LEFT JOIN delegation_handles h ON h.group_id=i.group_id
		WHERE c.run_kind='delegated_agent'
		  AND c.status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children')
		  AND p.status IN ('succeeded','failed','cancelled','interrupted')
		  AND (a.id IS NULL OR (a.generation=0 AND COALESCE(h.execution_mode,'blocking')='blocking'))
		ORDER BY c.created_at,c.id`)
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
	// Mirror the same facts into the attempt state machine.
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_item_attempts SET status=CASE
		WHEN (SELECT status FROM agent_runs WHERE id=child_run_id)='succeeded' THEN 'succeeded'
		WHEN (SELECT status FROM agent_runs WHERE id=child_run_id)='cancelled' THEN 'cancelled'
		WHEN (SELECT status FROM agent_runs WHERE id=child_run_id)='interrupted' THEN 'interrupted'
		ELSE 'failed' END
		WHERE status IN ('queued','running') AND child_run_id IN (
			SELECT id FROM agent_runs WHERE status IN ('succeeded','failed','cancelled','interrupted'))`); err != nil {
		return nil, err
	}
	// Settle generations whose generation-scoped attempts are all terminal,
	// regardless of whether the group status update below has run yet. Item
	// substrate columns are frozen at generation 0 and never gate later rounds.
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_group_generations SET status='settled',completed_at=?
		WHERE status IN ('queued','running')
		  AND NOT EXISTS (SELECT 1 FROM delegation_item_attempts a JOIN delegation_items i ON i.id=a.item_id
			WHERE i.group_id=delegation_group_generations.group_id
			  AND a.generation=delegation_group_generations.generation
			  AND a.status IN ('queued','running'))`,
		now); err != nil {
		return nil, err
	}
	// Reconcile every terminal child reservation, including children that were
	// never selected by the orphan scan above. Idempotent via root_reconciled_at.
	terminalRows, err := tx.QueryContext(ctx, `SELECT child_run_id FROM delegation_item_attempts
		WHERE child_run_id IN (
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

// RecoverDelegation settles every generation whose attempts are all terminal
// and re-reconciles terminal child budgets. It runs after RecoverActive and
// ReapOrphans in the startup sequence and is fully idempotent.
func (r *DelegationRepo) RecoverDelegation(ctx context.Context) ([]string, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Re-advance the dynamic task graph for every group before the settlement
	// scan: a crash between an attempt settling and its dependent tasks being
	// marked blocked leaves queued attempts that would otherwise stall the
	// generation settlement below. advanceDagTx is idempotent, so re-running it
	// here is safe (ready successors are enqueued by the recovery main path).
	groupRows, err := tx.QueryContext(ctx, `SELECT id FROM delegation_groups`)
	if err != nil {
		return nil, err
	}
	var groupIDs []string
	for groupRows.Next() {
		var groupID string
		if err := groupRows.Scan(&groupID); err != nil {
			groupRows.Close()
			return nil, err
		}
		groupIDs = append(groupIDs, groupID)
	}
	groupErr := groupRows.Err()
	groupCloseErr := groupRows.Close()
	if groupErr != nil || groupCloseErr != nil {
		return nil, fmt.Errorf("scan delegation groups for DAG recovery: %w", groupErr)
	}
	for _, groupID := range groupIDs {
		// Recovery needs advanceDagTx here to reconstruct durable blocked
		// states before generation settlement. Ready child IDs are deliberately
		// ignored: startup subsequently passes queued Runs through
		// ReadyChildrenForEnqueue, which performs the actual enqueue decision.
		if _, err := advanceDagTx(ctx, tx, groupID); err != nil {
			return nil, fmt.Errorf("advance task graph for group %s during recovery: %w", groupID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_group_generations SET status='settled',completed_at=?
		WHERE status IN ('queued','running')
		  AND NOT EXISTS (SELECT 1 FROM delegation_item_attempts a JOIN delegation_items i ON i.id=a.item_id
			WHERE i.group_id=delegation_group_generations.group_id
			  AND a.generation=delegation_group_generations.generation
			  AND a.status IN ('queued','running'))`,
		now); err != nil {
		return nil, err
	}
	// Approved authorizations whose generation never materialized (a crash
	// between the generation insert and child creation) fail the generation
	// closed so the operator can retry from the previous selection.
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_group_generations SET status='failed',completed_at=?
		WHERE status='awaiting_authorization' AND group_id IN (
			SELECT ar.group_id FROM delegation_approval_requests ar
			WHERE ar.status='rejected' AND ar.generation=delegation_group_generations.generation)`,
		now); err != nil {
		return nil, err
	}
	// Reconcile every terminal child reservation exactly once.
	terminalRows, err := tx.QueryContext(ctx, `SELECT child_run_id FROM delegation_item_attempts
		WHERE child_run_id IN (
			SELECT id FROM agent_runs WHERE status IN ('succeeded','failed','cancelled','interrupted'))`)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for terminalRows.Next() {
		var id string
		if err := terminalRows.Scan(&id); err != nil {
			terminalRows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := terminalRows.Close(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := reconcileRootBudgetTx(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nil, nil
}

// injectFoldedResultsTx updates the parent's delegate_tasks tool_calls row with
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

	// Find the delegate_tasks tool call for this parent run.
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
