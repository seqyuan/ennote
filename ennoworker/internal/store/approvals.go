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

var (
	ErrApprovalNotFound = errors.New("approval request not found")
	ErrApprovalConflict = errors.New("approval decision conflicts with the stored decision")
	ErrApprovalStale    = errors.New("approval request is no longer pending for this run")
)

type ApprovalRepo struct {
	DB        *sql.DB
	Publisher EventPublisher
}

func (r *ApprovalRepo) Suspend(ctx context.Context, runID string, schemaVersion, iteration int,
	batchDigest string, state json.RawMessage, items []domain.ApprovalItem) (*domain.ToolApprovalRequest, error) {
	if schemaVersion < 1 || iteration < 1 || strings.TrimSpace(batchDigest) == "" || !json.Valid(state) || len(items) == 0 {
		return nil, fmt.Errorf("valid checkpoint version, iteration, digest, state, and approval items are required")
	}
	encodedItems, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode approval items: %w", err)
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var sessionID string
	var turnID sql.NullString
	var status domain.RunStatus
	if err := tx.QueryRowContext(ctx, `SELECT session_id,turn_id,status FROM agent_runs WHERE id=?`, runID).
		Scan(&sessionID, &turnID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if status != domain.RunRunning {
		return nil, fmt.Errorf("%w: suspend requires running run", ErrInvalidRunState)
	}
	var activeCalls int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls WHERE run_id=? AND status='started'`, runID).Scan(&activeCalls); err != nil {
		return nil, err
	}
	if activeCalls != 0 {
		return nil, fmt.Errorf("cannot suspend with %d started tool calls", activeCalls)
	}

	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE run_execution_checkpoints SET status='consumed',finished_at=?
		WHERE run_id=? AND status='executing'`, timestamp, runID); err != nil {
		return nil, err
	}
	checkpointID, approvalID := uuid.NewString(), uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_execution_checkpoints
		(id,run_id,schema_version,iteration,batch_digest,state_json,status,created_at)
		VALUES(?,?,?,?,?,?,'pending',?)`, checkpointID, runID, schemaVersion, iteration,
		batchDigest, string(state), timestamp); err != nil {
		return nil, fmt.Errorf("insert execution checkpoint: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tool_approval_requests
		(id,run_id,session_id,checkpoint_id,iteration,batch_digest,status,items_json,requested_at)
		VALUES(?,?,?,?,?,?,'pending',?,?)`, approvalID, runID, sessionID, checkpointID,
		iteration, batchDigest, string(encodedItems), timestamp); err != nil {
		return nil, fmt.Errorf("insert approval request: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='waiting_for_approval',error_code=NULL,error_message=NULL
		WHERE id=? AND status='running'`, runID); err != nil {
		return nil, err
	}
	if turnID.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='waiting_for_approval',updated_at=? WHERE id=?`, timestamp, turnID.String); err != nil {
			return nil, err
		}
	}
	payload, _ := json.Marshal(map[string]any{"approvalId": approvalID, "iteration": iteration,
		"batchDigest": batchDigest, "items": items})
	committed, err := appendEventsTx(ctx, tx, runID, domain.PendingEvent{EventType: "approval_requested", Payload: payload})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return &domain.ToolApprovalRequest{ID: approvalID, RunID: runID, SessionID: sessionID,
		CheckpointID: checkpointID, Iteration: iteration, BatchDigest: batchDigest,
		Status: domain.ApprovalPending, Items: append([]domain.ApprovalItem(nil), items...), RequestedAt: now}, nil
}

func (r *ApprovalRepo) FindPendingBySession(ctx context.Context, sessionID string) (*domain.ToolApprovalRequest, error) {
	row := r.DB.QueryRowContext(ctx, approvalSelect+` WHERE a.session_id=? AND a.status='pending' ORDER BY a.requested_at DESC LIMIT 1`, sessionID)
	approval, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return approval, err
}

func (r *ApprovalRepo) FindPendingByRun(ctx context.Context, runID string) (*domain.ToolApprovalRequest, error) {
	row := r.DB.QueryRowContext(ctx, approvalSelect+` WHERE a.run_id=? AND a.status='pending' ORDER BY a.requested_at DESC LIMIT 1`, runID)
	approval, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return approval, err
}

