package compaction

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

const maxMidRunCompactions = 4

type RunHistoryUpdater interface {
	UseRunLocal(string, []domain.ChatMessage, int)
}

type RunCompactor struct {
	Service   *Service
	Run       *domain.AgentRun
	Effective domain.EffectiveRunConfig
	Config    domain.CompactionPolicyConfig
	History   RunHistoryUpdater
}

func NewRunCompactor(service *Service, run *domain.AgentRun,
	effective domain.EffectiveRunConfig, history ...RunHistoryUpdater) (*RunCompactor, error) {
	config, err := decodeConfig(effective.CompactionPolicy)
	if err != nil {
		return nil, err
	}
	compactor := &RunCompactor{Service: service, Run: run, Effective: effective, Config: config}
	if len(history) > 0 {
		compactor.History = history[0]
	}
	return compactor, nil
}

func (c *RunCompactor) CompactRunContext(ctx context.Context,
	request agent.MidRunCompactionRequest) (agent.MidRunCompactionResult, error) {
	unchanged := agent.MidRunCompactionResult{Messages: cloneChat(request.Messages), State: request.Previous}
	if c == nil || c.Service == nil || c.Run == nil || c.Service.RunRepo == nil ||
		c.Service.Calls == nil || c.Service.Providers == nil {
		return unchanged, errors.New("run compactor dependencies are required")
	}
	if c.Config.Mode != domain.CompactionManualAndAuto {
		if request.Reason == agent.MidRunCompactionThreshold {
			return unchanged, nil
		}
		return unchanged, domain.NewCodedError(domain.ErrorContextCompactionRequired,
			errors.New("mid-run compaction is not enabled"))
	}
	plan, err := agent.BuildRunCompactionPlan(request.Messages, request.Generated, request.Previous,
		c.Effective.CompactionPolicy, c.Config, request.Current, c.Effective.CompactionRuntime,
		request.SystemPrompt, request.Tools)
	if err != nil {
		if request.Reason == agent.MidRunCompactionThreshold &&
			domain.ErrorCodeOf(err) == domain.ErrorCompactionNothingToCompact {
			return unchanged, nil
		}
		return c.failOrOpen(request, unchanged, 0, 0, err)
	}
	if request.Reason == agent.MidRunCompactionThreshold &&
		(agent.CompactionTrigger{TriggerLimit: plan.TriggerLimit, MainUsable: plan.MainUsable}).BelowTrigger(plan.TokensBefore) {
		return unchanged, nil
	}
	if request.Previous.Attempts >= maxMidRunCompactions {
		return c.failOrOpen(request, unchanged, plan.TokensBefore, plan.MainUsable,
			errors.New("mid-run compaction limit reached"))
	}

	record, reused, err := c.Service.RunRepo.CreateOrReuse(ctx, store.RunCompactionCreate{
		RunID: request.RunID, PreviousCompactionID: request.Previous.ID,
		Reason: domain.CompactionReason(request.Reason), Iteration: request.Iteration,
		RequestGeneration: request.RequestGeneration, Policy: c.Effective.CompactionPolicy,
		EffectiveConfig: c.Run.EffectiveConfig, SourceDigest: plan.SourceDigest,
		SummaryContractDigest: plan.SummaryContractDigest, CoveredGenerated: plan.CoveredGenerated,
		TokensBefore: plan.TokensBefore,
	})
	if err != nil {
		return unchanged, domain.NewCodedError(domain.ErrorEventPersistence, err)
	}
	if reused {
		return c.completedResult(request, plan, *record)
	}
	if err := c.Service.RunRepo.Start(ctx, request.RunID, record.ID); err != nil {
		return unchanged, domain.NewCodedError(domain.ErrorEventPersistence, err)
	}
	unchanged.State.Attempts++

	provider, err := c.Service.Providers(c.Effective.CompactionRuntime)
	if err != nil {
		wrapped := domain.NewCodedError(domain.ErrorCompactionModelUnavailable, err)
		_ = c.Service.RunRepo.Fail(ctx, request.RunID, record.ID, domain.CompactionFailed,
			domain.ErrorCompactionModelUnavailable, wrapped)
		return c.failOrOpen(request, unchanged, plan.TokensBefore, plan.MainUsable, wrapped)
	}
	completion, callID, callAttempt, err := c.streamRunSummary(ctx, request, record.ID, provider,
		agent.RunSummaryRequest(plan, c.Config, c.Effective.CompactionRuntime, request.SystemPrompt, request.Tools))
	if err != nil {
		code := domain.ErrorCodeOf(err)
		if errors.Is(err, context.Canceled) {
			code = domain.ErrorCompactionCancelled
		}
		_ = c.Service.RunRepo.Fail(ctx, request.RunID, record.ID, terminalCompactionStatus(err), code, err)
		return c.failOrOpen(request, unchanged, plan.TokensBefore, plan.MainUsable, err)
	}
	summary := summaryText(completion)
	validationErr := agent.ValidateCompactionSummary(summary, c.Config.SummaryMaxOutputTokens)
	if completion.StopReason == domain.StopReasonLength {
		validationErr = domain.NewCodedError(domain.ErrorCompactionOutputInvalid,
			errors.New("mid-run summary output was truncated"))
	}
	if validationErr != nil {
		_ = c.Service.Calls.ModelFailed(context.WithoutCancel(ctx), domain.ModelCallFinish{
			ID: callID, RunID: request.RunID, Iteration: request.Iteration, Attempt: callAttempt,
			RequestGeneration: request.RequestGeneration, Purpose: domain.ModelCallContextCompaction,
			ErrorCode: string(domain.ErrorCompactionOutputInvalid), Error: validationErr.Error(), Final: true,
		})
		_ = c.Service.RunRepo.Fail(ctx, request.RunID, record.ID, domain.CompactionFailed,
			domain.ErrorCompactionOutputInvalid, validationErr)
		return c.failOrOpen(request, unchanged, plan.TokensBefore, plan.MainUsable, validationErr)
	}
	if err := c.Service.Calls.ModelCompleted(context.WithoutCancel(ctx), domain.ModelCallFinish{
		ID: callID, RunID: request.RunID, Iteration: request.Iteration, Attempt: callAttempt,
		RequestGeneration: request.RequestGeneration, Purpose: domain.ModelCallContextCompaction,
		ActualModel: completion.ActualModel, StopReason: completion.StopReason, Usage: completion.Usage,
	}); err != nil {
		wrapped := domain.NewCodedError(domain.ErrorEventPersistence, err)
		_ = c.Service.RunRepo.Fail(ctx, request.RunID, record.ID, domain.CompactionFailed,
			domain.ErrorEventPersistence, wrapped)
		return unchanged, wrapped
	}

	state := agent.MidRunCompactionState{ID: record.ID, Summary: summary,
		SourceDigest: plan.SourceDigest, SummaryContractDigest: plan.SummaryContractDigest,
		Count: request.Previous.Count + 1, Attempts: request.Previous.Attempts + 1,
		CoveredGenerated: plan.CoveredGenerated}
	messages := agent.RunCheckpointMessages(state, plan.TailMessages)
	after := agent.EstimateComposition(request.SystemPrompt, request.Tools, messages,
		request.Current.MaxOutputTokens).InputTokens
	reclaimed := plan.TokensBefore - after
	if reclaimed < 0 {
		reclaimed = 0
	}
	ratio := 0.0
	if plan.TokensBefore > 0 {
		ratio = float64(reclaimed) / float64(plan.TokensBefore)
	}
	if ratio < c.Config.IneffectiveReclaimRatio {
		ineffective := domain.NewCodedError(domain.ErrorCompactionOutputInvalid,
			fmt.Errorf("mid-run compaction reclaimed %.3f below required %.3f", ratio, c.Config.IneffectiveReclaimRatio))
		_ = c.Service.RunRepo.Fail(ctx, request.RunID, record.ID, domain.CompactionFailed,
			domain.ErrorCompactionOutputInvalid, ineffective)
		return c.failOrOpen(request, unchanged, plan.TokensBefore, plan.MainUsable, ineffective)
	}
	if err := c.Service.RunRepo.Complete(context.WithoutCancel(ctx), store.RunCompactionCompletion{
		ID: record.ID, RunID: request.RunID, ModelCallID: callID, Summary: summary,
		SummaryDigest: agent.DigestText(summary), EstimatedTokensAfter: after, ReclaimedTokens: reclaimed,
	}); err != nil {
		wrapped := domain.NewCodedError(domain.ErrorEventPersistence, err)
		_ = c.Service.RunRepo.Fail(ctx, request.RunID, record.ID, domain.CompactionFailed,
			domain.ErrorEventPersistence, wrapped)
		return unchanged, wrapped
	}
	if c.History != nil {
		c.History.UseRunLocal(request.RunID, request.Generated, state.CoveredGenerated)
	}
	return agent.MidRunCompactionResult{Messages: messages, State: state, Compacted: true}, nil
}

