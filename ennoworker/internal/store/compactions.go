package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

var (
	ErrSessionBusy        = errors.New("session has an active run")
	ErrSessionCompacting  = errors.New("session is already compacting")
	ErrCompactionNotFound = errors.New("context compaction not found")
)

type CompactionRepo struct {
	DB        *sql.DB
	Publisher EventPublisher
	// Policies resolves the file-backed compaction policy (V2). When nil, the
	// legacy global policy SQL is used.
	Policies *fileconfig.PolicyStore
}

type CompactionPlanRecord struct {
	CompactionID           string
	PreviousCompactionID   string
	SourceFromMessageID    string
	SourceThroughMessageID string
	FirstKeptMessageID     string
	SourceDigest           string
	SummaryContractDigest  string
	TokensBefore           int
}

type CompactionCompletion struct {
	CompactionID         string
	RunID                string
	CallID               string
	ActualModel          string
	StopReason           string
	Usage                domain.Usage
	Summary              string
	SummaryDigest        string
	EstimatedTokensAfter int
	ReclaimedTokens      int
	IneffectiveRatio     float64
	Ineffective          bool
}

func (r *CompactionRepo) CreateManual(ctx context.Context, input domain.ManualCompactionInput) (*domain.CompactionSubmission, error) {
	if strings.TrimSpace(input.ClientRequestID) == "" || strings.TrimSpace(input.BaseMessageID) == "" {
		return nil, fmt.Errorf("baseMessageId and clientRequestId are required")
	}
	if len(input.Instructions) > 4000 {
		return nil, fmt.Errorf("instructions exceed 4000 bytes")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if existing, findErr := findManualCompactionTx(ctx, tx, input.SessionID, input.ClientRequestID); findErr != nil {
		return nil, findErr
	} else if existing != nil {
		existing.Existing = true
		return existing, nil
	}

	var activeLeaf string
	var sessionPolicyID, agentID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT active_leaf_message_id,compaction_policy_profile_id,default_agent_profile_id
		FROM sessions WHERE id=? AND status='active'`, input.SessionID).Scan(&activeLeaf, &sessionPolicyID, &agentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	if activeLeaf != input.BaseMessageID {
		return nil, domain.NewCodedError(domain.ErrorCompactionCheckpointInvalid,
			errors.New("baseMessageId is not the current active leaf"))
	}
	var activeKind sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT run_kind FROM agent_runs WHERE session_id=?
		AND status IN ('queued','running','waiting_for_approval','waiting_delegation_admission','waiting_children') AND parent_run_id IS NULL LIMIT 1`, input.SessionID).Scan(&activeKind)
	if activeKind.Valid {
		if activeKind.String == string(domain.RunKindContextCompaction) {
			return nil, ErrSessionCompacting
		}
		return nil, ErrSessionBusy
	}

	policyID := nullString(sessionPolicyID)
	if policyID == "" && agentID.Valid {
		_ = tx.QueryRowContext(ctx, `SELECT compaction_policy_profile_id FROM agent_profiles
			WHERE id=? AND status='active'`, agentID.String).Scan(&policyID)
	}
	var policy domain.PolicySnapshot
	if r.Policies != nil {
		policy, err = r.Policies.Resolve(ctx, policyID, domain.PolicyKindCompaction)
	} else {
		policy, err = loadPolicySnapshotTx(ctx, tx, policyID, domain.PolicyKindCompaction, "default_compaction_policy_profile_id")
	}
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, err)
	}
	var config domain.CompactionPolicyConfig
	if err := json.Unmarshal(policy.Config, &config); err != nil {
		return nil, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, err)
	}
	if config.Mode == domain.CompactionDisabled {
		return nil, domain.NewCodedError(domain.ErrorCompactionNotAllowed, errors.New("manual compaction is disabled"))
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	runID, compactionID := uuid.NewString(), uuid.NewString()
	requested, _ := json.Marshal(map[string]any{"compactionPolicyProfileId": policy.ID})
	if len(input.RequestedConfig) > 0 && json.Valid(input.RequestedConfig) {
		requested = input.RequestedConfig
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs
		(id,turn_id,session_id,run_kind,base_message_id,attempt,status,requested_config_json,
		 effective_config_json,speaker_snapshot_json,root_run_id,execution_depth,publish_mode,
		 commit_format_version,context_snapshot_json,created_at)
		VALUES (?,NULL,?,'context_compaction',?,1,'queued',?,'{}',
		 '{"kind":"host","displayName":"Host"}',?,0,'public_final',1,'{}',?)`, runID, input.SessionID,
		input.BaseMessageID, string(requested), runID, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrSessionCompacting
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO context_compactions
		(id,run_id,session_id,client_request_id,status,reason,policy_profile_id,policy_version,
		 requested_config_json,effective_config_json,base_leaf_message_id,first_kept_message_id,
		 source_digest,summary_contract_digest,prompt_version,custom_instructions,created_at)
		VALUES (?,?,?,?,'planned','manual',?,?,?,'{}',?,?,'','',?,?,?)`, compactionID, runID,
		input.SessionID, input.ClientRequestID, policy.ID, policy.Version, string(requested), input.BaseMessageID,
		input.BaseMessageID, config.PromptVersion, input.Instructions, now); err != nil {
		return nil, err
	}
	queuedPayload, _ := json.Marshal(map[string]any{"runKind": domain.RunKindContextCompaction, "compactionId": compactionID})
	plannedPayload, _ := json.Marshal(map[string]any{"compactionId": compactionID, "reason": domain.CompactionReasonManual,
		"policyProfileId": policy.ID, "policyVersion": policy.Version})
	committed, err := appendEventsTx(ctx, tx,
		runID, domain.PendingEvent{EventType: "run_queued", Payload: queuedPayload},
		domain.PendingEvent{EventType: "context_compaction_planned", Payload: plannedPayload})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return &domain.CompactionSubmission{RunID: runID, CompactionID: compactionID, Status: "queued"}, nil
}

