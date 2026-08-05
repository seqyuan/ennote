package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var (
	ErrAttentionNotFound = errors.New("attention item not found")
)

// AttentionRepo projects authoritative execution facts into cross-session
// attention items. Rows are idempotent by source key; the source decision
// always wins and resolves its pending attention row in the same transaction.
type AttentionRepo struct{ DB *sql.DB }

// ProjectAttention inserts an attention item in its own transaction. Runtime
// paths use ProjectAttentionTx inside the source transaction; this wrapper is
// for tests and external integrations.
func (r *AttentionRepo) ProjectAttention(ctx context.Context, projectID, sessionID string,
	sourceKind domain.AttentionSourceKind, sourceID string, sourceGeneration int,
	kind domain.AttentionKind, requiresAction bool, display any, action *domain.AttentionAction) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ProjectAttentionTx(ctx, tx, projectID, sessionID, sourceKind, sourceID,
		sourceGeneration, kind, requiresAction, display, action); err != nil {
		return err
	}
	return tx.Commit()
}

// ProjectAttentionTx inserts an attention item with a bounded display payload.
// It is idempotent (unique source key) and safe to call from recovery replay.
func ProjectAttentionTx(ctx context.Context, tx *sql.Tx, projectID, sessionID string,
	sourceKind domain.AttentionSourceKind, sourceID string, sourceGeneration int,
	kind domain.AttentionKind, requiresAction bool, display any, action *domain.AttentionAction) error {
	displayJSON, err := json.Marshal(display)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO attention_items
		(id,project_id,session_id,source_kind,source_id,source_generation,kind,requires_action,status,display_json,created_at)
		VALUES(?,?,?,?,?,?,?,?, 'pending',?,?)`,
		uuid.NewString(), projectID, sessionID, string(sourceKind), sourceID, sourceGeneration,
		string(kind), boolInt(requiresAction), string(displayJSON), now); err != nil {
		return fmt.Errorf("project attention item: %w", err)
	}
	return nil
}

// ResolveAttentionForSourceTx marks every pending attention item of a source
// as resolved inside the source's own decision transaction. Missing attention
// rows never block the source decision.
func ResolveAttentionForSourceTx(ctx context.Context, tx *sql.Tx,
	sourceKind domain.AttentionSourceKind, sourceID string, sourceGeneration int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE attention_items SET status='resolved',resolved_at=?
		WHERE source_kind=? AND source_id=? AND source_generation=? AND status='pending'`,
		now, string(sourceKind), sourceID, sourceGeneration); err != nil {
		return fmt.Errorf("resolve attention items: %w", err)
	}
	return nil
}

// ReopenAttentionForSourceTx restores an action item when a typed command was
// rejected and the authoritative source becomes actionable again.
func ReopenAttentionForSourceTx(ctx context.Context, tx *sql.Tx,
	sourceKind domain.AttentionSourceKind, sourceID string, sourceGeneration int) error {
	if _, err := tx.ExecContext(ctx, `UPDATE attention_items SET status='pending',resolved_at=NULL,dismissed_at=NULL
		WHERE source_kind=? AND source_id=? AND source_generation=?`,
		string(sourceKind), sourceID, sourceGeneration); err != nil {
		return fmt.Errorf("reopen attention items: %w", err)
	}
	return nil
}

// Dismiss marks a notification item dismissed. Approval and needs_input items
// are never dismissible; callers must use their typed action instead.
func (r *AttentionRepo) Dismiss(ctx context.Context, attentionID string) error {
	result, err := r.DB.ExecContext(ctx, `UPDATE attention_items SET status='dismissed',dismissed_at=?
		WHERE id=? AND status='pending' AND requires_action=0`, time.Now().UTC().Format(time.RFC3339Nano), attentionID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var kind string
		if err := r.DB.QueryRowContext(ctx, `SELECT kind FROM attention_items WHERE id=?`, attentionID).
			Scan(&kind); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrAttentionNotFound
			}
			return err
		}
		return fmt.Errorf("%w: attention item %s cannot be dismissed", ErrAttentionConflict, attentionID)
	}
	return nil
}

var ErrAttentionConflict = errors.New("attention item requires its typed action")

