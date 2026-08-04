package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// DelegationSpec is one delegation request inside the delegate_roles tool call.
type DelegationSpec struct {
	Name           string                   `json:"name"`
	RoleHandle     string                   `json:"roleHandle"`
	RoleVersionID  string                   `json:"roleVersionId,omitempty"`
	Assignment     string                   `json:"assignment"`
	OutputContract string                   `json:"outputContract"`
	Budget         domain.BudgetCeilingJSON `json:"budget"`
}

// DelegateRolesResult is the placeholder returned by delegate_roles.
type DelegateRolesResult struct {
	Status  string                    `json:"status"`
	GroupID string                    `json:"groupId"`
	Items   []DelegateRolesItemResult `json:"items"`
}

// DelegateRolesItemResult is one item placeholder.
type DelegateRolesItemResult struct {
	Name       string `json:"name"`
	ItemID     string `json:"itemId"`
	ChildRunID string `json:"childRunId"`
}

// DelegateRolesTool is the Host delegation tool. It executes the actual
// delegation (group + items + children) through the injected provider,
// then returns a placeholder result. The loop detects the run is now
// waiting_children and exits gracefully.
type DelegateRolesTool struct {
	Provider  DelegateRolesProvider
	RunID     string
	SessionID string
}

// DelegateRolesProvider injects the delegation execution dependencies.
type DelegateRolesProvider interface {
	ExecuteDelegation(ctx context.Context, runID, sessionID, toolCallID string, specs []DelegationSpec) (*DelegateRolesResult, error)
}

func (t *DelegateRolesTool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionExclusive }

func (t *DelegateRolesTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name:        "delegate_roles",
		Description: "Delegate work to one or more Role agents. Each delegation item runs a published Role with a specific assignment. The children execute independently and return structured results. Use this to parallelize research, code review, or analysis tasks.",
		Parameters:  schema(`{"type":"object","required":["delegations"],"properties":{"delegations":{"type":"array","minItems":1,"maxItems":16,"items":{"type":"object","required":["name","roleHandle","assignment","budget"],"properties":{"name":{"type":"string","description":"A short descriptive label for this delegation item"},"roleHandle":{"type":"string","description":"The published Role handle to delegate to"},"roleVersionId":{"type":"string","description":"System-resolved immutable Role version"},"assignment":{"type":"string","minLength":1,"maxLength":32768,"description":"The task assignment for the child Role"},"outputContract":{"type":"string","enum":["text-v1","structured-v1"],"description":"Format of the child's submit_result output"},"budget":{"type":"object","required":["maxModelCalls","maxToolCalls"],"properties":{"maxModelCalls":{"type":"integer","minimum":1,"maximum":64},"maxToolCalls":{"type":"integer","minimum":1,"maximum":256},"maxTotalTokens":{"type":"integer","minimum":1,"maximum":2000000},"maxOutputTokens":{"type":"integer","minimum":1,"maximum":131072},"maxCostUsdMicros":{"type":"integer","minimum":0,"maximum":100000000},"maxWallTimeMs":{"type":"integer","minimum":1000,"maximum":1800000}}},"additionalProperties":false}}}},"additionalProperties":false}`),
	}
}

func (t *DelegateRolesTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	if t.Provider == nil {
		return errorResult(call, fmt.Errorf("delegate_roles provider not configured")), nil
	}
	var args struct {
		Delegations []DelegationSpec `json:"delegations"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return errorResult(call, fmt.Errorf("invalid delegate_roles arguments: %w", err)), nil
	}
	if len(args.Delegations) == 0 || len(args.Delegations) > 16 {
		return errorResult(call, fmt.Errorf("delegations must have 1-16 items, got %d", len(args.Delegations))), nil
	}
	runID := t.RunID
	if runID == "" {
		runID, _ = ctx.Value(delegateRolesRunIDKey).(string)
	}
	sessionID := t.SessionID
	if sessionID == "" {
		sessionID, _ = ctx.Value(delegateRolesSessionIDKey).(string)
	}
	result, err := t.Provider.ExecuteDelegation(ctx, runID, sessionID, call.ID, args.Delegations)
	if err != nil {
		return errorResult(call, err), nil
	}
	payload, _ := json.Marshal(result)
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: string(payload)}, nil
}
