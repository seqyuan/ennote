package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

type ProviderFactory func(domain.ModelRuntimeSnapshot) (llm.Provider, error)

type Service struct {
	Repo      *store.CompactionRepo
	RunRepo   *store.RunCompactionRepo
	Calls     *store.CallRepo
	Messages  *store.MessageRepo
	Events    agent.EventWriter
	Providers ProviderFactory
	Retry     agent.RetryPolicy
}

type PreparedContext struct {
	Messages   []domain.ChatMessage
	Checkpoint *domain.ContextCompaction
	Projected  bool
}

func (s *Service) ExecuteManual(ctx context.Context, run *domain.AgentRun,
	resolved *store.ResolvedRunConfig, systemPrompt string, tools []domain.ToolDefinition) error {
	checkpoint, err := s.Repo.ForRun(ctx, run.ID)
	if err != nil {
		return err
	}
	lineage, err := s.Messages.HostedContextLineage(ctx, run.SessionID, run.BaseMessageID)
	if err != nil {
		return err
	}
	config, err := decodeConfig(resolved.Effective.CompactionPolicy)
	if err != nil {
		return err
	}
	_, err = s.compact(ctx, run, checkpoint, lineage, resolved.Effective, config,
		domain.CompactionReasonManual, systemPrompt, tools, 0)
	return err
}

func (s *Service) Prepare(ctx context.Context, run *domain.AgentRun, lineage []domain.Message,
	effective domain.EffectiveRunConfig, systemPrompt string, tools []domain.ToolDefinition) (PreparedContext, error) {
	config, err := decodeConfig(effective.CompactionPolicy)
	if err != nil {
		return PreparedContext{}, err
	}
	selected, err := s.Repo.LatestValid(ctx, run.SessionID, lineage)
	if err != nil {
		return PreparedContext{}, err
	}
	if selected != nil {
		messages, err := agent.CheckpointMessages(selected, lineage)
		if err != nil {
			return s.fallbackCanonical(ctx, run, lineage, effective, config, systemPrompt, tools, err)
		}
		estimate := agent.EstimateComposition(systemPrompt, tools, messages, effective.InitialRuntime.MaxOutputTokens)
		trigger := agent.TriggerLimit(effective.InitialRuntime, effective.CompactionRuntime, config)
		if config.Mode == domain.CompactionManualAndAuto && estimate.InputTokens >= trigger {
			allowed, allowErr := s.Repo.AutoAllowed(ctx, run.SessionID, config)
			if allowErr != nil {
				return PreparedContext{}, allowErr
			}
			if allowed {
				planned, createErr := s.Repo.CreateForAgentRun(ctx, run, domain.CompactionReasonThreshold,
					effective.CompactionPolicy, config, run.EffectiveConfig)
				if createErr != nil {
					return PreparedContext{}, createErr
				}
				completed, compactErr := s.compact(ctx, run, planned, lineage, effective, config,
					domain.CompactionReasonThreshold, systemPrompt, tools, 0)
				if compactErr == nil {
					compactedMessages, buildErr := agent.CheckpointMessages(completed, lineage)
					return PreparedContext{Messages: compactedMessages, Checkpoint: completed}, buildErr
				}
				if estimate.InputTokens > agent.MainUsableTokens(effective.InitialRuntime) {
					return PreparedContext{}, domain.NewCodedError(domain.ErrorContextCompactionRequired, compactErr)
				}
			}
		}
		if config.Mode == domain.CompactionManualAndAuto && estimate.InputTokens < trigger {
			if err := s.Repo.ResetIneffectiveBelowTrigger(ctx, run.SessionID); err != nil {
				return PreparedContext{}, err
			}
		}
		if estimate.InputTokens > agent.MainUsableTokens(effective.InitialRuntime) {
			return PreparedContext{}, domain.NewCodedError(domain.ErrorContextCompactionRequired,
				fmt.Errorf("checkpoint context estimate %d exceeds hard budget", estimate.InputTokens))
		}
		if s.Events != nil {
			_ = appendEvent(ctx, s.Events, run.ID, "context_checkpoint_selected", map[string]any{
				"compactionId": selected.ID, "firstKeptMessageId": selected.FirstKeptMessageID,
				"sourceDigest": selected.SourceDigest,
			})
		}
		return PreparedContext{Messages: messages, Checkpoint: selected}, nil
	}
	return s.prepareWithoutCheckpoint(ctx, run, lineage, effective, config, systemPrompt, tools)
}

