package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// DelegateTasksResult is the placeholder returned by delegate_tasks.
type DelegateTasksResult struct {
	Status        string                   `json:"status"`
	GroupID       string                   `json:"groupId"`
	HandleID      string                   `json:"handleId,omitempty"`
	ExecutionMode string                   `json:"executionMode"`
	Items         []DelegateTasksItemResult `json:"items"`
}

// DelegateTasksItemResult is one task placeholder.
type DelegateTasksItemResult struct {
	Name       string `json:"name"`
	ItemID     string `json:"itemId"`
	ChildRunID string `json:"childRunId"`
}

// DelegateTasksTool is the Host delegation tool. It executes a bounded task
// graph (role + goal + skills + optional depends) through the injected
// provider, then returns a placeholder result. The loop detects the run is now
// waiting_children and exits gracefully.
type DelegateTasksTool struct {
	Provider  DelegateTasksProvider
	RunID     string
	SessionID string
}

// DelegateTasksProvider injects the delegation execution dependencies.
type DelegateTasksProvider interface {
	ExecuteDelegation(ctx context.Context, runID, sessionID, toolCallID string, specs []domain.TaskSpec) (*DelegateTasksResult, error)
}

func (t *DelegateTasksTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionExclusive }

// delegateTasksSchema is the model-visible argument schema (v1: flat batch).
// The tasks array elements carry role/goal/skills/depends; depends is reserved
// for the Stage 1 dynamic task graph and rejected by resolution until then.
func (t *DelegateTasksTool) delegateTasksSchema() string {
	return `{"type":"object","required":["tasks"],"properties":{"executionMode":{"type":"string","enum":["blocking","background"],"default":"blocking","description":"blocking keeps the parent waiting for tasks; background returns a handle and never blocks"},"autoResume":{"type":"boolean","default":false,"description":"when true and the parent session is idle on the source branch, the completion is delivered as an automatic follow-up Host run (off by default)"},"tasks":{"type":"array","minItems":1,"maxItems":16,"items":{"type":"object","required":["name","role","goal","budget"],"properties":{"name":{"type":"string","description":"A short descriptive label for this task"},"role":{"type":"string","description":"The published Role handle to run this task with"},"roleVersionId":{"type":"string","description":"System-resolved immutable Role version"},"goal":{"type":"string","minLength":1,"maxLength":32768,"description":"The task goal for the child Role"},"skills":{"type":"array","items":{"type":"string"},"description":"Optional Skill IDs bound to this task (empty inherits)"},"depends":{"type":"array","items":{"type":"string"},"description":"Task names in this batch that must settle first (reserved)"},"outputContract":{"type":"string","enum":["text-v1","structured-v1"],"description":"Format of the child's submit_result output"},"budget":{"type":"object","required":["maxModelCalls","maxToolCalls"],"properties":{"maxModelCalls":{"type":"integer","minimum":1,"maximum":64},"maxToolCalls":{"type":"integer","minimum":1,"maximum":256},"maxTotalTokens":{"type":"integer","minimum":1,"maximum":2000000},"maxOutputTokens":{"type":"integer","minimum":1,"maximum":131072},"maxCostUsdMicros":{"type":"integer","minimum":0,"maximum":100000000},"maxWallTimeMs":{"type":"integer","minimum":1000,"maximum":1800000}}},"additionalProperties":false}}}},"additionalProperties":false}`
}

// LegacySchema returns the pre-rename delegate_roles argument schema.
// RegisterAlias uses it so tool calls persisted before the rename (approval
// records, replayed calls of resumed runs) keep validating.
func (t *DelegateTasksTool) LegacySchema() string {
	return `{"type":"object","required":["delegations"],"properties":{"executionMode":{"type":"string","enum":["blocking","background"],"default":"blocking","description":"blocking keeps the parent waiting for children; background returns a handle and never blocks"},"autoResume":{"type":"boolean","default":false,"description":"when true and the parent session is idle on the source branch, the completion is delivered as an automatic follow-up Host run (off by default)"},"delegations":{"type":"array","minItems":1,"maxItems":16,"items":{"type":"object","required":["name","roleHandle","assignment","budget"],"properties":{"name":{"type":"string","description":"A short descriptive label for this delegation item"},"roleHandle":{"type":"string","description":"The published Role handle to delegate to"},"roleVersionId":{"type":"string","description":"System-resolved immutable Role version"},"assignment":{"type":"string","minLength":1,"maxLength":32768,"description":"The task assignment for the child Role"},"outputContract":{"type":"string","enum":["text-v1","structured-v1"],"description":"Format of the child's submit_result output"},"budget":{"type":"object","required":["maxModelCalls","maxToolCalls"],"properties":{"maxModelCalls":{"type":"integer","minimum":1,"maximum":64},"maxToolCalls":{"type":"integer","minimum":1,"maximum":256},"maxTotalTokens":{"type":"integer","minimum":1,"maximum":2000000},"maxOutputTokens":{"type":"integer","minimum":1,"maximum":131072},"maxCostUsdMicros":{"type":"integer","minimum":0,"maximum":100000000},"maxWallTimeMs":{"type":"integer","minimum":1000,"maximum":1800000}}},"additionalProperties":false}}}},"additionalProperties":false}`
}

func (t *DelegateTasksTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name:        "delegate_tasks",
		Description: "Delegate a bounded task graph to Role agents. Each task runs a published Role with a specific goal; tasks without dependencies run in parallel and tasks with `depends` start after their dependencies settle. Use this to parallelize research, code review, or multi-stage analysis. In background mode the parent continues immediately and delivery happens through a durable handle; auto-resume is optional and off by default.",
		Parameters:  json.RawMessage(t.delegateTasksSchema()),
		RiskClass:   domain.RiskDelegation,
	}
}

func (t *DelegateTasksTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	if t.Provider == nil {
		return errorResult(call, fmt.Errorf("delegate_tasks provider not configured")), nil
	}
	var args struct {
		ExecutionMode string            `json:"executionMode"`
		AutoResume    bool              `json:"autoResume"`
		Tasks         []domain.TaskSpec `json:"tasks"`
		Delegations   []domain.TaskSpec `json:"delegations"` // legacy replay compat
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid delegate_tasks arguments: %w", err)), nil
	}
	specs := args.Tasks
	if len(specs) == 0 {
		specs = args.Delegations // legacy delegate_roles shape
	}
	if len(specs) == 0 || len(specs) > 16 {
		return errorResult(call, fmt.Errorf("tasks must have 1-16 items, got %d", len(specs))), nil
	}
	for index := range specs {
		specs[index].Normalize()
	}
	// Freeze delivery semantics into the context so the provider and approval
	// digest see exactly what the client asked for.
	if args.ExecutionMode == "" {
		args.ExecutionMode = "blocking"
	}
	ctx = context.WithValue(ctx, delegateExecutionModeKey, args.ExecutionMode)
	ctx = context.WithValue(ctx, delegateAutoResumeKey, args.AutoResume)
	runID := t.RunID
	if runID == "" {
		runID, _ = ctx.Value(delegateRolesRunIDKey).(string)
	}
	sessionID := t.SessionID
	if sessionID == "" {
		sessionID, _ = ctx.Value(delegateRolesSessionIDKey).(string)
	}
	result, err := t.Provider.ExecuteDelegation(ctx, runID, sessionID, call.ID, specs)
	if err != nil {
		return errorResult(call, err), nil
	}
	payload, _ := json.Marshal(result)
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: string(payload)}, nil
}
