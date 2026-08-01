package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
)

var (
	ErrMaxIterations = errors.New("agent reached maximum iterations")
	ErrStuckToolLoop = errors.New("agent repeated an identical tool call without progress")
)

type EventWriter interface {
	Append(context.Context, string, ...domain.PendingEvent) ([]domain.RunEvent, error)
}

type CallRecorder interface {
	ModelStarted(context.Context, domain.ModelCallStart) error
	ModelUsage(context.Context, domain.ModelCallFinish) error
	ModelCompleted(context.Context, domain.ModelCallFinish) error
	ModelFailed(context.Context, domain.ModelCallFinish) error
	ToolStarted(context.Context, domain.ToolCallStart) error
	ToolCompleted(context.Context, domain.ToolCallFinish) error
	ToolFailed(context.Context, domain.ToolCallFinish) error
	ToolSkipped(context.Context, domain.ToolCallFinish) error
	ModelRouteSelected(context.Context, string, int, domain.ModelRuntimeSnapshot, string) error
}

type QueuedInputSource interface {
	Drain(context.Context, string, domain.QueuedInputKind, domain.QueueMode) ([]domain.QueuedInput, error)
}

type RetryPolicy struct {
	Delays []time.Duration
	Sleep  func(context.Context, time.Duration) error
}

type Loop struct {
	Provider           llm.Provider
	Tools              domain.ToolRunner
	Events             EventWriter
	Recorder           CallRecorder
	QueuedInputs       QueuedInputSource
	SteeringMode       domain.QueueMode
	FollowUpMode       domain.QueueMode
	Retry              RetryPolicy
	ToolExecution      domain.ToolExecutionConfig
	ToolPolicy         ToolPolicy
	ToolPolicySnapshot domain.PolicySnapshot
	WorkspaceID        string
	TurnPlanner        TurnPlanner
	ModelRouter        ModelRouter
	MidRunCompactor    MidRunCompactor
	VisionResolver     VisionResolver
	ImageDescriptions  ImageDescriptionCache
	Reminders          *ReminderRegistry
	TodoStore          *domain.TodoStore
	MaxIterations      int
	ContextTokens      int
	MaxOutput          int
}

type RunInput struct {
	RunID             string
	Model             string
	ProviderProfileID string
	ModelProfileID    string
	RequestedConfig   json.RawMessage
	EffectiveConfig   json.RawMessage
	InitialRuntime    domain.ModelRuntimeSnapshot
	Routing           domain.FrozenRoutingConfig
	VisionPolicy      domain.PolicySnapshot
	SystemPrompt      string
	History           []domain.ChatMessage
	OverflowRecovery  func(context.Context) ([]domain.ChatMessage, error)
	Resume            *ResumeState
	Approval          *ApprovalResolution
}

type RunResult struct {
	Messages   []domain.ChatMessage
	Generated  []domain.ChatMessage
	Completion domain.Completion
	Iterations int
}

