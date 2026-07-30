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
	ErrSessionNotFound  = errors.New("session not found")
	ErrSessionRunActive = errors.New("session already has an active run")
	ErrRunNotFound      = errors.New("agent run not found")
	ErrInvalidRunState  = errors.New("invalid agent run state transition")
)

type EventPublisher interface {
	Publish(...domain.RunEvent)
}

type RunRepo struct {
	DB        *sql.DB
	Publisher EventPublisher
}

func (r *RunRepo) SubmitTurn(ctx context.Context, input domain.SubmitTurnInput) (*domain.TurnSubmission, error) {
	if strings.TrimSpace(input.ClientRequestID) == "" {
		return nil, fmt.Errorf("client request id is required")
	}
	if strings.TrimSpace(input.Text) == "" && len(input.Parts) == 0 {
		return nil, fmt.Errorf("turn content is required")
	}
	requestedConfig := input.RequestedConfig
	if len(requestedConfig) == 0 {
		requestedConfig = json.RawMessage(`{}`)
	}
	if !json.Valid(requestedConfig) {
		return nil, fmt.Errorf("requested config is not valid JSON")
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin submit turn: %w", err)
	}
	defer tx.Rollback()

	if existing, err := findSubmissionTx(ctx, tx, input.SessionID, input.ClientRequestID); err != nil {
		return nil, err
	} else if existing != nil {
		existing.Existing = true
		return existing, nil
	}

	var activeLeaf, activeBranch sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT active_leaf_message_id,active_branch_id FROM sessions WHERE id = ? AND status = 'active'`, input.SessionID,
	).Scan(&activeLeaf, &activeBranch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("load session: %w", err)
	}

	var activeRunKind sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT run_kind FROM agent_runs WHERE session_id = ? AND status IN ('queued', 'running', 'waiting_for_approval') LIMIT 1`,
		input.SessionID,
	).Scan(&activeRunKind); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check active run: %w", err)
	}
	if activeRunKind.Valid {
		if activeRunKind.String == string(domain.RunKindContextCompaction) {
			return nil, ErrSessionCompacting
		}
		return nil, ErrSessionRunActive
	}
	activeBranchID, err := ensureActiveBranchTx(ctx, tx, input.SessionID, activeLeaf, activeBranch)
	if err != nil {
		return nil, err
	}

	baseMessageID := input.BaseMessageID
	if baseMessageID == "" && activeLeaf.Valid {
		baseMessageID = activeLeaf.String
	}
	if input.BaseMessageID != "" && (!activeLeaf.Valid || input.BaseMessageID != activeLeaf.String) {
		return nil, ErrBranchPointNotActive
	}
	if baseMessageID != "" {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM messages WHERE id = ? AND session_id = ?`,
			baseMessageID, input.SessionID,
		).Scan(&exists); err != nil {
			return nil, fmt.Errorf("validate base message: %w", err)
		}
		if exists != 1 {
			return nil, fmt.Errorf("base message does not belong to session: %s", baseMessageID)
		}
	}

	timestamp := time.Now().UTC()
	messageID := uuid.NewString()
	turnID := uuid.NewString()
	runID := uuid.NewString()
	parts, imageArtifactIDs, err := prepareUserPartsTx(ctx, tx, input.SessionID, input.Text, input.Parts)
	if err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, session_id, parent_message_id, role, status, created_at)
		 VALUES (?, ?, ?, 'user', 'complete', ?)`,
		messageID, input.SessionID, nullableStr(baseMessageID), timestamp.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("insert user message: %w", err)
	}
	if err := insertMessageParts(ctx, tx, messageID, parts); err != nil {
		return nil, fmt.Errorf("insert user message parts: %w", err)
	}
	for _, artifactID := range imageArtifactIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET message_id=? WHERE id=? AND message_id IS NULL`, messageID, artifactID); err != nil {
			return nil, fmt.Errorf("link image artifact: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO turns
		 (id, session_id, client_request_id, user_message_id, base_message_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`,
		turnID, input.SessionID, input.ClientRequestID, messageID, nullableStr(baseMessageID),
		timestamp.Format(time.RFC3339Nano), timestamp.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("insert turn: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_runs
		 (id, turn_id, session_id, run_kind, base_message_id, attempt, status, requested_config_json, effective_config_json, created_at)
		 VALUES (?, ?, ?, 'agent', ?, 1, 'queued', ?, '{}', ?)`,
		runID, turnID, input.SessionID, messageID, string(requestedConfig), timestamp.Format(time.RFC3339Nano),
	); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrSessionRunActive
		}
		return nil, fmt.Errorf("insert agent run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE session_branches SET leaf_message_id=?,updated_at=?
		WHERE id=? AND session_id=?`, messageID, timestamp.Format(time.RFC3339Nano), activeBranchID, input.SessionID); err != nil {
		return nil, fmt.Errorf("update branch leaf: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET active_leaf_message_id = ?, updated_at = ? WHERE id = ? AND active_branch_id=?`,
		messageID, timestamp.Format(time.RFC3339Nano), input.SessionID, activeBranchID,
	); err != nil {
		return nil, fmt.Errorf("update session leaf: %w", err)
	}
	queuedPayload, _ := json.Marshal(map[string]string{"turnId": turnID, "userMessageId": messageID})
	committedEvents, err := appendEventsTx(ctx, tx, runID, domain.PendingEvent{EventType: "run_queued", Payload: queuedPayload})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit submit turn: %w", err)
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committedEvents...)
	}

	return &domain.TurnSubmission{
		TurnID: turnID, UserMessageID: messageID,
		Run: domain.AgentRun{
			ID: runID, TurnID: turnID, SessionID: input.SessionID, RunKind: domain.RunKindAgent, BaseMessageID: messageID, Attempt: 1,
			Status: domain.RunQueued, RequestedConfig: requestedConfig,
			EffectiveConfig: json.RawMessage(`{}`), CreatedAt: timestamp,
		},
	}, nil
}

