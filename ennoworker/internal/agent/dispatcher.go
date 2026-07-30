package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type plannedToolCall struct {
	original         domain.ToolCall
	effective        domain.ToolCall
	decision         ToolDecision
	allowed          bool
	requiresApproval bool
}

func (l *Loop) executeToolBatch(ctx context.Context, runID string, iteration int, calls []domain.ToolCall) ([]domain.ToolResult, error) {
	results, _, err := l.executeToolBatchWithPolicy(ctx, runID, iteration, calls)
	return results, err
}

func (l *Loop) executeToolBatchWithPolicy(ctx context.Context, runID string, iteration int, calls []domain.ToolCall,
	resolutions ...ApprovalResolution) ([]domain.ToolResult, bool, error) {
	mode := l.ToolExecution.Mode
	if mode == "" {
		mode = "sequential"
	}
	if mode != "sequential" && mode != "safe_parallel" {
		return nil, false, domain.NewCodedError(domain.ErrorToolBatchFailed, fmt.Errorf("invalid tool execution mode %q", mode))
	}
	plans, terminate, err := l.preflightToolBatch(ctx, runID, iteration, calls)
	if err != nil {
		return nil, false, err
	}
	results := make([]domain.ToolResult, len(calls))
	if terminate {
		for index, plan := range plans {
			decision := plan.decision
			if decision.Action != ToolTerminateBatch {
				decision = ToolDecision{Action: ToolTerminateBatch, Code: "batch_terminated", Reason: "tool batch terminated by policy"}
			}
			result := policyDeniedResult(plan.original, decision.Reason)
			results[index] = result
			if err := l.recordToolSkipped(ctx, runID, iteration, index, plan.original, result, decision.Reason,
				l.policyMetadata(decision, false)); err != nil {
				return results, false, err
			}
		}
		return results, false, domain.NewCodedError(domain.ErrorToolPolicyTerminated, errors.New("tool batch terminated by policy"))
	}

	requiresApproval := false
	for _, plan := range plans {
		requiresApproval = requiresApproval || plan.requiresApproval
	}
	if requiresApproval {
		digest := approvalBatchDigest(plans, l.ToolPolicySnapshot)
		if len(resolutions) == 0 {
			return nil, false, &ApprovalRequiredError{BatchDigest: digest, Items: approvalItems(plans, l.ToolPolicy)}
		}
		resolution := resolutions[0]
		if resolution.BatchDigest != digest {
			return nil, false, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
				fmt.Errorf("approval batch digest mismatch"))
		}
		for index := range plans {
			if !plans[index].requiresApproval {
				continue
			}
			switch resolution.Decision {
			case domain.DecisionApproved:
				plans[index].allowed = true
				plans[index].decision.Code = "approval_approved"
			case domain.DecisionRejected:
				plans[index].requiresApproval = false
				plans[index].decision.Action = ToolDeny
				plans[index].decision.Code = "approval_rejected"
				plans[index].decision.Reason = "Tool call rejected by the user"
			default:
				return nil, false, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
					fmt.Errorf("unsupported approval decision %q", resolution.Decision))
			}
		}
	}

	for index, plan := range plans {
		if plan.allowed {
			continue
		}
		result := policyDeniedResult(plan.original, plan.decision.Reason)
		results[index] = result
		if err := l.recordToolSkipped(ctx, runID, iteration, index, plan.original, result, plan.decision.Reason,
			l.policyMetadata(plan.decision, false)); err != nil {
			return results, false, err
		}
	}

	stopAfterBatch := false
	for index := 0; index < len(plans); {
		if !plans[index].allowed {
			index++
			continue
		}
		if mode == "sequential" || l.executionClass(plans[index].effective.Name) != domain.ExecutionReadOnly {
			result, stop, err := l.executeOneToolPlan(ctx, runID, iteration, index, plans[index])
			results[index] = result
			stopAfterBatch = stopAfterBatch || stop
			if err != nil {
				return results, stopAfterBatch, err
			}
			index++
			continue
		}
		end := index + 1
		for end < len(plans) && plans[end].allowed && l.executionClass(plans[end].effective.Name) == domain.ExecutionReadOnly {
			end++
		}
		groupResults, stop, err := l.executeReadPlanGroup(ctx, runID, iteration, index, plans[index:end])
		copy(results[index:end], groupResults)
		stopAfterBatch = stopAfterBatch || stop
		if err != nil {
			return results, stopAfterBatch, err
		}
		index = end
	}
	return results, stopAfterBatch, nil
}

