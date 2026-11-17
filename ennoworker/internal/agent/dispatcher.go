package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
)

type plannedToolCall struct {
	// originalIndex is the call's position in the model's tool_calls batch. It is
	// the one callIndex that leaves the dispatcher (tool events, approval items,
	// standing snapshots, the results slice), because it is the only identity a
	// PreToolUse hook cannot renumber by blocking a neighbour. preflightToolBatch
	// builds plans compacted by those blocks, so a plan's own position in plans
	// is a grouping detail, never an index.
	originalIndex    int
	original         domain.ToolCall
	effective        domain.ToolCall
	decision         ToolDecision
	allowed          bool
	requiresApproval bool
}

// blockedToolResult is a call a PreToolUse hook refused before the batch ran.
// The call still owes the model a tool result: the transcript keeps the
// assistant tool_calls message, so a missing result for one of its IDs makes the
// next provider request fail protocol validation.
type blockedToolResult struct {
	originalIndex int
	result        domain.ToolResult
}

// toolBatchPreflight is one model batch after the PreToolUse hooks and
// ToolPolicy ran: the surviving plans, the results already settled for blocked
// calls, and whether policy terminated the batch.
type toolBatchPreflight struct {
	plans     []plannedToolCall
	blocked   []blockedToolResult
	terminate bool
}