func (r *ApprovalRepo) Decide(ctx context.Context, approvalID string, decision domain.ApprovalDecision,
	clientRequestID string) (*domain.ToolApprovalRequest, error) {
	if decision != domain.DecisionApproved && decision != domain.DecisionRejected {
		return nil, fmt.Errorf("unsupported approval decision: %s", decision)
	}
	if strings.TrimSpace(clientRequestID) == "" {
		return nil, fmt.Errorf("client request id is required")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	approval, runStatus, turnID, err := findApprovalForDecisionTx(ctx, tx, approvalID)
	if err != nil {
		return nil, err
	}
	desired := domain.ApprovalStatus(decision)
	if approval.Status != domain.ApprovalPending {
		if approval.Status == desired {
			return approval, nil
		}
		return nil, ErrApprovalConflict
	}
	if runStatus != domain.RunWaitingForApproval {
		return nil, ErrApprovalStale
	}
	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE tool_approval_requests SET status=?,decision_client_request_id=?,resolved_at=?
		WHERE id=? AND status='pending'`, desired, clientRequestID, timestamp, approvalID)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, ErrApprovalConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status='queued' WHERE id=? AND status='waiting_for_approval'`, approval.RunID); err != nil {
		return nil, err
	}
	if turnID.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='pending',updated_at=? WHERE id=?`, timestamp, turnID.String); err != nil {
			return nil, err
		}
	}
	resolvedPayload, _ := json.Marshal(map[string]any{"approvalId": approval.ID, "decision": decision,
		"iteration": approval.Iteration, "batchDigest": approval.BatchDigest})
	queuedPayload, _ := json.Marshal(map[string]any{"reason": "approval_resolved", "approvalId": approval.ID})
	committed, err := appendEventsTx(ctx, tx, approval.RunID,
		domain.PendingEvent{EventType: "approval_resolved", Payload: resolvedPayload},
		domain.PendingEvent{EventType: "run_queued", Payload: queuedPayload})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	approval.Status = desired
	approval.DecisionClientRequestID = clientRequestID
	approval.ResolvedAt = &now
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return approval, nil
}

func (r *ApprovalRepo) BeginResume(ctx context.Context, runID string) (*domain.ApprovalResume, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT a.id,a.run_id,a.session_id,a.checkpoint_id,a.iteration,a.batch_digest,
		a.status,a.items_json,a.decision_client_request_id,a.requested_at,a.resolved_at,
		c.schema_version,c.state_json,c.status,c.created_at,c.started_at,c.finished_at,ar.status
		FROM tool_approval_requests a JOIN run_execution_checkpoints c ON c.id=a.checkpoint_id
		JOIN agent_runs ar ON ar.id=a.run_id
		WHERE a.run_id=? AND a.status IN ('approved','rejected') AND c.status='pending'
		ORDER BY a.requested_at DESC LIMIT 1`, runID)
	resume, runStatus, err := scanApprovalResume(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if runStatus != domain.RunRunning {
		return nil, fmt.Errorf("%w: resume requires running run", ErrInvalidRunState)
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE run_execution_checkpoints SET status='executing',started_at=?
		WHERE id=? AND status='pending'`, now.Format(time.RFC3339Nano), resume.Checkpoint.ID)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, ErrApprovalStale
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	resume.Checkpoint.Status = domain.CheckpointExecuting
	resume.Checkpoint.StartedAt = &now
	return resume, nil
}

func (r *ApprovalRepo) CompleteExecuting(ctx context.Context, runID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := r.DB.ExecContext(ctx, `UPDATE run_execution_checkpoints SET status='consumed',finished_at=?
		WHERE run_id=? AND status='executing'`, now, runID)
	return err
}

const approvalSelect = `SELECT a.id,a.run_id,a.session_id,a.checkpoint_id,a.iteration,a.batch_digest,
	a.status,a.items_json,a.decision_client_request_id,a.requested_at,a.resolved_at
	FROM tool_approval_requests a`

func findApprovalForDecisionTx(ctx context.Context, tx *sql.Tx, id string) (*domain.ToolApprovalRequest, domain.RunStatus, sql.NullString, error) {
	row := tx.QueryRowContext(ctx, approvalSelect+` JOIN agent_runs ar ON ar.id=a.run_id WHERE a.id=?`, id)
	// The reusable selector cannot append run columns after WHERE, so load the approval and Run separately.
	approval, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", sql.NullString{}, ErrApprovalNotFound
	}
	if err != nil {
		return nil, "", sql.NullString{}, err
	}
	var status domain.RunStatus
	var turnID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status,turn_id FROM agent_runs WHERE id=?`, approval.RunID).Scan(&status, &turnID); err != nil {
		return nil, "", sql.NullString{}, err
	}
	return approval, status, turnID, nil
}

