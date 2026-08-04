package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var (
	ErrStandingApprovalNotFound = errors.New("standing approval not found")
	ErrStandingApprovalLimit    = errors.New("standing approval limit reached")
)

// StandingApprovalRepo manages the standing_approvals, standing_approval_candidates,
// and standing_approval_grants tables.
type StandingApprovalRepo struct {
	DB *sql.DB
}

// ListActive returns all non-revoked standing approvals for a session,
// ordered by most recent first.
func (r *StandingApprovalRepo) ListActive(ctx context.Context, sessionID string) ([]domain.StandingApproval, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, session_id, tool_name, scope_kind, scope_version, scope_key,
		        scope_display, risk_class, created_at, created_by_run_id,
		        created_by_approval_id, revoked_at, revoke_client_request_id
		 FROM standing_approvals
		 WHERE session_id = ? AND revoked_at IS NULL
		 ORDER BY created_at DESC, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list standing approvals: %w", err)
	}
	defer rows.Close()
	return scanStandingApprovals(rows)
}

// MatchActive returns a map of scope ref → standing approval for all
// provided scopes that have an active rule in the session.
func (r *StandingApprovalRepo) MatchActive(ctx context.Context, sessionID string,
	scopes []domain.StandingScopeRef) (map[domain.StandingScopeRef]domain.StandingApproval, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	// Build IN clause placeholders.
	placeholders := make([]string, len(scopes))
	args := make([]any, 0, len(scopes)*4+1)
	args = append(args, sessionID)
	for i, s := range scopes {
		placeholders[i] = "(?, ?, ?, ?)"
		args = append(args, s.ToolName, s.Kind, s.ScopeVersion, s.Key)
	}
	query := fmt.Sprintf(
		`SELECT id, session_id, tool_name, scope_kind, scope_version, scope_key,
		        scope_display, risk_class, created_at, created_by_run_id,
		        created_by_approval_id, revoked_at, revoke_client_request_id
		 FROM standing_approvals
		 WHERE session_id = ? AND revoked_at IS NULL
		   AND (tool_name, scope_kind, scope_version, scope_key) IN (%s)`,
		strings.Join(placeholders, ", "))
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("match standing approvals: %w", err)
	}
	defer rows.Close()
	all, err := scanStandingApprovals(rows)
	if err != nil {
		return nil, err
	}
	result := make(map[domain.StandingScopeRef]domain.StandingApproval, len(all))
	for _, sa := range all {
		ref := domain.StandingScopeRef{
			ToolName:     sa.ToolName,
			Kind:         sa.ScopeKind,
			ScopeVersion: sa.ScopeVersion,
			Key:          sa.ScopeKey,
		}
		result[ref] = sa
	}
	return result, nil
}