func (l *Loop) Run(ctx context.Context, input RunInput) (RunResult, error) {
	if (l.Provider == nil && l.ModelRouter == nil) || l.Tools == nil || l.Events == nil {
		return RunResult{}, fmt.Errorf("agent loop dependencies are required")
	}
	maxIterations := l.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 32
	}
	messages := append([]domain.ChatMessage(nil), input.History...)
	generated := make([]domain.ChatMessage, 0)
	guard := &stuckGuard{}
	var final domain.Completion
	steeringMode := queueModeOrDefault(l.SteeringMode)
	followUpMode := queueModeOrDefault(l.FollowUpMode)
	truncationRecoveries := 0
	current := input.InitialRuntime
	if current.ModelProfileID == "" {
		current = domain.ModelRuntimeSnapshot{ProviderProfileID: input.ProviderProfileID,
			ModelProfileID: input.ModelProfileID, APIModel: input.Model,
			ContextTokens: l.ContextTokens, MaxOutputTokens: l.MaxOutput}
	}
	routing := input.Routing
	if len(routing.Candidates) == 0 {
		routing.Candidates = []domain.ModelRuntimeSnapshot{current}
		routing.Pinned = true
	}
	pendingPlan := TurnPlan{ModelProfileID: current.ModelProfileID, Reason: "initial_model"}
	var midRunState MidRunCompactionState

	var initialSteering []domain.ChatMessage
	var err error
	requestGeneration := 0
	startIteration := 1
	if input.Resume != nil && l.TodoStore != nil {
		l.TodoStore.Set(input.Resume.Todos)
	}
	if input.Resume == nil {
		initialSteering, err = l.drainQueuedInputs(ctx, input.RunID, domain.QueuedInputSteer, steeringMode)
		if err != nil {
			return runResult(messages, generated, final, 0), classifyAgentError(err)
		}
		messages = append(messages, initialSteering...)
		generated = append(generated, initialSteering...)
	} else {
		resume := input.Resume
		if resume.Version < 1 || resume.Version > ResumeStateVersion || resume.Iteration < 1 ||
			resume.Iteration > maxIterations || input.Approval == nil {
			return runResult(messages, generated, final, 0), domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
				errors.New("approval resume checkpoint is invalid"))
		}
		messages = cloneMessages(resume.Messages)
		generated = cloneMessages(resume.Generated)
		final = resume.Completion
		current = resume.Current
		routing = resume.Routing
		requestGeneration = resume.RequestGeneration
		truncationRecoveries = resume.TruncationRecoveries
		initialSteering = cloneMessages(resume.InitialSteering)
		input.SystemPrompt = resume.SystemPrompt
		midRunState = resume.MidRunCompaction
		guard.Restore(resume.StuckSignatures)

		toolResults, stopAfterToolBatch, err := l.executeToolBatchWithPolicy(ctx, input.RunID,
			resume.Iteration, resume.Completion.ToolCalls, *input.Approval)
		if err != nil {
			return runResult(messages, generated, final, resume.Iteration), err
		}
		for index := range toolResults {
			result := toolResults[index]
			toolMessage := domain.ChatMessage{Role: domain.RoleTool, Content: []domain.ContentBlock{{
				Kind: domain.ContentToolResult, ToolResult: &result,
			}}}
			messages = append(messages, toolMessage)
			generated = append(generated, toolMessage)
		}
		var continueTurn bool
		messages, generated, pendingPlan, continueTurn, err = l.finishCompletedIteration(ctx, input,
			resume.Iteration, resume.Completion, current, routing, messages, generated, toolResults,
			stopAfterToolBatch, steeringMode, followUpMode)
		if err != nil {
			return runResult(messages, generated, final, resume.Iteration), err
		}
		if !continueTurn {
			return runResult(messages, generated, final, resume.Iteration), nil
		}
		startIteration = resume.Iteration + 1
	}

	for iteration := startIteration; iteration <= maxIterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return runResult(messages, generated, final, iteration-1), context.Canceled
		}
		var completion domain.Completion
		requestCompacted := false
		for {
			requestMessages := messages
			visionResolution := VisionResolution{RewrittenMessages: messages}
			if l.VisionResolver != nil {
				visionResolution, err = l.VisionResolver.ResolveImages(ctx, VisionContext{RunID: input.RunID,
					Iteration: iteration, Messages: messages, Current: current, Policy: input.VisionPolicy})
				if err != nil {
					return runResult(messages, generated, final, iteration-1), err
				}
				requestMessages = visionResolution.RewrittenMessages
			}
			runtime := TurnRuntime{Provider: l.Provider, Snapshot: current, EffectiveConfig: input.EffectiveConfig}
			if l.ModelRouter != nil {
				runtime, err = l.ModelRouter.ResolveTurn(ctx, routing, pendingPlan, visionResolution.Constraint)
				if err != nil {
					return runResult(messages, generated, final, iteration-1), classifyRoutingError(err)
				}
				current = runtime.Snapshot
				if err := l.recordModelRouteSelected(ctx, input.RunID, iteration, current, runtime.RouteReason); err != nil {
					return runResult(messages, generated, final, iteration-1), err
				}
			}
			if l.MidRunCompactor != nil && iteration > 1 && !requestCompacted {
				compacted, compactErr := l.MidRunCompactor.CompactRunContext(ctx, MidRunCompactionRequest{
					RunID: input.RunID, Iteration: iteration, RequestGeneration: requestGeneration + 1,
					Reason: MidRunCompactionThreshold, SystemPrompt: input.SystemPrompt,
					Messages: cloneMessages(messages), Generated: cloneMessages(generated), Current: current,
					Tools: l.Tools.Definitions(), Previous: midRunState,
				})
				if compactErr != nil {
					return runResult(messages, generated, final, iteration-1), compactErr
				}
				midRunState = compacted.State
				if compacted.Compacted {
					messages = cloneMessages(compacted.Messages)
					requestGeneration++
					requestCompacted = true
					requestMessages = messages
					visionResolution = VisionResolution{RewrittenMessages: messages}
					if l.VisionResolver != nil {
						visionResolution, err = l.VisionResolver.ResolveImages(ctx, VisionContext{RunID: input.RunID,
							Iteration: iteration, Messages: messages, Current: current, Policy: input.VisionPolicy})
						if err != nil {
							return runResult(messages, generated, final, iteration-1), err
						}
						requestMessages = visionResolution.RewrittenMessages
					}
				}
			}
			if len(visionResolution.DescriptorRequests) > 0 {
				requestMessages, err = l.describeImages(ctx, input, iteration, routing, requestMessages, visionResolution.DescriptorRequests)
				if err != nil {
					return runResult(messages, generated, final, iteration-1), err
				}
			}
			contextTokens := current.ContextTokens
			if contextTokens <= 0 {
				contextTokens = l.ContextTokens
			}
			maxOutput := current.MaxOutputTokens
			if maxOutput <= 0 {
				maxOutput = l.MaxOutput
			}
			model := current.APIModel
			if model == "" {
				model = input.Model
			}
			toolDefinitions := l.Tools.Definitions()
			preparedMessages := l.prepareRequestMessages(ctx, input.SystemPrompt, requestMessages,
				current, iteration, toolDefinitions)
			request := domain.CompletionRequest{
				Model: model, Messages: preparedMessages,
				Tools: toolDefinitions, MaxTokens: maxOutput,
			}
			completion, err = l.streamWithRetry(ctx, input, iteration, request, runtime,
				modelCallOptions{Purpose: domain.ModelCallAgentTurn, RequestGeneration: requestGeneration})
			if err == nil {
				break
			}
			if domain.ErrorCodeOf(err) != domain.ErrorContextOverflow {
				return runResult(messages, generated, final, iteration), err
			}
			if iteration != 1 || hasGeneratedAssistantOrTool(generated) {
				if requestCompacted {
					return runResult(messages, generated, final, iteration),
						domain.NewCodedError(domain.ErrorContextOverflowAfterCompaction, err)
				}
				if l.MidRunCompactor == nil {
					return runResult(messages, generated, final, iteration),
						domain.NewCodedError(domain.ErrorContextOverflowInRun, err)
				}
				compacted, compactErr := l.MidRunCompactor.CompactRunContext(ctx, MidRunCompactionRequest{
					RunID: input.RunID, Iteration: iteration, RequestGeneration: requestGeneration + 1,
					Reason: MidRunCompactionOverflow, SystemPrompt: input.SystemPrompt,
					Messages: cloneMessages(messages), Generated: cloneMessages(generated), Current: current,
					Tools: l.Tools.Definitions(), Previous: midRunState,
				})
				if compactErr != nil {
					return runResult(messages, generated, final, iteration), compactErr
				}
				if !compacted.Compacted {
					return runResult(messages, generated, final, iteration),
						domain.NewCodedError(domain.ErrorContextOverflowInRun, err)
				}
				messages = cloneMessages(compacted.Messages)
				midRunState = compacted.State
				requestGeneration++
				requestCompacted = true
				continue
			}
			if requestGeneration > 0 {
				return runResult(messages, generated, final, iteration),
					domain.NewCodedError(domain.ErrorContextOverflowAfterCompaction, err)
			}
			if input.OverflowRecovery == nil {
				return runResult(messages, generated, final, iteration),
					domain.NewCodedError(domain.ErrorContextCompactionRequired, err)
			}
			if eventErr := l.appendEvent(ctx, input.RunID, "context_overflow_recovery_started", map[string]any{
				"iteration": iteration, "requestGeneration": requestGeneration,
			}); eventErr != nil {
				return runResult(messages, generated, final, iteration), eventErr
			}
			recovered, recoveryErr := input.OverflowRecovery(ctx)
			if recoveryErr != nil {
				return runResult(messages, generated, final, iteration), recoveryErr
			}
			messages = append(recovered, initialSteering...)
			requestGeneration++
			if eventErr := l.appendEvent(ctx, input.RunID, "context_overflow_recovery_completed", map[string]any{
				"iteration": iteration, "requestGeneration": requestGeneration,
			}); eventErr != nil {
				return runResult(messages, generated, final, iteration), eventErr
			}
		}
		final = completion
		if completion.StopReason == domain.StopReasonLength {
			sanitizeTruncatedToolCalls(completion.ToolCalls)
		} else if err := validateToolCalls(completion.ToolCalls); err != nil {
			return runResult(messages, generated, completion, iteration),
				domain.NewCodedError(domain.ErrorModelProtocol, err)
		}

		assistant := domain.ChatMessage{Role: domain.RoleAssistant, Content: append([]domain.ContentBlock(nil), completion.Content...)}
		for index := range completion.ToolCalls {
			call := completion.ToolCalls[index]
			assistant.Content = append(assistant.Content, domain.ContentBlock{Kind: domain.ContentToolCall, ToolCall: &call})
		}
		messages = append(messages, assistant)
		generated = append(generated, assistant)

		if completion.StopReason == domain.StopReasonLength {
			if err := l.appendEvent(ctx, input.RunID, "output_truncated", map[string]any{
				"iteration": iteration, "partialToolCallCount": len(completion.ToolCalls),
			}); err != nil {
				return runResult(messages, generated, completion, iteration), err
			}
			if len(completion.ToolCalls) == 0 {
				return runResult(messages, generated, completion, iteration),
					domain.NewCodedError(domain.ErrorModelOutputTruncated, errors.New("model text output was truncated"))
			}
			for _, call := range completion.ToolCalls {
				if call.ID == "" || call.Name == "" {
					return runResult(messages, generated, completion, iteration),
						domain.NewCodedError(domain.ErrorModelProtocol, errors.New("truncated tool call is missing id or name"))
				}
			}
			for index, call := range completion.ToolCalls {
				result := domain.ToolResult{
					ToolCallID: call.ID, ToolName: call.Name, IsError: true,
					Content: "Tool not executed because the model output was truncated. Re-issue the call with complete arguments.",
				}
				if err := l.recordToolSkipped(ctx, input.RunID, iteration, index, call, result, "output_truncated"); err != nil {
					return runResult(messages, generated, completion, iteration), err
				}
				toolMessage := domain.ChatMessage{Role: domain.RoleTool, Content: []domain.ContentBlock{{
					Kind: domain.ContentToolResult, ToolResult: &result,
				}}}
				messages = append(messages, toolMessage)
				generated = append(generated, toolMessage)
			}
			truncationRecoveries++
			if truncationRecoveries >= 2 {
				return runResult(messages, generated, completion, iteration),
					domain.NewCodedError(domain.ErrorModelOutputTruncated, errors.New("model output was truncated in two consecutive recovery turns"))
			}
			continue
		}
		truncationRecoveries = 0

		hasToolCalls := len(completion.ToolCalls) > 0
		stopAfterToolBatch := false
		var toolResults []domain.ToolResult
		if hasToolCalls && guard.Repeated(completion.ToolCalls) {
			if err := l.appendEvent(context.WithoutCancel(ctx), input.RunID, "stuck_tool_loop", map[string]any{"iteration": iteration}); err != nil {
				return runResult(messages, generated, completion, iteration), err
			}
			return runResult(messages, generated, completion, iteration),
				domain.NewCodedError(domain.ErrorStuckToolLoop, ErrStuckToolLoop)
		}

		if hasToolCalls {
			toolResults, stopAfterToolBatch, err = l.executeToolBatchWithPolicy(ctx, input.RunID, iteration, completion.ToolCalls)
			if err != nil {
				var approvalRequired *ApprovalRequiredError
				if errors.As(err, &approvalRequired) {
					approvalRequired.State = ResumeState{Version: ResumeStateVersion, Iteration: iteration,
						Messages: cloneMessages(messages), Generated: cloneMessages(generated), Completion: completion,
						Current: current, Routing: routing, RequestGeneration: requestGeneration,
						TruncationRecoveries: truncationRecoveries, StuckSignatures: guard.Snapshot(),
						InitialSteering: cloneMessages(initialSteering), SystemPrompt: input.SystemPrompt,
						MidRunCompaction: midRunState, Todos: l.todoSnapshot()}
				}
				return runResult(messages, generated, completion, iteration), err
			}
			for index := range toolResults {
				result := toolResults[index]
				toolMessage := domain.ChatMessage{Role: domain.RoleTool, Content: []domain.ContentBlock{{
					Kind: domain.ContentToolResult, ToolResult: &result,
				}}}
				messages = append(messages, toolMessage)
				generated = append(generated, toolMessage)
			}
		}

		var continueTurn bool
		messages, generated, pendingPlan, continueTurn, err = l.finishCompletedIteration(ctx, input,
			iteration, completion, current, routing, messages, generated, toolResults, stopAfterToolBatch,
			steeringMode, followUpMode)
		if err != nil {
			return runResult(messages, generated, completion, iteration), err
		}
		if !continueTurn {
			return runResult(messages, generated, completion, iteration), nil
		}
		continue
	}
	return runResult(messages, generated, final, maxIterations),
		domain.NewCodedError(domain.ErrorMaxIterations, ErrMaxIterations)
}