func (s *Service) RecoverOverflow(ctx context.Context, run *domain.AgentRun, lineage []domain.Message,
	effective domain.EffectiveRunConfig, systemPrompt string, tools []domain.ToolDefinition) (PreparedContext, error) {
	config, err := decodeConfig(effective.CompactionPolicy)
	if err != nil {
		return PreparedContext{}, err
	}
	if config.Mode != domain.CompactionManualAndAuto || !config.AllowOverflowRecovery || config.MaxOverflowRecoveries < 1 {
		return PreparedContext{}, domain.NewCodedError(domain.ErrorContextCompactionRequired,
			errors.New("overflow recovery is not enabled"))
	}
	planned, err := s.Repo.CreateForAgentRun(ctx, run, domain.CompactionReasonOverflow,
		effective.CompactionPolicy, config, run.EffectiveConfig)
	if err != nil {
		return PreparedContext{}, err
	}
	completed, err := s.compact(ctx, run, planned, lineage, effective, config,
		domain.CompactionReasonOverflow, systemPrompt, tools, 1)
	if err != nil {
		return PreparedContext{}, err
	}
	messages, err := agent.CheckpointMessages(completed, lineage)
	return PreparedContext{Messages: messages, Checkpoint: completed}, err
}

func (s *Service) prepareWithoutCheckpoint(ctx context.Context, run *domain.AgentRun, lineage []domain.Message,
	effective domain.EffectiveRunConfig, config domain.CompactionPolicyConfig, systemPrompt string,
	tools []domain.ToolDefinition) (PreparedContext, error) {
	plan, planErr := agent.BuildCompactionPlan(lineage, nil, effective.CompactionPolicy, config,
		effective.InitialRuntime, effective.CompactionRuntime, systemPrompt, tools, "")
	if planErr != nil {
		if domain.ErrorCodeOf(planErr) != domain.ErrorCompactionNothingToCompact {
			return PreparedContext{}, planErr
		}
		messages, _ := agent.CheckpointMessages(nil, lineage)
		estimate := agent.EstimateComposition(systemPrompt, tools, messages, effective.InitialRuntime.MaxOutputTokens)
		if estimate.InputTokens > agent.MainUsableTokens(effective.InitialRuntime) {
			return PreparedContext{}, domain.NewCodedError(domain.ErrorContextTurnTooLarge,
				fmt.Errorf("protected context estimate %d exceeds hard budget", estimate.InputTokens))
		}
		return PreparedContext{Messages: messages}, nil
	}
	projected := plan.ProjectedMessages
	if s.Events != nil && plan.ProjectedTokens < plan.TokensBefore {
		_ = appendEvent(ctx, s.Events, run.ID, "context_pruned", map[string]any{
			"tokensBefore": plan.TokensBefore, "estimatedTokensAfter": plan.ProjectedTokens,
		})
	}
	trigger := agent.CompactionTrigger{TriggerLimit: plan.TriggerLimit, MainUsable: plan.MainUsable}
	if config.Mode != domain.CompactionManualAndAuto || trigger.ProjectionSufficient(plan.ProjectedTokens) {
		if config.Mode == domain.CompactionManualAndAuto && trigger.ProjectionSufficient(plan.ProjectedTokens) {
			if err := s.Repo.ResetIneffectiveBelowTrigger(ctx, run.SessionID); err != nil {
				return PreparedContext{}, err
			}
		}
		if plan.ProjectedTokens > plan.MainUsable {
			return PreparedContext{}, domain.NewCodedError(domain.ErrorContextCompactionRequired,
				fmt.Errorf("projected context estimate %d exceeds hard budget %d", plan.ProjectedTokens, plan.MainUsable))
		}
		return PreparedContext{Messages: projected, Projected: true}, nil
	}
	allowed, err := s.Repo.AutoAllowed(ctx, run.SessionID, config)
	if err != nil {
		return PreparedContext{}, err
	}
	if !allowed {
		if plan.ProjectedTokens <= plan.MainUsable {
			if s.Events != nil {
				_ = appendEvent(ctx, s.Events, run.ID, "context_warning", map[string]any{"reason": "compaction_breaker_open"})
			}
			return PreparedContext{Messages: projected, Projected: true}, nil
		}
		return PreparedContext{}, domain.NewCodedError(domain.ErrorContextCompactionRequired, errors.New("automatic compaction breaker is open"))
	}
	planned, err := s.Repo.CreateForAgentRun(ctx, run, domain.CompactionReasonThreshold,
		effective.CompactionPolicy, config, run.EffectiveConfig)
	if err != nil {
		return PreparedContext{}, err
	}
	completed, err := s.compactWithPlan(ctx, run, planned, lineage, effective, config, plan, systemPrompt, tools, 0)
	if err != nil {
		if plan.ProjectedTokens <= plan.MainUsable {
			return PreparedContext{Messages: projected, Projected: true}, nil
		}
		return PreparedContext{}, domain.NewCodedError(domain.ErrorContextCompactionRequired, err)
	}
	messages, err := agent.CheckpointMessages(completed, lineage)
	return PreparedContext{Messages: messages, Checkpoint: completed}, err
}

