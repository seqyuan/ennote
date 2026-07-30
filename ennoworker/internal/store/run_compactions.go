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

type RunCompactionRepo struct {
	DB        *sql.DB
	Publisher EventPublisher
}

type RunCompactionCreate struct {
	RunID                 string
	PreviousCompactionID  string
	Reason                domain.CompactionReason
	Iteration             int
	RequestGeneration     int
	Policy                domain.PolicySnapshot
	EffectiveConfig       json.RawMessage
	SourceDigest          string
	SummaryContractDigest string
	CoveredGenerated      int
	TokensBefore          int
}

type RunCompactionCompletion struct {
	ID                   string
	RunID                string
	ModelCallID          string
	Summary              string
	SummaryDigest        string
	EstimatedTokensAfter int
	ReclaimedTokens      int
}

func (r *RunCompactionRepo) CreateOrReuse(ctx context.Context, input RunCompactionCreate) (*domain.RunContextCompaction, bool, error) {
	if input.RunID == "" || input.Iteration <= 1 || input.RequestGeneration < 0 || input.SourceDigest == "" || input.SummaryContractDigest == "" {
		return nil, false, fmt.Errorf("valid run compaction identity, iteration, generation, and digests are required")
	}
	if input.Reason != domain.CompactionReasonThreshold && input.Reason != domain.CompactionReasonOverflow {
		return nil, false, fmt.Errorf("invalid run compaction reason: %s", input.Reason)
	}
	if existing, err := r.findReusable(ctx, input.RunID, input.SourceDigest, input.SummaryContractDigest); err != nil {
		return nil, false, err
	} else if existing != nil {
		return existing, true, nil
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if input.PreviousCompactionID != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_context_compactions
			WHERE id=? AND run_id=? AND status='completed'`, input.PreviousCompactionID, input.RunID).Scan(&count); err != nil {
			return nil, false, err
		}
		if count != 1 {
			return nil, false, fmt.Errorf("previous run compaction is not a completed ancestor")
		}
	}
	now := time.Now().UTC()
	value := &domain.RunContextCompaction{ID: uuid.NewString(), RunID: input.RunID,
		Status: domain.CompactionPlanned, Reason: input.Reason, Iteration: input.Iteration,
		RequestGeneration: input.RequestGeneration, PolicyProfileID: input.Policy.ID,
		PolicyVersion: input.Policy.Version, EffectiveConfig: json.RawMessage(validJSONOrEmpty(input.EffectiveConfig)),
		SourceDigest: input.SourceDigest, SummaryContractDigest: input.SummaryContractDigest,
		CoveredGenerated: input.CoveredGenerated, TokensBefore: input.TokensBefore, CreatedAt: now}
	if input.PreviousCompactionID != "" {
		value.PreviousCompactionID = &input.PreviousCompactionID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_context_compactions
		(id,run_id,previous_compaction_id,status,reason,iteration,request_generation,
		 policy_profile_id,policy_version,effective_config_json,source_digest,summary_contract_digest,
		 covered_generated,tokens_before,created_at)
		VALUES(?,?,?,'planned',?,?,?,?,?,?,?,?,?,?,?)`, value.ID, input.RunID,
		nullableStr(input.PreviousCompactionID), input.Reason, input.Iteration, input.RequestGeneration,
		nullableStr(input.Policy.ID), input.Policy.Version, validJSONOrEmpty(input.EffectiveConfig),
		input.SourceDigest, input.SummaryContractDigest, input.CoveredGenerated, input.TokensBefore,
		now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, false, err
	}
	payload, _ := json.Marshal(map[string]any{"compactionId": value.ID, "scope": "run",
		"reason": input.Reason, "iteration": input.Iteration, "requestGeneration": input.RequestGeneration})
	committed, err := appendEventsTx(ctx, tx, input.RunID,
		domain.PendingEvent{EventType: "run_context_compaction_planned", Payload: payload})
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return value, false, nil
}

