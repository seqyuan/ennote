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
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// itemState is the per-item retry planning record.
type itemState struct {
	item          domain.DelegationItem
	attemptID     string
	attemptChild  string
	attemptStatus domain.DelegationAttemptStatus
	resultDigest  string
}

// RetryGeneration atomically creates the next generation for a settled group.
// Only explicitly selected eligible attempts (failed/cancelled/interrupted) get
// new child Runs; every other item reuses its frozen current attempt. A budget
// increase freezes a new authorization snapshot and requires a durable retry
// budget approval before any child Run is created. All state changes commit in
// one transaction, so a stale, conflicting, or over-budget retry creates
// nothing.
func (r *DelegationRepo) RetryGeneration(ctx context.Context, groupID string,
	input domain.RetryDelegationInput) (*domain.DelegationGeneration, []*domain.AgentRun, *domain.DelegationApprovalRequest, error) {
	if strings.TrimSpace(input.ClientRequestID) == "" {
		return nil, nil, nil, fmt.Errorf("retry client request id is required")
	}
	if len(input.ItemIDs) == 0 {
		return nil, nil, nil, fmt.Errorf("retry requires at least one selected item")
	}

	// Idempotent fast path: the client request id already produced a generation.
	existing, children, approval, err := r.loadRetryResult(ctx, groupID, input.ClientRequestID)
	if err == nil && existing != nil {
		return existing, children, approval, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()

	// In-transaction idempotency check: a concurrent caller may have committed
	// the same client request id between our fast-path check and this
	// transaction. The read lock serializes the check against that commit.
	var alreadyExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM delegation_group_generations
		WHERE group_id=? AND client_request_id=?`, groupID, input.ClientRequestID).Scan(&alreadyExists); err != nil {
		return nil, nil, nil, err
	}
	if alreadyExists > 0 {
		if err := tx.Rollback(); err != nil {
			return nil, nil, nil, err
		}
		existing, children, approval, loadErr := r.loadRetryResult(ctx, groupID, input.ClientRequestID)
		if loadErr != nil {
			return nil, nil, nil, loadErr
		}
		return existing, children, approval, nil
	}

	var parentRunID, sessionID string
	var currentGeneration int
	if err := tx.QueryRowContext(ctx, `SELECT ar.id,ar.session_id,g.current_generation
		FROM delegation_groups g JOIN agent_runs ar ON ar.id=g.parent_run_id
		WHERE g.id=?`, groupID).Scan(&parentRunID, &sessionID, &currentGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil, ErrDelegationGroupNotFound
		}
		return nil, nil, nil, err
	}
	if currentGeneration != input.ExpectedGeneration {
		return nil, nil, nil, domain.NewCodedError(domain.ErrorDelegationGenerationConflict,
			fmt.Errorf("expected generation %d, current is %d", input.ExpectedGeneration, currentGeneration))
	}
	nextGeneration := currentGeneration + 1

	// The current generation must be terminal before retry is possible.
	var currentStatus domain.DelegationGenerationStatus
	if err := tx.QueryRowContext(ctx, `SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=?`, groupID, currentGeneration).Scan(&currentStatus); err != nil {
		return nil, nil, nil, ErrDelegationAttemptNotFound
	}
	switch currentStatus {
	case domain.DelegationGenerationSettled, domain.DelegationGenerationFailed, domain.DelegationGenerationCancelled:
	default:
		return nil, nil, nil, fmt.Errorf("%w: current generation is not terminal", ErrDelegationConflict)
	}

	// Resolve every logical item and its current attempt.
	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.name,i.role_version_id,i.output_contract,i.budget_json,i.status,
		COALESCE(a.id,''),COALESCE(a.child_run_id,''),COALESCE(a.status,''),COALESCE(a.result_digest,'')
		FROM delegation_items i
		LEFT JOIN delegation_item_attempts a ON a.item_id=i.id AND a.generation=?
		WHERE i.group_id=? ORDER BY i.ordinal`, currentGeneration, groupID)
	if err != nil {
		return nil, nil, nil, err
	}
	items := make([]itemState, 0)
	for rows.Next() {
		var state itemState
		var itemStatus, attemptID, attemptChild, attemptStatus, resultDigest string
		var budgetJSON string
		if err := rows.Scan(&state.item.ID, &state.item.Name, &state.item.RoleVersionID,
			&state.item.OutputContract, &budgetJSON, &itemStatus,
			&attemptID, &attemptChild, &attemptStatus, &resultDigest); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		state.item.Status = domain.DelegationItemStatus(itemStatus)
		state.item.BudgetJSON = json.RawMessage(budgetJSON)
		state.attemptID = attemptID
		state.attemptChild = attemptChild
		state.attemptStatus = domain.DelegationAttemptStatus(attemptStatus)
		state.resultDigest = resultDigest
		items = append(items, state)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	if len(items) == 0 {
		return nil, nil, nil, fmt.Errorf("delegation group has no items")
	}

	// Validate selection: every selected item must exist and be retry-eligible.
	selected := make(map[string]struct{}, len(input.ItemIDs))
	for _, itemID := range input.ItemIDs {
		if strings.TrimSpace(itemID) == "" {
			return nil, nil, nil, fmt.Errorf("retry selection contains an empty item id")
		}
		selected[itemID] = struct{}{}
	}
	reused := make([]domain.DelegationAttemptReference, 0, len(items))
	retryItems := make([]itemState, 0, len(selected))
	for index := range items {
		state := &items[index]
		if state.attemptID == "" {
			return nil, nil, nil, fmt.Errorf("item %s has no attempt in generation %d", state.item.ID, currentGeneration)
		}
		_, isSelected := selected[state.item.ID]
		if !isSelected {
			reused = append(reused, domain.DelegationAttemptReference{
				ItemID: state.item.ID, AttemptID: state.attemptID,
				Generation: currentGeneration, ChildRunID: state.attemptChild,
				ResultDigest: state.resultDigest,
			})
			continue
		}
		if !domain.AttemptRetryEligible(state.attemptStatus) {
			return nil, nil, nil, domain.NewCodedError(domain.ErrorDelegationRetryIneligible,
				fmt.Errorf("attempt for item %s has status %s", state.item.ID, state.attemptStatus))
		}
		retryItems = append(retryItems, *state)
	}
	if len(retryItems) == 0 {
		return nil, nil, nil, domain.NewCodedError(domain.ErrorDelegationRetryIneligible,
			errors.New("no selected item is retry-eligible"))
	}

	// Freeze authorization and budget snapshots for the new generation. The
	// Role version is the frozen item version — a republished or archived Role
	// never replaces it, but a disabled identity fails closed.
	authSnapshot := make([]map[string]string, 0, len(items))
	var budgetSum domain.BudgetCeilingJSON
	approvalRequired := false
	for index := range retryItems {
		state := &retryItems[index]
		effective := string(state.item.BudgetJSON)
		ceiling := domain.BudgetCeilingJSON{}
		if err := json.Unmarshal(state.item.BudgetJSON, &ceiling); err != nil {
			return nil, nil, nil, err
		}
		if override, ok := input.BudgetOverrides[state.item.ID]; ok {
			if err := r.validateRetryRoleAndCeilingTx(ctx, tx, state.item.RoleVersionID, override); err != nil {
				return nil, nil, nil, err
			}
			if budgetIncreased(ceiling, override) {
				approvalRequired = true
			}
			ceiling = override
			effective = mustMarshalJSON(ceiling)
		} else {
			if err := r.validateRetryRoleAndCeilingTx(ctx, tx, state.item.RoleVersionID, ceiling); err != nil {
				return nil, nil, nil, err
			}
		}
		state.item.BudgetJSON = json.RawMessage(effective)
		budgetSum.MaxModelCalls += ceiling.MaxModelCalls
		budgetSum.MaxToolCalls += ceiling.MaxToolCalls
		budgetSum.MaxTotalTokens += ceiling.MaxTotalTokens
		budgetSum.MaxOutputTokens += ceiling.MaxOutputTokens
		budgetSum.MaxCostMicros += ceiling.MaxCostMicros
		authSnapshot = append(authSnapshot, map[string]string{
			"itemId": state.item.ID, "roleVersionId": state.item.RoleVersionID,
		})
	}
	for _, reference := range reused {
		authSnapshot = append(authSnapshot, map[string]string{
			"itemId": reference.ItemID, "roleVersionId": referenceAttemptRoleVersion(ctx, tx, reference.AttemptID),
		})
	}
	authDigest, err := digestJSON(authSnapshot)
	if err != nil {
		return nil, nil, nil, err
	}
	budgetJSON, err := json.Marshal(budgetSum)
	if err != nil {
		return nil, nil, nil, err
	}
	budgetDigest, err := digestJSON(budgetSum)
	if err != nil {
		return nil, nil, nil, err
	}
	reusedJSON, err := json.Marshal(reused)
	if err != nil {
		return nil, nil, nil, err
	}
	retrySelectionJSON, err := json.Marshal(input.ItemIDs)
	if err != nil {
		return nil, nil, nil, err
	}

	generationStatus := domain.DelegationGenerationQueued
	if approvalRequired {
		generationStatus = domain.DelegationGenerationAwaitingAuthorization
	}
	now := time.Now().UTC()
	generationID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_group_generations
		(id,group_id,generation,kind,status,retry_selection_json,reused_attempts_json,
		 authorization_snapshot_json,authorization_snapshot_digest,budget_snapshot_json,budget_snapshot_digest,
		 client_request_id,created_at)
		VALUES(?,?,?, 'retry',?,?,?,?,?,?,?,?,?)`,
		generationID, groupID, nextGeneration, string(generationStatus),
		string(retrySelectionJSON), string(reusedJSON), authSnapshotJSON(authSnapshot),
		authDigest, string(budgetJSON), budgetDigest, input.ClientRequestID, now.Format(time.RFC3339Nano)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			// Either the same client request id (idempotent replay) or a
			// concurrent retry raced us on the generation slot.
			existing, children, approval, loadErr := r.loadRetryResult(ctx, groupID, input.ClientRequestID)
			if loadErr != nil {
				return nil, nil, nil, loadErr
			}
			if existing != nil {
				return existing, children, approval, nil
			}
			return nil, nil, nil, domain.NewCodedError(domain.ErrorDelegationGenerationConflict,
				errors.New("group generation changed concurrently"))
		}
		return nil, nil, nil, fmt.Errorf("create retry generation: %w", err)
	}

	// CAS the group cursor to the new generation before any child work.
	cursor, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET current_generation=?,updated_at=?
		WHERE id=? AND current_generation=?`, nextGeneration, now.Format(time.RFC3339Nano),
		groupID, currentGeneration)
	if err != nil {
		return nil, nil, nil, err
	}
	if changed, _ := cursor.RowsAffected(); changed != 1 {
		return nil, nil, nil, domain.NewCodedError(domain.ErrorDelegationGenerationConflict,
			fmt.Errorf("group generation changed concurrently"))
	}

	generation := &domain.DelegationGeneration{
		ID: generationID, GroupID: groupID, Generation: nextGeneration,
		Kind: domain.DelegationGenerationRetry, Status: generationStatus,
		RetrySelection: input.ItemIDs, ReusedAttempts: reused,
		AuthorizationSnapshot: authSnapshotJSON(authSnapshot), BudgetSnapshot: budgetJSON,
		ClientRequestID: input.ClientRequestID, CreatedAt: now,
	}

	if approvalRequired {
		approval, err := insertRetryBudgetApprovalTx(ctx, tx, groupID, nextGeneration, parentRunID, sessionID, retryItems, input)
		if err != nil {
			return nil, nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, nil, err
		}
		return generation, nil, approval, nil
	}

	// Reserve root budget and materialize only the selected attempts.
	for index := range retryItems {
		state := &retryItems[index]
		var override *domain.BudgetCeilingJSON
		if value, ok := input.BudgetOverrides[state.item.ID]; ok {
			override = &value
		}
		child, childErr := createChildRunTx(ctx, tx, CreateChildRunInput{
			ParentRunID: parentRunID, ItemID: state.item.ID, SessionID: sessionID,
			Generation: nextGeneration, RetryOfAttemptID: state.attemptID,
			BudgetOverride: override, AllowTerminalParent: true,
		})
		if childErr != nil {
			return nil, nil, nil, childErr
		}
		children = append(children, child)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	return generation, children, nil, nil
}

// loadRetryResult returns the generation and children already produced for a
// client request id, or (nil,nil,nil) when none exists.
func (r *DelegationRepo) loadRetryResult(ctx context.Context, groupID, clientRequestID string) (*domain.DelegationGeneration, []*domain.AgentRun, *domain.DelegationApprovalRequest, error) {
	var generation domain.DelegationGeneration
	var status, kind, createdAt string
	var completedAt sql.NullString
	var retrySelectionJSON, reusedJSON, authJSON, budgetJSON string
	err := r.DB.QueryRowContext(ctx, `SELECT id,generation,kind,status,retry_selection_json,reused_attempts_json,
		authorization_snapshot_json,budget_snapshot_json,client_request_id,created_at,completed_at
		FROM delegation_group_generations WHERE group_id=? AND client_request_id=?`,
		groupID, clientRequestID).Scan(&generation.ID, &generation.Generation, &kind, &status,
		&retrySelectionJSON, &reusedJSON, &authJSON, &budgetJSON, &generation.ClientRequestID,
		&createdAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	generation.GroupID = groupID
	generation.Kind = domain.DelegationGenerationKind(kind)
	generation.Status = domain.DelegationGenerationStatus(status)
	generation.AuthorizationSnapshot = json.RawMessage(authJSON)
	generation.BudgetSnapshot = json.RawMessage(budgetJSON)
	if err := json.Unmarshal([]byte(retrySelectionJSON), &generation.RetrySelection); err != nil {
		return nil, nil, nil, err
	}
	if err := json.Unmarshal([]byte(reusedJSON), &generation.ReusedAttempts); err != nil {
		return nil, nil, nil, err
	}
	generation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, nil, nil, err
	}
	if completedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, completedAt.String)
		if parseErr != nil {
			return nil, nil, nil, parseErr
		}
		generation.CompletedAt = &parsed
	}

	children, err := r.childrenForGeneration(ctx, groupID, generation.Generation)
	if err != nil {
		return nil, nil, nil, err
	}
	var approval *domain.DelegationApprovalRequest
	if generation.Status == domain.DelegationGenerationAwaitingAuthorization {
		approval, err = r.approvalForGeneration(ctx, groupID, generation.Generation)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return &generation, children, approval, nil
}