func (s *Service) fallbackCanonical(ctx context.Context, run *domain.AgentRun, lineage []domain.Message,
	effective domain.EffectiveRunConfig, config domain.CompactionPolicyConfig, systemPrompt string,
	tools []domain.ToolDefinition, invalid error) (PreparedContext, error) {
	if s.Events != nil {
		_ = appendEvent(ctx, s.Events, run.ID, "context_checkpoint_invalid", map[string]any{
			"errorCode": domain.ErrorCompactionCheckpointInvalid,
		})
	}
	prepared, err := s.prepareWithoutCheckpoint(ctx, run, lineage, effective, config, systemPrompt, tools)
	if err != nil {
		return PreparedContext{}, errors.Join(invalid, err)
	}
	return prepared, nil
}

func (s *Service) compact(ctx context.Context, run *domain.AgentRun, checkpoint *domain.ContextCompaction,
	lineage []domain.Message, effective domain.EffectiveRunConfig, config domain.CompactionPolicyConfig,
	reason domain.CompactionReason, systemPrompt string, tools []domain.ToolDefinition,
	requestGeneration int) (*domain.ContextCompaction, error) {
	previous, err := s.Repo.LatestValid(ctx, run.SessionID, lineage)
	if err != nil {
		return nil, err
	}
	plan, err := agent.BuildCompactionPlan(lineage, previous, effective.CompactionPolicy, config,
		effective.InitialRuntime, effective.CompactionRuntime, systemPrompt, tools, checkpoint.CustomInstructions)
	if err != nil {
		_ = s.fail(ctx, checkpoint, run.ID, config, err)
		return nil, err
	}
	return s.compactWithPlan(ctx, run, checkpoint, lineage, effective, config, plan, systemPrompt, tools, requestGeneration)
}

