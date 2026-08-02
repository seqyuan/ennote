package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func restrictedPolicy(t *testing.T) *BuiltinToolPolicy {
	t.Helper()
	config, err := json.Marshal(domain.ToolPolicyConfig{Mode: "restricted",
		AllowedTools: []string{"read", "bash", "exec"}, AllowedExecutables: []string{"git", "rg"},
		DeniedSubcommands: map[string][]string{"git": {"push", "clean"}}, AllowPipes: true,
		AllowedWriteRoots: []string{"/workspace"}, MaxTimeoutSeconds: 30,
		RedactPatterns: []string{`secret=[^\s]+`}})
	require.NoError(t, err)
	policy, err := NewBuiltinToolPolicy(domain.PolicySnapshot{ID: "policy", Kind: domain.PolicyKindTool, Version: 1, Config: config})
	require.NoError(t, err)
	return policy
}

func permissionPolicy(t *testing.T, config domain.ToolPolicyConfig) *BuiltinToolPolicy {
	t.Helper()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	policy, err := NewBuiltinToolPolicy(domain.PolicySnapshot{ID: "permission", Kind: domain.PolicyKindTool, Version: 1, Config: raw})
	require.NoError(t, err)
	return policy
}

func TestDiscussAllowsOnlyReadOnlyRisk(t *testing.T) {
	policy := permissionPolicy(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionDiscuss),
		AllowedTools: []string{"read", "ls", "grep", "find", "search_compacted_history"}})
	decisions, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, []domain.ToolCall{
		{Name: "read"}, {Name: "write"}, {Name: "bash"}, {Name: "future_tool"},
	})
	require.NoError(t, err)
	assert.Equal(t, ToolAllow, decisions[0].Action)
	assert.Equal(t, domain.RiskReadOnly, decisions[0].RiskClass)
	for index, risk := range []domain.RiskClass{domain.RiskLocalWrite, domain.RiskShell, domain.RiskSensitive} {
		assert.Equal(t, ToolDeny, decisions[index+1].Action)
		assert.Equal(t, "permission_mode_discuss", decisions[index+1].Code)
		assert.Equal(t, risk, decisions[index+1].RiskClass)
	}
}

func TestAskRequiresApprovalOnlyAfterSafetyChecks(t *testing.T) {
	policy := permissionPolicy(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk),
		DeniedSubcommands: map[string][]string{"git": {"push"}}, MaxTimeoutSeconds: 30})
	decisions, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, []domain.ToolCall{
		{Name: "read", Arguments: json.RawMessage(`{"path":"notes.txt"}`)},
		{Name: "write", Arguments: json.RawMessage(`{"path":"notes.txt","content":"ok"}`)},
		{Name: "exec", Arguments: json.RawMessage(`{"argv":["git","status"]}`)},
		{Name: "exec", Arguments: json.RawMessage(`{"argv":["git","push"]}`)},
		{Name: "future_tool", Arguments: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)
	assert.Equal(t, ToolAllow, decisions[0].Action)
	assert.Equal(t, ToolRequireApproval, decisions[1].Action)
	assert.Equal(t, ToolRequireApproval, decisions[2].Action)
	assert.Equal(t, ToolDeny, decisions[3].Action)
	assert.Equal(t, "process_not_allowed", decisions[3].Code)
	assert.Equal(t, ToolDeny, decisions[4].Action)
	assert.Equal(t, "permission_mode_sensitive", decisions[4].Code)
}

func TestApprovalDigestAndPreviewAreStableAndRedacted(t *testing.T) {
	policy := permissionPolicy(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk),
		RedactPatterns: []string{`secret=[^\s\"]+`}})
	plans := []plannedToolCall{{
		original:  domain.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(`{"path":"a","token":"raw"}`)},
		effective: domain.ToolCall{ID: "call", Name: "write", Arguments: json.RawMessage(`{"path":"a","token":"raw"}`)},
		decision:  ToolDecision{Action: ToolRequireApproval, RiskClass: domain.RiskLocalWrite}, requiresApproval: true,
	}}
	snapshot := domain.PolicySnapshot{ID: "ask", Version: 1}
	assert.Equal(t, approvalBatchDigest(plans, snapshot), approvalBatchDigest(plans, snapshot))
	items := approvalItems(plans, policy)
	require.Len(t, items, 1)
	assert.Contains(t, items[0].ArgumentsPreview, `"path":"a"`)
	assert.NotContains(t, items[0].ArgumentsPreview, "raw")
	plans[0].effective.Arguments = json.RawMessage(`{"path":"b"}`)
	assert.NotEqual(t, approvalBatchDigest(plans, snapshot), approvalBatchDigest([]plannedToolCall{{
		original: plans[0].original, effective: plans[0].original, decision: plans[0].decision, requiresApproval: true,
	}}, snapshot))
}

func TestAutoStillAppliesProcessSafetyPolicy(t *testing.T) {
	policy := permissionPolicy(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAuto),
		DeniedSubcommands: map[string][]string{"git": {"push"}}, MaxTimeoutSeconds: 30})
	decisions, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, []domain.ToolCall{
		{Name: "write", Arguments: json.RawMessage(`{}`)},
		{Name: "exec", Arguments: json.RawMessage(`{"argv":["git","push"]}`)},
	})
	require.NoError(t, err)
	assert.Equal(t, ToolAllow, decisions[0].Action)
	assert.Equal(t, domain.RiskLocalWrite, decisions[0].RiskClass)
	assert.Equal(t, ToolDeny, decisions[1].Action)
	assert.Equal(t, "process_not_allowed", decisions[1].Code)
	assert.Equal(t, domain.RiskShell, decisions[1].RiskClass)
}

