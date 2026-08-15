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
	ErrRunRetryUnsafe   = errors.New("run retry crosses a committed side-effect boundary")
	ErrRunRetryStale    = errors.New("run is no longer the retryable active-lineage attempt")
	ErrRunRetryConflict = errors.New("retry idempotency key belongs to a different source run")
)

func (r *RunRepo) FindRecoveryBySession(ctx context.Context, sessionID string) (*domain.RunRecovery, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var activeLeaf sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT active_leaf_message_id FROM sessions WHERE id=? AND status='active'`, sessionID).Scan(&activeLeaf); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	if !activeLeaf.Valid {
		return nil, tx.Commit()
	}
	run, err := scanAgentRun(tx.QueryRowContext(ctx, runSelect+` JOIN turns t ON t.id=agent_runs.turn_id
		WHERE agent_runs.session_id=? AND agent_runs.run_kind='agent' AND t.user_message_id=?
		AND agent_runs.status IN ('failed','interrupted') ORDER BY agent_runs.attempt DESC LIMIT 1`,
		sessionID, activeLeaf.String))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	reason, err := retryBlockedReasonTx(ctx, tx, run, activeLeaf.String)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.RunRecovery{Run: run, Retryable: reason == "", BlockedReason: reason}, nil
}

func (r *RunRepo) Retry(ctx context.Context, sourceRunID, clientRequestID string) (*domain.RunRetrySubmission, error) {
	if strings.TrimSpace(clientRequestID) == "" {
		return nil, fmt.Errorf("retry client request id is required")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := scanAgentRun(tx.QueryRowContext(ctx, runSelect+` WHERE agent_runs.id=?`, sourceRunID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	if source.RunKind != domain.RunKindAgent || source.TurnID == "" {
		return nil, ErrRunRetryStale
	}
	if source.CommitFormatVersion != domain.CommitFormatLegacyV1 && source.CommitFormatVersion != domain.CommitFormatSpeakerV2 {
		return nil, domain.NewCodedError(domain.ErrorCommitFormatNotEnabled,
			fmt.Errorf("retry source uses unsupported commit format %d", source.CommitFormatVersion))
	}
	if source.CommitFormatVersion == domain.CommitFormatSpeakerV2 && r.RoleSources == nil {
		var writerSetting string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='hosted_commit_format_version'`).Scan(&writerSetting); err != nil || writerSetting != "2" {
			return nil, domain.NewCodedError(domain.ErrorCommitFormatNotEnabled,
				fmt.Errorf("format 2 writer is not enabled"))
		}
	}
	var userMessageID string
	if err := tx.QueryRowContext(ctx, `SELECT user_message_id FROM turns WHERE id=?`, source.TurnID).Scan(&userMessageID); err != nil {
		return nil, err
	}

	existing, err := scanAgentRun(tx.QueryRowContext(ctx, runSelect+` WHERE agent_runs.session_id=?
		AND agent_runs.retry_client_request_id=? LIMIT 1`, source.SessionID, clientRequestID))
	if err == nil {
		if existing.RetryOfRunID != sourceRunID {
			return nil, ErrRunRetryConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &domain.RunRetrySubmission{SourceRunID: sourceRunID, Run: existing, Existing: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	reason, err := retryBlockedReasonTx(ctx, tx, source, userMessageID)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		switch reason {
		case domain.RetryBlockedSideEffect, domain.RetryBlockedProjectedOutput:
			return nil, fmt.Errorf("%w: %s", ErrRunRetryUnsafe, reason)
		case domain.RetryBlockedActiveRun:
			return nil, ErrSessionRunActive
		default:
			return nil, fmt.Errorf("%w: %s", ErrRunRetryStale, reason)
		}
	}

	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	runID := uuid.NewString()
	attempt := source.Attempt + 1
	requested := source.RequestedConfig
	if len(requested) == 0 || !json.Valid(requested) {
		requested = json.RawMessage(`{}`)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs
		(id,turn_id,session_id,run_kind,base_message_id,attempt,status,requested_config_json,
		effective_config_json,retry_of_run_id,retry_client_request_id,commit_format_version,
		parent_run_id,root_run_id,execution_depth,publish_mode,speaker_snapshot_json,
		context_snapshot_json,context_snapshot_digest,created_at)
		VALUES(?,?,?,'agent',?,?,'queued',?,'{}',?,?,?, ?,?,?,?, ?,?,?,?)`,
		runID, source.TurnID, source.SessionID, userMessageID, attempt, string(requested),
		sourceRunID, clientRequestID, source.CommitFormatVersion, nullableStr(source.ParentRunID),
		firstNonEmpty(source.RootRunID, runID), source.ExecutionDepth, source.PublishMode,
		string(source.SpeakerSnapshot), string(source.ContextSnapshot), source.ContextSnapshotDigest, timestamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrSessionRunActive
		}
		return nil, fmt.Errorf("create retry attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET status='pending',updated_at=? WHERE id=?`, timestamp, source.TurnID); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"reason": "run_retry", "sourceRunId": sourceRunID,
		"turnId": source.TurnID, "attempt": attempt})
	committed, err := appendEventsTx(ctx, tx, runID, domain.PendingEvent{EventType: "run_queued", Payload: payload})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return &domain.RunRetrySubmission{SourceRunID: sourceRunID, Run: domain.AgentRun{
		ID: runID, TurnID: source.TurnID, SessionID: source.SessionID, RunKind: domain.RunKindAgent,
		BaseMessageID: userMessageID, Attempt: attempt, Status: domain.RunQueued, RetryOfRunID: sourceRunID,
		CommitFormatVersion: source.CommitFormatVersion, ParentRunID: source.ParentRunID,
		RootRunID: firstNonEmpty(source.RootRunID, runID), ExecutionDepth: source.ExecutionDepth,
		PublishMode: source.PublishMode, SpeakerSnapshot: append(json.RawMessage(nil), source.SpeakerSnapshot...),
		ContextSnapshot: append(json.RawMessage(nil), source.ContextSnapshot...), ContextSnapshotDigest: source.ContextSnapshotDigest,
		RequestedConfig: requested, EffectiveConfig: json.RawMessage(`{}`), CreatedAt: now,
	}}, nil
}