func (s *Service) compactWithPlan(ctx context.Context, run *domain.AgentRun, checkpoint *domain.ContextCompaction,
	lineage []domain.Message, effective domain.EffectiveRunConfig, config domain.CompactionPolicyConfig,
	plan agent.CompactionPlan, systemPrompt string, tools []domain.ToolDefinition,
	requestGeneration int) (*domain.ContextCompaction, error) {
	if s.Repo == nil || s.Calls == nil || s.Providers == nil {
		return nil, errors.New("compaction service dependencies are required")
	}
	if err := s.Repo.Start(ctx, run.ID, store.CompactionPlanRecord{
		CompactionID: checkpoint.ID, PreviousCompactionID: plan.PreviousCompactionID,
		SourceFromMessageID: plan.SourceFromMessageID, SourceThroughMessageID: plan.SourceThroughMessageID,
		FirstKeptMessageID: plan.FirstKeptMessageID, SourceDigest: plan.SourceDigest,
		SummaryContractDigest: plan.SummaryContractDigest, TokensBefore: plan.TokensBefore,
	}, run.EffectiveConfig); err != nil {
		_ = s.fail(ctx, checkpoint, run.ID, config, err)
		return nil, err
	}
	provider, err := s.Providers(effective.CompactionRuntime)
	if err != nil {
		wrapped := domain.NewCodedError(domain.ErrorCompactionModelUnavailable, err)
		_ = s.fail(ctx, checkpoint, run.ID, config, wrapped)
		return nil, wrapped
	}
	request := agent.SummaryRequest(plan, config, effective.CompactionRuntime, systemPrompt, tools)
	completion, callID, err := s.streamSummary(ctx, run, checkpoint.ID, provider, request,
		effective.CompactionRuntime, requestGeneration)
	if err != nil {
		_ = s.fail(ctx, checkpoint, run.ID, config, err)
		return nil, err
	}
	summary := strings.TrimSpace(summaryText(completion))
	validationErr := agent.ValidateCompactionSummary(summary, config.SummaryMaxOutputTokens)
	if completion.StopReason == domain.StopReasonLength {
		validationErr = domain.NewCodedError(domain.ErrorCompactionOutputInvalid, errors.New("summary output was truncated"))
	}
	if validationErr != nil {
		err := validationErr
		failed := domain.ModelCallFinish{ID: callID, RunID: run.ID, Iteration: 0, Attempt: 1,
			RequestGeneration: requestGeneration, Purpose: domain.ModelCallContextCompaction,
			CompactionID: checkpoint.ID, ErrorCode: string(domain.ErrorCompactionOutputInvalid), Error: err.Error(), Final: true}
		_ = s.Calls.ModelFailed(context.WithoutCancel(ctx), failed)
		_ = s.fail(ctx, checkpoint, run.ID, config, err)
		return nil, err
	}
	temporary := *checkpoint
	temporary.Summary = summary
	temporary.SourceDigest = plan.SourceDigest
	temporary.FirstKeptMessageID = plan.FirstKeptMessageID
	afterMessages, err := agent.CheckpointMessages(&temporary, lineage)
	if err != nil {
		_ = s.fail(ctx, checkpoint, run.ID, config, err)
		return nil, err
	}
	after := agent.EstimateComposition("", nil, afterMessages, effective.InitialRuntime.MaxOutputTokens).InputTokens
	reclaimed := plan.TokensBefore - after
	if reclaimed < 0 {
		reclaimed = 0
	}
	ratio := 0.0
	if plan.TokensBefore > 0 {
		ratio = float64(reclaimed) / float64(plan.TokensBefore)
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Repo.Complete(commitCtx, store.CompactionCompletion{CompactionID: checkpoint.ID,
		RunID: run.ID, CallID: callID, ActualModel: completion.ActualModel, StopReason: completion.StopReason,
		Usage: completion.Usage, Summary: summary, SummaryDigest: agent.DigestText(summary),
		EstimatedTokensAfter: after, ReclaimedTokens: reclaimed, IneffectiveRatio: ratio,
		Ineffective: ratio < config.IneffectiveReclaimRatio,
	}, config); err != nil {
		wrapped := domain.NewCodedError(domain.ErrorEventPersistence, err)
		_ = s.fail(ctx, checkpoint, run.ID, config, wrapped)
		return nil, wrapped
	}
	completed, err := s.Repo.Get(commitCtx, checkpoint.ID)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return completed, context.Canceled
	}
	return completed, nil
}