func (r *RunCompactionRepo) Start(ctx context.Context, runID, id string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE run_context_compactions SET status='running',started_at=?
		WHERE id=? AND run_id=? AND status='planned'`, now, id, runID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("run compaction is not planned: %s", id)
	}
	payload, _ := json.Marshal(map[string]any{"compactionId": id, "scope": "run"})
	committed, err := appendEventsTx(ctx, tx, runID,
		domain.PendingEvent{EventType: "run_context_compaction_started", Payload: payload})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return nil
}

func (r *RunCompactionRepo) Complete(ctx context.Context, completion RunCompactionCompletion) error {
	if completion.ID == "" || completion.RunID == "" || completion.ModelCallID == "" || completion.Summary == "" || completion.SummaryDigest == "" {
		return fmt.Errorf("complete run compaction requires ids and summary")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE run_context_compactions SET status='completed',summary=?,
		summary_digest=?,model_call_id=?,estimated_tokens_after=?,reclaimed_tokens=?,finished_at=?,
		error_code=NULL,error_message=NULL WHERE id=? AND run_id=? AND status='running'`,
		completion.Summary, completion.SummaryDigest, completion.ModelCallID, completion.EstimatedTokensAfter,
		completion.ReclaimedTokens, now, completion.ID, completion.RunID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("run compaction is not running: %s", completion.ID)
	}
	payload, _ := json.Marshal(map[string]any{"compactionId": completion.ID, "scope": "run",
		"estimatedTokensAfter": completion.EstimatedTokensAfter, "reclaimedTokens": completion.ReclaimedTokens,
		"modelCallId": completion.ModelCallID})
	committed, err := appendEventsTx(ctx, tx, completion.RunID,
		domain.PendingEvent{EventType: "run_context_compaction_completed", Payload: payload})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return nil
}

func (r *RunCompactionRepo) Fail(ctx context.Context, runID, id string, status domain.CompactionStatus,
	code domain.ErrorCode, cause error) error {
	if status != domain.CompactionFailed && status != domain.CompactionCancelled {
		return fmt.Errorf("invalid run compaction terminal status: %s", status)
	}
	tx, err := r.DB.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(context.WithoutCancel(ctx), `UPDATE run_context_compactions
		SET status=?,error_code=?,error_message=?,finished_at=? WHERE id=? AND run_id=?
		AND status IN ('planned','running')`, status, code, message, now, id, runID); err != nil {
		return err
	}
	eventType := "run_context_compaction_failed"
	if status == domain.CompactionCancelled {
		eventType = "run_context_compaction_cancelled"
	}
	payload, _ := json.Marshal(map[string]any{"compactionId": id, "scope": "run", "errorCode": code})
	committed, err := appendEventsTx(context.WithoutCancel(ctx), tx, runID,
		domain.PendingEvent{EventType: eventType, Payload: payload})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return nil
}

func (r *RunCompactionRepo) Get(ctx context.Context, id string) (*domain.RunContextCompaction, error) {
	value, err := scanRunCompaction(r.DB.QueryRowContext(ctx, runCompactionSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCompactionNotFound
	}
	return &value, err
}

func (r *RunCompactionRepo) findReusable(ctx context.Context, runID, sourceDigest,
	contractDigest string) (*domain.RunContextCompaction, error) {
	value, err := scanRunCompaction(r.DB.QueryRowContext(ctx, runCompactionSelect+`
		WHERE run_id=? AND source_digest=? AND summary_contract_digest=? AND status='completed'
		ORDER BY created_at DESC,id DESC LIMIT 1`, runID, sourceDigest, contractDigest))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &value, err
}

const runCompactionSelect = `SELECT id,run_id,previous_compaction_id,status,reason,iteration,
	request_generation,COALESCE(policy_profile_id,''),COALESCE(policy_version,0),effective_config_json,
	source_digest,summary_contract_digest,summary,summary_digest,covered_generated,model_call_id,
	tokens_before,estimated_tokens_after,reclaimed_tokens,error_code,error_message,started_at,finished_at,created_at
	FROM run_context_compactions`

func scanRunCompaction(scanner interface{ Scan(...any) error }) (domain.RunContextCompaction, error) {
	var value domain.RunContextCompaction
	var previous, modelCall, errorCode, errorMessage, startedAt, finishedAt sql.NullString
	var effective, createdAt string
	err := scanner.Scan(&value.ID, &value.RunID, &previous, &value.Status, &value.Reason,
		&value.Iteration, &value.RequestGeneration, &value.PolicyProfileID, &value.PolicyVersion,
		&effective, &value.SourceDigest, &value.SummaryContractDigest, &value.Summary,
		&value.SummaryDigest, &value.CoveredGenerated, &modelCall, &value.TokensBefore,
		&value.EstimatedTokensAfter, &value.ReclaimedTokens, &errorCode, &errorMessage,
		&startedAt, &finishedAt, &createdAt)
	if err != nil {
		return value, err
	}
	value.EffectiveConfig = json.RawMessage(effective)
	if previous.Valid {
		value.PreviousCompactionID = &previous.String
	}
	if modelCall.Valid {
		value.ModelCallID = &modelCall.String
	}
	if errorCode.Valid {
		value.ErrorCode = &errorCode.String
	}
	if errorMessage.Valid {
		value.ErrorMessage = &errorMessage.String
	}
	value.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return value, err
	}
	if startedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, startedAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.StartedAt = &parsed
	}
	if finishedAt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, finishedAt.String)
		if parseErr != nil {
			return value, parseErr
		}
		value.FinishedAt = &parsed
	}
	return value, nil
}