func (l *Loop) finishCompletedIteration(ctx context.Context, input RunInput, iteration int,
	completion domain.Completion, current domain.ModelRuntimeSnapshot, routing domain.FrozenRoutingConfig,
	messages, generated []domain.ChatMessage, toolResults []domain.ToolResult, stopAfterToolBatch bool,
	steeringMode, followUpMode domain.QueueMode) ([]domain.ChatMessage, []domain.ChatMessage, TurnPlan, bool, error) {
	steering, err := l.drainQueuedInputs(ctx, input.RunID, domain.QueuedInputSteer, steeringMode)
	if err != nil {
		return messages, generated, TurnPlan{}, false, classifyAgentError(err)
	}
	if len(steering) > 0 {
		messages = append(messages, steering...)
		generated = append(generated, steering...)
	}
	turnContext := TurnContext{RunID: input.RunID, Iteration: iteration,
		Messages: append([]domain.ChatMessage(nil), messages...), Completion: completion,
		ToolResults: append([]domain.ToolResult(nil), toolResults...), Current: current, Routing: routing,
		EstimatedTokens: EstimateTokens(messages), StopAfterToolBatch: stopAfterToolBatch}
	stopDecision := StopDecision{Stop: stopAfterToolBatch, Code: "tool_policy_stop_after_batch",
		Reason: "tool policy requested stop after batch"}
	if l.TurnPlanner != nil {
		plannedStop, plannerErr := l.TurnPlanner.ShouldStopAfterTurn(ctx, turnContext)
		if plannerErr != nil {
			return messages, generated, TurnPlan{}, false,
				domain.NewCodedError(domain.ErrorTurnPolicyFailed, plannerErr)
		}
		if plannedStop.Stop {
			stopDecision = plannedStop
		}
	}
	continueTurn := len(steering) > 0 || (len(completion.ToolCalls) > 0 && !stopDecision.Stop)
	if !continueTurn {
		followUps, drainErr := l.drainQueuedInputs(ctx, input.RunID, domain.QueuedInputFollowUp, followUpMode)
		if drainErr != nil {
			return messages, generated, TurnPlan{}, false, classifyAgentError(drainErr)
		}
		if len(followUps) > 0 {
			messages = append(messages, followUps...)
			generated = append(generated, followUps...)
			turnContext.Messages = append([]domain.ChatMessage(nil), messages...)
			turnContext.EstimatedTokens = EstimateTokens(messages)
			continueTurn = true
		}
	}
	if !continueTurn {
		if stopDecision.Stop {
			if err := l.appendEvent(ctx, input.RunID, "turn_stop_requested", map[string]any{
				"iteration": iteration, "code": stopDecision.Code, "reason": stopDecision.Reason,
			}); err != nil {
				return messages, generated, TurnPlan{}, false, err
			}
		}
		return messages, generated, TurnPlan{}, false, nil
	}
	pendingPlan := TurnPlan{ModelProfileID: current.ModelProfileID, Reason: "continue_current_model"}
	if l.TurnPlanner != nil {
		pendingPlan, err = l.TurnPlanner.PrepareNextTurn(ctx, turnContext)
		if err != nil {
			return messages, generated, TurnPlan{}, false,
				domain.NewCodedError(domain.ErrorTurnPolicyFailed, err)
		}
	}
	return messages, generated, pendingPlan, true, nil
}