func prepareUserPartsTx(ctx context.Context, tx *sql.Tx, sessionID, text string, requested []domain.ContentBlock) ([]domain.ContentBlock, []string, error) {
	parts := make([]domain.ContentBlock, 0, len(requested)+1)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, domain.ContentBlock{Kind: domain.ContentText, Text: text})
	}
	parts = append(parts, requested...)
	artifactIDs := make([]string, 0)
	for index := range parts {
		switch parts[index].Kind {
		case domain.ContentText:
			if strings.TrimSpace(parts[index].Text) == "" {
				return nil, nil, fmt.Errorf("text content part cannot be empty")
			}
		case domain.ContentImage:
			if parts[index].Image == nil || strings.TrimSpace(parts[index].Image.ArtifactID) == "" {
				return nil, nil, fmt.Errorf("image content part requires artifactId")
			}
			var mime, sha, metadata string
			err := tx.QueryRowContext(ctx, `SELECT a.mime_type,a.sha256,a.metadata_json FROM artifacts a
				JOIN sessions s ON s.id=? WHERE a.id=? AND a.kind='image_attachment'
				AND a.project_id=s.project_id AND (a.session_id IS NULL OR a.session_id=s.id)`,
				sessionID, parts[index].Image.ArtifactID).Scan(&mime, &sha, &metadata)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil, domain.NewCodedError(domain.ErrorImageInvalid, fmt.Errorf("image artifact does not belong to session"))
			}
			if err != nil {
				return nil, nil, err
			}
			var dimensions struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			}
			_ = json.Unmarshal([]byte(metadata), &dimensions)
			parts[index].Image = &domain.ImageRef{ArtifactID: parts[index].Image.ArtifactID,
				MIMEType: mime, SHA256: sha, Width: dimensions.Width, Height: dimensions.Height}
			artifactIDs = append(artifactIDs, parts[index].Image.ArtifactID)
		default:
			return nil, nil, fmt.Errorf("unsupported user content kind: %s", parts[index].Kind)
		}
	}
	return parts, artifactIDs, nil
}

