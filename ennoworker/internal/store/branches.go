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
	ErrBranchNotFound       = errors.New("session branch not found")
	ErrBranchPointNotActive = errors.New("branch point is not on the active session lineage")
)

type BranchRepo struct{ DB *sql.DB }

type sqlReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *BranchRepo) List(ctx context.Context, sessionID string) ([]domain.SessionBranch, error) {
	session, err := findSession(ctx, r.DB, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}
	return listBranches(ctx, r.DB, sessionID, session.ActiveBranchID)
}

func (r *BranchRepo) Create(ctx context.Context, sessionID, fromMessageID, label string) (*domain.BranchNavigation, error) {
	if strings.TrimSpace(fromMessageID) == "" {
		return nil, ErrBranchPointNotActive
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var activeLeaf, activeBranch sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT active_leaf_message_id,active_branch_id FROM sessions
		WHERE id=? AND status='active'`, sessionID).Scan(&activeLeaf, &activeBranch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	if err := requireSessionIdleTx(ctx, tx, sessionID); err != nil {
		return nil, err
	}
	activeBranchID, err := ensureActiveBranchTx(ctx, tx, sessionID, activeLeaf, activeBranch)
	if err != nil {
		return nil, err
	}
	if !activeLeaf.Valid {
		return nil, ErrBranchPointNotActive
	}
	var onLineage int
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE chain(id,parent_message_id) AS (
		SELECT id,parent_message_id FROM messages WHERE id=? AND session_id=?
		UNION ALL
		SELECT m.id,m.parent_message_id FROM messages m JOIN chain c ON m.id=c.parent_message_id
		WHERE m.session_id=?
	) SELECT COUNT(*) FROM chain WHERE id=?`, activeLeaf.String, sessionID, sessionID, fromMessageID).Scan(&onLineage); err != nil {
		return nil, err
	}
	if onLineage != 1 {
		return nil, ErrBranchPointNotActive
	}
	var branchCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_branches WHERE session_id=?`, sessionID).Scan(&branchCount); err != nil {
		return nil, err
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = fmt.Sprintf("Branch %d", branchCount+1)
	}
	branchID := uuid.NewString()
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_branches
		(id,session_id,parent_branch_id,fork_message_id,leaf_message_id,label,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, branchID, sessionID, activeBranchID, fromMessageID, fromMessageID,
		label, timestamp, timestamp); err != nil {
		return nil, fmt.Errorf("create session branch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET active_branch_id=?,active_leaf_message_id=?,updated_at=? WHERE id=?`,
		branchID, fromMessageID, timestamp, sessionID); err != nil {
		return nil, fmt.Errorf("activate created branch: %w", err)
	}
	navigation, err := branchNavigation(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return navigation, nil
}

func (r *BranchRepo) Activate(ctx context.Context, sessionID, branchID string) (*domain.BranchNavigation, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id=? AND status='active'`, sessionID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 1 {
		return nil, ErrSessionNotFound
	}
	if err := requireSessionIdleTx(ctx, tx, sessionID); err != nil {
		return nil, err
	}
	var leaf sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT leaf_message_id FROM session_branches WHERE id=? AND session_id=?`,
		branchID, sessionID).Scan(&leaf); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBranchNotFound
		}
		return nil, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET active_branch_id=?,active_leaf_message_id=?,updated_at=? WHERE id=?`,
		branchID, nullableNullString(leaf), timestamp, sessionID); err != nil {
		return nil, fmt.Errorf("activate session branch: %w", err)
	}
	navigation, err := branchNavigation(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return navigation, nil
}

func requireSessionIdleTx(ctx context.Context, tx *sql.Tx, sessionID string) error {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE session_id=?
		AND status IN ('queued','running','waiting_for_approval')`, sessionID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return ErrSessionRunActive
	}
	return nil
}

func ensureActiveBranchTx(ctx context.Context, tx *sql.Tx, sessionID string, activeLeaf, activeBranch sql.NullString) (string, error) {
	if activeBranch.Valid && strings.TrimSpace(activeBranch.String) != "" {
		return activeBranch.String, nil
	}
	branchID := uuid.NewString()
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_branches
		(id,session_id,leaf_message_id,label,created_at,updated_at) VALUES(?,?,?,'Main',?,?)`,
		branchID, sessionID, nullableNullString(activeLeaf), timestamp, timestamp); err != nil {
		return "", fmt.Errorf("create compatibility main branch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET active_branch_id=? WHERE id=?`, branchID, sessionID); err != nil {
		return "", fmt.Errorf("activate compatibility main branch: %w", err)
	}
	return branchID, nil
}

func branchNavigation(ctx context.Context, reader sqlReader, sessionID string) (*domain.BranchNavigation, error) {
	session, err := findSession(ctx, reader, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrSessionNotFound
	}
	branches, err := listBranches(ctx, reader, sessionID, session.ActiveBranchID)
	if err != nil {
		return nil, err
	}
	return &domain.BranchNavigation{Session: *session, Branches: branches}, nil
}

func findSession(ctx context.Context, reader sqlReader, id string) (*domain.Session, error) {
	var session domain.Session
	var createdAt, updatedAt string
	err := reader.QueryRowContext(ctx, `SELECT id,project_id,title,status,active_leaf_message_id,active_branch_id,
		default_agent_profile_id,default_model_profile_id,compaction_policy_profile_id,
		source_session_id,source_message_id,created_at,updated_at FROM sessions WHERE id=?`, id).Scan(
		&session.ID, &session.ProjectID, &session.Title, &session.Status, &session.ActiveLeafMessageID,
		&session.ActiveBranchID, &session.DefaultAgentProfileID, &session.DefaultModelProfileID,
		&session.CompactionPolicyProfileID, &session.SourceSessionID, &session.SourceMessageID,
		&createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func listBranches(ctx context.Context, reader sqlReader, sessionID string, activeBranchID *string) ([]domain.SessionBranch, error) {
	rows, err := reader.QueryContext(ctx, `SELECT id,session_id,parent_branch_id,fork_message_id,leaf_message_id,
		label,created_at,updated_at FROM session_branches WHERE session_id=? ORDER BY created_at,id`, sessionID)
	if err != nil {
		return nil, err
	}
	branches := make([]domain.SessionBranch, 0)
	for rows.Next() {
		var branch domain.SessionBranch
		var createdAt, updatedAt string
		if err := rows.Scan(&branch.ID, &branch.SessionID, &branch.ParentBranchID, &branch.ForkMessageID,
			&branch.LeafMessageID, &branch.Label, &createdAt, &updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		branch.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		branch.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		branch.Active = activeBranchID != nil && *activeBranchID == branch.ID
		branches = append(branches, branch)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range branches {
		if branches[index].LeafMessageID == nil {
			continue
		}
		if err := reader.QueryRowContext(ctx, `WITH RECURSIVE chain(id,parent_message_id) AS (
			SELECT id,parent_message_id FROM messages WHERE id=? AND session_id=?
			UNION ALL SELECT m.id,m.parent_message_id FROM messages m JOIN chain c ON m.id=c.parent_message_id
			WHERE m.session_id=?
		) SELECT COUNT(*) FROM chain`, *branches[index].LeafMessageID, sessionID, sessionID).Scan(&branches[index].MessageCount); err != nil {
			return nil, err
		}
	}
	return branches, nil
}

func nullableNullString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
