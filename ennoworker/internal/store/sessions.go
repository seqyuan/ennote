package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var (
	ErrSessionStateConflict = fmt.Errorf("session state conflict")
	ErrSessionSearchInvalid = fmt.Errorf("invalid session search")
)

const (
	SessionStatusActive   = "active"
	SessionStatusArchived = "archived"
)

type SessionRepo struct{ DB *sql.DB }

func (r *SessionRepo) Create(ctx context.Context, input domain.CreateSessionInput) (*domain.Session, error) {
	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	id, branchID := uuid.NewString(), uuid.NewString()

	var defaultAgent, defaultModel, compactionPolicy any
	if input.DefaultAgentProfileID != nil {
		defaultAgent = *input.DefaultAgentProfileID
	}
	if input.DefaultModelProfileID != nil {
		defaultModel = *input.DefaultModelProfileID
	}
	if input.CompactionPolicyProfileID != nil {
		compactionPolicy = *input.CompactionPolicyProfileID
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create session: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (id, project_id, title, status, active_leaf_message_id, active_branch_id,
		 default_agent_profile_id, default_model_profile_id, compaction_policy_profile_id,
		 source_session_id, source_message_id, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', NULL, NULL, ?, ?, ?, ?, ?, ?, ?)`,
		id, input.ProjectID, input.Title, defaultAgent, defaultModel, compactionPolicy,
		input.SourceSessionID, input.SourceMessageID, timestamp, timestamp,
	); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_branches
		(id,session_id,label,created_at,updated_at) VALUES(?,?,'Main',?,?)`,
		branchID, id, timestamp, timestamp); err != nil {
		return nil, fmt.Errorf("create main branch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET active_branch_id=? WHERE id=?`, branchID, id); err != nil {
		return nil, fmt.Errorf("activate main branch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create session: %w", err)
	}

	return &domain.Session{
		ID: id, ProjectID: input.ProjectID, Title: input.Title, Status: "active", ActiveBranchID: &branchID,
		DefaultAgentProfileID:     input.DefaultAgentProfileID,
		DefaultModelProfileID:     input.DefaultModelProfileID,
		CompactionPolicyProfileID: input.CompactionPolicyProfileID,
		SourceSessionID:           input.SourceSessionID,
		SourceMessageID:           input.SourceMessageID,
		CreatedAt:                 now, UpdatedAt: now,
	}, nil
}

func (r *SessionRepo) ListByProject(ctx context.Context, projectID string) ([]domain.Session, error) {
	return r.SearchByProject(ctx, projectID, SessionStatusActive, "")
}

func (r *SessionRepo) SearchByProject(ctx context.Context, projectID, status, query string) ([]domain.Session, error) {
	if status != SessionStatusActive && status != SessionStatusArchived {
		return nil, fmt.Errorf("%w: unsupported status %q", ErrSessionSearchInvalid, status)
	}
	query = strings.TrimSpace(query)
	if len(query) > 120 {
		return nil, fmt.Errorf("%w: query exceeds 120 characters", ErrSessionSearchInvalid)
	}
	arguments := []any{projectID, status}
	statement := `SELECT id, project_id, title, status, active_leaf_message_id, active_branch_id,
		default_agent_profile_id, default_model_profile_id, compaction_policy_profile_id,
		source_session_id, source_message_id, created_at, updated_at
		FROM sessions WHERE project_id=? AND status=?`
	if query != "" {
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(query))
		statement += ` AND LOWER(title) LIKE ? ESCAPE '\'`
		arguments = append(arguments, "%"+escaped+"%")
	}
	statement += ` ORDER BY updated_at DESC, id`
	rows, err := r.DB.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSessionRows(rows)
}

func scanSessionRows(rows *sql.Rows) ([]domain.Session, error) {
	sessions := make([]domain.Session, 0)
	for rows.Next() {
		var session domain.Session
		var createdAt, updatedAt string
		if err := rows.Scan(&session.ID, &session.ProjectID, &session.Title, &session.Status,
			&session.ActiveLeafMessageID, &session.ActiveBranchID, &session.DefaultAgentProfileID,
			&session.DefaultModelProfileID, &session.CompactionPolicyProfileID, &session.SourceSessionID,
			&session.SourceMessageID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var err error
		session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse session created_at: %w", err)
		}
		session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("parse session updated_at: %w", err)
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (r *SessionRepo) Archive(ctx context.Context, sessionID string) (*domain.Session, error) {
	return r.transitionStatus(ctx, sessionID, SessionStatusActive, SessionStatusArchived)
}

func (r *SessionRepo) Restore(ctx context.Context, sessionID string) (*domain.Session, error) {
	return r.transitionStatus(ctx, sessionID, SessionStatusArchived, SessionStatusActive)
}

func (r *SessionRepo) transitionStatus(ctx context.Context, sessionID, expected, target string) (*domain.Session, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin session lifecycle transition: %w", err)
	}
	defer tx.Rollback()
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET status=?,updated_at=?
		WHERE id=? AND status=? AND NOT EXISTS (
			SELECT 1 FROM agent_runs WHERE session_id=?
			AND status IN ('queued','running','waiting_for_approval')
		)`, target, timestamp, sessionID, expected, sessionID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		var current string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM sessions WHERE id=? AND status!='deleted'`, sessionID).Scan(&current); err != nil {
			if err == sql.ErrNoRows {
				return nil, ErrSessionNotFound
			}
			return nil, err
		}
		if current != expected {
			return nil, fmt.Errorf("%w: expected %s, found %s", ErrSessionStateConflict, expected, current)
		}
		if err := requireSessionIdleTx(ctx, tx, sessionID); err != nil {
			return nil, err
		}
		return nil, ErrSessionStateConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session lifecycle transition: %w", err)
	}
	return r.FindByID(ctx, sessionID)
}

