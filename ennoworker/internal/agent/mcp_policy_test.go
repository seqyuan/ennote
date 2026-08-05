package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpExternalRiskClassifier resolves MCP-style tools to RiskExternal.
type mcpExternalRiskClassifier map[string]domain.RiskClass

func (m mcpExternalRiskClassifier) RiskClass(name string) domain.RiskClass {
	if risk, ok := m[name]; ok {
		return risk
	}
	return domain.RiskSensitive
}

func TestMCPToolPolicyAskRequiresApproval(t *testing.T) {
	risk := mcpExternalRiskClassifier{"pubmed__search": domain.RiskExternal}
	policy, err := NewBuiltinToolPolicy(domain.PolicySnapshot{
		ID: "ask", Kind: domain.PolicyKindTool, Version: 1,
		Config: mustJSON(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk)}),
	}, risk)
	require.NoError(t, err)

	decisions, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, []domain.ToolCall{
		{ID: "c1", Name: "pubmed__search", Arguments: json.RawMessage(`{"query":"cancer"}`)},
	})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, ToolRequireApproval, decisions[0].Action)
	assert.Equal(t, domain.RiskExternal, decisions[0].RiskClass)
}

func TestMCPToolPolicyDiscussDeniesExternal(t *testing.T) {
	risk := mcpExternalRiskClassifier{"pubmed__search": domain.RiskExternal}
	policy, err := NewBuiltinToolPolicy(domain.PolicySnapshot{
		ID: "discuss", Kind: domain.PolicyKindTool, Version: 1,
		Config: mustJSON(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionDiscuss),
			AllowedTools: []string{"pubmed__search"}}),
	}, risk)
	require.NoError(t, err)

	decisions, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, []domain.ToolCall{
		{ID: "c1", Name: "pubmed__search", Arguments: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)
	assert.Equal(t, ToolDeny, decisions[0].Action)
	assert.Equal(t, "permission_mode_discuss", decisions[0].Code)
}

func TestMCPToolPolicyUnregisteredFailsClosed(t *testing.T) {
	// A tool not present in the effective registry resolves RiskSensitive via
	// the real Registry, but here we simulate the classifier returning
	// sensitive for an unknown name (mirrors Registry.RiskClass fallback).
	risk := mcpExternalRiskClassifier{} // empty: everything sensitive
	policy, err := NewBuiltinToolPolicy(domain.PolicySnapshot{
		ID: "ask", Kind: domain.PolicyKindTool, Version: 1,
		Config: mustJSON(t, domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk)}),
	}, risk)
	require.NoError(t, err)

	decisions, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, []domain.ToolCall{
		{ID: "c1", Name: "ghost__tool", Arguments: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)
	assert.Equal(t, ToolDeny, decisions[0].Action)
	assert.Equal(t, "permission_mode_sensitive", decisions[0].Code)
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}