func scanApproval(scanner interface{ Scan(...any) error }) (*domain.ToolApprovalRequest, error) {
	var approval domain.ToolApprovalRequest
	var items, requestedAt string
	var resolvedAt, requestID sql.NullString
	if err := scanner.Scan(&approval.ID, &approval.RunID, &approval.SessionID, &approval.CheckpointID,
		&approval.Iteration, &approval.BatchDigest, &approval.Status, &items, &requestID,
		&requestedAt, &resolvedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(items), &approval.Items); err != nil {
		return nil, fmt.Errorf("decode approval items: %w", err)
	}
	approval.DecisionClientRequestID = requestID.String
	var err error
	approval.RequestedAt, err = time.Parse(time.RFC3339Nano, requestedAt)
	if err != nil {
		return nil, err
	}
	if resolvedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, resolvedAt.String)
		if parseErr != nil {
			return nil, parseErr
		}
		approval.ResolvedAt = &value
	}
	return &approval, nil
}

func scanApprovalResume(scanner interface{ Scan(...any) error }) (*domain.ApprovalResume, domain.RunStatus, error) {
	var resume domain.ApprovalResume
	var items, requestedAt, checkpointCreated, checkpointState string
	var resolvedAt, requestID, checkpointStarted, checkpointFinished sql.NullString
	var runStatus domain.RunStatus
	err := scanner.Scan(&resume.Approval.ID, &resume.Approval.RunID, &resume.Approval.SessionID,
		&resume.Approval.CheckpointID, &resume.Approval.Iteration, &resume.Approval.BatchDigest,
		&resume.Approval.Status, &items, &requestID, &requestedAt, &resolvedAt,
		&resume.Checkpoint.SchemaVersion, &checkpointState, &resume.Checkpoint.Status,
		&checkpointCreated, &checkpointStarted, &checkpointFinished, &runStatus)
	if err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal([]byte(items), &resume.Approval.Items); err != nil {
		return nil, "", err
	}
	resume.Approval.DecisionClientRequestID = requestID.String
	resume.Approval.RequestedAt, err = time.Parse(time.RFC3339Nano, requestedAt)
	if err != nil {
		return nil, "", err
	}
	if resolvedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, resolvedAt.String)
		if parseErr != nil {
			return nil, "", parseErr
		}
		resume.Approval.ResolvedAt = &value
	}
	resume.Checkpoint.ID = resume.Approval.CheckpointID
	resume.Checkpoint.RunID = resume.Approval.RunID
	resume.Checkpoint.Iteration = resume.Approval.Iteration
	resume.Checkpoint.BatchDigest = resume.Approval.BatchDigest
	resume.Checkpoint.State = json.RawMessage(checkpointState)
	resume.Checkpoint.CreatedAt, err = time.Parse(time.RFC3339Nano, checkpointCreated)
	if err != nil {
		return nil, "", err
	}
	if checkpointStarted.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, checkpointStarted.String)
		if parseErr != nil {
			return nil, "", parseErr
		}
		resume.Checkpoint.StartedAt = &value
	}
	if checkpointFinished.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, checkpointFinished.String)
		if parseErr != nil {
			return nil, "", parseErr
		}
		resume.Checkpoint.FinishedAt = &value
	}
	resume.Decision = domain.ApprovalDecision(resume.Approval.Status)
	return &resume, runStatus, nil
}