func (l *Loop) preflightToolBatch(ctx context.Context, runID string, iteration int, calls []domain.ToolCall) ([]plannedToolCall, bool, error) {
	decisions := make([]ToolDecision, len(calls))
	for index := range decisions {
		decisions[index].Action = ToolAllow
	}
	if l.ToolPolicy != nil {
		var err error
		decisions, err = callBeforeToolBatch(ctx, l.ToolPolicy, ToolBatchContext{
			RunID: runID, Iteration: iteration, Policy: l.ToolPolicySnapshot, WorkspaceID: l.WorkspaceID,
		}, append([]domain.ToolCall(nil), calls...))
		if err != nil {
			return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed, err)
		}
		if len(decisions) != len(calls) {
			return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed,
				fmt.Errorf("tool policy returned %d decisions for %d calls", len(decisions), len(calls)))
		}
	}
	plans := make([]plannedToolCall, len(calls))
	terminate := false
	for index, call := range calls {
		decision := decisions[index]
		if decision.Action == "" {
			decision.Action = ToolAllow
		}
		plan := plannedToolCall{original: call, effective: call, decision: decision}
		if len(plan.effective.Arguments) == 0 {
			plan.effective.Arguments = json.RawMessage(`{}`)
		}
		if len(plan.original.Arguments) == 0 {
			plan.original.Arguments = json.RawMessage(`{}`)
		}
		switch decision.Action {
		case ToolAllow:
			if len(decision.Arguments) > 0 {
				if !json.Valid(decision.Arguments) {
					return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed,
						fmt.Errorf("tool policy returned invalid JSON for call %d", index))
				}
				plan.effective.Arguments = append(json.RawMessage(nil), decision.Arguments...)
			}
			if err := l.validateToolArguments(plan.effective); err != nil {
				return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed, err)
			}
			plan.allowed = true
		case ToolRequireApproval:
			if len(decision.Arguments) > 0 {
				if !json.Valid(decision.Arguments) {
					return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed,
						fmt.Errorf("tool policy returned invalid JSON for call %d", index))
				}
				plan.effective.Arguments = append(json.RawMessage(nil), decision.Arguments...)
			}
			if err := l.validateToolArguments(plan.effective); err != nil {
				return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed, err)
			}
			plan.requiresApproval = true
		case ToolDeny:
			if len(decision.Arguments) > 0 {
				return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed,
					fmt.Errorf("deny decision %d contains rewritten arguments", index))
			}
		case ToolTerminateBatch:
			if len(decision.Arguments) > 0 {
				return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed,
					fmt.Errorf("terminate decision %d contains rewritten arguments", index))
			}
			terminate = true
		default:
			return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed,
				fmt.Errorf("unknown tool policy action %q", decision.Action))
		}
		plans[index] = plan
	}
	return plans, terminate, nil
}

func (l *Loop) executeOneTool(ctx context.Context, runID string, iteration, callIndex int, call domain.ToolCall) (domain.ToolResult, error) {
	result, _, err := l.executeOneToolPlan(ctx, runID, iteration, callIndex,
		plannedToolCall{original: call, effective: call, decision: ToolDecision{Action: ToolAllow}, allowed: true})
	return result, err
}

