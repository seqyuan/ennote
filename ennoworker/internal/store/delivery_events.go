package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// DeliveryEventsAfter returns delivery events after a cursor (event id) for a
// session. Reconnect replay is safe because each logical event has exactly one
// durable id; clients dedupe by id and source key.
func (r *DelegationRepo) DeliveryEventsAfter(ctx context.Context, sessionID string, after int64, limit int) ([]domain.DeliveryEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT event_id,session_id,source_kind,source_id,source_generation,event_type,payload_json,created_at
		FROM delivery_events WHERE session_id=? AND event_id>? ORDER BY event_id LIMIT ?`, sessionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.DeliveryEvent, 0)
	for rows.Next() {
		var event domain.DeliveryEvent
		var payload, createdAt string
		if err := rows.Scan(&event.EventID, &event.SessionID, &event.SourceKind, &event.SourceID,
			&event.SourceGeneration, &event.EventType, &payload, &createdAt); err != nil {
			return nil, err
		}
		event.Payload = json.RawMessage(payload)
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// GetHandle returns one delegation handle by id.
func (r *DelegationRepo) GetHandle(ctx context.Context, handleID string) (*domain.DelegationHandle, error) {
	var handle domain.DelegationHandle
	var mode, status, createdAt, updatedAt string
	var autoResume int
	err := r.DB.QueryRowContext(ctx, `SELECT id,group_id,session_id,source_parent_run_id,source_branch_id,
		execution_mode,auto_resume,status,created_at,updated_at
		FROM delegation_handles WHERE id=?`, handleID).Scan(
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

// HandlePage is a cursor-paginated handle list for one session.
type HandlePage struct {
	Items      []domain.DelegationHandle `json:"items"`
	NextCursor string                    `json:"nextCursor,omitempty"`
}

// ListHandles lists delegation handles of a session, newest first, optionally
// filtered by status, with cursor pagination.
func (r *DelegationRepo) ListHandles(ctx context.Context, sessionID, status, before string, limit int) (*HandlePage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := `SELECT id,group_id,session_id,source_parent_run_id,source_branch_id,execution_mode,auto_resume,status,created_at,updated_at
		FROM delegation_handles WHERE session_id=?`
	args := []any{sessionID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	if before != "" {
		query += ` AND id<?`
		args = append(args, before)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &HandlePage{Items: []domain.DelegationHandle{}}
	for rows.Next() {
		var handle domain.DelegationHandle
		var mode, statusValue, createdAt, updatedAt string
		var autoResume int
		if err := rows.Scan(&handle.ID, &handle.GroupID, &handle.SessionID, &handle.SourceParentRunID,
			&handle.SourceBranchID, &mode, &autoResume, &statusValue, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		handle.ExecutionMode = domain.DelegationExecutionMode(mode)
		handle.AutoResume = autoResume == 1
		handle.Status = statusValue
		handle.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		handle.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, handle)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

// CompletionForHandle returns the latest completion of a handle.
func (r *DelegationRepo) CompletionForHandle(ctx context.Context, handleID string) (*domain.DelegationCompletion, error) {
	var completion domain.DelegationCompletion
	var result, createdAt string
	err := r.DB.QueryRowContext(ctx, `SELECT id,handle_id,session_id,generation,kind,result_json,result_digest,sequence,delivery_status,created_at
		FROM delegation_completions WHERE handle_id=? ORDER BY generation DESC LIMIT 1`, handleID).Scan(
		&completion.ID, &completion.HandleID, &completion.SessionID, &completion.Generation,
		&completion.Kind, &result, &completion.ResultDigest, &completion.Sequence,
		&completion.DeliveryStatus, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDelegationGroupNotFound
	}
	if err != nil {
		return nil, err
	}
	completion.ResultJSON = result
	completion.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, err
	}
	return &completion, nil
}