func (c *RunCompactor) completedResult(request agent.MidRunCompactionRequest, plan agent.RunCompactionPlan,
	record domain.RunContextCompaction) (agent.MidRunCompactionResult, error) {
	if record.Summary == "" || agent.DigestText(record.Summary) != record.SummaryDigest {
		return agent.MidRunCompactionResult{}, domain.NewCodedError(domain.ErrorCompactionCheckpointInvalid,
			errors.New("reused run compaction summary digest is invalid"))
	}
	state := agent.MidRunCompactionState{ID: record.ID, Summary: record.Summary,
		SourceDigest: record.SourceDigest, SummaryContractDigest: record.SummaryContractDigest,
		Count: request.Previous.Count + 1, Attempts: request.Previous.Attempts,
		CoveredGenerated: record.CoveredGenerated}
	if c.History != nil {
		c.History.UseRunLocal(request.RunID, request.Generated, state.CoveredGenerated)
	}
	return agent.MidRunCompactionResult{Messages: agent.RunCheckpointMessages(state, plan.TailMessages),
		State: state, Compacted: true}, nil
}

func (c *RunCompactor) failOrOpen(request agent.MidRunCompactionRequest,
	unchanged agent.MidRunCompactionResult, tokensBefore, mainUsable int, cause error) (agent.MidRunCompactionResult, error) {
	if request.Reason == agent.MidRunCompactionThreshold &&
		(agent.CompactionTrigger{MainUsable: mainUsable}).NoMeaningfulWork(tokensBefore) {
		return unchanged, nil
	}
	return unchanged, domain.NewCodedError(domain.ErrorContextCompactionRequired, cause)
}