func retryBlockedReasonTx(ctx context.Context, tx *sql.Tx, run domain.AgentRun, userMessageID string) (domain.RetryBlockedReason, error) {
	if run.RunKind != domain.RunKindAgent || run.TurnID == "" ||
		(run.Status != domain.RunFailed && run.Status != domain.RunInterrupted) {
		return domain.RetryBlockedNotLatest, nil
	}
	var latestAttempt int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt),0) FROM agent_runs WHERE turn_id=?`, run.TurnID).Scan(&latestAttempt); err != nil {
		return "", err
	}
	if latestAttempt != run.Attempt {
		return domain.RetryBlockedNotLatest, nil
	}
	var activeRuns int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE session_id=?
		AND parent_run_id IS NULL
		AND status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children')`, run.SessionID).Scan(&activeRuns); err != nil {
		return "", err
	}
	if activeRuns != 0 {
		return domain.RetryBlockedActiveRun, nil
	}
	var sessionLeaf, branchLeaf sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT s.active_leaf_message_id,b.leaf_message_id FROM sessions s
		LEFT JOIN session_branches b ON b.id=s.active_branch_id AND b.session_id=s.id WHERE s.id=? AND s.status='active'`,
		run.SessionID).Scan(&sessionLeaf, &branchLeaf); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RetryBlockedInactiveTurn, nil
		}
		return "", err
	}
	if !sessionLeaf.Valid || !branchLeaf.Valid || sessionLeaf.String != userMessageID || branchLeaf.String != userMessageID {
		return domain.RetryBlockedInactiveTurn, nil
	}
	var projected int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE run_id=?`, run.ID).Scan(&projected); err != nil {
		return "", err
	}
	if projected != 0 {
		return domain.RetryBlockedProjectedOutput, nil
	}
	var unsafeTools int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls WHERE run_id=? AND status<>'skipped'
		AND COALESCE(risk_class,'')<>'read_only'`, run.ID).Scan(&unsafeTools); err != nil {
		return "", err
	}
	if unsafeTools != 0 {
		return domain.RetryBlockedSideEffect, nil
	}
	return "", nil
}