func (r *SessionRepo) FindByID(ctx context.Context, id string) (*domain.Session, error) {
	return findSession(ctx, r.DB, id)
}

func (r *SessionRepo) UpdateTitle(ctx context.Context, sessionID, title string) (*domain.Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("session title cannot be empty")
	}
	if len([]rune(title)) > 200 {
		return nil, fmt.Errorf("session title cannot exceed 200 characters")
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE sessions SET title = ?, updated_at = ?
		WHERE id = ? AND status = 'active'`, title, time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	if err != nil {
		return nil, fmt.Errorf("update session title: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return r.FindByID(ctx, sessionID)
}

func (r *SessionRepo) UpdateDefaultModel(ctx context.Context, sessionID string, modelID *string) (*domain.Session, error) {
	var value any
	if modelID != nil {
		trimmed := strings.TrimSpace(*modelID)
		if trimmed == "" {
			return nil, fmt.Errorf("defaultModelProfileId cannot be empty")
		}
		var exists int
		if err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_profiles m
			JOIN provider_profiles p ON p.id = m.provider_id
			WHERE m.id = ? AND m.status = 'active' AND p.status = 'active'`, trimmed).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 1 {
			return nil, fmt.Errorf("active model profile not found: %s", trimmed)
		}
		value = trimmed
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE sessions SET default_model_profile_id = ?, updated_at = ?
		WHERE id = ? AND status = 'active'`, value, time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	if err != nil {
		return nil, fmt.Errorf("update session default model: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return r.FindByID(ctx, sessionID)
}

func (r *SessionRepo) UpdateCompactionPolicy(ctx context.Context, sessionID string, policyID *string) (*domain.Session, error) {
	var value any
	if policyID != nil {
		trimmed := strings.TrimSpace(*policyID)
		if trimmed == "" {
			return nil, fmt.Errorf("compactionPolicyProfileId cannot be empty")
		}
		var exists int
		if err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM policy_profiles
			WHERE id = ? AND kind = 'compaction' AND status = 'active'`, trimmed).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 1 {
			return nil, fmt.Errorf("active compaction policy profile not found: %s", trimmed)
		}
		value = trimmed
	}
	result, err := r.DB.ExecContext(ctx, `UPDATE sessions SET compaction_policy_profile_id = ?, updated_at = ?
		WHERE id = ? AND status = 'active'`, value, time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	if err != nil {
		return nil, fmt.Errorf("update session compaction policy: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return r.FindByID(ctx, sessionID)
}

func (r *SessionRepo) ActivateLeaf(ctx context.Context, sessionID, messageID string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin activate session leaf: %w", err)
	}
	defer tx.Rollback()
	var activeLeaf, branchID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT active_leaf_message_id,active_branch_id FROM sessions WHERE id=?`, sessionID).
		Scan(&activeLeaf, &branchID); err != nil {
		return fmt.Errorf("load session branch: %w", err)
	}
	activeBranchID, err := ensureActiveBranchTx(ctx, tx, sessionID, activeLeaf, branchID)
	if err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id=? AND session_id=?`, messageID, sessionID).Scan(&exists); err != nil {
		return fmt.Errorf("validate session leaf: %w", err)
	}
	if exists != 1 {
		return fmt.Errorf("session or message not found: session=%s message=%s", sessionID, messageID)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE session_branches SET leaf_message_id=?,updated_at=? WHERE id=? AND session_id=?`,
		messageID, timestamp, activeBranchID, sessionID); err != nil {
		return fmt.Errorf("update branch leaf: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET active_leaf_message_id=?,updated_at=? WHERE id=?`,
		messageID, timestamp, sessionID); err != nil {
		return fmt.Errorf("activate session leaf: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit active leaf: %w", err)
	}
	return nil
}
