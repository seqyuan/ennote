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
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

var (
	ErrDelegationApprovalNotFound = errors.New("delegation approval not found")
	ErrDelegationApprovalConflict = errors.New("delegation approval decision conflicts")
)

// DelegationApprovalRepo decides durable retry-budget authorizations. It is
// separate from tool-approval checkpoints because the original parent may
// already be terminal.
type DelegationApprovalRepo struct {
	DB *sql.DB
	// Policies forwards the file-backed delegation policy for retry child
	// materialization (V2); nil keeps the legacy global policy SQL path.
	Policies *fileconfig.PolicyStore
}

// Decide approves or rejects a retry-budget authorization. Approval creates the
// selected child Runs from the already frozen generation snapshot; rejection
// terminalizes the generation and rewinds the group cursor to the previous
// selected generation without touching its attempts. First committed decision
// wins; replaying the same decision with the same client request id is
// idempotent; an opposite decision conflicts.
func (r *DelegationApprovalRepo) Decide(ctx context.Context, approvalID string,
	decision domain.ApprovalDecision, clientRequestID string) (*domain.DelegationApprovalRequest, []*domain.AgentRun, error) {
	if decision != domain.DecisionApproved && decision != domain.DecisionRejected {
		return nil, nil, fmt.Errorf("unsupported delegation approval decision: %s", decision)
	}
	if strings.TrimSpace(clientRequestID) == "" {
		return nil, nil, fmt.Errorf("decision client request id is required")
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var groupID, parentRunID, sessionID, status, itemsJSON string
	var generation int
	var decisionClientRequestID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT group_id,generation,parent_run_id,session_id,status,items_json,decision_client_request_id
		FROM delegation_approval_requests WHERE id=?`, approvalID).Scan(
		&groupID, &generation, &parentRunID, &sessionID, &status, &itemsJSON, &decisionClientRequestID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrDelegationApprovalNotFound
		}
		return nil, nil, err
	}
	if status == "approved" || status == "rejected" {
		if decisionClientRequestID.Valid && decisionClientRequestID.String == clientRequestID &&
			string(decision) == status {
			// Idempotent replay of the same committed decision. Release the
			// single connection before loading outside the transaction.
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return nil, nil, rollbackErr
			}
			approval, err := r.loadApproval(ctx, approvalID)
			if err != nil {
				return nil, nil, err
			}
			if status == "approved" {
				children, loadErr := (&DelegationRepo{DB: r.DB}).childrenForGeneration(ctx, groupID, generation)
				if loadErr != nil {
					return nil, nil, loadErr
				}
				return approval, children, nil
			}
			return approval, nil, nil
		}
		return nil, nil, ErrDelegationApprovalConflict
	}
	if status != "pending" {
		return nil, nil, ErrDelegationApprovalConflict
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if decision == domain.DecisionRejected {
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_approval_requests SET status='rejected',
			decision_client_request_id=?,resolved_at=? WHERE id=? AND status='pending'`,
			clientRequestID, now, approvalID); err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_group_generations SET status='failed',completed_at=?
			WHERE group_id=? AND generation=? AND status='awaiting_authorization'`, now, groupID, generation); err != nil {
			return nil, nil, err
		}
		// Rewind the cursor to the previous selected generation; its attempts
		// and folded result are untouched.
		previous := generation - 1
		if previous < 0 {
			previous = 0
		}
		if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET current_generation=?,updated_at=?
			WHERE id=? AND current_generation=?`, previous, now, groupID, generation); err != nil {
			return nil, nil, err
		}
		var generationKind string
		if err := tx.QueryRowContext(ctx, `SELECT kind FROM delegation_group_generations
			WHERE group_id=? AND generation=?`, groupID, generation).Scan(&generationKind); err != nil {
			return nil, nil, err
		}
		if generationKind == string(domain.DelegationGenerationInput) {
			var plans []retryBudgetItem
			if err := json.Unmarshal([]byte(itemsJSON), &plans); err != nil {
				return nil, nil, err
			}
			var projectID string
			_ = tx.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id=?`, sessionID).Scan(&projectID)
			for _, plan := range plans {
				if err := ProjectAttentionTx(ctx, tx, projectID, sessionID,
					domain.AttentionSourceDelegationItem, plan.ItemID, previous,
					domain.AttentionNeedsInput, true,
					map[string]any{"kind": "needs_input", "generation": previous},
					&domain.AttentionAction{Kind: "delegation_input", ItemID: plan.ItemID,
						ExpectedGeneration: previous}); err != nil {
					return nil, nil, err
				}
				if err := ReopenAttentionForSourceTx(ctx, tx,
					domain.AttentionSourceDelegationItem, plan.ItemID, previous); err != nil {
					return nil, nil, err
				}
			}
		}
		if err := ResolveAttentionForSourceTx(ctx, tx,
			domain.AttentionSourceDelegationApproval, approvalID, generation); err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		approval, err := r.loadApproval(ctx, approvalID)
		if err != nil {
			return nil, nil, err
		}
		return approval, nil, nil
	}

	// Approval path: materialize the frozen retry selection.
	var selectionJSON string
	if err := tx.QueryRowContext(ctx, `SELECT retry_selection_json FROM delegation_group_generations
		WHERE group_id=? AND generation=? AND status='awaiting_authorization'`,
		groupID, generation).Scan(&selectionJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, domain.NewCodedError(domain.ErrorDelegationGenerationConflict,
				errors.New("generation is not awaiting authorization"))
		}
		return nil, nil, err
	}
	var selectedItems []string
	if err := json.Unmarshal([]byte(selectionJSON), &selectedItems); err != nil {
		return nil, nil, err
	}
	var itemPlans []retryBudgetItem
	if err := json.Unmarshal([]byte(itemsJSON), &itemPlans); err != nil {
		return nil, nil, err
	}
	plansByItem := make(map[string]retryBudgetItem, len(itemPlans))
	for _, plan := range itemPlans {
		plansByItem[plan.ItemID] = plan
	}

	children := make([]*domain.AgentRun, 0, len(selectedItems))
	for _, itemID := range selectedItems {
		plan, ok := plansByItem[itemID]
		if !ok {
			return nil, nil, fmt.Errorf("approval items do not cover selected item %s", itemID)
		}
		// Fail closed when the frozen Role meta is missing; the frozen version
		// never changes. Budget was validated at request time and is
		// re-validated here against the frozen definition's ceiling.
		repo := &DelegationRepo{DB: r.DB, Policies: r.Policies}
		if err := repo.validateRetryRoleAndCeilingTx(ctx, tx, itemID, plan.Budget); err != nil {
			return nil, nil, err
		}
		override := plan.Budget
		child, childErr := createChildRunTx(ctx, tx, CreateChildRunInput{
			ParentRunID: parentRunID, ItemID: itemID, SessionID: sessionID,
			Generation: generation, RetryOfAttemptID: plan.RetryOfAttemptID,
			BudgetOverride: &override, AllowTerminalParent: true,
			Policies: repo.Policies,
		})
		if childErr != nil {
			return nil, nil, childErr
		}
		children = append(children, child)
		if plan.ContinuationKind != "" {
			kind := domain.DelegationGenerationKind(plan.ContinuationKind)
			if kind != domain.DelegationGenerationInput && kind != domain.DelegationGenerationFollowUp {
				return nil, nil, fmt.Errorf("approval contains unsupported continuation kind %q", plan.ContinuationKind)
			}
			if strings.TrimSpace(plan.ContinuationInput) == "" || len(plan.ContinuationInput) > 16384 {
				return nil, nil, fmt.Errorf("approval contains invalid continuation input")
			}
			inputPayload := map[string]string{"text": plan.ContinuationInput}
			inputJSON, err := json.Marshal(inputPayload)
			if err != nil {
				return nil, nil, err
			}
			inputDigest, err := digestJSON(inputPayload)
			if err != nil {
				return nil, nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_attempt_continuations
				(attempt_id,source_attempt_id,kind,input_json,input_digest,created_at)
				VALUES(?,?,?,?,?,?)`, continuationAttemptID(ctx, tx, itemID, generation),
				plan.RetryOfAttemptID, string(kind), string(inputJSON), inputDigest, now); err != nil {
				return nil, nil, fmt.Errorf("create approved continuation fact: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_approval_requests SET status='approved',
		decision_client_request_id=?,resolved_at=? WHERE id=? AND status='pending'`,
		clientRequestID, now, approvalID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_group_generations SET status='running'
		WHERE group_id=? AND generation=? AND status='awaiting_authorization'`, groupID, generation); err != nil {
		return nil, nil, err
	}
	if err := ResolveAttentionForSourceTx(ctx, tx,
		domain.AttentionSourceDelegationApproval, approvalID, generation); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	approval, err := r.loadApproval(ctx, approvalID)
	if err != nil {
		return nil, nil, err
	}
	return approval, children, nil
}