type modelCallOptions struct {
	Purpose           domain.ModelCallPurpose
	SourceArtifactID  string
	RequestGeneration int
	CompactionID      string
}

func (l *Loop) describeImages(ctx context.Context, input RunInput, iteration int, routing domain.FrozenRoutingConfig,
	messages []domain.ChatMessage, requests []ImageDescriptionRequest) ([]domain.ChatMessage, error) {
	if l.ModelRouter == nil {
		return nil, domain.NewCodedError(domain.ErrorVisionFallbackFailed, errors.New("model router is required for image description"))
	}
	rewritten := cloneMessages(messages)
	seen := make(map[string]struct{}, len(requests))
	for _, descriptor := range requests {
		if _, exists := seen[descriptor.Image.ArtifactID]; exists {
			continue
		}
		seen[descriptor.Image.ArtifactID] = struct{}{}
		if l.ImageDescriptions != nil {
			cached, found, cacheErr := l.ImageDescriptions.Get(ctx, descriptor.Image.SHA256, descriptor.ModelProfileID, descriptor.PromptVersion)
			if cacheErr != nil {
				return nil, domain.NewCodedError(domain.ErrorVisionFallbackFailed, cacheErr)
			}
			if found {
				rewritten = replaceImageWithDescription(rewritten, descriptor, cached, descriptor.ModelProfileID)
				continue
			}
		}
		runtime, err := l.ModelRouter.ResolveTurn(ctx, routing,
			TurnPlan{ModelProfileID: descriptor.ModelProfileID, Reason: "vision_description"},
			RoutingConstraint{RequiresVision: true})
		if err != nil {
			return nil, domain.NewCodedError(domain.ErrorVisionFallbackFailed, err)
		}
		if err := l.recordModelRouteSelected(ctx, input.RunID, iteration, runtime.Snapshot, "vision_description"); err != nil {
			return nil, err
		}
		prompt := "Describe this image accurately for another model. Include visible text and relevant spatial relationships."
		request := domain.CompletionRequest{Model: runtime.Snapshot.APIModel, MaxTokens: runtime.Snapshot.MaxOutputTokens,
			Messages: []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{
				{Kind: domain.ContentText, Text: prompt}, {Kind: domain.ContentImage, Image: &descriptor.Image},
			}}}}
		completion, err := l.streamWithRetry(ctx, input, iteration, request, runtime, modelCallOptions{
			Purpose: domain.ModelCallImageDescription, SourceArtifactID: descriptor.Image.ArtifactID})
		if err != nil {
			return nil, domain.NewCodedError(domain.ErrorVisionFallbackFailed, err)
		}
		description := completionText(completion)
		if description == "" {
			return nil, domain.NewCodedError(domain.ErrorVisionFallbackFailed, errors.New("image descriptor returned no text"))
		}
		if l.ImageDescriptions != nil {
			if err := l.ImageDescriptions.Put(ctx, descriptor.Image, runtime.Snapshot.ModelProfileID,
				runtime.Snapshot.APIModel, descriptor.PromptVersion, description, completion.CallID); err != nil {
				return nil, domain.NewCodedError(domain.ErrorVisionFallbackFailed, err)
			}
		}
		rewritten = replaceImageWithDescription(rewritten, descriptor, description, runtime.Snapshot.ModelProfileID)
	}
	return rewritten, nil
}

