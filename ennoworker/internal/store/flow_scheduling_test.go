package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schedulingToolMatrix is the representative classification matrix shared with
// the domain and graphrun tests: every entry must resolve to the same answer on
// every scheduling path.
var schedulingToolMatrix = []struct {
	tool     string
	readOnly bool
}{
	{"read", true},
	{"ls", true},
	{"grep", true},
	{"find", true},
	{"git_readonly", true},
	{"search_compacted_history", true},
	{"todo", true},
	{"ask_user", true},
	{"bash", false},
	{"exec", false},
	{"write", false},
	{"edit", false},
	{"publish_artifact", false},
	{"web_fetch", false},
	{"http", false},
	{"delegate_tasks", false},
	{"mcp__server__read", false},
	{"mcp__server__write", false},
	{"unknown_tool", false},
}

// TestFlowPublishResolverToolReadOnlyMatrix pins the publish resolver to the
// shared domain classifier, so the fan_out / writer-scope gate can never drift
// from what Graph Run freezing and the test fixtures believe.
func TestFlowPublishResolverToolReadOnlyMatrix(t *testing.T) {
	ctx := context.Background()
	resolver := store.NewFlowValidator(store.FlowPublishOptions{}).Resolver
	for _, tc := range schedulingToolMatrix {
		assert.Equal(t, tc.readOnly, resolver.ToolReadOnly(ctx, tc.tool),
			"publish resolver classification for %q", tc.tool)
		assert.Equal(t, domain.IsReadOnlySchedulingTool(tc.tool), resolver.ToolReadOnly(ctx, tc.tool),
			"publish resolver and domain classifier must agree for %q", tc.tool)
	}
}

// publishSchedulingRole publishes a file-native Role with an explicit authority
// and tool allowlist so publish-time scheduling can be exercised end to end.
func publishSchedulingRole(t *testing.T, stack *fileConfigStack, handle string,
	authority rolesource.Authority, tools []string) {
	t.Helper()
	document := &rolesource.Document{
		SchemaVersion: 1, Handle: handle, Name: handle,
		Description: "Scheduling fixture role", Positioning: "Scheduling fixture.",
		Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: stack.DefaultRef, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: authority,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: append([]string(nil), tools...),
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextRoom,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single", "parallel"},
			MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 200000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 1_800_000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Execute one task in a flow.",
	}
	_, _, err := stack.Sources.CreateRole(document)
	require.NoError(t, err)
	_, err = stack.Sources.PublishRoleRevision(handle)
	require.NoError(t, err)
}

// fanOutFlow is a one-task fan_out flow over the given role pin.
func fanOutFlow(id, roleRef string) *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion,
		ID:            id,
		Inputs:        map[string]domain.FlowPort{"target": {Type: domain.PortTypePath}},
		Outputs:       map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:        domain.FlowBudget{MaxTotalTokens: 200000},
		Tasks: map[string]domain.FlowTask{
			"scan": {
				Type:   domain.FlowTaskRole,
				Role:   roleRef,
				Goal:   "Scan {inputs.target}",
				FanOut: &domain.FlowFanOut{Min: 1, Max: 4},
			},
			"accept": {
				Terminal: &domain.FlowTerminal{Status: "success", Output: "report"},
				Depends:  []string{"scan"},
			},
		},
	}
}

// TestFlowPublishFanOutRequiresSharedReadOnlyClassifier proves publish-time
// scheduling now uses the conservative shared allowlist: a read_only Role that
// carries an MCP tool is no longer fan_out-eligible (the old graphrun copy
// accepted any mcp__* name without "__write__"), while a genuinely read-only
// Role keeps validating.
func TestFlowPublishFanOutRequiresSharedReadOnlyClassifier(t *testing.T) {
	ctx := context.Background()
	stack := newFileConfigStack(t)
	publishSchedulingRole(t, stack, "pure-reader", rolesource.AuthorityReadOnly,
		[]string{"read", "ls", "grep", "find", "git_readonly", "ask_user"})
	publishSchedulingRole(t, stack, "mcp-reader", rolesource.AuthorityReadOnly,
		[]string{"read", "mcp__server__read"})

	validator := store.NewFlowValidator(store.FlowPublishOptions{
		Sources: stack.Sources, Models: stack.ModelRepo, Skills: map[string]bool{},
	})

	// Regression guard: real read-only Roles keep their fan_out parallelism.
	pure := validator.Validate(ctx, fanOutFlow("fanout-pure", "pure-reader@1"))
	require.Empty(t, pure.Diagnostics, "read-only Role must stay fan_out-eligible")
	assert.True(t, pure.Valid)

	// A read_only Role containing an MCP tool must not be scheduled as read-only.
	mcp := validator.Validate(ctx, fanOutFlow("fanout-mcp", "mcp-reader@1"))
	assert.False(t, mcp.Valid)
	codes := make([]string, 0, len(mcp.Diagnostics))
	for _, diagnostic := range mcp.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	assert.Contains(t, codes, "fan_out_not_read_only")
}

// TestFlowFreezeSchedulingMatchesDomainClassifier proves the store freeze
// helper the flow tests rely on reports the same class as production.
func TestFlowFreezeSchedulingMatchesDomainClassifier(t *testing.T) {
	readOnlyRole := domain.RoleDefinition{Authority: domain.RoleAuthorityReadOnly,
		AllowedTools: []string{"read", "ls", "grep", "find", "git_readonly"}}
	assert.Equal(t, domain.RoleDefinitionIsReadOnly(readOnlyRole), testRoleDefinitionIsReadOnly(readOnlyRole))

	mcpRole := domain.RoleDefinition{Authority: domain.RoleAuthorityReadOnly,
		AllowedTools: []string{"read", "mcp__server__read"}}
	assert.False(t, testRoleDefinitionIsReadOnly(mcpRole))
	assert.Equal(t, domain.RoleDefinitionIsReadOnly(mcpRole), testRoleDefinitionIsReadOnly(mcpRole))
}