type retryBudgetItem struct {
	ItemID            string                   `json:"itemId"`
	Name              string                   `json:"name"`
	RetryOfAttemptID  string                   `json:"retryOfAttemptId"`
	Budget            domain.BudgetCeilingJSON `json:"budget"`
	ContinuationKind  string                   `json:"continuationKind,omitempty"`
	ContinuationInput string                   `json:"continuationInput,omitempty"`
}

func (r *DelegationApprovalRepo) loadApproval(ctx context.Context, approvalID string) (*domain.DelegationApprovalRequest, error) {
	var approval domain.DelegationApprovalRequest
	var itemsJSON, requestedAt, resolvedAt string
	var decisionID sql.NullString
	err := r.DB.QueryRowContext(ctx, `SELECT id,group_id,generation,kind,parent_run_id,session_id,status,items_json,requested_at,COALESCE(resolved_at,''),COALESCE(decision_client_request_id,'')
		FROM delegation_approval_requests WHERE id=?`, approvalID).Scan(
		&approval.ID, &approval.GroupID, &approval.Generation, &approval.Kind,
		&approval.ParentRunID, &approval.SessionID, &approval.Status, &itemsJSON, &requestedAt, &resolvedAt, &decisionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDelegationApprovalNotFound
	}
	if err != nil {
		return nil, err
	}
	approval.ItemsJSON = json.RawMessage(itemsJSON)
	approval.RequestedAt, err = time.Parse(time.RFC3339Nano, requestedAt)
	if err != nil {
		return nil, err
	}
	if resolvedAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, resolvedAt)
		if parseErr != nil {
			return nil, parseErr
		}
		approval.ResolvedAt = &parsed
	}
	return &approval, nil
}