// planIndexOf returns the position in plans of the call that occupied
// originalIndex in the model's batch, or -1 when that call never reached a plan
// (a PreToolUse hook blocked it, or it was never in the batch).
func planIndexOf(plans []plannedToolCall, originalIndex int) int {
	for index := range plans {
		if plans[index].originalIndex == originalIndex {
			return index
		}
	}
	return -1
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
	preflight, err := l.preflightToolBatch(ctx, runID, iteration, calls)
	if err != nil {
		return nil, false, err
	}
	plans, terminate := preflight.plans, preflight.terminate
	// results is indexed by the call's position in the model's batch, so a call a
	// PreToolUse hook blocked keeps its own slot next to the calls that ran.
	results := make([]domain.ToolResult, len(calls))
	for _, blocked := range preflight.blocked {
		results[blocked.originalIndex] = blocked.result
	}
	if terminate {
		for _, plan := range plans {
			decision := plan.decision
			if decision.Action != ToolTerminateBatch {
				decision = ToolDecision{Action: ToolTerminateBatch, Code: "batch_terminated", Reason: "tool batch terminated by policy"}
			}
			result := policyDeniedResult(plan.original, decision.Reason)
			results[plan.originalIndex] = result
			if err := l.recordToolSkipped(ctx, runID, iteration, plan.originalIndex, plan.original, result, decision.Reason,
				l.policyMetadata(decision, false)); err != nil {
				return results, false, err
			}
		}
		return results, false, domain.NewCodedError(domain.ErrorToolPolicyTerminated, errors.New("tool batch terminated by policy"))
	}

	var precomputed map[int]domain.ToolResult
	questionResumed := false
	if len(resolutions) > 0 && resolutions[0].Kind == domain.ApprovalKindQuestion {
		index, result, resumeErr := l.prepareQuestionResume(runID, plans, resolutions[0])
		if resumeErr != nil {
			return nil, false, resumeErr
		}
		precomputed = map[int]domain.ToolResult{index: result}
		plans[index].allowed = true
		plans[index].requiresApproval = false
		plans[index].decision = ToolDecision{Action: ToolAllow, Code: "question_answered", RiskClass: domain.RiskReadOnly}
		questionResumed = true
	} else if len(resolutions) == 0 {
		if questionErr := l.maybePauseForQuestion(runID, plans); questionErr != nil {
			return nil, false, questionErr
		}
	}

	requiresApproval := false
	for _, plan := range plans {
		requiresApproval = requiresApproval || plan.requiresApproval
	}
	if requiresApproval && !questionResumed {
		// Digest version: resume states pin the exact version; fresh runs fall
		// back to V1 only when no standing infrastructure is wired.
		digestVersion := l.ApprovalDigestVersion
		if digestVersion == 0 {
			digestVersion = ApprovalDigestV2
			if l.StandingScopeResolver == nil || l.StandingApprovals == nil {
				digestVersion = ApprovalDigestV1
			}
		}
		digest := approvalBatchDigest(plans, l.ToolPolicySnapshot, digestVersion)
		if len(resolutions) == 0 {
			var candidates []domain.StandingGrantCandidate
			// Collect standing candidates for require_approval + external calls.
			for _, plan := range plans {
				if !plan.requiresApproval || plan.decision.RiskClass != domain.RiskExternal {
					continue
				}
				if l.StandingScopeResolver != nil {
					scope, ok, _ := l.StandingScopeResolver.ResolveStandingApprovalScope(plan.effective.Name, plan.effective.Arguments)
					if ok {
						candidates = append(candidates, domain.StandingGrantCandidate{
							CallIndex:    plan.originalIndex,
							ToolCallID:   plan.effective.ID,
							ToolName:     plan.effective.Name,
							ScopeKind:    scope.Kind,
							ScopeVersion: scope.ScopeVersion,
							ScopeKey:     scope.Key,
							ScopeDisplay: scope.Display,
							RiskClass:    plan.decision.RiskClass,
						})
					}
				}
			}
			items := approvalItems(plans, l.ToolPolicy)
			attachStandingScopes(items, plans, l.StandingScopeResolver)
			return nil, false, &ApprovalRequiredError{
				BatchDigest:            digest,
				ApprovalDigestVersion:  digestVersion,
				Items:                  items,
				StandingCandidates:     candidates,
				StandingAuthorizations: standingAuthorizationSnapshot(plans),
			}
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

	for _, plan := range plans {
		if plan.allowed {
			continue
		}
		result := policyDeniedResult(plan.original, plan.decision.Reason)
		results[plan.originalIndex] = result
		if err := l.recordToolSkipped(ctx, runID, iteration, plan.originalIndex, plan.original, result, plan.decision.Reason,
			l.policyMetadata(plan.decision, false)); err != nil {
			return results, false, err
		}
	}

	allowedCount := 0
	for _, plan := range plans {
		if plan.allowed {
			allowedCount++
		}
	}
	if l.BudgetController != nil && allowedCount > 0 {
		if err := l.BudgetController.AdmitToolCalls(ctx, runID, allowedCount); err != nil {
			return results, false, domain.NewCodedError(domain.ErrorDelegationBudgetExceeded, err)
		}
	}

	stopAfterBatch := false
	// index walks plans (grouping adjacent read-only calls); every result is
	// stored at plan.originalIndex so the batch keeps the model's call order.
	for index := 0; index < len(plans); {
		if !plans[index].allowed {
			index++
			continue
		}
		if result, ok := precomputed[index]; ok {
			if err := l.completePrecomputedToolCall(ctx, runID, iteration, plans[index], result); err != nil {
				results[plans[index].originalIndex] = result
				return results, stopAfterBatch, err
			}
			results[plans[index].originalIndex] = result
			index++
			continue
		}
		if mode == "sequential" || l.executionClass(plans[index].effective.Name) != domain.ExecutionReadOnly {
			result, stop, err := l.executeOneToolPlan(ctx, runID, iteration, plans[index])
			results[plans[index].originalIndex] = result
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
		group := plans[index:end]
		groupResults, stop, err := l.executeReadPlanGroup(ctx, runID, iteration, group)
		for offset, result := range groupResults {
			results[group[offset].originalIndex] = result
		}
		stopAfterBatch = stopAfterBatch || stop
		if err != nil {
			return results, stopAfterBatch, err
		}
		index = end
	}
	return results, stopAfterBatch, nil
}

// maybePauseForQuestion turns a valid ask_user call into a checkpoint question.
// Invalid or disallowed calls are denied in place so the rest of the batch can
// still execute and the model gets a tool-visible error instead of a failed Run.
func (l *Loop) maybePauseForQuestion(runID string, plans []plannedToolCall) error {
	indexes := askUserIndexes(plans)
	if len(indexes) == 0 {
		return nil
	}
	if len(indexes) > 1 {
		for _, index := range indexes {
			denyQuestionPlan(&plans[index], "question_batch_invalid", "at most one ask_user call is allowed per batch")
		}
		return nil
	}
	index := indexes[0]
	if !plans[index].allowed {
		return nil
	}
	if !l.AllowQuestions {
		denyQuestionPlan(&plans[index], "questions_not_allowed", "ask_user is not available for delegated runs")
		return nil
	}
	for planIndex, plan := range plans {
		if planIndex == index {
			continue
		}
		if plan.decision.RiskClass != domain.RiskReadOnly || plan.requiresApproval {
			denyQuestionPlan(&plans[index], "question_batch_invalid",
				"ask_user requires a read-only batch; call it alone or with read-only tools")
			return nil
		}
	}
	questions, err := domain.ParseAskUserQuestions(plans[index].effective.Arguments)
	if err != nil {
		denyQuestionPlan(&plans[index], "question_invalid", err.Error())
		return nil
	}
	digest, err := domain.QuestionDigest(runID, plans[index].original.ID, questions)
	if err != nil {
		denyQuestionPlan(&plans[index], "question_invalid", err.Error())
		return nil
	}
	return &QuestionRequiredError{Digest: digest, Questions: questions}
}

func (l *Loop) prepareQuestionResume(runID string, plans []plannedToolCall, resolution ApprovalResolution) (int, domain.ToolResult, error) {
	indexes := askUserIndexes(plans)
	if len(indexes) != 1 {
		return 0, domain.ToolResult{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
			fmt.Errorf("question checkpoint requires exactly one ask_user call"))
	}
	index := indexes[0]
	questions, err := domain.ParseAskUserQuestions(plans[index].effective.Arguments)
	if err != nil {
		return 0, domain.ToolResult{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, err)
	}
	digest, err := domain.QuestionDigest(runID, plans[index].original.ID, questions)
	if err != nil {
		return 0, domain.ToolResult{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, err)
	}
	if digest != resolution.BatchDigest {
		return 0, domain.ToolResult{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
			fmt.Errorf("question digest mismatch"))
	}
	answers, err := domain.CanonicalAnswers(questions, resolution.Answers)
	if err != nil {
		return 0, domain.ToolResult{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, err)
	}
	payload, err := json.Marshal(map[string]any{"answers": answers})
	if err != nil {
		return 0, domain.ToolResult{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, err)
	}
	return index, domain.ToolResult{
		ToolCallID: plans[index].original.ID,
		ToolName:   plans[index].original.Name,
		Content:    string(payload),
	}, nil
}

func askUserIndexes(plans []plannedToolCall) []int {
	var indexes []int
	for index, plan := range plans {
		if plan.effective.Name == "ask_user" {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func denyQuestionPlan(plan *plannedToolCall, code, reason string) {
	plan.allowed = false
	plan.requiresApproval = false
	plan.decision = ToolDecision{Action: ToolDeny, Code: code, Reason: reason, RiskClass: plan.decision.RiskClass}
}

func (l *Loop) completePrecomputedToolCall(ctx context.Context, runID string, iteration int,
	plan plannedToolCall, result domain.ToolResult) error {
	startID := uuid.NewString()
	if err := l.recordToolStarted(ctx, domain.ToolCallStart{
		ID: startID, RunID: runID, Iteration: iteration, CallIndex: plan.originalIndex, Call: plan.original,
	}); err != nil {
		return err
	}
	return l.recordToolCompleted(ctx, domain.ToolCallFinish{
		ID: startID, RunID: runID, Iteration: iteration, CallIndex: plan.originalIndex,
		Call: plan.original, Result: result, Status: "completed", AttemptCount: 1,
	})
}

func (l *Loop) preflightToolBatch(ctx context.Context, runID string, iteration int, calls []domain.ToolCall) (toolBatchPreflight, error) {
	// PreToolUse hooks run BEFORE ToolPolicy: they may block a call (recorded
	// as skipped) or rewrite its arguments. Rewritten arguments flow through
	// the full ToolPolicy + approval path below, so a hook can never smuggle
	// schema-invalid or approval-worthy arguments past the policy gate.
	originalIndexes := make([]int, len(calls))
	for index := range calls {
		originalIndexes[index] = index
	}
	var blocked []blockedToolResult
	if l.HookLife != nil {
		filtered := make([]domain.ToolCall, 0, len(calls))
		keptIndexes := make([]int, 0, len(calls))
		for index, call := range calls {
			dec := l.HookLife.PreToolUse(ctx, call.Name, call.Arguments)
			if dec.Block {
				reason := dec.Reason
				if reason == "" {
					reason = "tool " + call.Name + " blocked by PreToolUse hook"
				}
				result := domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
					Content: "hook_blocked: " + reason, IsError: true}
				metadata := l.policyMetadata(ToolDecision{Action: ToolDeny, Code: "hook_blocked", Reason: reason}, false)
				if err := l.recordToolSkipped(ctx, runID, iteration, index, call, result, "hook_blocked", metadata); err != nil {
					return toolBatchPreflight{}, err
				}
				blocked = append(blocked, blockedToolResult{originalIndex: index, result: result})
				continue
			}
			if len(dec.UpdatedInput) > 0 {
				call.Arguments = append(json.RawMessage(nil), dec.UpdatedInput...)
				// Validate the rewritten args before they reach ToolPolicy.
				if err := l.validateToolArguments(call); err != nil {
					return toolBatchPreflight{}, domain.NewCodedError(domain.ErrorToolPolicyFailed,
						fmt.Errorf("hook rewritten arguments for %s failed schema validation: %w", call.Name, err))
				}
			}
			filtered = append(filtered, call)
			keptIndexes = append(keptIndexes, index)
		}
		// Always replace the batch with the hook-processed list: even a rewrite
		// changes the argument bytes, so the original `calls` slice is stale.
		calls = filtered
		originalIndexes = keptIndexes
		// A fully blocked batch still owes each call its own result; only the
		// planning below skips it.
		if len(calls) == 0 {
			return toolBatchPreflight{blocked: blocked}, nil
		}
	}

	plans, terminate, err := l.planToolBatch(ctx, runID, iteration, calls, originalIndexes)
	if err != nil {
		return toolBatchPreflight{}, err
	}
	return toolBatchPreflight{plans: plans, blocked: blocked, terminate: terminate}, nil
}

// planToolBatch runs ToolPolicy and the standing-approval gate over the calls
// that survived PreToolUse. originalIndexes keeps each call's position in the
// model's batch, which is the callIndex the plan carries from here on.
func (l *Loop) planToolBatch(ctx context.Context, runID string, iteration int, calls []domain.ToolCall,
	originalIndexes []int) ([]plannedToolCall, bool, error) {
	execs := make([]*ToolExecution, len(calls))
	for index, call := range calls {
		execs[index] = &ToolExecution{
			RunID:       runID,
			Iteration:   iteration,
			CallIndex:   originalIndexes[index],
			Original:    call,
			Effective:   call,
			Policy:      l.ToolPolicySnapshot,
			WorkspaceID: l.WorkspaceID,
			RiskClass:   l.toolRiskClass(call.Name),
		}
	}
	var decisions []ToolDecision
	terminate := false
	if l.PolicyChain != nil {
		var err error
		decisions, terminate, err = l.PolicyChain.Preflight(ctx, execs)
		if err != nil {
			return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed, err)
		}
		if len(decisions) != len(calls) {
			return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed,
				fmt.Errorf("tool policy returned %d decisions for %d calls", len(decisions), len(calls)))
		}
	} else if l.ToolPolicy != nil {
		// Legacy direct path: callers that set ToolPolicy without a chain keep
		// the pre-refactor behaviour.
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
		for index := range decisions {
			if decisions[index].Action == ToolTerminateBatch {
				terminate = true
			}
		}
	} else {
		decisions = make([]ToolDecision, len(calls))
		for index := range decisions {
			decisions[index].Action = ToolAllow
		}
	}
	plans := make([]plannedToolCall, len(calls))
	for index, call := range calls {
		decision := decisions[index]
		if decision.Action == "" {
			decision.Action = ToolAllow
		}
		plan := plannedToolCall{originalIndex: originalIndexes[index], original: call, effective: call, decision: decision}
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
		default:
			return nil, false, domain.NewCodedError(domain.ErrorToolPolicyFailed,
				fmt.Errorf("unknown tool policy action %q", decision.Action))
		}
		plans[index] = plan
	}

	// Standing approval gate: for require_approval + external risk calls, try to
	// match against active standing rules.  On resume, the frozen checkpoint
	// snapshot is replayed instead of querying live rules (so grants/revokes
	// that happened while the batch waited never change this batch).
	if l.StandingApprovals != nil && l.StandingScopeResolver != nil {
		if len(l.StandingAuthorizationSnapshot) > 0 {
			// Resume mode: replay the frozen snapshot. Validate every entry
			// (non-empty identity fields, unique CallIndex/ToolCallID) and
			// apply only exact CallIndex+ToolCallID+ToolName matches.
			seenIndex := make(map[int]bool, len(l.StandingAuthorizationSnapshot))
			seenCallID := make(map[string]bool, len(l.StandingAuthorizationSnapshot))
			for _, snap := range l.StandingAuthorizationSnapshot {
				if snap.RuleID == "" || snap.ToolCallID == "" || snap.ToolName == "" || snap.ScopeKind == "" ||
					snap.ScopeVersion < 1 || snap.ScopeKey == "" || snap.CallIndex < 0 {
					return nil, false, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
						fmt.Errorf("standing authorization snapshot contains an invalid entry"))
				}
				if seenIndex[snap.CallIndex] || seenCallID[snap.ToolCallID] {
					return nil, false, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
						fmt.Errorf("standing authorization snapshot has duplicate call index or tool call id"))
				}
				seenIndex[snap.CallIndex] = true
				seenCallID[snap.ToolCallID] = true
			}
			for _, snap := range l.StandingAuthorizationSnapshot {
				planIndex := planIndexOf(plans, snap.CallIndex)
				if planIndex < 0 {
					continue
				}
				plan := &plans[planIndex]
				if !plan.requiresApproval || plan.decision.RiskClass != domain.RiskExternal {
					continue
				}
				if plan.effective.ID != snap.ToolCallID || plan.effective.Name != snap.ToolName {
					continue
				}
				plan.allowed = true
				plan.requiresApproval = false
				plan.decision.Action = ToolAllow
				plan.decision.Code = "standing_approval"
				plan.decision.RuleID = snap.RuleID
				plan.decision.StandingScopeKind = snap.ScopeKind
				plan.decision.StandingScopeVersion = snap.ScopeVersion
				plan.decision.StandingScopeKey = snap.ScopeKey
			}
		} else {
			scopes := make([]domain.StandingScopeRef, 0)
			scopePlanIndexes := make([]int, 0)
			for index := range plans {
				if !plans[index].requiresApproval || plans[index].decision.RiskClass != domain.RiskExternal {
					continue
				}
				scope, ok, _ := l.StandingScopeResolver.ResolveStandingApprovalScope(
					plans[index].effective.Name, plans[index].effective.Arguments)
				if !ok {
					continue
				}
				scopes = append(scopes, domain.StandingScopeRef{
					ToolName:     plans[index].effective.Name,
					Kind:         scope.Kind,
					ScopeVersion: scope.ScopeVersion,
					Key:          scope.Key,
				})
				scopePlanIndexes = append(scopePlanIndexes, index)
			}
			if len(scopes) > 0 {
				matched, err := l.StandingApprovals.MatchActive(ctx, l.SessionID, scopes)
				if err != nil {
					// Fail safe: keep require_approval for all.
				} else {
					for i, ref := range scopes {
						if rule, ok := matched[ref]; ok {
							planIndex := scopePlanIndexes[i]
							plans[planIndex].allowed = true
							plans[planIndex].requiresApproval = false
							plans[planIndex].decision.Action = ToolAllow
							plans[planIndex].decision.Code = "standing_approval"
							plans[planIndex].decision.RuleID = rule.ID
							plans[planIndex].decision.StandingScopeKind = rule.ScopeKind
							plans[planIndex].decision.StandingScopeVersion = rule.ScopeVersion
							plans[planIndex].decision.StandingScopeKey = rule.ScopeKey
						}
					}
				}
			}
		}
	}

	return plans, terminate, nil
}

