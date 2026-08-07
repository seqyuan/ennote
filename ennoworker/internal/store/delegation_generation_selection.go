package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// itemState is one item's selected attempt in a logical generation. The
// selected attempt may have been created in this generation or explicitly
// reused from an earlier generation.
type itemState struct {
	item          domain.DelegationItem
	attemptID     string
	attemptChild  string
	attemptStatus domain.DelegationAttemptStatus
	resultDigest  string
	resultJSON    json.RawMessage
	attemptGen    int
}

// resolveGenerationItemStatesTx resolves the complete logical selection for a
// generation. New attempts override reused references; every item must resolve
// to exactly one immutable attempt.
func resolveGenerationItemStatesTx(ctx context.Context, tx *sql.Tx, groupID string, generation int) ([]itemState, error) {
	var reusedJSON string
	if err := tx.QueryRowContext(ctx, `SELECT reused_attempts_json FROM delegation_group_generations
		WHERE group_id=? AND generation=?`, groupID, generation).Scan(&reusedJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrDelegationAttemptNotFound
		}
		return nil, err
	}
	var reused []domain.DelegationAttemptReference
	if err := json.Unmarshal([]byte(reusedJSON), &reused); err != nil {
		return nil, fmt.Errorf("decode reused attempts: %w", err)
	}
	reusedByItem := make(map[string]domain.DelegationAttemptReference, len(reused))
	for _, reference := range reused {
		if reference.ItemID == "" || reference.AttemptID == "" {
			return nil, fmt.Errorf("generation %d has an invalid reused attempt reference", generation)
		}
		if _, exists := reusedByItem[reference.ItemID]; exists {
			return nil, fmt.Errorf("generation %d reuses item %s more than once", generation, reference.ItemID)
		}
		reusedByItem[reference.ItemID] = reference
	}

	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.name,i.role_version_id,i.assignment_json,
		i.output_contract,i.budget_json,COALESCE(i.result_json,''),i.status,i.ordinal,i.created_at,
		COALESCE(i.depends_json,'[]'),COALESCE(i.skills_json,'[]'),
		COALESCE(a.id,''),COALESCE(a.child_run_id,''),COALESCE(a.status,''),
		COALESCE(a.result_digest,''),COALESCE(a.result_json,''),COALESCE(a.generation,-1)
		FROM delegation_items i
		LEFT JOIN delegation_item_attempts a ON a.item_id=i.id AND a.generation=?
		WHERE i.group_id=? ORDER BY i.ordinal`, generation, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	states := make([]itemState, 0)
	for rows.Next() {
		var state itemState
		var assignmentJSON, budgetJSON, itemResultJSON, itemStatus, createdAt, dependsJSON, skillsJSON string
		var attemptStatus, attemptResultJSON string
		if err := rows.Scan(&state.item.ID, &state.item.Name, &state.item.RoleVersionID,
			&assignmentJSON, &state.item.OutputContract, &budgetJSON, &itemResultJSON,
			&itemStatus, &state.item.Ordinal, &createdAt, &dependsJSON, &skillsJSON, &state.attemptID,
			&state.attemptChild, &attemptStatus, &state.resultDigest,
			&attemptResultJSON, &state.attemptGen); err != nil {
			return nil, err
		}
		if dependsJSON != "" && dependsJSON != "[]" {
			if err := json.Unmarshal([]byte(dependsJSON), &state.item.Depends); err != nil {
				return nil, fmt.Errorf("decode depends for item %s: %w", state.item.ID, err)
			}
		}
		if skillsJSON != "" && skillsJSON != "[]" {
			if err := json.Unmarshal([]byte(skillsJSON), &state.item.Skills); err != nil {
				return nil, fmt.Errorf("decode skills for item %s: %w", state.item.ID, err)
			}
		}
		state.item.GroupID = groupID
		state.item.AssignmentJSON = json.RawMessage(assignmentJSON)
		state.item.BudgetJSON = json.RawMessage(budgetJSON)
		state.item.ResultJSON = json.RawMessage(itemResultJSON)
		state.item.Status = domain.DelegationItemStatus(itemStatus)
		state.attemptStatus = domain.DelegationAttemptStatus(attemptStatus)
		if attemptResultJSON != "" {
			state.resultJSON = json.RawMessage(attemptResultJSON)
		}
		if state.attemptID == "" {
			reference, ok := reusedByItem[state.item.ID]
			if !ok {
				return nil, fmt.Errorf("item %s has no selected attempt in generation %d", state.item.ID, generation)
			}
			var selectedItemID, selectedResult string
			if err := tx.QueryRowContext(ctx, `SELECT item_id,child_run_id,status,
				COALESCE(result_digest,''),COALESCE(result_json,''),generation
				FROM delegation_item_attempts WHERE id=?`, reference.AttemptID).Scan(
				&selectedItemID, &state.attemptChild, &attemptStatus,
				&state.resultDigest, &selectedResult, &state.attemptGen); err != nil {
				return nil, fmt.Errorf("resolve reused attempt %s: %w", reference.AttemptID, err)
			}
			if selectedItemID != state.item.ID {
				return nil, fmt.Errorf("reused attempt %s belongs to item %s, not %s",
					reference.AttemptID, selectedItemID, state.item.ID)
			}
			if reference.Generation != state.attemptGen || reference.ChildRunID != state.attemptChild ||
				(reference.ResultDigest != "" && reference.ResultDigest != state.resultDigest) {
				return nil, fmt.Errorf("reused attempt reference %s does not match its immutable attempt", reference.AttemptID)
			}
			state.attemptID = reference.AttemptID
			state.attemptStatus = domain.DelegationAttemptStatus(attemptStatus)
			if selectedResult != "" {
				state.resultJSON = json.RawMessage(selectedResult)
			}
			delete(reusedByItem, state.item.ID)
		} else if _, duplicated := reusedByItem[state.item.ID]; duplicated {
			return nil, fmt.Errorf("generation %d both creates and reuses an attempt for item %s", generation, state.item.ID)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, fmt.Errorf("delegation group has no items")
	}
	if len(reusedByItem) != 0 {
		return nil, fmt.Errorf("generation %d contains reused references for unknown items", generation)
	}
	return states, nil
}