func (r *DelegationRepo) childrenForGeneration(ctx context.Context, groupID string, generation int) ([]*domain.AgentRun, error) {
	rows, err := r.DB.QueryContext(ctx, runSelect+` JOIN delegation_item_attempts a ON a.child_run_id=agent_runs.id
		JOIN delegation_items i ON i.id=a.item_id
		WHERE i.group_id=? AND a.generation=? ORDER BY i.ordinal`, groupID, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	children := make([]*domain.AgentRun, 0)
	for rows.Next() {
		run, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		children = append(children, &run)
	}
	return children, rows.Err()
}

func (r *DelegationRepo) approvalForGeneration(ctx context.Context, groupID string, generation int) (*domain.DelegationApprovalRequest, error) {
	var approval domain.DelegationApprovalRequest
	var itemsJSON, requestedAt string
	err := r.DB.QueryRowContext(ctx, `SELECT id,group_id,generation,kind,parent_run_id,session_id,status,items_json,requested_at
		FROM delegation_approval_requests WHERE group_id=? AND generation=?`, groupID, generation).
		Scan(&approval.ID, &approval.GroupID, &approval.Generation, &approval.Kind,
			&approval.ParentRunID, &approval.SessionID, &approval.Status, &itemsJSON, &requestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	approval.ItemsJSON = json.RawMessage(itemsJSON)
	approval.RequestedAt, err = time.Parse(time.RFC3339Nano, requestedAt)
	if err != nil {
		return nil, err
	}
	return &approval, nil
}

// validateRetryRoleAndCeilingTx fails closed on a disabled or unpublished Role
// identity while never replacing the frozen version, and validates an override
// against the frozen Role's budget ceiling.
func (r *DelegationRepo) validateRetryRoleAndCeilingTx(ctx context.Context, tx *sql.Tx,
	roleVersionID string, request domain.BudgetCeilingJSON) error {
	var profileStatus, versionStatus string
	var delegationEnabled int
	var definitionJSON string
	if err := tx.QueryRowContext(ctx, `SELECT p.status,p.delegation_enabled,v.status,v.definition_json
		FROM agent_profiles p JOIN agent_profile_versions v ON v.id=? AND v.agent_profile_id=p.id
		WHERE v.id=?`, roleVersionID, roleVersionID).Scan(
		&profileStatus, &delegationEnabled, &versionStatus, &definitionJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: frozen Role version is unavailable", ErrDelegationNotAuthorized)
		}
		return err
	}
	if profileStatus != "active" || delegationEnabled != 1 || versionStatus != "published" {
		return fmt.Errorf("%w: Role identity is disabled or unpublished", ErrDelegationNotAuthorized)
	}
	var definition domain.RoleDefinition
	if err := json.Unmarshal([]byte(definitionJSON), &definition); err != nil {
		return fmt.Errorf("decode frozen Role definition: %w", err)
	}
	if err := validateDelegationBudget(request, definition.DelegationPolicy.BudgetCeiling); err != nil {
		return fmt.Errorf("%w: %v", ErrDelegationNotAuthorized, err)
	}
	return nil
}

func insertRetryBudgetApprovalTx(ctx context.Context, tx *sql.Tx, groupID string, generation int,
	parentRunID, sessionID string, retryItems []itemState, input domain.RetryDelegationInput) (*domain.DelegationApprovalRequest, error) {
	items := make([]map[string]any, 0, len(retryItems))
	for _, state := range retryItems {
		var ceiling domain.BudgetCeilingJSON
		if override, ok := input.BudgetOverrides[state.item.ID]; ok {
			ceiling = override
		} else if err := json.Unmarshal(state.item.BudgetJSON, &ceiling); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"itemId": state.item.ID, "name": state.item.Name,
			"retryOfAttemptId": state.attemptID, "budget": ceiling,
		})
	}
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	approval := &domain.DelegationApprovalRequest{
		ID: uuid.NewString(), GroupID: groupID, Generation: generation, Kind: "retry_budget",
		ParentRunID: parentRunID, SessionID: sessionID, Status: "pending",
		ItemsJSON: itemsJSON, RequestedAt: time.Now().UTC(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_approval_requests
		(id,group_id,generation,kind,parent_run_id,session_id,status,items_json,requested_at)
		VALUES(?,?,?,?,?,?,'pending',?,?)`,
		approval.ID, groupID, generation, "retry_budget", parentRunID, sessionID,
		string(itemsJSON), now); err != nil {
		return nil, fmt.Errorf("create retry budget approval: %w", err)
	}
	return approval, nil
}

func decodeBudgetCeiling(raw json.RawMessage) (domain.BudgetCeilingJSON, error) {
	var ceiling domain.BudgetCeilingJSON
	if len(raw) == 0 {
		return ceiling, nil
	}
	err := json.Unmarshal(raw, &ceiling)
	return ceiling, err
}

func budgetIncreased(before, after domain.BudgetCeilingJSON) bool {
	return after.MaxModelCalls > before.MaxModelCalls ||
		after.MaxToolCalls > before.MaxToolCalls ||
		after.MaxTotalTokens > before.MaxTotalTokens ||
		after.MaxOutputTokens > before.MaxOutputTokens ||
		after.MaxCostMicros > before.MaxCostMicros
}

func mustMarshalJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func authSnapshotJSON(selection []map[string]string) json.RawMessage {
	encoded, _ := json.Marshal(selection)
	return encoded
}

// referenceAttemptRoleVersion returns the frozen Role version of a reused
// attempt (used to complete the generation authorization snapshot).
func referenceAttemptRoleVersion(ctx context.Context, tx *sql.Tx, attemptID string) string {
	var version string
	_ = tx.QueryRowContext(ctx, `SELECT role_version_id FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id WHERE a.id=?`, attemptID).Scan(&version)
	return version
}
