package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// Inspect returns the parent-visible projection of a delegation group: logical
// items with their full attempt history, all generations, pending
// authorization, and the currently valid actions. It never exposes the private
// transcript, credentials, Role prompt, or full assignment.
func (r *DelegationRepo) Inspect(ctx context.Context, groupID string) (*domain.DelegationInspection, error) {
	group, err := r.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	inspection := &domain.DelegationInspection{Group: *group, Items: []domain.DelegationInspectionItem{}, Generations: []domain.DelegationGeneration{}}
	if err := r.DB.QueryRowContext(ctx, `SELECT current_generation FROM delegation_groups WHERE id=?`,
		groupID).Scan(&inspection.CurrentGeneration); err != nil {
		return nil, err
	}

	// Items + attempts in ordinal order.
	rows, err := r.DB.QueryContext(ctx, `SELECT i.id,i.name,i.status,i.ordinal,
		a.id,COALESCE(a.generation,0),COALESCE(a.status,''),COALESCE(a.child_run_id,''),
		COALESCE(a.result_json,''),COALESCE(a.result_digest,''),COALESCE(a.error_code,''),
		COALESCE(a.actual_usage_json,'{}')
		FROM delegation_items i
		LEFT JOIN delegation_item_attempts a ON a.item_id=i.id
		WHERE i.group_id=? ORDER BY i.ordinal,a.generation`, groupID)
	if err != nil {
		return nil, err
	}
	itemIndexes := make(map[string]int)
	for rows.Next() {
		var itemID, name, itemStatus, attemptID, generation, attemptStatus, childRunID string
		var resultJSON, resultDigest, errorCode, usageJSON string
		if err := rows.Scan(&itemID, &name, &itemStatus, new(int),
			&attemptID, &generation, &attemptStatus, &childRunID,
			&resultJSON, &resultDigest, &errorCode, &usageJSON); err != nil {
			rows.Close()
			return nil, err
		}
		index, ok := itemIndexes[itemID]
		if !ok {
			index = len(inspection.Items)
			itemIndexes[itemID] = index
			inspection.Items = append(inspection.Items, domain.DelegationInspectionItem{
				ItemID: itemID, Name: name, Status: domain.DelegationItemStatus(itemStatus),
				Attempts: []domain.DelegationAttemptSummary{},
			})
		}
		summary := domain.DelegationAttemptSummary{
			AttemptID: attemptID, Generation: atoiSafe(generation), ChildRunID: childRunID,
			Status: domain.DelegationAttemptStatus(attemptStatus), ResultDigest: resultDigest,
			ErrorCode: errorCode,
		}
		if resultJSON != "" {
			var result domain.SubmitResult
			if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
				rows.Close()
				return nil, fmt.Errorf("decode attempt result for %s: %w", attemptID, err)
			}
			summary.Result = &result
		}
		if err := json.Unmarshal([]byte(usageJSON), &summary.Usage); err != nil {
			rows.Close()
			return nil, err
		}
		inspection.Items[index].Attempts = append(inspection.Items[index].Attempts, summary)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// Generations.
	genRows, err := r.DB.QueryContext(ctx, `SELECT id,generation,kind,status,retry_selection_json,reused_attempts_json,
		authorization_snapshot_json,budget_snapshot_json,client_request_id,created_at,completed_at
		FROM delegation_group_generations WHERE group_id=? ORDER BY generation`, groupID)
	if err != nil {
		return nil, err
	}
	for genRows.Next() {
		var generation domain.DelegationGeneration
		var kind, status, selectionJSON, reusedJSON, authJSON, budgetJSON, createdAt string
		var completedAt sql.NullString
		if err := genRows.Scan(&generation.ID, &generation.Generation, &kind, &status,
			&selectionJSON, &reusedJSON, &authJSON, &budgetJSON, &generation.ClientRequestID,
			&createdAt, &completedAt); err != nil {
			genRows.Close()
			return nil, err
		}
		generation.GroupID = groupID
		generation.Kind = domain.DelegationGenerationKind(kind)
		generation.Status = domain.DelegationGenerationStatus(status)
		generation.AuthorizationSnapshot = json.RawMessage(authJSON)
		generation.BudgetSnapshot = json.RawMessage(budgetJSON)
		if err := json.Unmarshal([]byte(selectionJSON), &generation.RetrySelection); err != nil {
			genRows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(reusedJSON), &generation.ReusedAttempts); err != nil {
			genRows.Close()
			return nil, err
		}
		generation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			genRows.Close()
			return nil, err
		}
		if completedAt.Valid {
			parsed, parseErr := time.Parse(time.RFC3339Nano, completedAt.String)
			if parseErr != nil {
				genRows.Close()
				return nil, parseErr
			}
			generation.CompletedAt = &parsed
		}
		inspection.Generations = append(inspection.Generations, generation)
	}
	if err := genRows.Close(); err != nil {
		return nil, err
	}

	// Pending authorization for the current generation.
	var approval *domain.DelegationApprovalRequest
	if inspection.CurrentGeneration > 0 {
		approvalRepo := &DelegationApprovalRepo{DB: r.DB}
		var found *domain.DelegationApprovalRequest
		rows2, err := r.DB.QueryContext(ctx, `SELECT id FROM delegation_approval_requests
			WHERE group_id=? AND generation=? AND status='pending'`, groupID, inspection.CurrentGeneration)
		if err != nil {
			return nil, err
		}
		if rows2.Next() {
			var id string
			if err := rows2.Scan(&id); err != nil {
				rows2.Close()
				return nil, err
			}
			if err := rows2.Close(); err != nil {
				return nil, err
			}
			found, err = approvalRepo.loadApproval(ctx, id)
			if err != nil {
				return nil, err
			}
			if err := rows2.Err(); err != nil {
				return nil, err
			}
		} else {
			if err := rows2.Close(); err != nil {
				return nil, err
			}
		}
		approval = found
	}
	inspection.PendingApproval = approval
	inspection.ValidActions = validGroupActions(inspection)
	return inspection, nil
}