func (r *RunRepo) Get(ctx context.Context, runID string) (*domain.AgentRun, error) {
	row := r.DB.QueryRowContext(ctx, runSelect+` WHERE id = ?`, runID)
	run, err := scanAgentRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *RunRepo) FindActiveBySession(ctx context.Context, sessionID string) (*domain.AgentRun, error) {
	run, err := scanAgentRun(r.DB.QueryRowContext(ctx, runSelect+
		` WHERE session_id=? AND status IN ('queued','running','waiting_for_approval') ORDER BY created_at DESC LIMIT 1`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *RunRepo) Claim(ctx context.Context, runID string) (*domain.AgentRun, error) {
	if err := r.transition(ctx, runID, domain.RunRunning, "run_started", nil, nil); err != nil {
		return nil, err
	}
	return r.Get(ctx, runID)
}

func (r *RunRepo) Succeed(ctx context.Context, runID string) error {
	return r.transition(ctx, runID, domain.RunSucceeded, "run_succeeded", nil, nil)
}

func (r *RunRepo) FinalizeSuccess(ctx context.Context, runID string, output domain.RunOutput) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin successful run finalization: %w", err)
	}
	defer tx.Rollback()

	var current domain.RunStatus
	var turnID, sessionID, parentMessageID string
	var activeLeaf, activeBranch sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ar.status,ar.turn_id,ar.session_id,t.user_message_id,
		s.active_leaf_message_id,s.active_branch_id FROM agent_runs ar
		JOIN turns t ON t.id=ar.turn_id JOIN sessions s ON s.id=ar.session_id WHERE ar.id=?`, runID).
		Scan(&current, &turnID, &sessionID, &parentMessageID, &activeLeaf, &activeBranch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("load run for finalization: %w", err)
	}
	if current == domain.RunSucceeded {
		return nil
	}
	if !domain.CanTransitionRun(current, domain.RunSucceeded) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidRunState, current, domain.RunSucceeded)
	}
	if !activeLeaf.Valid || activeLeaf.String != parentMessageID || !activeBranch.Valid {
		return fmt.Errorf("%w: active branch moved before run finalization", ErrBranchPointNotActive)
	}
	activeBranchID := activeBranch.String

	timestamp := time.Now().UTC()
	var activeCalls int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM model_calls WHERE run_id = ? AND status = 'started') +
		(SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND status = 'started')`, runID, runID).Scan(&activeCalls); err != nil {
		return fmt.Errorf("check active calls before success: %w", err)
	}
	if activeCalls != 0 {
		return fmt.Errorf("successful run still has %d active calls", activeCalls)
	}
	messageIDs := make([]string, 0, len(output.Messages))
	assistantMessageID := ""
	for _, message := range output.Messages {
		if message.Role != domain.RoleUser && message.Role != domain.RoleAssistant && message.Role != domain.RoleTool {
			return fmt.Errorf("unsupported projected message role: %s", message.Role)
		}
		messageID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages
			(id, session_id, parent_message_id, role, status, run_id, created_at)
			VALUES (?, ?, ?, ?, 'complete', ?, ?)`, messageID, sessionID, parentMessageID,
			message.Role, runID, timestamp.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("insert projected message: %w", err)
		}
		if err := insertMessageParts(ctx, tx, messageID, message.Content); err != nil {
			return err
		}
		if err := linkToolResultArtifactsTx(ctx, tx, messageID, sessionID, runID, message.Content); err != nil {
			return err
		}
		parentMessageID = messageID
		messageIDs = append(messageIDs, messageID)
		if message.Role == domain.RoleAssistant {
			assistantMessageID = messageID
		}
	}
	if assistantMessageID == "" {
		return fmt.Errorf("successful run has no complete assistant message")
	}

	finishedAt := timestamp.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = 'succeeded',
		assistant_message_id = ?, finished_at = ?, error_code = NULL, error_message = NULL WHERE id = ?`,
		assistantMessageID, finishedAt, runID); err != nil {
		return fmt.Errorf("complete run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET status = 'succeeded', updated_at = ? WHERE id = ?`,
		finishedAt, turnID); err != nil {
		return fmt.Errorf("complete turn: %w", err)
	}
	branchResult, err := tx.ExecContext(ctx, `UPDATE session_branches SET leaf_message_id=?,updated_at=?
		WHERE id=? AND session_id=? AND leaf_message_id=?`, parentMessageID, finishedAt,
		activeBranchID, sessionID, activeLeaf.String)
	if err != nil {
		return fmt.Errorf("advance branch leaf: %w", err)
	}
	if changed, _ := branchResult.RowsAffected(); changed != 1 {
		return fmt.Errorf("%w: active branch leaf changed", ErrBranchPointNotActive)
	}
	sessionResult, err := tx.ExecContext(ctx, `UPDATE sessions SET active_leaf_message_id=?,updated_at=?
		WHERE id=? AND active_branch_id=? AND active_leaf_message_id=?`, parentMessageID, finishedAt,
		sessionID, activeBranchID, activeLeaf.String)
	if err != nil {
		return fmt.Errorf("activate assistant message: %w", err)
	}
	if changed, _ := sessionResult.RowsAffected(); changed != 1 {
		return fmt.Errorf("%w: active session branch changed", ErrBranchPointNotActive)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_input_queue SET status = 'cancelled', cancelled_at = ?
		WHERE run_id = ? AND status = 'queued'`, finishedAt, runID); err != nil {
		return fmt.Errorf("cancel pending run inputs: %w", err)
	}
	messagePayload, _ := json.Marshal(map[string]any{
		"assistantMessageId": assistantMessageID, "messageIds": messageIDs,
	})
	runPayload, _ := json.Marshal(map[string]any{"status": domain.RunSucceeded})
	committedEvents, err := appendEventsTx(ctx, tx, runID,
		domain.PendingEvent{EventType: "message_committed", Payload: messagePayload},
		domain.PendingEvent{EventType: "run_succeeded", Payload: runPayload},
	)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit successful run finalization: %w", err)
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committedEvents...)
	}
	return nil
}

func linkToolResultArtifactsTx(ctx context.Context, tx *sql.Tx, messageID, sessionID, runID string,
	blocks []domain.ContentBlock) error {
	linked := make(map[string]struct{})
	for _, block := range blocks {
		if block.Kind != domain.ContentToolResult || block.ToolResult == nil {
			continue
		}
		for _, reference := range block.ToolResult.Artifacts {
			if _, duplicate := linked[reference.ArtifactID]; duplicate {
				return fmt.Errorf("artifact %s is referenced more than once in projected message", reference.ArtifactID)
			}
			linked[reference.ArtifactID] = struct{}{}
			var stored domain.ArtifactReference
			var artifactRunID, sourceToolCallID, metadataJSON string
			var currentMessageID sql.NullString
			err := tx.QueryRowContext(ctx, `SELECT a.id,a.name,a.kind,a.mime_type,a.size_bytes,a.sha256,
				COALESCE(a.run_id,''),a.source_tool_call_id,a.metadata_json,a.message_id
				FROM artifacts a JOIN sessions s ON s.id=?
				WHERE a.id=? AND a.session_id=s.id AND a.project_id=s.project_id`, sessionID, reference.ArtifactID).Scan(
				&stored.ArtifactID, &stored.Name, &stored.Kind, &stored.MIMEType, &stored.SizeBytes, &stored.SHA256,
				&artifactRunID, &sourceToolCallID, &metadataJSON, &currentMessageID)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("artifact %s does not belong to projected session", reference.ArtifactID)
			}
			if err != nil {
				return fmt.Errorf("load projected artifact %s: %w", reference.ArtifactID, err)
			}
			var dimensions struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			}
			_ = json.Unmarshal([]byte(metadataJSON), &dimensions)
			stored.Width, stored.Height = dimensions.Width, dimensions.Height
			if artifactRunID != runID || sourceToolCallID != block.ToolResult.ToolCallID || stored != reference {
				return fmt.Errorf("artifact %s provenance or metadata does not match projected reference", reference.ArtifactID)
			}
			if currentMessageID.Valid && currentMessageID.String != messageID {
				return fmt.Errorf("artifact %s is already linked to another message", reference.ArtifactID)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET message_id=? WHERE id=? AND (message_id IS NULL OR message_id=?)`,
				messageID, reference.ArtifactID, messageID); err != nil {
				return fmt.Errorf("link artifact %s: %w", reference.ArtifactID, err)
			}
		}
	}
	return nil
}