func (l *Loop) executeOneToolPlan(ctx context.Context, runID string, iteration int, plan plannedToolCall) (domain.ToolResult, bool, error) {
	recordID := uuid.NewString()

	start := l.toolCallStart(recordID, runID, iteration, plan)
	if err := l.recordToolStarted(ctx, start); err != nil {
		return domain.ToolResult{}, false, err
	}
	outcome := l.executeToolAttempts(ctx, runID, plan.effective)
	return l.settleToolPlan(ctx, runID, iteration, recordID, plan, outcome)
}

// settleToolPlan is the single producer of a settled tool outcome; the mode decides
// how an outcome is obtained, never how it settles. The no-execution transcript paths
// (completePrecomputedToolCall, submit_result in loop.go) record their own completion.
// Every event it records belongs to plan.originalIndex.
func (l *Loop) settleToolPlan(ctx context.Context, runID string, iteration int, recordID string,
	plan plannedToolCall, outcome domain.ToolExecutionOutcome) (domain.ToolResult, bool, error) {
	raw := BudgetToolResult(outcome.Result, defaultToolResultBudget)
	callIndex := plan.originalIndex

	// A panic or a cancellation is never a completed call: record it as failed so
	// event terminal state does not depend on which dispatch path ran.
	if code, failed := terminationCode(outcome.Kind); failed {
		metadata := l.policyMetadata(plan.decision, false)
		// The termination code wins over the decision code: a call that was allowed
		// through an approval and then panicked did not fail because of the approval.
		// The decision code survives in the tool_call_started payload.
		metadata.Code = code
		finish := domain.ToolCallFinish{ID: recordID, RunID: runID, Iteration: iteration, CallIndex: callIndex,
			Call: plan.effective, RawResult: raw, Result: raw, Status: "failed", Policy: metadata,
			AttemptCount: outcome.AttemptCount}
		if err := l.recordToolFailed(context.WithoutCancel(ctx), finish); err != nil {
			return raw, false, err
		}
		return raw, false, nil
	}

	projected, stop, policyCode, policyErr := l.projectToolResult(context.WithoutCancel(ctx), runID, iteration, plan, raw)

	// PostToolUse hook: feedback appended to the projected result (cannot undo execution).
	if l.HookLife != nil {
		resultJSON, _ := json.Marshal(raw)
		feedback := l.HookLife.PostToolUse(ctx, plan.effective.Name, plan.effective.Arguments, resultJSON, raw.IsError)
		if feedback != "" {
			projected.Content = projected.Content + "\n" + feedback
		}
	}

	metadata := l.policyMetadata(plan.decision, stop)
	metadata.Code = firstNonEmptyString(policyCode, metadata.Code)
	if policyErr != nil {
		finish := domain.ToolCallFinish{ID: recordID, RunID: runID, Iteration: iteration, CallIndex: callIndex,
			Call: plan.effective, RawResult: raw, Result: raw, Status: "failed", Policy: metadata,
			AttemptCount: outcome.AttemptCount}
		if err := l.recordToolFailed(context.WithoutCancel(ctx), finish); err != nil {
			return raw, false, errors.Join(policyErr, err)
		}
		return raw, false, domain.NewCodedError(domain.ErrorToolPolicyFailed, policyErr)
	}
	finish := domain.ToolCallFinish{ID: recordID, RunID: runID, Iteration: iteration,
		CallIndex: callIndex, Call: plan.effective, RawResult: raw, Result: projected,
		Status: "completed", Policy: metadata, AttemptCount: outcome.AttemptCount}
	if err := l.recordToolCompleted(context.WithoutCancel(ctx), finish); err != nil {
		return projected, stop, err
	}
	if ctx.Err() != nil {
		return projected, stop, context.Canceled
	}
	return projected, stop, nil
}

