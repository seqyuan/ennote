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

// TickSession delivers the oldest pending auto-resume completion of a session
// as exactly one continuation Run, in completion sequence order, only when the
// session is idle on the source branch and auto-resume is enabled. The CAS on
// delivery_status makes duplicate ticks no-ops; the unique source_completion_id
// index makes at-most-one continuation durable even across restarts. Returns
// the created Run or nil.
func (r *DelegationRepo) TickSession(ctx context.Context, sessionID string) (*domain.AgentRun, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1. The session must be idle: no active top-level Run.
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs
		WHERE session_id=? AND parent_run_id IS NULL
		  AND status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children')`,
		sessionID).Scan(&active); err != nil {
		return nil, err
	}
	if active != 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// 2. The session must be active and on a stable branch.
	var activeBranchID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(active_branch_id,'') FROM sessions
		WHERE id=? AND status='active'`, sessionID).Scan(&activeBranchID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	if activeBranchID == "" {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// 3. Oldest pending auto-resume completion on the source branch.
	var completionID, handleID, branchID string
	var generation int
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.handle_id,h.source_branch_id,c.generation
		FROM delegation_completions c JOIN delegation_handles h ON h.id=c.handle_id
		WHERE c.session_id=? AND c.delivery_status='pending' AND h.auto_resume=1 AND h.status='active'
		ORDER BY c.sequence LIMIT 1`, sessionID).Scan(&completionID, &handleID, &branchID, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Branch must still be the source branch (no fork/switch in between).
	if branchID != activeBranchID {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// 4. CAS the delivery slot; a concurrent tick loses cleanly.
	cas, err := tx.ExecContext(ctx, `UPDATE delegation_completions SET delivery_status='resume_queued'
		WHERE id=? AND delivery_status='pending'`, completionID)
	if err != nil {
		return nil, err
	}
	if changed, _ := cas.RowsAffected(); changed != 1 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// 5. Build the system-originated input message with a bounded completion
	//    summary and provenance (never the private transcript or credentials).
	var resultJSON, sourceParentRunID string
	if err := tx.QueryRowContext(ctx, `SELECT c.result_json,h.source_parent_run_id FROM delegation_completions c
		JOIN delegation_handles h ON h.id=c.handle_id WHERE c.id=?`, completionID).
		Scan(&resultJSON, &sourceParentRunID); err != nil {
		return nil, err
	}
	inputText, err := continuationInputText(completionID, resultJSON, sourceParentRunID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	messageID := uuid.NewString()
	turnID := uuid.NewString()
	runID := uuid.NewString()
	var leafMessageID sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(active_leaf_message_id,'') FROM sessions WHERE id=?`,
		sessionID).Scan(&leafMessageID)
	if leafMessageID.String == "" {
		leafMessageID.Valid = false
	}
	messageSeq, err := nextMessageSeq(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages
		(id,session_id,parent_message_id,role,status,speaker_kind,speaker_snapshot_json,visibility,created_at,seq)
		VALUES(?,?,?,'user','complete','system','{"kind":"system","displayName":"System"}','public',?,?)`,
		messageID, sessionID, nullableBackfillString(leafMessageID), now, messageSeq); err != nil {
		return nil, fmt.Errorf("create continuation input message: %w", err)
	}
	if err := insertMessageParts(ctx, tx, messageID, []domain.ContentBlock{{
		Kind: domain.ContentText, Text: inputText,
	}}); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO turns
		(id,session_id,client_request_id,user_message_id,base_message_id,status,
		 input_message_id,input_kind,target_kind,context_mode,reply_to_json,created_at,updated_at)
		VALUES(?,?,?,?,?,'pending',?,'delegation_completion','host','task_only','[]',?,?)`,
		turnID, sessionID, "auto-resume:"+completionID, messageID,
		nullableBackfillString(leafMessageID), messageID, now, now); err != nil {
		return nil, fmt.Errorf("create continuation turn: %w", err)
	}
	hostSpeaker := `{"kind":"host","displayName":"Host"}`
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs
		(id,turn_id,session_id,run_kind,base_message_id,attempt,status,requested_config_json,
		 effective_config_json,speaker_snapshot_json,root_run_id,execution_depth,publish_mode,
		 commit_format_version,context_snapshot_json,source_completion_id,created_at)
		VALUES(?,?,?,'agent',?,1,'queued','{}','{}',?,?,0,'public_final',2,'{}',?,?)`,
		runID, turnID, sessionID, nullableBackfillString(leafMessageID), hostSpeaker, runID, completionID, now); err != nil {
		return nil, fmt.Errorf("create continuation run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE delegation_completions SET resume_run_id=?
		WHERE id=? AND delivery_status='resume_queued'`, runID, completionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.AgentRun{
		ID: runID, TurnID: turnID, SessionID: sessionID, RunKind: domain.RunKindAgent,
		BaseMessageID: leafMessageID.String, Attempt: 1, Status: domain.RunQueued,
		CommitFormatVersion: domain.CommitFormatSpeakerV2, RootRunID: runID,
		ExecutionDepth: 0, PublishMode: domain.PublishPublicFinal,
		SpeakerSnapshot: json.RawMessage(hostSpeaker), ContextSnapshot: json.RawMessage(`{}`),
		RequestedConfig: json.RawMessage(`{}`), EffectiveConfig: json.RawMessage(`{}`),
		CreatedAt: time.Now().UTC(),
	}, nil
}

// continuationInputText renders a bounded, safe summary of the completion for
// the Host continuation run. It includes provenance but never the private
// transcript, credentials, or full assignment.
func continuationInputText(completionID, resultJSON, sourceParentRunID string) (string, error) {
	var folded map[string]any
	if err := json.Unmarshal([]byte(resultJSON), &folded); err != nil {
		return "", err
	}
	summary, err := json.Marshal(folded)
	if err != nil {
		return "", err
	}
	const maxSummary = 6000
	if len(summary) > maxSummary {
		summary = summary[:maxSummary]
	}
	return fmt.Sprintf("A background delegation has completed.\n\nCompletion id: %s\nSource parent run: %s\n\nResult:\n%s",
		completionID, sourceParentRunID, string(summary)), nil
}
