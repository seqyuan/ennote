package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
)

var (
	ErrSessionStateConflict = fmt.Errorf("session state conflict")
	ErrSessionSearchInvalid = fmt.Errorf("invalid session search")
)

const (
	SessionStatusActive   = "active"
	SessionStatusArchived = "archived"
)

type SessionRepo struct {
	DB     *sql.DB
	Files  *sessionstore.Manager
	Models *ModelRepo
}

func (r *SessionRepo) Create(ctx context.Context, input domain.CreateSessionInput) (*domain.Session, error) {
	// V2: Sessions are created through the file-native sessionstore Manager
	// (per-Session SQLite files). The legacy global sessions SQL path was
	// removed.
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.Create(ctx, input)
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
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	sessions, err := r.Files.ListByProject(ctx, projectID, status)
	if err != nil || query == "" {
		return sessions, err
	}
	filtered := sessions[:0]
	needle := strings.ToLower(query)
	for _, session := range sessions {
		if strings.Contains(strings.ToLower(session.Title), needle) {
			filtered = append(filtered, session)
		}
	}
	return filtered, nil
}

func scanSessionRows(rows *sql.Rows) ([]domain.Session, error) {
	sessions := make([]domain.Session, 0)
	for rows.Next() {
		var session domain.Session
		var createdAt, updatedAt string
		if err := rows.Scan(&session.ID, &session.ProjectID, &session.Title, &session.Status, &session.Mode,
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
	if r == nil || (r.Files == nil && r.DB == nil) {
		return nil, ErrFileBackedStoreRequired
	}
	db := r.DB
	if r.Files != nil {
		var err error
		db, err = r.Files.OpenSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	session, err := transitionSessionStatus(ctx, db, sessionID, expected, target)
	if err == nil {
		err = queueSessionProjection(ctx, db, session)
		if err == nil && r.Files != nil {
			r.Files.InvalidateSession(sessionID)
		}
	}
	return session, err
}

// transitionSessionStatus is the per-Session-database status transition. The
// Session's SQLite file owns the sessions row; the manager only locates it.
func transitionSessionStatus(ctx context.Context, db *sql.DB, sessionID, expected, target string) (*domain.Session, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin session lifecycle transition: %w", err)
	}
	defer tx.Rollback()
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET status=?,updated_at=?
		WHERE id=? AND status=? AND NOT EXISTS (
			SELECT 1 FROM agent_runs WHERE session_id=?
			AND status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children') AND parent_run_id IS NULL
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
	return findSession(ctx, db, sessionID)
}

func (r *SessionRepo) FindByID(ctx context.Context, id string) (*domain.Session, error) {
	// V2 dual mode: with Files the manager locates the per-Session database;
	// without Files the caller holds an opened per-Session database directly
	// (the executor's Session repo). The legacy global sessions SQL path was
	// removed.
	if r.Files != nil {
		return r.Files.FindByID(ctx, id)
	}
	if r == nil || r.DB == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return findSession(ctx, r.DB, id)
}

func (r *SessionRepo) UpdateTitle(ctx context.Context, sessionID, title string) (*domain.Session, error) {
	if r == nil || (r.Files == nil && r.DB == nil) {
		return nil, ErrFileBackedStoreRequired
	}
	db := r.DB
	if r.Files != nil {
		var err error
		db, err = r.Files.OpenSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	session, err := updateSessionTitle(ctx, db, sessionID, title)
	if err == nil {
		err = queueSessionProjection(ctx, db, session)
		if err == nil && r.Files != nil {
			r.Files.InvalidateSession(sessionID)
		}
	}
	return session, err
}

func updateSessionTitle(ctx context.Context, db *sql.DB, sessionID, title string) (*domain.Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("session title cannot be empty")
	}
	if len([]rune(title)) > 200 {
		return nil, fmt.Errorf("session title cannot exceed 200 characters")
	}
	result, err := db.ExecContext(ctx, `UPDATE sessions SET title = ?, updated_at = ?
		WHERE id = ? AND status = 'active'`, title, time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	if err != nil {
		return nil, fmt.Errorf("update session title: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return findSession(ctx, db, sessionID)
}

func (r *SessionRepo) UpdateDefaultModel(ctx context.Context, sessionID string, modelID *string) (*domain.Session, error) {
	if r == nil || (r.Files == nil && r.DB == nil) {
		return nil, ErrFileBackedStoreRequired
	}
	if modelID != nil && r.Models != nil {
		model, err := r.Models.FindByID(ctx, strings.TrimSpace(*modelID))
		if err != nil {
			return nil, err
		}
		if model == nil {
			return nil, fmt.Errorf("active model profile not found: %s", *modelID)
		}
	}
	db := r.DB
	if r.Files != nil {
		var err error
		db, err = r.Files.OpenSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	session, err := updateSessionDefaultModel(ctx, db, sessionID, modelID)
	if err == nil {
		err = queueSessionProjection(ctx, db, session)
	}
	return session, err
}

func (r *SessionRepo) UpdateCompactionPolicy(ctx context.Context, sessionID string, policyID *string) (*domain.Session, error) {
	if r == nil || (r.Files == nil && r.DB == nil) {
		return nil, ErrFileBackedStoreRequired
	}
	db := r.DB
	if r.Files != nil {
		var err error
		db, err = r.Files.OpenSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	session, err := updateSessionCompactionPolicy(ctx, db, sessionID, policyID)
	if err == nil {
		err = queueSessionProjection(ctx, db, session)
	}
	return session, err
}

func queueSessionProjection(ctx context.Context, db *sql.DB, session *domain.Session) error {
	if session == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]string{
		"sessionId": session.ID, "projectId": session.ProjectID, "title": session.Title,
		"status": session.Status, "createdAt": session.CreatedAt.Format(time.RFC3339Nano),
		"updatedAt": session.UpdatedAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO projection_outbox
		(event_id,event_type,payload_json,created_at) VALUES(?,?,?,?)`, uuid.NewString(),
		"session.upsert", string(payload), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func updateSessionDefaultModel(ctx context.Context, db *sql.DB, sessionID string, modelID *string) (*domain.Session, error) {
	var value any
	if modelID != nil {
		trimmed := strings.TrimSpace(*modelID)
		if trimmed == "" {
			return nil, fmt.Errorf("defaultModelProfileId cannot be empty")
		}
		value = trimmed
	}
	result, err := db.ExecContext(ctx, `UPDATE sessions SET default_model_profile_id = ?, updated_at = ?
		WHERE id = ? AND status = 'active'`, value, time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	if err != nil {
		return nil, fmt.Errorf("update session default model: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return findSession(ctx, db, sessionID)
}

func updateSessionCompactionPolicy(ctx context.Context, db *sql.DB, sessionID string, policyID *string) (*domain.Session, error) {
	var value any
	if policyID != nil {
		trimmed := strings.TrimSpace(*policyID)
		if trimmed == "" {
			return nil, fmt.Errorf("compactionPolicyProfileId cannot be empty")
		}
		value = trimmed
	}
	result, err := db.ExecContext(ctx, `UPDATE sessions SET compaction_policy_profile_id = ?, updated_at = ?
		WHERE id = ? AND status = 'active'`, value, time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	if err != nil {
		return nil, fmt.Errorf("update session compaction policy: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	return findSession(ctx, db, sessionID)
}

func (r *SessionRepo) ActivateLeaf(ctx context.Context, sessionID, messageID string) error {
	if r == nil || (r.Files == nil && r.DB == nil) {
		return ErrFileBackedStoreRequired
	}
	db := r.DB
	if r.Files != nil {
		var err error
		db, err = r.Files.OpenSession(ctx, sessionID)
		if err != nil {
			return err
		}
	}
	return activateSessionLeaf(ctx, db, sessionID, messageID)
}

func activateSessionLeaf(ctx context.Context, db *sql.DB, sessionID, messageID string) error {
	tx, err := db.BeginTx(ctx, nil)
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