// Revoke sets revoked_at on an active rule belonging to the session.
// Returns ErrStandingApprovalNotFound if the rule doesn't exist or belongs
// to another session. Already-revoked rules return nil (idempotent).
func (r *StandingApprovalRepo) Revoke(ctx context.Context, sessionID, ruleID, clientRequestID string) error {
	if clientRequestID == "" {
		clientRequestID = uuid.NewString()
	}
	result, err := r.DB.ExecContext(ctx,
		`UPDATE standing_approvals
		 SET revoked_at = ?, revoke_client_request_id = ?
		 WHERE id = ? AND session_id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), clientRequestID, ruleID, sessionID)
	if err != nil {
		return fmt.Errorf("revoke standing approval: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		// Check if the rule exists at all (maybe in another session).
		var exists int
		if err := r.DB.QueryRowContext(ctx,
			`SELECT 1 FROM standing_approvals WHERE id = ?`, ruleID).Scan(&exists); err != nil {
			return ErrStandingApprovalNotFound
		}
		// Rule exists but already revoked or in another session.
		// Already revoked → idempotent; other session → 404.
		var ownerSession string
		var revoked sql.NullString
		if err := r.DB.QueryRowContext(ctx,
			`SELECT session_id, revoked_at FROM standing_approvals WHERE id = ?`, ruleID,
		).Scan(&ownerSession, &revoked); err != nil {
			return ErrStandingApprovalNotFound
		}
		if ownerSession != sessionID {
			return ErrStandingApprovalNotFound
		}
		// Already revoked — idempotent.
	}
	return nil
}

// SaveCandidatesTx inserts grant candidates within the provided transaction.
func (r *StandingApprovalRepo) SaveCandidatesTx(ctx context.Context, tx *sql.Tx,
	approvalID string, candidates []domain.StandingGrantCandidate) error {
	if len(candidates) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO standing_approval_candidates
		 (approval_id, call_index, tool_call_id, tool_name, scope_kind, scope_version,
		  scope_key, scope_display, risk_class)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare candidate insert: %w", err)
	}
	defer stmt.Close()
	for _, c := range candidates {
		if _, err := stmt.ExecContext(ctx, approvalID, c.CallIndex, c.ToolCallID,
			c.ToolName, c.ScopeKind, c.ScopeVersion, c.ScopeKey,
			c.ScopeDisplay, string(c.RiskClass)); err != nil {
			return fmt.Errorf("insert candidate: %w", err)
		}
	}
	return nil
}

// GetCandidatesTx returns all candidates for an approval within the transaction.
func (r *StandingApprovalRepo) GetCandidatesTx(ctx context.Context, tx *sql.Tx,
	approvalID string) ([]domain.StandingGrantCandidate, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT approval_id, call_index, tool_call_id, tool_name, scope_kind,
		        scope_version, scope_key, scope_display, risk_class
		 FROM standing_approval_candidates
		 WHERE approval_id = ?
		 ORDER BY call_index`, approvalID)
	if err != nil {
		return nil, fmt.Errorf("get candidates: %w", err)
	}
	defer rows.Close()
	return scanCandidates(rows)
}