// validGroupActions derives the currently permitted operations from the
// frozen state, never from live Role drafts or timestamps.
func validGroupActions(inspection *domain.DelegationInspection) []string {
	actions := make([]string, 0)
	if inspection.PendingApproval != nil {
		actions = append(actions, "decide_approval")
		return actions
	}
	var currentStatus domain.DelegationGenerationStatus
	for index := range inspection.Generations {
		if inspection.Generations[index].Generation == inspection.CurrentGeneration {
			currentStatus = inspection.Generations[index].Status
		}
	}
	if currentStatus.Terminal() {
		for index := range inspection.Items {
			for _, attempt := range inspection.Items[index].Attempts {
				if attempt.Generation == inspection.CurrentGeneration &&
					domain.AttemptRetryEligible(attempt.Status) {
					actions = append(actions, "retry")
					break
				}
			}
		}
	}
	switch currentStatus {
	case domain.DelegationGenerationQueued, domain.DelegationGenerationRunning:
		actions = append(actions, "cancel")
	}
	return actions
}

func atoiSafe(value string) int {
	var result int
	_, _ = fmt.Sscanf(value, "%d", &result)
	return result
}

// CancelGroup cancels the active attempts of the group's current generation
// and terminalizes that generation. Generation 0 and the frozen substrate
// columns are never rewritten.
func (r *DelegationRepo) CancelGroup(ctx context.Context, groupID string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT current_generation FROM delegation_groups WHERE id=?`, groupID).
		Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDelegationGroupNotFound
		}
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.child_run_id FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		WHERE i.group_id=? AND a.generation=? AND a.child_run_id IN (
			SELECT id FROM agent_runs WHERE status IN ('queued','running','waiting_for_approval'))`,
		groupID, generation)
	if err != nil {
		return err
	}
	childIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		childIDs = append(childIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	runs := &RunRepo{DB: r.DB}
	for _, childID := range childIDs {
		if err := runs.Cancel(ctx, childID); err != nil {
			return err
		}
	}
	if _, err := r.DB.ExecContext(ctx, `UPDATE delegation_group_generations SET status='cancelled',completed_at=?
		WHERE group_id=? AND generation=? AND status IN ('queued','running')`,
		now, groupID, generation); err != nil {
		return err
	}
	if _, err := r.DB.ExecContext(ctx, `UPDATE delegation_groups SET status='cancelled',updated_at=?
		WHERE id=? AND status IN ('waiting_children','settled')`, now, groupID); err != nil {
		return err
	}
	return nil
}

var _ = strings.TrimSpace

// HandleForGroup returns the stable delivery handle of a delegation group.
func (r *DelegationRepo) HandleForGroup(ctx context.Context, groupID string) (*domain.DelegationHandle, error) {
	var handle domain.DelegationHandle
	var mode, status, createdAt, updatedAt string
	var autoResume int
	err := r.DB.QueryRowContext(ctx, `SELECT id,group_id,session_id,source_parent_run_id,source_branch_id,
		execution_mode,auto_resume,status,created_at,updated_at
		FROM delegation_handles WHERE group_id=?`, groupID).Scan(
		&handle.ID, &handle.GroupID, &handle.SessionID, &handle.SourceParentRunID,
		&handle.SourceBranchID, &mode, &autoResume, &status, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDelegationGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	handle.ExecutionMode = domain.DelegationExecutionMode(mode)
	handle.AutoResume = autoResume == 1
	handle.Status = status
	handle.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	handle.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, err
	}
	return &handle, nil
}