func (l *Loop) executeOneToolPlan(ctx context.Context, runID string, iteration, callIndex int, plan plannedToolCall) (domain.ToolResult, bool, error) {
	recordID := uuid.NewString()
	start := l.toolCallStart(recordID, runID, iteration, callIndex, plan)
	if err := l.recordToolStarted(ctx, start); err != nil {
		return domain.ToolResult{}, false, err
	}
	raw := BudgetToolResult(l.Tools.Execute(ctx, plan.effective), defaultToolResultBudget)
	projected, stop, policyCode, policyErr := l.projectToolResult(context.WithoutCancel(ctx), runID, iteration, callIndex, plan, raw)
	metadata := l.policyMetadata(plan.decision, stop)
	metadata.Code = firstNonEmptyString(policyCode, metadata.Code)
	if policyErr != nil {
		finish := domain.ToolCallFinish{ID: recordID, RunID: runID, Iteration: iteration, CallIndex: callIndex,
			Call: plan.effective, RawResult: raw, Result: raw, Status: "failed", Policy: metadata}
		if err := l.recordToolFailed(context.WithoutCancel(ctx), finish); err != nil {
			return raw, false, errors.Join(policyErr, err)
		}
		return raw, false, domain.NewCodedError(domain.ErrorToolPolicyFailed, policyErr)
	}
	finish := domain.ToolCallFinish{ID: recordID, RunID: runID, Iteration: iteration,
		CallIndex: callIndex, Call: plan.effective, RawResult: raw, Result: projected,
		Status: "completed", Policy: metadata}
	if err := l.recordToolCompleted(context.WithoutCancel(ctx), finish); err != nil {
		return projected, stop, err
	}
	if ctx.Err() != nil {
		return projected, stop, context.Canceled
	}
	return projected, stop, nil
}

func (l *Loop) executeReadGroup(ctx context.Context, runID string, iteration, baseIndex int, calls []domain.ToolCall) ([]domain.ToolResult, error) {
	plans := make([]plannedToolCall, len(calls))
	for index, call := range calls {
		plans[index] = plannedToolCall{original: call, effective: call, decision: ToolDecision{Action: ToolAllow}, allowed: true}
	}
	results, _, err := l.executeReadPlanGroup(ctx, runID, iteration, baseIndex, plans)
	return results, err
}

func (l *Loop) executeReadPlanGroup(ctx context.Context, runID string, iteration, baseIndex int, plans []plannedToolCall) ([]domain.ToolResult, bool, error) {
	limit := l.ToolExecution.MaxConcurrentReadTools
	if limit <= 0 {
		limit = 4
	}
	recordIDs := make([]string, len(plans))
	for index, plan := range plans {
		recordIDs[index] = uuid.NewString()
		if err := l.recordToolStarted(ctx, l.toolCallStart(recordIDs[index], runID, iteration, baseIndex+index, plan)); err != nil {
			return nil, false, err
		}
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	semaphore := make(chan struct{}, limit)
	rawResults := make([]chan domain.ToolResult, len(plans))
	for index, plan := range plans {
		rawResults[index] = make(chan domain.ToolResult, 1)
		go func(index int, plan plannedToolCall) {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-batchCtx.Done():
				rawResults[index] <- policyDeniedResult(plan.effective, batchCtx.Err().Error())
				return
			}
			rawResults[index] <- BudgetToolResult(l.Tools.Execute(batchCtx, plan.effective), defaultToolResultBudget)
		}(index, plan)
	}

	results := make([]domain.ToolResult, len(plans))
	stopAfterBatch := false
	var firstErr error
	for index, plan := range plans {
		raw := <-rawResults[index]
		projected, stop, policyCode, policyErr := l.projectToolResult(context.WithoutCancel(ctx), runID,
			iteration, baseIndex+index, plan, raw)
		metadata := l.policyMetadata(plan.decision, stop)
		metadata.Code = firstNonEmptyString(policyCode, metadata.Code)
		if policyErr != nil {
			finish := domain.ToolCallFinish{ID: recordIDs[index], RunID: runID, Iteration: iteration,
				CallIndex: baseIndex + index, Call: plan.effective, RawResult: raw, Result: raw,
				Status: "failed", Policy: metadata}
			if err := l.recordToolFailed(context.WithoutCancel(ctx), finish); err != nil && firstErr == nil {
				firstErr = err
			}
			if firstErr == nil {
				firstErr = domain.NewCodedError(domain.ErrorToolPolicyFailed, policyErr)
			}
			cancel()
			results[index] = raw
			continue
		}
		finish := domain.ToolCallFinish{ID: recordIDs[index], RunID: runID, Iteration: iteration,
			CallIndex: baseIndex + index, Call: plan.effective, RawResult: raw, Result: projected,
			Status: "completed", Policy: metadata}
		if err := l.recordToolCompleted(context.WithoutCancel(ctx), finish); err != nil && firstErr == nil {
			firstErr = err
			cancel()
		}
		results[index] = projected
		stopAfterBatch = stopAfterBatch || stop
	}
	if firstErr != nil {
		return results, stopAfterBatch, firstErr
	}
	if ctx.Err() != nil {
		return results, stopAfterBatch, context.Canceled
	}
	return results, stopAfterBatch, nil
}