// terminationCode is the only judge of whether an outcome may settle as completed.
// Cancellation has exactly one code: attemptCount == 0 means the call never
// started, which is diagnostic detail, not a separate category.
func terminationCode(kind domain.ToolOutcomeKind) (code string, failed bool) {
	switch kind {
	case domain.ToolPanicked:
		return "tool_panicked", true
	case domain.ToolCancelled:
		return "tool_cancelled", true
	default:
		return "", false // returned / infrastructure_failed
	}
}

func (l *Loop) executeReadPlanGroup(ctx context.Context, runID string, iteration int, plans []plannedToolCall) ([]domain.ToolResult, bool, error) {
	limit := l.ToolExecution.MaxConcurrentReadTools
	if limit <= 0 {
		limit = 4
	}
	recordIDs := make([]string, len(plans))
	for index, plan := range plans {
		recordIDs[index] = uuid.NewString()
		if err := l.recordToolStarted(ctx, l.toolCallStart(recordIDs[index], runID, iteration, plan)); err != nil {
			return nil, false, err
		}
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	semaphore := make(chan struct{}, limit)
	outcomes := make([]chan domain.ToolExecutionOutcome, len(plans))
	for index, plan := range plans {
		outcomes[index] = make(chan domain.ToolExecutionOutcome, 1)
		go func(index int, plan plannedToolCall) {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-batchCtx.Done():
				// The call never started: nothing denied it, so report cancellation
				// with its own text instead of a fabricated policy denial.
				outcomes[index] <- domain.ToolExecutionOutcome{
					Result: domain.ToolResult{ToolCallID: plan.effective.ID, ToolName: plan.effective.Name,
						Content: fmt.Sprintf("tool execution cancelled before this call started: %v", batchCtx.Err()),
						IsError: true},
					AttemptCount: 0, Kind: domain.ToolCancelled, Cause: batchCtx.Err(),
				}
				return
			}
			outcomes[index] <- l.executeToolAttempts(batchCtx, runID, plan.effective)
		}(index, plan)
	}

	results := make([]domain.ToolResult, len(plans))
	stopAfterBatch := false
	var firstErr error
	for index, plan := range plans {
		result, stop, err := l.settleToolPlan(ctx, runID, iteration, recordIDs[index], plan, <-outcomes[index])
		results[index] = result
		stopAfterBatch = stopAfterBatch || stop
		if err != nil && firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	if firstErr != nil {
		return results, stopAfterBatch, firstErr
	}
	if ctx.Err() != nil {
		return results, stopAfterBatch, context.Canceled
	}
	return results, stopAfterBatch, nil
}

func (l *Loop) toolCallStart(recordID, runID string, iteration int, plan plannedToolCall) domain.ToolCallStart {
	return domain.ToolCallStart{ID: recordID, RunID: runID, Iteration: iteration, CallIndex: plan.originalIndex,
		Call: plan.effective, OriginalArguments: plan.original.Arguments,
		EffectiveArguments: plan.effective.Arguments, Policy: l.policyMetadata(plan.decision, false)}
}

func (l *Loop) projectToolResult(ctx context.Context, runID string, iteration int, plan plannedToolCall, raw domain.ToolResult) (domain.ToolResult, bool, string, error) {
	callIndex := plan.originalIndex
	if l.PolicyChain != nil {
		exec := &ToolExecution{
			RunID:       runID,
			Iteration:   iteration,
			CallIndex:   callIndex,
			Original:    plan.original,
			Effective:   plan.effective,
			Policy:      l.ToolPolicySnapshot,
			WorkspaceID: l.WorkspaceID,
			RiskClass:   l.toolRiskClass(plan.effective.Name),
		}
		decision, err := l.PolicyChain.Post(ctx, exec, raw)
		if err != nil {
			return raw, false, "policy_hook_failed", err
		}
		decision.Result.ToolCallID = plan.effective.ID
		decision.Result.ToolName = plan.effective.Name
		return decision.Result, decision.StopAfterBatch, decision.Code, nil
	}
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
		Action: string(decision.Action), Code: decision.Code, RiskClass: decision.RiskClass,
		StopAfterBatch: stop, StandingRuleID: decision.RuleID}
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

// toolRiskClass resolves one call's risk class for the policy chain. Unknown or
// restricted-away tools resolve to RiskSensitive (fail closed), mirroring the
// Registry contract.
func (l *Loop) toolRiskClass(toolName string) domain.RiskClass {
	classifier, ok := l.Tools.(domain.ToolRiskClassifier)
	if !ok {
		return domain.RiskSensitive
	}
	return classifier.RiskClass(toolName)
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

// executeToolAttempts executes a single tool call with automatic retry on
// typed transient errors when the tool opts into retry. It returns a
// ToolExecutionOutcome that distinguishes returned / failed / panicked / cancelled.
func (l *Loop) executeToolAttempts(ctx context.Context, runID string, call domain.ToolCall) domain.ToolExecutionOutcome {
	policy := l.toolRetryPolicy(call.Name)
	retries := maxToolRetries(l.ToolExecution.MaxToolRetries, policy.MaxRetries)

	var lastErr error
	for attempt := 1; attempt <= retries+1; attempt++ {
		if err := ctx.Err(); err != nil {
			return domain.ToolExecutionOutcome{
				Result: domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
					Content: fmt.Sprintf("tool execution cancelled: %v", err), IsError: true},
				AttemptCount: attempt, Kind: domain.ToolCancelled, Cause: err,
			}
		}

		// Support streaming output via ToolOutputSink.
		var result domain.ToolResult
		var execErr error
		var panicValue any
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					panicValue = recovered
					result = domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
						Content: fmt.Sprintf("tool panic: %v", recovered), IsError: true}
					execErr = nil
				}
			}()
			// If the tool implements StreamingToolRunner, provide a LiveCoalescer sink.
			if l.Hub != nil && l.LivePublisher != nil {
				if streaming, ok := l.Tools.(domain.StreamingToolRunner); ok {
					sink := &liveToolSink{
						// Scope the coalescer to the RUN so SSE subscribers keyed by
						// runID receive the live deltas, and carry the tool name into
						// them: the durable tool_call_* events are the only other
						// place the client learns it.
						coalescer: events.NewLiveCoalescer(runID, l.Hub, call.Name),
					}
					defer sink.coalescer.Close()
					result, execErr = streaming.ExecuteStreaming(ctx, call, sink)
					return
				}
			}
			result, execErr = l.Tools.Execute(ctx, call)
		}()

		// This must precede the execErr check: the recover above clears execErr, so
		// a panic is only distinguishable from a normal return through panicValue.
		if panicValue != nil {
			return domain.ToolExecutionOutcome{
				Result:       result,
				AttemptCount: attempt,
				Kind:         domain.ToolPanicked,
				Cause:        fmt.Errorf("tool panic: %v", panicValue),
			}
		}

		if execErr == nil {
			return domain.ToolExecutionOutcome{
				Result:       result,
				AttemptCount: attempt,
				Kind:         domain.ToolReturned,
			}
		}

		lastErr = execErr

		if domain.IsToolErrorTerminal(execErr) {
			return domain.ToolExecutionOutcome{
				Result: domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
					Content: fmt.Sprintf("tool execution failed: %v", execErr), IsError: true},
				AttemptCount: attempt, Kind: domain.ToolInfrastructureFailed, Cause: execErr,
			}
		}

		if policy.Mode != domain.ToolRetryTransient {
			return domain.ToolExecutionOutcome{
				Result: domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
					Content: fmt.Sprintf("tool execution failed: %v", execErr), IsError: true},
				AttemptCount: attempt, Kind: domain.ToolInfrastructureFailed, Cause: execErr,
			}
		}

		if !domain.IsToolErrorTransient(execErr) {
			return domain.ToolExecutionOutcome{
				Result: domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
					Content: fmt.Sprintf("tool execution failed: %v", execErr), IsError: true},
				AttemptCount: attempt, Kind: domain.ToolInfrastructureFailed, Cause: execErr,
			}
		}

		if attempt <= retries {
			delay := time.Duration(100*attempt) * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return domain.ToolExecutionOutcome{
					Result: domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
						Content: fmt.Sprintf("tool execution cancelled during retry backoff: %v", ctx.Err()),
						IsError: true},
					AttemptCount: attempt, Kind: domain.ToolCancelled, Cause: ctx.Err(),
				}
			case <-timer.C:
			}
		}
	}
	// Retries exhausted.
	return domain.ToolExecutionOutcome{
		Result: domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
			Content: fmt.Sprintf("tool execution failed after %d attempts: %v", retries+1, lastErr),
			IsError: true},
		AttemptCount: retries + 1, Kind: domain.ToolInfrastructureFailed, Cause: lastErr,
	}
}

// liveToolSink adapts a LiveCoalescer to the ToolOutputSink interface for
// per-tool-call live streaming.
type liveToolSink struct {
	coalescer *events.LiveCoalescer
}

func (s *liveToolSink) TryEmit(u domain.ToolOutputUpdate) bool {
	streamID := u.ToolCallID + ":" + u.Stream
	s.coalescer.Push(streamID, u.Data, time.Now())
	return true
}

// toolRetryPolicy resolves the retry policy for a tool by name.
func (l *Loop) toolRetryPolicy(toolName string) domain.ToolRetryPolicy {
	classifier, ok := l.Tools.(domain.ToolRetryClassifier)
	if !ok {
		return domain.ToolRetryPolicy{Mode: domain.ToolRetryNever, MaxRetries: 0}
	}
	return classifier.RetryPolicy(toolName)
}

// maxToolRetries returns the effective retry cap, respecting both the run
// config and the per-tool hard cap.
func maxToolRetries(configCap, toolCap int) int {
	cap := configCap
	if cap <= 0 {
		cap = 2
	}
	if toolCap > 0 && toolCap < cap {
		cap = toolCap
	}
	return cap
}