func TestBuiltinToolPolicyChecksStructuredExecAndBashAST(t *testing.T) {
	policy := restrictedPolicy(t)
	calls := []domain.ToolCall{
		{ID: "1", Name: "exec", Arguments: json.RawMessage(`{"argv":["git","status"]}`)},
		{ID: "2", Name: "exec", Arguments: json.RawMessage(`{"argv":["git","push"]}`)},
		{ID: "3", Name: "bash", Arguments: json.RawMessage(`{"command":"rg TODO . | rg agent"}`)},
		{ID: "4", Name: "bash", Arguments: json.RawMessage(`{"command":"rg $(cat token) ."}`)},
		{ID: "5", Name: "write", Arguments: json.RawMessage(`{}`)},
	}
	decisions, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, calls)
	require.NoError(t, err)
	assert.Equal(t, ToolAllow, decisions[0].Action)
	assert.Equal(t, ToolDeny, decisions[1].Action)
	assert.Equal(t, ToolAllow, decisions[2].Action)
	assert.Equal(t, ToolDeny, decisions[3].Action)
	assert.Equal(t, ToolDeny, decisions[4].Action)
}

type scriptedToolPolicy struct {
	before []ToolDecision
	after  func(ToolCallContext, domain.ToolResult) AfterToolDecision
}

func (p scriptedToolPolicy) BeforeToolBatch(context.Context, ToolBatchContext, []domain.ToolCall) ([]ToolDecision, error) {
	return p.before, nil
}
func (p scriptedToolPolicy) AfterToolCall(_ context.Context, call ToolCallContext, _ domain.ToolCall, result domain.ToolResult) (AfterToolDecision, error) {
	if p.after != nil {
		return p.after(call, result), nil
	}
	return AfterToolDecision{Result: result}, nil
}

func TestToolPolicyTerminatePreflightStartsNoTool(t *testing.T) {
	var executions atomic.Int32
	tools := &fakeTools{execute: func(context.Context, domain.ToolCall) (domain.ToolResult, error) {
		executions.Add(1)
		return domain.ToolResult{}, nil
	}}
	loop := &Loop{Tools: tools, Events: &memoryWriter{}, ToolPolicy: scriptedToolPolicy{before: []ToolDecision{
		{Action: ToolAllow}, {Action: ToolTerminateBatch, Code: "blocked", Reason: "blocked"},
	}}}
	_, err := loop.executeToolBatch(context.Background(), "run", 1, []domain.ToolCall{{ID: "1", Name: "read"}, {ID: "2", Name: "bash"}})
	assert.Equal(t, domain.ErrorToolPolicyTerminated, domain.ErrorCodeOf(err))
	assert.Zero(t, executions.Load())
}

func TestSafeParallelAfterPolicyRunsInCallIndexOrder(t *testing.T) {
	var mu sync.Mutex
	var order []int
	policy := scriptedToolPolicy{before: []ToolDecision{{Action: ToolAllow}, {Action: ToolAllow}}, after: func(call ToolCallContext, result domain.ToolResult) AfterToolDecision {
		mu.Lock()
		order = append(order, call.CallIndex)
		mu.Unlock()
		return AfterToolDecision{Result: result}
	}}
	tools := &fakeTools{classes: map[string]domain.ExecutionClass{"read": domain.ExecutionReadOnly}, execute: func(_ context.Context, call domain.ToolCall) (domain.ToolResult, error) {
		if call.ID == "0" {
			time.Sleep(20 * time.Millisecond)
		}
		return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name}, nil
	}}
	loop := &Loop{Tools: tools, Events: &memoryWriter{}, ToolPolicy: policy,
		ToolExecution: domain.ToolExecutionConfig{Mode: "safe_parallel", MaxConcurrentReadTools: 2}}
	_, err := loop.executeToolBatch(context.Background(), "run", 1, []domain.ToolCall{{ID: "0", Name: "read"}, {ID: "1", Name: "read"}})
	require.NoError(t, err)
	assert.Equal(t, []int{0, 1}, order)
}

func TestBuiltinToolPolicyRedactsProjectedResult(t *testing.T) {
	policy := restrictedPolicy(t)
	decision, err := policy.AfterToolCall(context.Background(), ToolCallContext{}, domain.ToolCall{},
		domain.ToolResult{Content: "ok secret=value"})
	require.NoError(t, err)
	assert.Equal(t, "ok [REDACTED]", decision.Result.Content)
}

func TestTodoPolicyRespectsExplicitAllowlists(t *testing.T) {
	ask := permissionPolicy(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk)})
	auto := permissionPolicy(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAuto)})
	restricted := permissionPolicy(t, domain.ToolPolicyConfig{
		Mode: "restricted", AllowedTools: []string{"read"},
	})

	assert.Equal(t, ToolAllow, singleDecision(t, ask, "todo").Action)
	assert.Equal(t, ToolAllow, singleDecision(t, auto, "todo").Action)
	denied := singleDecision(t, restricted, "todo")
	assert.Equal(t, ToolDeny, denied.Action)
	assert.Equal(t, "tool_not_allowed", denied.Code)
}

func singleDecision(t *testing.T, policy *BuiltinToolPolicy, toolName string) ToolDecision {
	t.Helper()
	decisions, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, []domain.ToolCall{{
		Name: toolName, Arguments: json.RawMessage(`{"todos":[]}`),
	}})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	return decisions[0]
}
