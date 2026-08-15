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

// ContinueNeedsInput resumes the current selected needs_input attempt of one
// item with an explicit bounded instruction, as a new 'input' generation. The
// new child replays the source private transcript plus one explicit user
// instruction; no sibling or public transcript is added, and the source
// attempt/result stay immutable.
func (r *DelegationRepo) ContinueNeedsInput(ctx context.Context, itemID string,
	input domain.DelegationInputCommand) (*domain.DelegationGeneration, *domain.AgentRun, error) {
	return r.createContinuationGeneration(ctx, itemID, domain.DelegationGenerationInput, input)
}

// FollowUp resumes the current selected completed/blocked attempt of one item
// with a private follow-up instruction, as a new 'follow_up' generation.
func (r *DelegationRepo) FollowUp(ctx context.Context, itemID string,
	input domain.DelegationInputCommand) (*domain.DelegationGeneration, *domain.AgentRun, error) {
	return r.createContinuationGeneration(ctx, itemID, domain.DelegationGenerationFollowUp, input)
}

func (r *DelegationRepo) createContinuationGeneration(ctx context.Context, itemID string,
	kind domain.DelegationGenerationKind, input domain.DelegationInputCommand) (*domain.DelegationGeneration, *domain.AgentRun, error) {
	if strings.TrimSpace(input.ClientRequestID) == "" {
		return nil, nil, fmt.Errorf("continuation client request id is required")
	}
	if strings.TrimSpace(input.Text) == "" || len(input.Text) > 16384 {
		return nil, nil, fmt.Errorf("continuation instruction must be 1-16384 bytes")
	}
	if strings.TrimSpace(input.SourceAttemptID) == "" {
		return nil, nil, fmt.Errorf("continuation source attempt id is required")
	}
	requestDigest, err := continuationRequestDigest(itemID, kind, input)
	if err != nil {
		return nil, nil, err
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	var groupID, parentRunID, sessionID string
	var currentGeneration int
	if err := tx.QueryRowContext(ctx, `SELECT i.group_id,g.parent_run_id,ar.session_id,g.current_generation
		FROM delegation_items i
		JOIN delegation_groups g ON g.id=i.group_id
		JOIN agent_runs ar ON ar.id=g.parent_run_id
		WHERE i.id=?`, itemID).Scan(&groupID, &parentRunID, &sessionID, &currentGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrDelegationItemNotFound
		}
		return nil, nil, err
	}
	// Idempotency first: the client request id already produced a generation,
	// even if the group cursor has advanced since then.
	var existingID, storedRequestDigest string
	if err := tx.QueryRowContext(ctx, `SELECT id,COALESCE(request_digest,'') FROM delegation_group_generations
		WHERE group_id=? AND client_request_id=?`, groupID, input.ClientRequestID).
		Scan(&existingID, &storedRequestDigest); err == nil {
		if err := requestDigestConflict(storedRequestDigest, requestDigest); err != nil {
			return nil, nil, err
		}
		// Release the single connection before loading outside the transaction.
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return nil, nil, rollbackErr
		}
		generation, child, loadErr := r.loadContinuationResult(ctx, existingID)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		return generation, child, nil
	}
	if input.ExpectedGeneration != currentGeneration {
		return nil, nil, domain.NewCodedError(domain.ErrorDelegationInputStale,
			fmt.Errorf("expected generation %d, current is %d", input.ExpectedGeneration, currentGeneration))
	}
	selectedItems, err := resolveGenerationItemStatesTx(ctx, tx, groupID, currentGeneration)
	if err != nil {
		return nil, nil, err
	}
	var source itemState
	foundSource := false
	for _, selected := range selectedItems {
		if selected.item.ID == itemID {
			source = selected
			foundSource = true
			break
		}
	}
	if !foundSource {
		return nil, nil, ErrDelegationItemNotFound
	}
	sourceAttemptID := source.attemptID
	sourceStatus := string(source.attemptStatus)
	if input.SourceAttemptID != sourceAttemptID {
		return nil, nil, domain.NewCodedError(domain.ErrorDelegationInputStale,
			fmt.Errorf("source attempt is no longer current"))
	}
	// Eligibility depends on the continuation kind.
	switch kind {
	case domain.DelegationGenerationInput:
		if sourceStatus != string(domain.DelegationAttemptNeedsInput) {
			return nil, nil, domain.NewCodedError(domain.ErrorDelegationInputStale,
				fmt.Errorf("attempt %s has status %s; only needs_input accepts input", sourceAttemptID, sourceStatus))
		}
	case domain.DelegationGenerationFollowUp:
		if sourceStatus != string(domain.DelegationAttemptSucceeded) &&
			sourceStatus != string(domain.DelegationAttemptBlocked) {
			return nil, nil, domain.NewCodedError(domain.ErrorDelegationFollowUpForbidden,
				fmt.Errorf("attempt %s has status %s; only completed/blocked accept follow-up", sourceAttemptID, sourceStatus))
		}
	default:
		return nil, nil, fmt.Errorf("unsupported continuation kind %q", kind)
	}

	// Reuse every sibling's logical selection; select only this item.
	reused := make([]domain.DelegationAttemptReference, 0, len(selectedItems)-1)
	for _, selected := range selectedItems {
		if selected.item.ID == itemID {
			continue
		}
		reused = append(reused, domain.DelegationAttemptReference{
			ItemID: selected.item.ID, AttemptID: selected.attemptID,
			Generation: selected.attemptGen, ChildRunID: selected.attemptChild,
			ResultDigest: selected.resultDigest,
		})
	}

	// Freeze the new generation snapshot.
	itemRoleVersion := source.item.RoleVersionID
	itemBudgetJSON := string(source.item.BudgetJSON)
	var ceiling domain.BudgetCeilingJSON
	if err := json.Unmarshal([]byte(itemBudgetJSON), &ceiling); err != nil {
		return nil, nil, err
	}
	originalCeiling := ceiling
	approvalRequired := false
	if input.Budget != nil {
		if err := r.validateRetryRoleAndCeilingTx(ctx, tx, itemID, *input.Budget); err != nil {
			return nil, nil, err
		}
		approvalRequired = budgetIncreased(originalCeiling, *input.Budget)
		ceiling = *input.Budget
		itemBudgetJSON = mustMarshalJSON(ceiling)
	}
	authSnapshot := []map[string]string{{"itemId": itemID, "roleVersionId": itemRoleVersion}}
	for _, reference := range reused {
		authSnapshot = append(authSnapshot, map[string]string{
			"itemId": reference.ItemID, "roleVersionId": referenceAttemptRoleVersion(ctx, tx, reference.AttemptID),
		})
	}
	authDigest, err := digestJSON(authSnapshot)
	if err != nil {
		return nil, nil, err
	}
	budgetJSON, err := json.Marshal(ceiling)
	if err != nil {
		return nil, nil, err
	}
	budgetDigest, err := digestJSON(ceiling)
	if err != nil {
		return nil, nil, err
	}
	reusedJSON, err := json.Marshal(reused)
	if err != nil {
		return nil, nil, err
	}
	selectionJSON, err := json.Marshal([]string{itemID})
	if err != nil {
		return nil, nil, err
	}
	var maxGeneration int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0) FROM delegation_group_generations
		WHERE group_id=?`, groupID).Scan(&maxGeneration); err != nil {
		return nil, nil, err
	}
	nextGeneration := maxGeneration + 1
	now := time.Now().UTC()
	generationID := uuid.NewString()
	generationStatus := domain.DelegationGenerationRunning
	if approvalRequired {
		generationStatus = domain.DelegationGenerationAwaitingAuthorization
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_group_generations
		(id,group_id,generation,kind,status,retry_selection_json,reused_attempts_json,
		 authorization_snapshot_json,authorization_snapshot_digest,budget_snapshot_json,budget_snapshot_digest,
		 client_request_id,request_digest,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		generationID, groupID, nextGeneration, string(kind), string(generationStatus),
		string(selectionJSON), string(reusedJSON), string(authSnapshotJSON(authSnapshot)),
		authDigest, string(budgetJSON), budgetDigest, input.ClientRequestID, requestDigest,
		now.Format(time.RFC3339Nano)); err != nil {
		return nil, nil, fmt.Errorf("create continuation generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_groups SET current_generation=?,updated_at=?
		WHERE id=? AND current_generation=?`, nextGeneration, now.Format(time.RFC3339Nano),
		groupID, currentGeneration); err != nil {
		return nil, nil, err
	}

	generation := &domain.DelegationGeneration{
		ID: generationID, GroupID: groupID, Generation: nextGeneration, Kind: kind,
		Status: generationStatus, RetrySelection: []string{itemID},
		ReusedAttempts: reused, AuthorizationSnapshot: authSnapshotJSON(authSnapshot),
		BudgetSnapshot: budgetJSON, ClientRequestID: input.ClientRequestID, CreatedAt: now,
	}
	if approvalRequired {
		if _, err := insertContinuationBudgetApprovalTx(ctx, tx, groupID, nextGeneration,
			parentRunID, sessionID, source, ceiling, kind, input.Text); err != nil {
			return nil, nil, err
		}
		if err := ResolveAttentionForSourceTx(ctx, tx, domain.AttentionSourceDelegationItem,
			itemID, currentGeneration); err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		return generation, nil, nil
	}

	var override *domain.BudgetCeilingJSON
	if input.Budget != nil {
		override = input.Budget
	}
	child, err := createChildRunTx(ctx, tx, CreateChildRunInput{
		ParentRunID: parentRunID, ItemID: itemID, SessionID: sessionID,
		Generation: nextGeneration, RetryOfAttemptID: sourceAttemptID,
		BudgetOverride: override, AllowTerminalParent: true,
		Policies: r.Policies,
	})
	if err != nil {
		return nil, nil, err
	}
	// Continuation fact: exact source attempt + bounded input with digest.
	inputJSON, err := json.Marshal(map[string]string{"text": input.Text})
	if err != nil {
		return nil, nil, err
	}
	inputDigest, err := digestJSON(map[string]string{"text": input.Text})
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_attempt_continuations
		(attempt_id,source_attempt_id,kind,input_json,input_digest,created_at)
		VALUES(?,?,?,?,?,?)`,
		continuationAttemptID(ctx, tx, itemID, nextGeneration), sourceAttemptID, string(kind),
		string(inputJSON), inputDigest, now.Format(time.RFC3339Nano)); err != nil {
		return nil, nil, fmt.Errorf("create continuation fact: %w", err)
	}

	if err := ResolveAttentionForSourceTx(ctx, tx, domain.AttentionSourceDelegationItem,
		itemID, currentGeneration); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return generation, child, nil
}

func insertContinuationBudgetApprovalTx(ctx context.Context, tx *sql.Tx, groupID string, generation int,
	parentRunID, sessionID string, source itemState, ceiling domain.BudgetCeilingJSON,
	kind domain.DelegationGenerationKind, text string) (*domain.DelegationApprovalRequest, error) {
	itemsJSON, err := json.Marshal([]retryBudgetItem{{
		ItemID: source.item.ID, Name: source.item.Name, RetryOfAttemptID: source.attemptID,
		Budget: ceiling, ContinuationKind: string(kind), ContinuationInput: text,
	}})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	approval := &domain.DelegationApprovalRequest{
		ID: uuid.NewString(), GroupID: groupID, Generation: generation, Kind: "retry_budget",
		ParentRunID: parentRunID, SessionID: sessionID, Status: "pending",
		ItemsJSON: itemsJSON, RequestedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delegation_approval_requests
		(id,group_id,generation,kind,parent_run_id,session_id,status,items_json,requested_at)
		VALUES(?,?,?,?,?,?,'pending',?,?)`, approval.ID, groupID, generation, "retry_budget",
		parentRunID, sessionID, string(itemsJSON), now.Format(time.RFC3339Nano)); err != nil {
		return nil, fmt.Errorf("create continuation budget approval: %w", err)
	}
	var projectID string
	_ = tx.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id=?`, sessionID).Scan(&projectID)
	if err := ProjectAttentionTx(ctx, tx, projectID, sessionID,
		domain.AttentionSourceDelegationApproval, approval.ID, generation,
		domain.AttentionApprovalRequired, true,
		map[string]any{"kind": "retry_budget", "generation": generation},
		&domain.AttentionAction{Kind: "delegation_approval", ApprovalID: approval.ID}); err != nil {
		return nil, err
	}
	return approval, nil
}

// continuationAttemptID returns the newly created attempt id for the item in
// the new generation (used as the continuation fact key).
func continuationAttemptID(ctx context.Context, tx *sql.Tx, itemID string, generation int) string {
	var attemptID string
	_ = tx.QueryRowContext(ctx, `SELECT id FROM delegation_item_attempts
		WHERE item_id=? AND generation=?`, itemID, generation).Scan(&attemptID)
	return attemptID
}

func (r *DelegationRepo) loadContinuationResult(ctx context.Context, generationID string) (*domain.DelegationGeneration, *domain.AgentRun, error) {
	var generation domain.DelegationGeneration
	var status, kind, createdAt string
	var selectionJSON, reusedJSON, authJSON, budgetJSON string
	if err := r.DB.QueryRowContext(ctx, `SELECT id,group_id,generation,kind,status,retry_selection_json,reused_attempts_json,
		authorization_snapshot_json,budget_snapshot_json,client_request_id,created_at
		FROM delegation_group_generations WHERE id=?`, generationID).Scan(
		&generation.ID, &generation.GroupID, &generation.Generation, &kind, &status,
		&selectionJSON, &reusedJSON, &authJSON, &budgetJSON, &generation.ClientRequestID, &createdAt); err != nil {
		return nil, nil, err
	}
	generation.Kind = domain.DelegationGenerationKind(kind)
	generation.Status = domain.DelegationGenerationStatus(status)
	generation.AuthorizationSnapshot = json.RawMessage(authJSON)
	generation.BudgetSnapshot = json.RawMessage(budgetJSON)
	_ = json.Unmarshal([]byte(selectionJSON), &generation.RetrySelection)
	_ = json.Unmarshal([]byte(reusedJSON), &generation.ReusedAttempts)
	generation.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	child, err := r.childForGeneration(ctx, generation.GroupID, generation.Generation)
	if err != nil {
		return nil, nil, err
	}
	return &generation, child, nil
}

func (r *DelegationRepo) childForGeneration(ctx context.Context, groupID string, generation int) (*domain.AgentRun, error) {
	var childID string
	if err := r.DB.QueryRowContext(ctx, `SELECT a.child_run_id FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		WHERE i.group_id=? AND a.generation=? LIMIT 1`, groupID, generation).Scan(&childID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	run, err := (&RunRepo{DB: r.DB}).Get(ctx, childID)
	if err != nil {
		return nil, err
	}
	return run, nil
}

// ContinuationSeed is the private execution continuity for a continuation
// child: the exact source attempt's private transcript plus the bounded
// explicit instruction. It never includes sibling or public history.
type ContinuationSeed struct {
	SourceAttemptID  string
	SourceChildRunID string
	Transcript       []domain.ChatMessage
	Instruction      string
}

// ContinuationSeedForChild resolves the continuation seed for a child Run, or
// returns (nil, nil) when the child is not a continuation.
func (r *DelegationRepo) ContinuationSeedForChild(ctx context.Context, childRunID string) (*ContinuationSeed, error) {
	var sourceAttemptID, sourceChildRunID, inputJSON string
	err := r.DB.QueryRowContext(ctx, `SELECT c.source_attempt_id,a2.child_run_id,c.input_json
		FROM delegation_attempt_continuations c
		JOIN delegation_item_attempts a ON a.id=c.attempt_id
		JOIN delegation_item_attempts a2 ON a2.id=c.source_attempt_id
		WHERE a.child_run_id=?`, childRunID).Scan(&sourceAttemptID, &sourceChildRunID, &inputJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	transcript, err := (&RunMessageRepo{DB: r.DB}).ResumeMessages(ctx, sourceChildRunID)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &payload); err != nil {
		return nil, err
	}
	return &ContinuationSeed{
		SourceAttemptID: sourceAttemptID, SourceChildRunID: sourceChildRunID,
		Transcript: transcript, Instruction: payload.Text,
	}, nil
}
