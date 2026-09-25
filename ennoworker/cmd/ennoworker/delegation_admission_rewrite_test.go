package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDelegationAdmissionRewriteValidatesAgainstToolSchema pins the regression
// where the admission policy re-marshaled the delegation arguments through a
// narrow {tasks,delegations} struct. That injected `"delegations": null` into a
// canonical delegate_tasks call, which the additionalProperties:false schema
// rejected, failing the whole Run as tool_policy_failed before admission could
// even run. The rewrite must produce arguments that pass the registered schema
// for the exact tool name the model used, and keep every other argument key.
func TestDelegationAdmissionRewriteValidatesAgainstToolSchema(t *testing.T) {
	db, _, _ := newSessionDB(t)
	ctx := context.Background()
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "delegation-rewrite", HostPath: t.TempDir()})
	require.NoError(t, err)
	session := sqlCreateSession(t, db, project.ID)

	sources, models, modelID, _ := setupExecutorFileRoleDelegation(t)
	document := &rolesource.Document{
		SchemaVersion: 1, Handle: "auto-role", Name: "Auto",
		Description: "Automatically admitted.", Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: rolesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextFresh,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextFresh}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 1, MaxToolCalls: 1,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Read the file.",
	}
	_, _, err = sources.CreateRole(document)
	require.NoError(t, err)
	_, err = sources.PublishRoleRevision(document.Handle)
	require.NoError(t, err)

	hook := delegationAdmissionHook(&store.DelegationRepo{DB: db, RoleSources: sources, Models: models},
		session.ID, map[string]bool{})

	delegationTool := &tools.DelegateTasksTool{}
	registry, err := tools.NewRegistry(delegationTool)
	require.NoError(t, err)
	_, err = registry.RegisterAlias("delegate_roles", "delegate_tasks", delegationTool.LegacySchema())
	require.NoError(t, err)

	cases := []struct {
		name             string
		tool             string
		arguments        string
		wantOtherKey     string
		wantAbsentInList string
	}{
		{
			name: "canonical_tasks",
			tool: "delegate_tasks",
			arguments: `{"executionMode":"background","autoResume":true,"tasks":[
				{"name":"read","role":"auto-role","goal":"Read /workspace/notes.txt",
				 "budget":{"maxModelCalls":1,"maxToolCalls":1}}]}`,
			wantOtherKey: "delegations",
		},
		{
			name: "legacy_delegations",
			tool: "delegate_roles",
			arguments: `{"executionMode":"background","autoResume":true,"delegations":[
				{"name":"read","roleHandle":"auto-role","assignment":"Read /workspace/notes.txt",
				 "budget":{"maxModelCalls":1,"maxToolCalls":1}}]}`,
			wantOtherKey: "tasks",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &agent.ToolExecution{Effective: domain.ToolCall{
				ID: "delegate-1", Name: tc.tool, Arguments: json.RawMessage(tc.arguments)}}
			decision, err := hook(ctx, exec, func(*agent.ToolExecution) (agent.PreToolDecision, error) {
				return agent.PreToolDecision{Action: agent.ToolAllow}, nil
			})
			require.NoError(t, err)
			require.Equal(t, agent.ToolAllow, decision.Action)

			// The whole point: the rewritten arguments must satisfy the schema
			// registered under the very name the model used.
			require.NoError(t, registry.ValidateArguments(tc.tool, decision.Arguments))

			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(decision.Arguments, &decoded))
			// The unused task-list key must be gone, not null.
			_, present := decoded[tc.wantOtherKey]
			assert.Falsef(t, present, "rewritten arguments still carry %q", tc.wantOtherKey)
			// Model-supplied arguments other than the task list must survive.
			var executionMode string
			require.NoError(t, json.Unmarshal(decoded["executionMode"], &executionMode))
			assert.Equal(t, "background", executionMode)
			var autoResume bool
			require.NoError(t, json.Unmarshal(decoded["autoResume"], &autoResume))
			assert.True(t, autoResume)
			// The resolved immutable Role version is frozen into the item.
			assert.Contains(t, string(decision.Arguments), "roleVersionId")
		})
	}
}