// ListAttention returns pending-first, cursor-paginated attention items for a
// project, optionally filtered by session and status.
func (r *AttentionRepo) ListAttention(ctx context.Context, projectID, sessionID, status string, limit int) ([]domain.AttentionItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := `SELECT id,project_id,session_id,source_kind,source_id,source_generation,kind,requires_action,status,display_json,created_at,COALESCE(resolved_at,''),COALESCE(dismissed_at,'')
		FROM attention_items WHERE project_id=?`
	args := []any{projectID}
	if sessionID != "" {
		query += ` AND session_id=?`
		args = append(args, sessionID)
	}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY requires_action DESC, created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.AttentionItem, 0)
	for rows.Next() {
		item, err := scanAttentionItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanAttentionItem(row interface{ Scan(...any) error }) (domain.AttentionItem, error) {
	var item domain.AttentionItem
	var kind, sourceKind, status, display, createdAt, resolvedAt, dismissedAt string
	var requiresAction int
	if err := row.Scan(&item.ID, &item.ProjectID, &item.SessionID, &sourceKind, &item.SourceID,
		&item.SourceGeneration, &kind, &requiresAction, &status, &display, &createdAt,
		&resolvedAt, &dismissedAt); err != nil {
		return item, err
	}
	item.SourceKind = domain.AttentionSourceKind(sourceKind)
	item.Kind = domain.AttentionKind(kind)
	item.RequiresAction = requiresAction == 1
	item.Status = status
	item.Display = json.RawMessage(display)
	item.Action = decodeAttentionAction(item.SourceKind, item.Kind, item.SourceID, item.SourceGeneration)
	parsedCreated, parseErr := time.Parse(time.RFC3339Nano, createdAt)
	if parseErr != nil {
		return item, parseErr
	}
	item.CreatedAt = parsedCreated
	if resolvedAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, resolvedAt)
		if parseErr != nil {
			return item, parseErr
		}
		item.ResolvedAt = &parsed
	}
	if dismissedAt != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, dismissedAt)
		if parseErr != nil {
			return item, parseErr
		}
		item.DismissedAt = &parsed
	}
	return item, nil
}

// decodeAttentionAction derives the typed action from the source kind. It
// never invents a generic decision action.
func decodeAttentionAction(sourceKind domain.AttentionSourceKind, kind domain.AttentionKind, sourceID string, generation int) *domain.AttentionAction {
	switch sourceKind {
	case domain.AttentionSourceToolApproval:
		return &domain.AttentionAction{Kind: "tool_approval", ApprovalID: sourceID}
	case domain.AttentionSourceDelegationApproval:
		return &domain.AttentionAction{Kind: "delegation_approval", ApprovalID: sourceID}
	case domain.AttentionSourceDelegationItem:
		if kind == domain.AttentionNeedsInput {
			return &domain.AttentionAction{Kind: "delegation_input", ItemID: sourceID, ExpectedGeneration: generation}
		}
		return &domain.AttentionAction{Kind: "none"}
	default:
		return &domain.AttentionAction{Kind: "none"}
	}
}