func (r *RunRepo) Fail(ctx context.Context, runID, code, message string) error {
	return r.transition(ctx, runID, domain.RunFailed, "run_failed", &code, &message)
}

func (r *RunRepo) Cancel(ctx context.Context, runID string) error {
	return r.transition(ctx, runID, domain.RunCancelled, "run_cancelled", nil, nil)
}

func (r *RunRepo) Interrupt(ctx context.Context, runID, message string) error {
	code := "worker_restarted"
	return r.transition(ctx, runID, domain.RunInterrupted, "run_interrupted", &code, &message)
}

func (r *RunRepo) RecoverActive(ctx context.Context) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id,status FROM agent_runs WHERE status IN ('queued','running') ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list recoverable runs: %w", err)
	}
	var queued, running []string
	for rows.Next() {
		var id string
		var status domain.RunStatus
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return nil, err
		}
		if status == domain.RunQueued {
			queued = append(queued, id)
		} else {
			running = append(running, id)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range running {
		if err := r.Interrupt(ctx, id, "worker restarted during run execution"); err != nil {
			return nil, err
		}
	}
	return queued, nil
}

func (r *RunRepo) transition(ctx context.Context, runID string, target domain.RunStatus, eventType string, errorCode, errorMessage *string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var current domain.RunStatus
	var turnID sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT status, turn_id FROM agent_runs WHERE id = ?`, runID,
	).Scan(&current, &turnID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return fmt.Errorf("load run state: %w", err)
	}
	if current == target {
		return nil
	}
	if !domain.CanTransitionRun(current, target) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidRunState, current, target)
	}

	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	var startedAt, finishedAt any
	if target == domain.RunRunning {
		startedAt = timestamp
	}
	if target.Terminal() {
		finishedAt = timestamp
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_runs
		 SET status = ?, started_at = COALESCE(?, started_at), finished_at = COALESCE(?, finished_at),
		     error_code = ?, error_message = ?
		 WHERE id = ?`,
		target, startedAt, finishedAt, errorCode, errorMessage, runID,
	); err != nil {
		return fmt.Errorf("update run state: %w", err)
	}
	if turnID.Valid {
		turnStatus := turnStatusForRun(target)
		if _, err := tx.ExecContext(ctx,
			`UPDATE turns SET status = ?, updated_at = ? WHERE id = ?`, turnStatus, timestamp, turnID.String,
		); err != nil {
			return fmt.Errorf("update turn state: %w", err)
		}
	}
	var callEvents []domain.PendingEvent
	if target.Terminal() {
		if target == domain.RunSucceeded {
			var activeCalls int
			if err := tx.QueryRowContext(ctx, `SELECT
				(SELECT COUNT(*) FROM model_calls WHERE run_id = ? AND status = 'started') +
				(SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND status = 'started')`, runID, runID).Scan(&activeCalls); err != nil {
				return fmt.Errorf("check active calls before success: %w", err)
			}
			if activeCalls != 0 {
				return fmt.Errorf("successful run still has %d active calls", activeCalls)
			}
		} else {
			callEvents, err = closeStartedCallsTx(ctx, tx, runID, target, errorCode, timestamp)
			if err != nil {
				return err
			}
			compactionEvents, closeErr := closeActiveCompactionsTx(ctx, tx, runID, target, errorCode, errorMessage, timestamp)
			if closeErr != nil {
				return closeErr
			}
			callEvents = append(callEvents, compactionEvents...)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE run_input_queue SET status = 'cancelled', cancelled_at = ?
			 WHERE run_id = ? AND status = 'queued'`, timestamp, runID,
		); err != nil {
			return fmt.Errorf("cancel pending run inputs: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_approval_requests SET status='cancelled',resolved_at=?
			WHERE run_id=? AND status='pending'`, timestamp, runID); err != nil {
			return fmt.Errorf("cancel pending approvals: %w", err)
		}
		checkpointStatus := domain.CheckpointInterrupted
		if target == domain.RunCancelled {
			checkpointStatus = domain.CheckpointCancelled
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_execution_checkpoints SET status=?,finished_at=?
			WHERE run_id=? AND status IN ('pending','executing')`, checkpointStatus, timestamp, runID); err != nil {
			return fmt.Errorf("close execution checkpoints: %w", err)
		}
	}
	payload := map[string]any{"status": target}
	if errorCode != nil {
		payload["errorCode"] = *errorCode
	}
	if errorMessage != nil {
		payload["errorMessage"] = *errorMessage
	}
	encoded, _ := json.Marshal(payload)
	pendingEvents := append(callEvents, domain.PendingEvent{EventType: eventType, Payload: encoded})
	committedEvents, err := appendEventsTx(ctx, tx, runID, pendingEvents...)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run transition: %w", err)
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committedEvents...)
	}
	return nil
}

func closeActiveCompactionsTx(ctx context.Context, tx *sql.Tx, runID string, target domain.RunStatus,
	errorCode, errorMessage *string, timestamp string) ([]domain.PendingEvent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM context_compactions WHERE run_id=? AND status IN ('planned','running')`, runID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	status := domain.CompactionFailed
	eventType := "context_compaction_failed"
	code := string(domain.ErrorCompactionProviderFailed)
	if errorCode != nil {
		code = *errorCode
	}
	if target == domain.RunCancelled || target == domain.RunInterrupted {
		status = domain.CompactionCancelled
		eventType = "context_compaction_cancelled"
		code = string(domain.ErrorCompactionCancelled)
	}
	message := "run terminated before context compaction completed"
	if errorMessage != nil {
		message = *errorMessage
	}
	var pending []domain.PendingEvent
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE context_compactions SET status=?,error_code=?,error_message=?,
			finished_at=? WHERE id=? AND status IN ('planned','running')`, status, code, message, timestamp, id); err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(map[string]any{"compactionId": id, "errorCode": code})
		pending = append(pending, domain.PendingEvent{EventType: eventType, Payload: payload})
	}

	runRows, err := tx.QueryContext(ctx, `SELECT id FROM run_context_compactions
		WHERE run_id=? AND status IN ('planned','running')`, runID)
	if err != nil {
		return nil, err
	}
	var runIDs []string
	for runRows.Next() {
		var id string
		if err := runRows.Scan(&id); err != nil {
			runRows.Close()
			return nil, err
		}
		runIDs = append(runIDs, id)
	}
	if err := runRows.Close(); err != nil {
		return nil, err
	}
	runEventType := "run_context_compaction_failed"
	if status == domain.CompactionCancelled {
		runEventType = "run_context_compaction_cancelled"
	}
	for _, id := range runIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE run_context_compactions SET status=?,error_code=?,
			error_message=?,finished_at=? WHERE id=? AND status IN ('planned','running')`,
			status, code, message, timestamp, id); err != nil {
			return nil, err
		}
		payload, _ := json.Marshal(map[string]any{"compactionId": id, "scope": "run", "errorCode": code})
		pending = append(pending, domain.PendingEvent{EventType: runEventType, Payload: payload})
	}
	return pending, nil
}