func replaceImageWithDescription(messages []domain.ChatMessage, descriptor ImageDescriptionRequest, description, modelID string) []domain.ChatMessage {
	for messageIndex := range messages {
		for blockIndex := range messages[messageIndex].Content {
			block := &messages[messageIndex].Content[blockIndex]
			if block.Kind == domain.ContentImage && block.Image != nil && block.Image.ArtifactID == descriptor.Image.ArtifactID {
				block.Kind = domain.ContentImageDescription
				block.Image = nil
				block.ImageDescription = &domain.DerivedImageDescription{ArtifactID: descriptor.Image.ArtifactID,
					Text: description, ModelID: modelID, PromptVersion: descriptor.PromptVersion}
			}
		}
	}
	return messages
}

func completionText(completion domain.Completion) string {
	var text strings.Builder
	for _, block := range completion.Content {
		if block.Kind == domain.ContentText {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func (l *Loop) streamWithRetry(ctx context.Context, input RunInput, iteration int, request domain.CompletionRequest, runtime TurnRuntime, options ...modelCallOptions) (domain.Completion, error) {
	delays := l.Retry.Delays
	if delays == nil {
		delays = []time.Duration{time.Second, 5 * time.Second, 10 * time.Second}
	}
	sleep := l.Retry.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	callOptions := modelCallOptions{Purpose: domain.ModelCallAgentTurn}
	if len(options) > 0 {
		callOptions = options[0]
		if callOptions.Purpose == "" {
			callOptions.Purpose = domain.ModelCallAgentTurn
		}
	}
	provider := runtime.Provider
	if provider == nil {
		provider = l.Provider
	}
	providerID := runtime.Snapshot.ProviderProfileID
	if providerID == "" {
		providerID = input.ProviderProfileID
	}
	modelID := runtime.Snapshot.ModelProfileID
	if modelID == "" {
		modelID = input.ModelProfileID
	}
	effectiveConfig := runtime.EffectiveConfig
	if len(effectiveConfig) == 0 {
		effectiveConfig = input.EffectiveConfig
	}
	for attempt := 1; ; attempt++ {
		callID := uuid.NewString()
		started := domain.ModelCallStart{
			ID: callID, RunID: input.RunID, Iteration: iteration, Attempt: attempt,
			RequestGeneration: callOptions.RequestGeneration, Purpose: callOptions.Purpose,
			SourceArtifactID: callOptions.SourceArtifactID, CompactionID: callOptions.CompactionID,
			ProviderProfileID: providerID, ModelProfileID: modelID,
			RouteReason: runtime.RouteReason, RequestedConfig: input.RequestedConfig, EffectiveConfig: effectiveConfig,
		}
		if err := l.recordModelStarted(ctx, started); err != nil {
			return domain.Completion{}, err
		}

		baseSink := &eventSink{ctx: ctx, loop: l, runID: input.RunID, iteration: iteration,
			attempt: attempt, requestGeneration: callOptions.RequestGeneration, callID: callID,
			purpose: callOptions.Purpose, sourceArtifactID: callOptions.SourceArtifactID}
		sink := &attemptSink{inner: baseSink}
		completion, streamErr := provider.Stream(ctx, request, sink)
		if streamErr == nil {
			finished := domain.ModelCallFinish{ID: callID, RunID: input.RunID, Iteration: iteration,
				Attempt: attempt, RequestGeneration: callOptions.RequestGeneration, Purpose: callOptions.Purpose,
				SourceArtifactID: callOptions.SourceArtifactID, CompactionID: callOptions.CompactionID,
				ActualModel: completion.ActualModel, StopReason: completion.StopReason,
				Usage: completion.Usage,
			}
			if err := l.recordModelCompleted(ctx, finished); err != nil {
				return domain.Completion{}, err
			}
			completion.CallID = callID
			return completion, nil
		}

		if ctx.Err() != nil || errors.Is(streamErr, llm.ErrCancelled) {
			streamErr = context.Canceled
		}
		if sink.sinkErr != nil {
			failed := domain.ModelCallFinish{ID: callID, RunID: input.RunID, Iteration: iteration,
				Attempt: attempt, RequestGeneration: callOptions.RequestGeneration, Purpose: callOptions.Purpose,
				SourceArtifactID: callOptions.SourceArtifactID, CompactionID: callOptions.CompactionID,
				ErrorCode: string(domain.ErrorEventPersistence),
				Error:     sink.sinkErr.Error(), Final: true,
			}
			if recordErr := l.recordModelFailed(context.WithoutCancel(ctx), failed); recordErr != nil {
				return domain.Completion{}, errors.Join(sink.sinkErr, recordErr)
			}
			return domain.Completion{}, sink.sinkErr
		}
		retryable := ctx.Err() == nil && !sink.committed && llm.IsRetryable(streamErr) && attempt <= len(delays)
		code := modelErrorCode(streamErr)
		failed := domain.ModelCallFinish{ID: callID, RunID: input.RunID, Iteration: iteration,
			Attempt: attempt, RequestGeneration: callOptions.RequestGeneration, Purpose: callOptions.Purpose,
			SourceArtifactID: callOptions.SourceArtifactID, CompactionID: callOptions.CompactionID,
			ErrorCode: string(code), Error: streamErr.Error(), HTTPStatus: modelHTTPStatus(streamErr),
			Retryable: retryable, Final: !retryable,
		}
		if err := l.recordModelFailed(context.WithoutCancel(ctx), failed); err != nil {
			return domain.Completion{}, err
		}
		if !retryable {
			return domain.Completion{}, domain.NewCodedError(code, streamErr)
		}
		delay := delays[attempt-1]
		if err := l.appendEvent(ctx, input.RunID, "model_call_retry_scheduled", map[string]any{
			"iteration": iteration, "requestGeneration": callOptions.RequestGeneration,
			"nextAttempt": attempt + 1, "delayMs": delay.Milliseconds(),
		}); err != nil {
			return domain.Completion{}, err
		}
		if err := sleep(ctx, delay); err != nil {
			return domain.Completion{}, context.Canceled
		}
	}
}

func (l *Loop) drainQueuedInputs(ctx context.Context, runID string, kind domain.QueuedInputKind, mode domain.QueueMode) ([]domain.ChatMessage, error) {
	if l.QueuedInputs == nil {
		return nil, nil
	}
	items, err := l.QueuedInputs.Drain(ctx, runID, kind, mode)
	if err != nil {
		return nil, err
	}
	messages := make([]domain.ChatMessage, 0, len(items))
	for _, item := range items {
		messages = append(messages, domain.ChatMessage{
			Role:    domain.RoleUser,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: item.Text}},
		})
	}
	return messages, nil
}

func queueModeOrDefault(mode domain.QueueMode) domain.QueueMode {
	if mode == domain.QueueAll {
		return domain.QueueAll
	}
	return domain.QueueOneAtATime
}

func sanitizeTruncatedToolCalls(calls []domain.ToolCall) {
	for index := range calls {
		calls[index].Partial = true
		if !json.Valid(calls[index].Arguments) {
			if calls[index].ArgumentsFragment == "" {
				calls[index].ArgumentsFragment = string(calls[index].Arguments)
			}
			calls[index].Arguments = json.RawMessage(`{}`)
		}
		if len(calls[index].Arguments) == 0 {
			calls[index].Arguments = json.RawMessage(`{}`)
		}
	}
}

func validateToolCalls(calls []domain.ToolCall) error {
	ids := make(map[string]struct{}, len(calls))
	for index, call := range calls {
		if call.ID == "" || call.Name == "" {
			return fmt.Errorf("tool call %d is missing id or name", index)
		}
		if !json.Valid(call.Arguments) {
			return fmt.Errorf("tool call %d has invalid JSON arguments", index)
		}
		if _, duplicate := ids[call.ID]; duplicate {
			return fmt.Errorf("tool call %d repeats id %q", index, call.ID)
		}
		ids[call.ID] = struct{}{}
	}
	return nil
}

func hasGeneratedAssistantOrTool(messages []domain.ChatMessage) bool {
	for _, message := range messages {
		if message.Role == domain.RoleAssistant || message.Role == domain.RoleTool {
			return true
		}
	}
	return false
}

func runResult(messages, generated []domain.ChatMessage, completion domain.Completion, iterations int) RunResult {
	return RunResult{Messages: messages, Generated: generated, Completion: completion, Iterations: iterations}
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

func modelErrorCode(err error) domain.ErrorCode {
	if errors.Is(err, context.Canceled) || errors.Is(err, llm.ErrCancelled) {
		return domain.ErrorRunCancelled
	}
	if llm.IsContextOverflow(err) {
		return domain.ErrorContextOverflow
	}
	var protocol *llm.ProtocolError
	if errors.As(err, &protocol) {
		return domain.ErrorModelProtocol
	}
	var provider *llm.ProviderError
	if errors.As(err, &provider) {
		switch llm.ClassifyProviderFailure(err).Category {
		case domain.ProviderFailureAuthentication:
			return domain.ErrorProviderAuthentication
		case domain.ProviderFailureModelNotFound:
			return domain.ErrorProviderModelNotFound
		case domain.ProviderFailureRateLimited:
			return domain.ErrorProviderRateLimited
		case domain.ProviderFailureTimeout:
			return domain.ErrorProviderTimeout
		case domain.ProviderFailureRequestRejected:
			return domain.ErrorProviderRequestRejected
		default:
			return domain.ErrorProviderUnavailable
		}
	}
	if errors.Is(err, llm.ErrIncompleteStream) {
		return domain.ErrorModelStreamInterrupted
	}
	return domain.ErrorModelStreamInterrupted
}

func modelHTTPStatus(err error) int {
	var provider *llm.ProviderError
	if errors.As(err, &provider) {
		return provider.StatusCode
	}
	return 0
}

func classifyRoutingError(err error) error {
	var coded *domain.CodedError
	if errors.As(err, &coded) {
		return err
	}
	return domain.NewCodedError(domain.ErrorModelRouteFailed, err)
}

func classifyAgentError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	var coded *domain.CodedError
	if errors.As(err, &coded) {
		return err
	}
	return domain.NewCodedError(domain.ErrorEventPersistence, err)
}

type attemptSink struct {
	inner     llm.StreamSink
	committed bool
	sinkErr   error
}

func (s *attemptSink) TextDelta(value string) error {
	return s.commit(func() error { return s.inner.TextDelta(value) })
}
func (s *attemptSink) ThinkingDelta(value string) error {
	return s.commit(func() error { return s.inner.ThinkingDelta(value) })
}
func (s *attemptSink) ToolCallDelta(value llm.ToolCallDelta) error {
	return s.commit(func() error { return s.inner.ToolCallDelta(value) })
}
func (s *attemptSink) Usage(value domain.Usage) error {
	return s.commit(func() error { return s.inner.Usage(value) })
}

func (s *attemptSink) commit(callback func() error) error {
	if err := callback(); err != nil {
		s.sinkErr = err
		return err
	}
	s.committed = true
	return nil
}

type eventSink struct {
	ctx               context.Context
	loop              *Loop
	runID             string
	iteration         int
	attempt           int
	requestGeneration int
	callID            string
	purpose           domain.ModelCallPurpose
	sourceArtifactID  string
}

func (s *eventSink) TextDelta(value string) error {
	eventType := "text_delta"
	if s.purpose == domain.ModelCallImageDescription {
		eventType = "vision_description_delta"
	}
	return s.loop.appendEvent(s.ctx, s.runID, eventType, map[string]any{
		"iteration": s.iteration, "attempt": s.attempt, "requestGeneration": s.requestGeneration,
		"text": value, "sourceArtifactId": s.sourceArtifactID,
	})
}
func (s *eventSink) ThinkingDelta(value string) error {
	return s.loop.appendEvent(s.ctx, s.runID, "thinking_delta", map[string]any{
		"iteration": s.iteration, "attempt": s.attempt, "requestGeneration": s.requestGeneration, "text": value,
	})
}
func (s *eventSink) ToolCallDelta(value llm.ToolCallDelta) error {
	return s.loop.appendEvent(s.ctx, s.runID, "tool_call_delta", map[string]any{
		"iteration": s.iteration, "attempt": s.attempt, "requestGeneration": s.requestGeneration,
		"index": value.Index, "id": value.ID,
		"name": value.Name, "argumentsFragment": value.ArgumentsFragment,
	})
}
func (s *eventSink) Usage(value domain.Usage) error {
	return s.loop.recordModelUsage(s.ctx, domain.ModelCallFinish{
		ID: s.callID, RunID: s.runID, Iteration: s.iteration, Attempt: s.attempt,
		RequestGeneration: s.requestGeneration, Usage: value,
	})
}

// compositionInput returns the input token estimate for a message list and tool
// definitions without counting system-prompt overhead (the caller supplies a
// separate system message).
func compositionInput(messages []domain.ChatMessage, tools []domain.ToolDefinition) int {
	return EstimateComposition("", tools, messages, 0).InputTokens
}

// toolDefinitionTokens returns the incremental token cost of tool definitions
// relative to the fixed overhead of EstimateComposition.
func toolDefinitionTokens(tools []domain.ToolDefinition) int {
	base := EstimateComposition("", nil, nil, 0).InputTokens
	withTools := EstimateComposition("", tools, nil, 0).InputTokens
	if withTools <= base {
		return 0
	}
	return withTools - base
}

// effectiveRuntime fills missing context/output values from Loop fallbacks so
// MainUsableTokens produces a meaningful budget even when the routed snapshot
// has partial fields.
func (l *Loop) effectiveRuntime(runtime domain.ModelRuntimeSnapshot) domain.ModelRuntimeSnapshot {
	if runtime.ContextTokens <= 0 {
		runtime.ContextTokens = l.ContextTokens
	}
	if runtime.MaxOutputTokens <= 0 {
		runtime.MaxOutputTokens = l.MaxOutput
	}
	return runtime
}

// prepareRequestMessages builds the final message list for the LLM request.
// It resolves reminders from the registry, reserves their token cost from the
// usable budget, trims durable history with PrepareContext, and appends
// reminders only when they fit within the remaining budget. The returned slice
// is never written back to canonical messages or generated history.
func (l *Loop) prepareRequestMessages(ctx context.Context, systemPrompt string,
	requestMessages []domain.ChatMessage, runtime domain.ModelRuntimeSnapshot,
	iteration int, tools []domain.ToolDefinition) []domain.ChatMessage {
	effective := l.effectiveRuntime(runtime)
	usable := MainUsableTokens(effective)

	var reminders []domain.ChatMessage
	if l.Reminders != nil && !l.Reminders.Empty() {
		reminders = l.Reminders.Messages(ctx, ReminderContext{
			Messages: requestMessages, SystemPrompt: systemPrompt, Tools: tools,
			Runtime: effective, Iteration: iteration, InputTokenBudget: usable,
		})
	}

	// Unknown context window: no trimming; just append reminders.
	if effective.ContextTokens <= 0 || usable <= 0 {
		prepared := PrepareContext(systemPrompt, requestMessages, 0)
		if len(reminders) == 0 {
			return prepared
		}
		out := make([]domain.ChatMessage, 0, len(prepared)+len(reminders))
		out = append(out, prepared...)
		out = append(out, reminders...)
		return out
	}

	// Build the reminder-free fallback request.
	durableBudget := usable - toolDefinitionTokens(tools)
	if durableBudget < 0 {
		durableBudget = 0
	}
	fallback := PrepareContext(systemPrompt, requestMessages, durableBudget)

	if len(reminders) == 0 {
		return fallback
	}

	// Try to fit reminders: reduce durable budget by reminder cost.
	reminderTokens := EstimateComposition("", nil, reminders, 0).InputTokens
	reducedBudget := durableBudget - reminderTokens
	if reducedBudget < 0 {
		reducedBudget = 0
	}
	candidate := PrepareContext(systemPrompt, requestMessages, reducedBudget)
	out := make([]domain.ChatMessage, 0, len(candidate)+len(reminders))
	out = append(out, candidate...)
	out = append(out, reminders...)

	// Validate the candidate fits within the usable budget.
	// Pass empty system prompt because PrepareContext already prepends it.
	if compositionInput(out, tools) <= usable {
		return out
	}
	return fallback
}

// todoSnapshot returns the current run todo list, or nil when no store is
// attached (so an empty todo never bloats a checkpoint).
func (l *Loop) todoSnapshot() []domain.TodoItem {
	if l.TodoStore == nil {
		return nil
	}
	return l.TodoStore.Snapshot()
}