func (s *Service) streamSummary(ctx context.Context, run *domain.AgentRun, compactionID string,
	provider llm.Provider, request domain.CompletionRequest, runtime domain.ModelRuntimeSnapshot,
	requestGeneration int) (domain.Completion, string, error) {
	delays := s.Retry.Delays
	if delays == nil {
		delays = []time.Duration{time.Second, 5 * time.Second, 10 * time.Second}
	}
	sleep := s.Retry.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	for attempt := 1; ; attempt++ {
		callID := uuid.NewString()
		started := domain.ModelCallStart{ID: callID, RunID: run.ID, Iteration: 0, Attempt: attempt,
			RequestGeneration: requestGeneration, Purpose: domain.ModelCallContextCompaction,
			CompactionID: compactionID, ProviderProfileID: runtime.ProviderProfileID,
			ModelProfileID: runtime.ModelProfileID, RouteReason: "context_compaction",
			RequestedConfig: run.RequestedConfig, EffectiveConfig: run.EffectiveConfig}
		if err := s.Calls.ModelStarted(ctx, started); err != nil {
			return domain.Completion{}, "", err
		}
		completion, streamErr := provider.Stream(ctx, request, llm.NopSink{})
		if streamErr == nil {
			return completion, callID, nil
		}
		if ctx.Err() != nil || errors.Is(streamErr, llm.ErrCancelled) {
			streamErr = context.Canceled
		}
		retryable := ctx.Err() == nil && llm.IsRetryable(streamErr) && attempt <= len(delays)
		code := domain.ErrorCompactionProviderFailed
		if errors.Is(streamErr, context.Canceled) {
			code = domain.ErrorCompactionCancelled
		}
		failed := domain.ModelCallFinish{ID: callID, RunID: run.ID, Iteration: 0, Attempt: attempt,
			RequestGeneration: requestGeneration, Purpose: domain.ModelCallContextCompaction,
			CompactionID: compactionID, ErrorCode: string(code), Error: streamErr.Error(),
			Retryable: retryable, Final: !retryable}
		if err := s.Calls.ModelFailed(context.WithoutCancel(ctx), failed); err != nil {
			return domain.Completion{}, "", err
		}
		if !retryable {
			if errors.Is(streamErr, context.Canceled) {
				return domain.Completion{}, "", context.Canceled
			}
			return domain.Completion{}, "", domain.NewCodedError(domain.ErrorCompactionProviderFailed, streamErr)
		}
		delay := delays[attempt-1]
		if s.Events != nil {
			if err := appendEvent(ctx, s.Events, run.ID, "context_compaction_retry_scheduled", map[string]any{
				"compactionId": compactionID, "nextAttempt": attempt + 1, "delayMs": delay.Milliseconds(),
			}); err != nil {
				return domain.Completion{}, "", err
			}
		}
		if err := sleep(ctx, delay); err != nil {
			return domain.Completion{}, "", context.Canceled
		}
		if s.Events != nil {
			_ = appendEvent(ctx, s.Events, run.ID, "context_compaction_retry_started", map[string]any{
				"compactionId": compactionID, "attempt": attempt + 1,
			})
		}
	}
}

func (s *Service) fail(ctx context.Context, checkpoint *domain.ContextCompaction, runID string,
	config domain.CompactionPolicyConfig, err error) error {
	status := domain.CompactionFailed
	code := domain.ErrorCodeOf(err)
	if errors.Is(err, context.Canceled) {
		status = domain.CompactionCancelled
		code = domain.ErrorCompactionCancelled
	}
	return s.Repo.Fail(ctx, checkpoint.ID, runID, status, code, err, config)
}

func decodeConfig(snapshot domain.PolicySnapshot) (domain.CompactionPolicyConfig, error) {
	var config domain.CompactionPolicyConfig
	if err := json.Unmarshal(snapshot.Config, &config); err != nil {
		return config, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, err)
	}
	return config, nil
}

func summaryText(completion domain.Completion) string {
	var text strings.Builder
	for _, block := range completion.Content {
		if block.Kind == domain.ContentText {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func appendEvent(ctx context.Context, writer agent.EventWriter, runID, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = writer.Append(ctx, runID, domain.PendingEvent{EventType: eventType, Payload: encoded})
	return err
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