func closeStartedCallsTx(ctx context.Context, tx *sql.Tx, runID string, target domain.RunStatus, errorCode *string, timestamp string) ([]domain.PendingEvent, error) {
	code := string(domain.ErrorToolBatchFailed)
	if errorCode != nil {
		code = *errorCode
	} else if target == domain.RunCancelled {
		code = string(domain.ErrorRunCancelled)
	} else if target == domain.RunInterrupted {
		code = "worker_restarted"
	}
	status := "failed"
	if target == domain.RunCancelled || target == domain.RunInterrupted {
		status = "cancelled"
	}

	type modelCall struct {
		id                 string
		iteration, attempt int
	}
	modelRows, err := tx.QueryContext(ctx, `SELECT id, iteration, attempt FROM model_calls
		WHERE run_id = ? AND status = 'started' ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list active model calls: %w", err)
	}
	var models []modelCall
	for modelRows.Next() {
		var call modelCall
		if err := modelRows.Scan(&call.id, &call.iteration, &call.attempt); err != nil {
			modelRows.Close()
			return nil, err
		}
		models = append(models, call)
	}
	if err := modelRows.Close(); err != nil {
		return nil, err
	}

	type toolCall struct {
		id, toolCallID, name string
		iteration, callIndex int
	}
	toolRows, err := tx.QueryContext(ctx, `SELECT id, tool_call_id, tool_name, iteration, call_index
		FROM tool_calls WHERE run_id = ? AND status = 'started' ORDER BY seq`, runID)
	if err != nil {
		return nil, fmt.Errorf("list active tool calls: %w", err)
	}
	var tools []toolCall
	for toolRows.Next() {
		var call toolCall
		if err := toolRows.Scan(&call.id, &call.toolCallID, &call.name, &call.iteration, &call.callIndex); err != nil {
			toolRows.Close()
			return nil, err
		}
		tools = append(tools, call)
	}
	if err := toolRows.Close(); err != nil {
		return nil, err
	}

	var pending []domain.PendingEvent
	for _, call := range models {
		if _, err := tx.ExecContext(ctx, `UPDATE model_calls SET status = ?, error_code = ?, finished_at = ?
			WHERE id = ? AND status = 'started'`, status, code, timestamp, call.id); err != nil {
			return nil, fmt.Errorf("close active model call: %w", err)
		}
		payload, _ := json.Marshal(map[string]any{"callId": call.id, "iteration": call.iteration,
			"attempt": call.attempt, "category": code, "retryable": false})
		pending = append(pending, domain.PendingEvent{EventType: "model_call_failed", Payload: payload})
	}
	for _, call := range tools {
		content := fmt.Sprintf("Tool call ended because the run became %s.", target)
		rawArtifacts, err := loadRawToolArtifactsTx(ctx, tx, runID, call.toolCallID)
		if err != nil {
			return nil, err
		}
		encodedArtifacts, _ := json.Marshal(nonNilArtifactReferences(rawArtifacts))
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = ?, result_preview = ?,
			raw_artifact_refs_json = ?, is_error = 1, finished_at = ?
			WHERE id = ? AND status = 'started'`, status, content, string(encodedArtifacts), timestamp, call.id); err != nil {
			return nil, fmt.Errorf("close active tool call: %w", err)
		}
		payload, _ := json.Marshal(map[string]any{"recordId": call.id, "iteration": call.iteration,
			"callIndex": call.callIndex, "toolCallId": call.toolCallID, "toolName": call.name,
			"content": content, "isError": true})
		pending = append(pending, domain.PendingEvent{EventType: "tool_call_completed", Payload: payload})
	}
	return pending, nil
}