func (l *Loop) toolCallStart(recordID, runID string, iteration, callIndex int, plan plannedToolCall) domain.ToolCallStart {
	return domain.ToolCallStart{ID: recordID, RunID: runID, Iteration: iteration, CallIndex: callIndex,
		Call: plan.effective, OriginalArguments: plan.original.Arguments,
		EffectiveArguments: plan.effective.Arguments, Policy: l.policyMetadata(plan.decision, false)}
}

func (l *Loop) projectToolResult(ctx context.Context, runID string, iteration, callIndex int, plan plannedToolCall, raw domain.ToolResult) (domain.ToolResult, bool, string, error) {
	if l.ToolPolicy == nil {
		return raw, false, "", nil
	}
	decision, err := callAfterTool(ctx, l.ToolPolicy, ToolCallContext{RunID: runID, Iteration: iteration,
		CallIndex: callIndex, Policy: l.ToolPolicySnapshot, WorkspaceID: l.WorkspaceID}, plan.effective, raw)
	if err != nil {
		return raw, false, "policy_hook_failed", err
	}
	decision.Result.ToolCallID = plan.effective.ID
	decision.Result.ToolName = plan.effective.Name
	return decision.Result, decision.StopAfterBatch, decision.Code, nil
}

func (l *Loop) policyMetadata(decision ToolDecision, stop bool) domain.ToolPolicyMetadata {
	return domain.ToolPolicyMetadata{PolicyID: l.ToolPolicySnapshot.ID, PolicyVersion: l.ToolPolicySnapshot.Version,
		Action: string(decision.Action), Code: decision.Code, RiskClass: decision.RiskClass, StopAfterBatch: stop}
}

func (l *Loop) validateToolArguments(call domain.ToolCall) error {
	if !json.Valid(call.Arguments) {
		return fmt.Errorf("tool %s arguments are not valid JSON", call.Name)
	}
	if validator, ok := l.Tools.(domain.ToolArgumentValidator); ok {
		return validator.ValidateArguments(call.Name, call.Arguments)
	}
	return nil
}

func (l *Loop) executionClass(toolName string) domain.ExecutionClass {
	classifier, ok := l.Tools.(domain.ToolExecutionClassifier)
	if !ok {
		return domain.ExecutionExclusive
	}
	class := classifier.ExecutionClass(toolName)
	if class != domain.ExecutionReadOnly && class != domain.ExecutionWorkspaceWrite && class != domain.ExecutionExclusive {
		return domain.ExecutionExclusive
	}
	return class
}

func callBeforeToolBatch(ctx context.Context, policy ToolPolicy, batch ToolBatchContext, calls []domain.ToolCall) (decisions []ToolDecision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("before-tool policy panic: %v", recovered)
		}
	}()
	return policy.BeforeToolBatch(ctx, batch, calls)
}

func callAfterTool(ctx context.Context, policy ToolPolicy, callCtx ToolCallContext, call domain.ToolCall, result domain.ToolResult) (decision AfterToolDecision, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("after-tool policy panic: %v", recovered)
		}
	}()
	return policy.AfterToolCall(ctx, callCtx, call, result)
}

func policyDeniedResult(call domain.ToolCall, reason string) domain.ToolResult {
	if reason == "" {
		reason = "tool call denied by policy"
	}
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: reason, IsError: true}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