func (c *RunCompactor) streamRunSummary(ctx context.Context, request agent.MidRunCompactionRequest,
	recordID string, provider llm.Provider, completionRequest domain.CompletionRequest) (domain.Completion, string, int, error) {
	delays := c.Service.Retry.Delays
	if delays == nil {
		delays = []time.Duration{time.Second, 5 * time.Second, 10 * time.Second}
	}
	sleep := c.Service.Retry.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	for attempt := 1; ; attempt++ {
		callID := uuid.NewString()
		started := domain.ModelCallStart{ID: callID, RunID: request.RunID,
			Iteration: request.Iteration, Attempt: attempt, RequestGeneration: request.RequestGeneration,
			Purpose: domain.ModelCallContextCompaction, ProviderProfileID: c.Effective.CompactionRuntime.ProviderProfileID,
			ModelProfileID: c.Effective.CompactionRuntime.ModelProfileID, RouteReason: "mid_run_context_compaction",
			RequestedConfig: c.Run.RequestedConfig, EffectiveConfig: c.Run.EffectiveConfig}
		if err := c.Service.Calls.ModelStarted(ctx, started); err != nil {
			return domain.Completion{}, "", 0, err
		}
		completion, streamErr := provider.Stream(ctx, completionRequest, llm.NopSink{})
		if streamErr == nil {
			return completion, callID, attempt, nil
		}
		if ctx.Err() != nil || errors.Is(streamErr, llm.ErrCancelled) {
			streamErr = context.Canceled
		}
		retryable := ctx.Err() == nil && llm.IsRetryable(streamErr) && attempt <= len(delays)
		code := domain.ErrorCompactionProviderFailed
		if errors.Is(streamErr, context.Canceled) {
			code = domain.ErrorCompactionCancelled
		}
		if err := c.Service.Calls.ModelFailed(context.WithoutCancel(ctx), domain.ModelCallFinish{
			ID: callID, RunID: request.RunID, Iteration: request.Iteration, Attempt: attempt,
			RequestGeneration: request.RequestGeneration, Purpose: domain.ModelCallContextCompaction,
			ErrorCode: string(code), Error: streamErr.Error(), Retryable: retryable, Final: !retryable,
		}); err != nil {
			return domain.Completion{}, "", 0, err
		}
		if !retryable {
			if errors.Is(streamErr, context.Canceled) {
				return domain.Completion{}, "", 0, context.Canceled
			}
			return domain.Completion{}, "", 0, domain.NewCodedError(domain.ErrorCompactionProviderFailed, streamErr)
		}
		delay := delays[attempt-1]
		if c.Service.Events != nil {
			if err := appendEvent(ctx, c.Service.Events, request.RunID, "run_context_compaction_retry_scheduled", map[string]any{
				"compactionId": recordID, "iteration": request.Iteration, "nextAttempt": attempt + 1,
				"delayMs": delay.Milliseconds(),
			}); err != nil {
				return domain.Completion{}, "", 0, err
			}
		}
		if err := sleep(ctx, delay); err != nil {
			return domain.Completion{}, "", 0, context.Canceled
		}
	}
}

func terminalCompactionStatus(err error) domain.CompactionStatus {
	if errors.Is(err, context.Canceled) {
		return domain.CompactionCancelled
	}
	return domain.CompactionFailed
}

func cloneChat(messages []domain.ChatMessage) []domain.ChatMessage {
	return append([]domain.ChatMessage(nil), messages...)
}