// GetOrCreateActiveTx returns an active standing rule for the candidate scope,
// creating one if needed. Must run within the Decide transaction (*sql.Tx).
// Returns (rule, created_new, error). created_new is true iff a new row was
// inserted, for audit dedup.
func (r *StandingApprovalRepo) GetOrCreateActiveTx(ctx context.Context, tx *sql.Tx,
	candidate domain.StandingGrantCandidate, approval *domain.ToolApprovalRequest) (domain.StandingApproval, bool, error) {
	// Try to find existing active rule.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, session_id, tool_name, scope_kind, scope_version, scope_key,
		        scope_display, risk_class, created_at, created_by_run_id,
		        created_by_approval_id, revoked_at, revoke_client_request_id
		 FROM standing_approvals
		 WHERE session_id = ? AND tool_name = ? AND scope_kind = ?
		   AND scope_version = ? AND scope_key = ? AND revoked_at IS NULL`,
		approval.SessionID, candidate.ToolName, candidate.ScopeKind,
		candidate.ScopeVersion, candidate.ScopeKey)
	if err != nil {
		return domain.StandingApproval{}, false, fmt.Errorf("lookup active rule: %w", err)
	}
	existing, err := scanStandingApprovals(rows)
	rows.Close()
	if err != nil {
		return domain.StandingApproval{}, false, err
	}
	if len(existing) > 0 {
		return existing[0], false, nil
	}
	// Create a new rule.
	now := time.Now().UTC().Format(time.RFC3339)
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO standing_approvals
		 (id, session_id, tool_name, scope_kind, scope_version, scope_key,
		  scope_display, risk_class, created_at, created_by_run_id,
		  created_by_approval_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, approval.SessionID, candidate.ToolName, candidate.ScopeKind,
		candidate.ScopeVersion, candidate.ScopeKey, candidate.ScopeDisplay,
		string(candidate.RiskClass), now, approval.RunID, approval.ID); err != nil {
		// Map the trigger's RAISE(ABORT, 'standing_approval_limit') to a typed error.
		if strings.Contains(err.Error(), "standing_approval_limit") {
			return domain.StandingApproval{}, false, ErrStandingApprovalLimit
		}
		return domain.StandingApproval{}, false, fmt.Errorf("insert standing approval: %w", err)
	}
	createdAt, _ := time.Parse(time.RFC3339, now)
	return domain.StandingApproval{
		ID:                   id,
		SessionID:            approval.SessionID,
		ToolName:             candidate.ToolName,
		ScopeKind:            candidate.ScopeKind,
		ScopeVersion:         candidate.ScopeVersion,
		ScopeKey:             candidate.ScopeKey,
		ScopeDisplay:         candidate.ScopeDisplay,
		RiskClass:            candidate.RiskClass,
		CreatedAt:            createdAt,
		CreatedByRunID:       approval.RunID,
		CreatedByApprovalID:  approval.ID,
	}, true, nil
}

// SaveGrantsTx inserts grant mappings within the transaction.
func (r *StandingApprovalRepo) SaveGrantsTx(ctx context.Context, tx *sql.Tx,
	approvalID string, grants []domain.StandingGrantResult) error {
	if len(grants) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR REPLACE INTO standing_approval_grants
		 (approval_id, call_index, rule_id, created_at)
		 VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare grant insert: %w", err)
	}
	defer stmt.Close()
	for _, g := range grants {
		if _, err := stmt.ExecContext(ctx, approvalID, g.CallIndex, g.RuleID, now); err != nil {
			return fmt.Errorf("insert grant: %w", err)
		}
	}
	return nil
}

// GetGrantsTx returns existing grants for an approval within the transaction.
func (r *StandingApprovalRepo) GetGrantsTx(ctx context.Context, tx *sql.Tx,
	approvalID string) ([]domain.StandingGrantResult, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT approval_id, call_index, rule_id
		 FROM standing_approval_grants
		 WHERE approval_id = ?
		 ORDER BY call_index`, approvalID)
	if err != nil {
		return nil, fmt.Errorf("get grants: %w", err)
	}
	defer rows.Close()
	var grants []domain.StandingGrantResult
	for rows.Next() {
		var g domain.StandingGrantResult
		var aid string
		if err := rows.Scan(&aid, &g.CallIndex, &g.RuleID); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// ActiveCount returns the number of active rules for the session.
func (r *StandingApprovalRepo) ActiveCount(ctx context.Context, sessionID string) (int, error) {
	var count int
	if err := r.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM standing_approvals
		 WHERE session_id = ? AND revoked_at IS NULL`, sessionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active rules: %w", err)
	}
	return count, nil
}

func scanStandingApprovals(rows *sql.Rows) ([]domain.StandingApproval, error) {
	var result []domain.StandingApproval
	for rows.Next() {
		var sa domain.StandingApproval
		var createdAt, revokedAt sql.NullString
		if err := rows.Scan(&sa.ID, &sa.SessionID, &sa.ToolName, &sa.ScopeKind,
			&sa.ScopeVersion, &sa.ScopeKey, &sa.ScopeDisplay, &sa.RiskClass,
			&createdAt, &sa.CreatedByRunID, &sa.CreatedByApprovalID,
			&revokedAt, &sa.RevokeClientRequestID); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, createdAt.String); err == nil {
			sa.CreatedAt = t
		}
		if revokedAt.Valid {
			if t, err := time.Parse(time.RFC3339, revokedAt.String); err == nil {
				sa.RevokedAt = &t
			}
		}
		result = append(result, sa)
	}
	return result, rows.Err()
}

func scanCandidates(rows *sql.Rows) ([]domain.StandingGrantCandidate, error) {
	var result []domain.StandingGrantCandidate
	for rows.Next() {
		var c domain.StandingGrantCandidate
		var aid string
		if err := rows.Scan(&aid, &c.CallIndex, &c.ToolCallID, &c.ToolName,
			&c.ScopeKind, &c.ScopeVersion, &c.ScopeKey, &c.ScopeDisplay,
			&c.RiskClass); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