func loadRawToolArtifactsTx(ctx context.Context, tx *sql.Tx, runID, toolCallID string) ([]domain.ArtifactReference, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,kind,mime_type,size_bytes,sha256,metadata_json
		FROM artifacts WHERE run_id=? AND source_tool_call_id=? ORDER BY created_at,id`, runID, toolCallID)
	if err != nil {
		return nil, fmt.Errorf("load raw tool artifacts: %w", err)
	}
	defer rows.Close()
	var references []domain.ArtifactReference
	for rows.Next() {
		var reference domain.ArtifactReference
		var metadataJSON string
		if err := rows.Scan(&reference.ArtifactID, &reference.Name, &reference.Kind, &reference.MIMEType,
			&reference.SizeBytes, &reference.SHA256, &metadataJSON); err != nil {
			return nil, err
		}
		var dimensions struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		_ = json.Unmarshal([]byte(metadataJSON), &dimensions)
		reference.Width, reference.Height = dimensions.Width, dimensions.Height
		references = append(references, reference)
	}
	return references, rows.Err()
}

func findSubmissionTx(ctx context.Context, tx *sql.Tx, sessionID, requestID string) (*domain.TurnSubmission, error) {
	row := tx.QueryRowContext(ctx,
		runSelect+` JOIN turns t ON t.id = agent_runs.turn_id
		 WHERE t.session_id = ? AND t.client_request_id = ? ORDER BY agent_runs.attempt DESC LIMIT 1`,
		sessionID, requestID,
	)
	run, err := scanAgentRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var messageID string
	if err := tx.QueryRowContext(ctx, `SELECT user_message_id FROM turns WHERE id = ?`, run.TurnID).Scan(&messageID); err != nil {
		return nil, err
	}
	return &domain.TurnSubmission{TurnID: run.TurnID, UserMessageID: messageID, Run: run}, nil
}

const runSelect = `SELECT agent_runs.id, agent_runs.turn_id, agent_runs.session_id, agent_runs.run_kind,
	agent_runs.base_message_id, agent_runs.attempt, agent_runs.status, agent_runs.assistant_message_id,
	agent_runs.retry_of_run_id, agent_runs.requested_config_json,
	agent_runs.effective_config_json, agent_runs.error_code, agent_runs.error_message,
	agent_runs.started_at, agent_runs.finished_at, agent_runs.created_at
	FROM agent_runs`

func scanAgentRun(row rowScanner) (domain.AgentRun, error) {
	var run domain.AgentRun
	var turnID, baseMessageID, assistantID, retryOfRunID, errorCode, errorMessage sql.NullString
	var startedAt, finishedAt sql.NullString
	var requestedConfig, effectiveConfig, createdAt string
	if err := row.Scan(
		&run.ID, &turnID, &run.SessionID, &run.RunKind, &baseMessageID, &run.Attempt, &run.Status,
		&assistantID, &retryOfRunID, &requestedConfig, &effectiveConfig, &errorCode, &errorMessage,
		&startedAt, &finishedAt, &createdAt,
	); err != nil {
		return run, err
	}
	if turnID.Valid {
		run.TurnID = turnID.String
	}
	if baseMessageID.Valid {
		run.BaseMessageID = baseMessageID.String
	}
	if assistantID.Valid {
		run.AssistantMessageID = &assistantID.String
	}
	if retryOfRunID.Valid {
		run.RetryOfRunID = retryOfRunID.String
	}
	if errorCode.Valid {
		run.ErrorCode = &errorCode.String
	}
	if errorMessage.Valid {
		run.ErrorMessage = &errorMessage.String
	}
	run.RequestedConfig = json.RawMessage(requestedConfig)
	run.EffectiveConfig = json.RawMessage(effectiveConfig)
	var err error
	run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return run, err
	}
	if startedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, startedAt.String)
		if parseErr != nil {
			return run, parseErr
		}
		run.StartedAt = &value
	}
	if finishedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, finishedAt.String)
		if parseErr != nil {
			return run, parseErr
		}
		run.FinishedAt = &value
	}
	return run, nil
}

func turnStatusForRun(status domain.RunStatus) domain.TurnStatus {
	switch status {
	case domain.RunRunning:
		return domain.TurnRunning
	case domain.RunWaitingForApproval:
		return domain.TurnWaitingForApproval
	case domain.RunSucceeded:
		return domain.TurnSucceeded
	case domain.RunFailed:
		return domain.TurnFailed
	case domain.RunCancelled:
		return domain.TurnCancelled
	case domain.RunInterrupted:
		return domain.TurnInterrupted
	default:
		return domain.TurnPending
	}
}
