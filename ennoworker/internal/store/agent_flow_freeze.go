package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// FreezeFlowDefinition resolves every task of a flow version into its frozen
// node snapshot: topological order (depends first), exact role@version binding
// (or a flow-scoped bare-handle role when flowID owns it), skill catalog
// resolution, goal digest, and the per-task budget ceiling.
// Freeze failures are fail-loud diagnostics; nothing is persisted until every
// task freezes.
//
// Phase 1 admission: roles with delegation admission 'denied' or
// 'approval_required' reject the flow run — the flow start request is the
// authorization and must not widen a Role's admission policy.
func (r *AgentFlowRunRepo) FreezeFlowDefinition(ctx context.Context, projectID, flowID string,
	def *domain.FlowDefinition, inputsJSON json.RawMessage) ([]FlowNodeFreeze, []string, error) {
	var diagnostics []string
	add := func(format string, args ...any) {
		diagnostics = append(diagnostics, fmt.Sprintf(format, args...))
	}
	order, err := agentflow.TopologicalOrder(def.Tasks)
	if err != nil {
		return nil, nil, err
	}
	indexOf := make(map[string]int, len(order))
	for i, name := range order {
		indexOf[name] = i
	}
	var inputs map[string]any
	var vars map[string]any
	if len(inputsJSON) > 0 {
		var wrapped FlowDefinitionInputs
		if err := json.Unmarshal(inputsJSON, &wrapped); err != nil {
			return nil, nil, fmt.Errorf("flow inputs are not valid JSON: %w", err)
		}
		inputs = wrapped.Inputs
		vars = wrapped.Vars
	}
	_ = vars
	// Required input ports must be present in the run request.
	for name, port := range def.Inputs {
		if port.Required {
			if _, ok := inputs[name]; !ok {
				add("input %q is required but missing from the run request", name)
			}
		}
	}
	freeze := make([]FlowNodeFreeze, 0, len(order))
	for _, name := range order {
		task := def.Tasks[name]
		node := FlowNodeFreeze{TaskIndex: indexOf[name], Handle: name, GoalDigest: agentflow.TaskGoalDigest(task.Goal)}
		if task.Terminal != nil {
			// Terminal gates complete the flow; they freeze no Role binding.
			node.BudgetJSON = json.RawMessage(`{}`)
			freeze = append(freeze, node)
			continue
		}
		switch task.Type {
		case domain.FlowTaskCheck:
			node.BudgetJSON = json.RawMessage(`{}`)
			freeze = append(freeze, node)
			continue
		case domain.FlowTaskRole:
		default:
			add("task %q has unsupported type %q", name, task.Type)
			continue
		}
		versionID, definitionJSON, err := r.ResolveFlowRoleVersion(ctx, task.Role, projectID, flowID)
		if err != nil {
			add("task %q: %v", name, err)
			continue
		}
		var roleDef domain.RoleDefinition
		if err := json.Unmarshal(definitionJSON, &roleDef); err != nil {
			add("task %q: decode Role definition: %v", name, err)
			continue
		}
		switch roleDef.DelegationPolicy.Admission {
		case domain.DelegationAutoWithinBudget:
		case domain.DelegationDenied:
			add("task %q: Role %s denies Host delegation", name, task.Role)
			continue
		case domain.DelegationApprovalRequired:
			add("task %q: Role %s requires explicit delegation approval; Phase 1 flows only admit auto_within_budget Roles", name, task.Role)
			continue
		default:
			add("task %q: Role %s has an invalid admission policy", name, task.Role)
			continue
		}
		skillIDs, err := r.ResolveFlowSkills(ctx, task.Skills)
		if err != nil {
			add("task %q: %v", name, err)
			continue
		}
		ceiling := roleDef.DelegationPolicy.BudgetCeiling
		if task.Budget != nil && task.Budget.Tokens > 0 {
			if task.Budget.Tokens > ceiling.MaxTotalTokens {
				add("task %q: budget tokens %d exceed the Role ceiling %d", name, task.Budget.Tokens, ceiling.MaxTotalTokens)
				continue
			}
			ceiling.MaxTotalTokens = task.Budget.Tokens
		}
		budgetJSON, _ := json.Marshal(domain.BudgetCeilingJSON{
			MaxModelCalls: ceiling.MaxModelCalls, MaxToolCalls: ceiling.MaxToolCalls,
			MaxTotalTokens: ceiling.MaxTotalTokens, MaxOutputTokens: ceiling.MaxOutputTokens,
			MaxCostMicros: ceiling.MaxCostUSDMicros, MaxWallTimeMS: ceiling.MaxWallTimeMS,
		})
		node.RoleVersionID = versionID
		node.SkillIDs = skillIDs
		node.BudgetJSON = budgetJSON
		// Freeze the scheduler concurrency class: reader (role is read-only)
		// vs writer (can mutate). Writes scope is frozen verbatim from the
		// task declaration; empty means the whole workspace (exclusive lane).
		node.ReadOnly = roleDefinitionIsReadOnly(roleDef)
		node.Writes = append([]string(nil), task.Writes...)
		freeze = append(freeze, node)
	}
	if len(diagnostics) > 0 {
		return nil, diagnostics, fmt.Errorf("flow freeze failed: %s", diagnostics[0])
	}
	return freeze, nil, nil
}

// FlowDefinitionInputs separates run inputs from runtime vars.
type FlowDefinitionInputs struct {
	Inputs map[string]any `json:"inputs,omitempty"`
	Vars   map[string]any `json:"vars,omitempty"`
}

// NormalizeFlowInputs builds the frozen inputs payload from user-supplied
// inputs and vars, validating input names against the declared ports.
func NormalizeFlowInputs(def *domain.FlowDefinition, inputs, vars map[string]any) (json.RawMessage, error) {
	normalized := FlowDefinitionInputs{Inputs: make(map[string]any), Vars: vars}
	unknown := make([]string, 0)
	for name, value := range inputs {
		if _, declared := def.Inputs[name]; !declared {
			unknown = append(unknown, name)
			continue
		}
		normalized.Inputs[name] = value
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown flow inputs: %s", joinStrings(unknown, ", "))
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func joinStrings(values []string, sep string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}