func (r *CompactionRepo) CreateForAgentRun(ctx context.Context, run *domain.AgentRun, reason domain.CompactionReason,
	policy domain.PolicySnapshot, config domain.CompactionPolicyConfig, effective json.RawMessage) (*domain.ContextCompaction, error) {
	now := time.Now().UTC()
	compaction := &domain.ContextCompaction{ID: uuid.NewString(), RunID: &run.ID, SessionID: run.SessionID,
		Status: domain.CompactionPlanned, Reason: reason, PolicyProfileID: policy.ID, PolicyVersion: policy.Version,
		RequestedConfig: run.RequestedConfig, EffectiveConfig: effective, BaseLeafMessageID: run.BaseMessageID,
		FirstKeptMessageID: run.BaseMessageID, PromptVersion: config.PromptVersion, CreatedAt: now}
	if compaction.BaseLeafMessageID == "" {
		var leaf sql.NullString
		if err := r.DB.QueryRowContext(ctx, `SELECT active_leaf_message_id FROM sessions WHERE id=?`, run.SessionID).Scan(&leaf); err != nil {
			return nil, err
		}
		compaction.BaseLeafMessageID = leaf.String
		compaction.FirstKeptMessageID = leaf.String
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO context_compactions
		(id,run_id,session_id,status,reason,policy_profile_id,policy_version,requested_config_json,
		 effective_config_json,base_leaf_message_id,first_kept_message_id,source_digest,
		 summary_contract_digest,prompt_version,created_at)
		VALUES (?,?,?,'planned',?,?,?,?,?,?,?,?,?,?,?)`, compaction.ID, run.ID, run.SessionID, reason,
		policy.ID, policy.Version, validJSONOrEmpty(run.RequestedConfig), validJSONOrEmpty(effective),
		compaction.BaseLeafMessageID, compaction.FirstKeptMessageID, "", "", config.PromptVersion,
		now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"compactionId": compaction.ID, "reason": reason,
		"policyProfileId": policy.ID, "policyVersion": policy.Version})
	committed, err := appendEventsTx(ctx, tx, run.ID,
		domain.PendingEvent{EventType: "context_compaction_planned", Payload: payload})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if r.Publisher != nil {
		r.Publisher.Publish(committed...)
	}
	return compaction, nil
}

func (r *CompactionRepo) ForRun(ctx context.Context, runID string) (*domain.ContextCompaction, error) {
	row := r.DB.QueryRowContext(ctx, compactionSelect+` WHERE run_id=? ORDER BY created_at DESC LIMIT 1`, runID)
	value, err := scanCompaction(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCompactionNotFound
	}
	return &value, err
}

func (r *CompactionRepo) Get(ctx context.Context, id string) (*domain.ContextCompaction, error) {
	value, err := scanCompaction(r.DB.QueryRowContext(ctx, compactionSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCompactionNotFound
	}
	return &value, err
}

func (r *CompactionRepo) List(ctx context.Context, sessionID string) ([]domain.ContextCompaction, error) {
	rows, err := r.DB.QueryContext(ctx, compactionSelect+` WHERE session_id=? ORDER BY created_at DESC,id DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []domain.ContextCompaction
	for rows.Next() {
		value, err := scanCompaction(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *CompactionRepo) Start(ctx context.Context, runID string, plan CompactionPlanRecord, effective json.RawMessage) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE context_compactions SET status='running',previous_compaction_id=?,
		source_from_message_id=?,source_through_message_id=?,first_kept_message_id=?,source_digest=?,
		summary_contract_digest=?,tokens_before=?,effective_config_json=?,started_at=?
		WHERE id=? AND run_id=? AND status='planned'`, nullableStr(plan.PreviousCompactionID),
		nullableStr(plan.SourceFromMessageID), nullableStr(plan.SourceThroughMessageID), plan.FirstKeptMessageID,
		plan.SourceDigest, plan.SummaryContractDigest, plan.TokensBefore, validJSONOrEmpty(effective), now,
		plan.CompactionID, runID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return fmt.Errorf("compaction is not planned: %s", plan.CompactionID)
	}
	payload, _ := json.Marshal(map[string]any{"compactionId": plan.CompactionID, "tokensBefore": plan.TokensBefore,
		"firstKeptMessageId": plan.FirstKeptMessageID, "sourceDigest": plan.SourceDigest})
	committed, err := appendEventsTx(ctx, tx, runID, domain.PendingEvent{EventType: "context_compaction_started", Payload: payload})
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

func (r *CompactionRepo) Complete(ctx context.Context, completion CompactionCompletion, config domain.CompactionPolicyConfig) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	callResult, err := tx.ExecContext(ctx, `UPDATE model_calls SET status='completed',actual_model=?,stop_reason=?,
		uncached_input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=?,reasoning_tokens=?,finished_at=?
		WHERE id=? AND run_id=? AND status='started'`, completion.ActualModel, completion.StopReason,
		completion.Usage.UncachedInputTokens, completion.Usage.OutputTokens, completion.Usage.CacheReadTokens,
		completion.Usage.CacheWriteTokens, completion.Usage.ReasoningTokens, now, completion.CallID, completion.RunID)
	if err != nil {
		return err
	}
	if n, _ := callResult.RowsAffected(); n != 1 {
		return fmt.Errorf("summary model call is not started: %s", completion.CallID)
	}
	if err := upsertUsage(ctx, tx, completion.RunID, completion.CallID, completion.Usage, now); err != nil {
		return err
	}
	checkpointResult, err := tx.ExecContext(ctx, `UPDATE context_compactions SET status='completed',summary=?,
		summary_digest=?,model_call_id=?,estimated_tokens_after=?,reclaimed_tokens=?,finished_at=?,
		error_code=NULL,error_message=NULL WHERE id=? AND run_id=? AND status='running'`, completion.Summary,
		completion.SummaryDigest, completion.CallID, completion.EstimatedTokensAfter, completion.ReclaimedTokens,
		now, completion.CompactionID, completion.RunID)
	if err != nil {
		return err
	}
	if n, _ := checkpointResult.RowsAffected(); n != 1 {
		return fmt.Errorf("compaction is not running: %s", completion.CompactionID)
	}
	ineffectiveCount := 0
	if completion.Ineffective {
		_ = tx.QueryRowContext(ctx, `SELECT ineffective_count+1 FROM session_compaction_state
			WHERE session_id=(SELECT session_id FROM context_compactions WHERE id=?)`, completion.CompactionID).Scan(&ineffectiveCount)
		if ineffectiveCount == 0 {
			ineffectiveCount = 1
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO session_compaction_state
		(session_id,failure_cooldown_until,last_failure_code,ineffective_count,last_reclaim_ratio,updated_at)
		SELECT session_id,NULL,NULL,?,?,? FROM context_compactions WHERE id=?
		ON CONFLICT(session_id) DO UPDATE SET failure_cooldown_until=NULL,last_failure_code=NULL,
		ineffective_count=excluded.ineffective_count,last_reclaim_ratio=excluded.last_reclaim_ratio,
		updated_at=excluded.updated_at`, ineffectiveCount, completion.IneffectiveRatio, now, completion.CompactionID)
	if err != nil {
		return err
	}
	modelPayload, _ := json.Marshal(map[string]any{"callId": completion.CallID, "iteration": 0,
		"attempt": 1, "requestGeneration": 0, "stopReason": completion.StopReason,
		"actualModel": completion.ActualModel, "usage": completion.Usage})
	completedPayload, _ := json.Marshal(map[string]any{"compactionId": completion.CompactionID,
		"estimatedTokensAfter": completion.EstimatedTokensAfter, "reclaimedTokens": completion.ReclaimedTokens,
		"modelCallId": completion.CallID})
	committed, err := appendEventsTx(ctx, tx, completion.RunID,
		domain.PendingEvent{EventType: "model_call_completed", Payload: modelPayload},
		domain.PendingEvent{EventType: "context_compaction_completed", Payload: completedPayload})
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

func (r *CompactionRepo) Fail(ctx context.Context, id, runID string, status domain.CompactionStatus,
	code domain.ErrorCode, cause error, config domain.CompactionPolicyConfig) error {
	if status != domain.CompactionFailed && status != domain.CompactionCancelled {
		return fmt.Errorf("invalid compaction terminal status: %s", status)
	}
	tx, err := r.DB.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	_, err = tx.ExecContext(context.WithoutCancel(ctx), `UPDATE context_compactions SET status=?,error_code=?,
		error_message=?,finished_at=? WHERE id=? AND status IN ('planned','running')`, status, code, message,
		now.Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if status == domain.CompactionFailed && config.FailureCooldownSeconds > 0 {
		until := now.Add(time.Duration(config.FailureCooldownSeconds) * time.Second).Format(time.RFC3339Nano)
		_, err = tx.ExecContext(context.WithoutCancel(ctx), `INSERT INTO session_compaction_state
			(session_id,failure_cooldown_until,last_failure_code,updated_at)
			SELECT session_id,?,?,? FROM context_compactions WHERE id=?
			ON CONFLICT(session_id) DO UPDATE SET failure_cooldown_until=excluded.failure_cooldown_until,
			last_failure_code=excluded.last_failure_code,updated_at=excluded.updated_at`, until, code,
			now.Format(time.RFC3339Nano), id)
		if err != nil {
			return err
		}
	}
	eventType := "context_compaction_failed"
	if status == domain.CompactionCancelled {
		eventType = "context_compaction_cancelled"
	}
	payload, _ := json.Marshal(map[string]any{"compactionId": id, "errorCode": code})
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

func (r *CompactionRepo) LatestValid(ctx context.Context, sessionID string, lineage []domain.Message) (*domain.ContextCompaction, error) {
	if len(lineage) == 0 {
		return nil, nil
	}
	positions := make(map[string]int, len(lineage))
	for index, message := range lineage {
		positions[message.ID] = index
	}
	rows, err := r.DB.QueryContext(ctx, compactionSelect+` WHERE session_id=? AND status='completed'
		ORDER BY created_at DESC,id DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	var candidates []domain.ContextCompaction
	for rows.Next() {
		candidate, scanErr := scanCompaction(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	byID := make(map[string]*domain.ContextCompaction, len(candidates))
	for index := range candidates {
		byID[candidates[index].ID] = &candidates[index]
	}
	var best *domain.ContextCompaction
	bestBoundary := -1
	for index := range candidates {
		candidate := &candidates[index]
		basePos, baseOK := positions[candidate.BaseLeafMessageID]
		keptPos, keptOK := positions[candidate.FirstKeptMessageID]
		if !baseOK || !keptOK || keptPos > basePos || candidate.Summary == "" ||
			digestText(candidate.Summary) != candidate.SummaryDigest || candidate.SourceFromMessageID == nil ||
			candidate.SourceThroughMessageID == nil {
			continue
		}
		fromPos, fromOK := positions[*candidate.SourceFromMessageID]
		throughPos, throughOK := positions[*candidate.SourceThroughMessageID]
		if !fromOK || !throughOK || fromPos > throughPos || throughPos >= keptPos {
			continue
		}
		var previous *domain.ContextCompaction
		if candidate.PreviousCompactionID != nil {
			previous = byID[*candidate.PreviousCompactionID]
			if previous == nil || previous.SessionID != candidate.SessionID ||
				digestText(previous.Summary) != previous.SummaryDigest {
				continue
			}
		}
		var effective domain.EffectiveRunConfig
		if err := json.Unmarshal(candidate.EffectiveConfig, &effective); err != nil ||
			effective.CompactionPolicy.ID == "" || effective.CompactionRuntime.ModelProfileID == "" {
			continue
		}
		source := lineage[fromPos : throughPos+1]
		if agent.ComputeSourceDigest(previous, source, candidate.PromptVersion, candidate.CustomInstructions,
			effective.CompactionRuntime) != candidate.SourceDigest {
			continue
		}
		if agent.ComputeSummaryContractDigest(effective.CompactionPolicy, effective.CompactionRuntime,
			candidate.PromptVersion, candidate.CustomInstructions) != candidate.SummaryContractDigest {
			continue
		}
		if keptPos > bestBoundary {
			best = candidate
			bestBoundary = keptPos
		}
	}
	return best, nil
}

func (r *CompactionRepo) ResetIneffectiveBelowTrigger(ctx context.Context, sessionID string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE session_compaction_state SET ineffective_count=0,updated_at=?
		WHERE session_id=? AND ineffective_count<>0`, time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	return err
}

func (r *CompactionRepo) AutoAllowed(ctx context.Context, sessionID string, config domain.CompactionPolicyConfig) (bool, error) {
	var cooldown sql.NullString
	var ineffective int
	err := r.DB.QueryRowContext(ctx, `SELECT failure_cooldown_until,ineffective_count
		FROM session_compaction_state WHERE session_id=?`, sessionID).Scan(&cooldown, &ineffective)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if cooldown.Valid {
		until, parseErr := time.Parse(time.RFC3339Nano, cooldown.String)
		if parseErr == nil && time.Now().UTC().Before(until) {
			return false, nil
		}
	}
	return ineffective < config.IneffectiveLimit, nil
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func findManualCompactionTx(ctx context.Context, tx *sql.Tx, sessionID, requestID string) (*domain.CompactionSubmission, error) {
	var submission domain.CompactionSubmission
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(run_id,''),id,status FROM context_compactions
		WHERE session_id=? AND client_request_id=?`, sessionID, requestID).Scan(&submission.RunID,
		&submission.CompactionID, &submission.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &submission, err
}

const compactionSelect = `SELECT id,run_id,session_id,client_request_id,status,reason,
	COALESCE(policy_profile_id,''),COALESCE(policy_version,0),requested_config_json,effective_config_json,
	base_leaf_message_id,previous_compaction_id,source_from_message_id,source_through_message_id,
	first_kept_message_id,source_digest,summary_contract_digest,summary,summary_digest,prompt_version,
	custom_instructions,model_call_id,tokens_before,estimated_tokens_after,reclaimed_tokens,error_code,
	error_message,started_at,finished_at,created_at FROM context_compactions`

type compactionScanner interface{ Scan(...any) error }

func scanCompaction(scanner compactionScanner) (domain.ContextCompaction, error) {
	var value domain.ContextCompaction
	var runID, clientID, previousID, sourceFrom, sourceThrough, callID sql.NullString
	var errorCode, errorMessage, startedAt, finishedAt sql.NullString
	var requested, effective, createdAt string
	err := scanner.Scan(&value.ID, &runID, &value.SessionID, &clientID, &value.Status, &value.Reason,
		&value.PolicyProfileID, &value.PolicyVersion, &requested, &effective, &value.BaseLeafMessageID,
		&previousID, &sourceFrom, &sourceThrough, &value.FirstKeptMessageID, &value.SourceDigest,
		&value.SummaryContractDigest, &value.Summary, &value.SummaryDigest, &value.PromptVersion,
		&value.CustomInstructions, &callID, &value.TokensBefore, &value.EstimatedTokensAfter,
		&value.ReclaimedTokens, &errorCode, &errorMessage, &startedAt, &finishedAt, &createdAt)
	if err != nil {
		return value, err
	}
	assignString := func(source sql.NullString, target **string) {
		if source.Valid {
			copy := source.String
			*target = &copy
		}
	}
	assignString(runID, &value.RunID)
	assignString(clientID, &value.ClientRequestID)
	assignString(previousID, &value.PreviousCompactionID)
	assignString(sourceFrom, &value.SourceFromMessageID)
	assignString(sourceThrough, &value.SourceThroughMessageID)
	assignString(callID, &value.ModelCallID)
	assignString(errorCode, &value.ErrorCode)
	assignString(errorMessage, &value.ErrorMessage)
	value.RequestedConfig = json.RawMessage(requested)
	value.EffectiveConfig = json.RawMessage(effective)
	value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if startedAt.Valid {
		timeValue, parseErr := time.Parse(time.RFC3339Nano, startedAt.String)
		if parseErr == nil {
			value.StartedAt = &timeValue
		}
	}
	if finishedAt.Valid {
		timeValue, parseErr := time.Parse(time.RFC3339Nano, finishedAt.String)
		if parseErr == nil {
			value.FinishedAt = &timeValue
		}
	}
	return value, nil
}