// RebuildAttention reconstructs pending/resolved attention items from source
// facts. Idempotent by source key.
func (r *AttentionRepo) RebuildAttention(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Pending tool approvals.
	toolRows, err := tx.QueryContext(ctx, `SELECT a.id,a.session_id,s.project_id,a.items_json
		FROM tool_approval_requests a JOIN sessions s ON s.id=a.session_id
		WHERE a.status='pending' LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type pendingToolApproval struct {
		id, sessionID, projectID, itemsJSON string
	}
	var toolApprovals []pendingToolApproval
	for toolRows.Next() {
		var entry pendingToolApproval
		if err := toolRows.Scan(&entry.id, &entry.sessionID, &entry.projectID, &entry.itemsJSON); err != nil {
			toolRows.Close()
			return 0, err
		}
		toolApprovals = append(toolApprovals, entry)
	}
	if err := toolRows.Close(); err != nil {
		return 0, err
	}
	added := 0
	for _, approval := range toolApprovals {
		var items []domain.ApprovalItem
		if err := json.Unmarshal([]byte(approval.itemsJSON), &items); err != nil {
			return 0, err
		}
		toolNames := make([]string, 0, len(items))
		for _, item := range items {
			toolNames = append(toolNames, item.ToolName)
		}
		if err := ProjectAttentionTx(ctx, tx, approval.projectID, approval.sessionID,
			domain.AttentionSourceToolApproval, approval.id, 0,
			domain.AttentionApprovalRequired, true,
			map[string]any{"kind": "tool_approval", "tools": toolNames},
			&domain.AttentionAction{Kind: "tool_approval", ApprovalID: approval.id}); err != nil {
			return 0, err
		}
		added++
	}

	// Pending delegation approvals.
	rows, err := tx.QueryContext(ctx, `SELECT ar.id,ar.session_id,ar.generation,s.project_id,ar.status
		FROM delegation_approval_requests ar JOIN sessions s ON s.id=ar.session_id
		WHERE ar.status='pending' LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	var approvals []struct {
		id, sessionID, projectID string
		generation               int
	}
	for rows.Next() {
		var entry struct {
			id, sessionID, projectID string
			generation               int
		}
		if err := rows.Scan(&entry.id, &entry.sessionID, &entry.generation, &entry.projectID, new(string)); err != nil {
			rows.Close()
			return 0, err
		}
		approvals = append(approvals, entry)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, approval := range approvals {
		if err := ProjectAttentionTx(ctx, tx, approval.projectID, approval.sessionID,
			domain.AttentionSourceDelegationApproval, approval.id, approval.generation,
			domain.AttentionApprovalRequired, true,
			map[string]any{"kind": "retry_budget", "generation": approval.generation},
			&domain.AttentionAction{Kind: "delegation_approval", ApprovalID: approval.id}); err != nil {
			return 0, err
		}
		added++
	}

	// Pending needs_input delegation items with a terminal attempt.
	rows2, err := tx.QueryContext(ctx, `SELECT a.item_id,s.id,s.project_id,a.generation
		FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		JOIN delegation_groups g ON g.id=i.group_id
		JOIN agent_runs ar ON ar.id=g.parent_run_id
		JOIN sessions s ON s.id=ar.session_id
		WHERE a.status='needs_input' LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	var inputs []struct {
		itemID, sessionID, projectID string
		generation                   int
	}
	for rows2.Next() {
		var entry struct {
			itemID, sessionID, projectID string
			generation                   int
		}
		if err := rows2.Scan(&entry.itemID, &entry.sessionID, &entry.generation, &entry.projectID); err != nil {
			rows2.Close()
			return 0, err
		}
		inputs = append(inputs, entry)
	}
	if err := rows2.Close(); err != nil {
		return 0, err
	}
	for _, input := range inputs {
		if err := ProjectAttentionTx(ctx, tx, input.projectID, input.sessionID,
			domain.AttentionSourceDelegationItem, input.itemID, input.generation,
			domain.AttentionNeedsInput, true,
			map[string]any{"kind": "needs_input", "generation": input.generation},
			&domain.AttentionAction{Kind: "delegation_input", ItemID: input.itemID,
				ExpectedGeneration: input.generation}); err != nil {
			return 0, err
		}
		added++
	}

	// Missing completion notifications are reconstructed from terminal facts.
	rows3, err := tx.QueryContext(ctx, `SELECT c.handle_id,c.session_id,c.generation,c.kind,s.project_id
		FROM delegation_completions c JOIN sessions s ON s.id=c.session_id
		WHERE NOT EXISTS (SELECT 1 FROM attention_items a
			WHERE a.source_kind='delegation_completion' AND a.source_id=c.handle_id
			  AND a.source_generation=c.generation)
		ORDER BY c.sequence LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	var completions []struct {
		handleID, sessionID, kind, projectID string
		generation                           int
	}
	for rows3.Next() {
		var entry struct {
			handleID, sessionID, kind, projectID string
			generation                           int
		}
		if err := rows3.Scan(&entry.handleID, &entry.sessionID, &entry.generation, &entry.kind, &entry.projectID); err != nil {
			rows3.Close()
			return 0, err
		}
		completions = append(completions, entry)
	}
	if err := rows3.Close(); err != nil {
		return 0, err
	}
	for _, completion := range completions {
		attentionKind := domain.AttentionDelegationCompleted
		if completion.kind == "cancelled" || completion.kind == "failed" {
			attentionKind = domain.AttentionDelegationFailed
		}
		if err := ProjectAttentionTx(ctx, tx, completion.projectID, completion.sessionID,
			domain.AttentionSourceDelegationCompletion, completion.handleID, completion.generation,
			attentionKind, false,
			map[string]any{"kind": completion.kind, "generation": completion.generation},
			&domain.AttentionAction{Kind: "none"}); err != nil {
			return 0, err
		}
		added++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}
