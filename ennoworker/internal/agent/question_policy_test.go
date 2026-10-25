package agent

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAskUserIsExemptFromTheDiscussAllowlist(t *testing.T) {
	raw, err := json.Marshal(domain.ToolPolicyConfig{
		Mode: string(domain.PermissionDiscuss), AllowedTools: []string{"read"},
	})
	require.NoError(t, err)
	policy, err := NewBuiltinToolPolicy(domain.PolicySnapshot{ID: "discuss", Kind: domain.PolicyKindTool, Version: 3, Config: raw}, testRiskMap)
	require.NoError(t, err)
	decisions, err := policy.BeforeToolBatch(t.Context(), ToolBatchContext{}, []domain.ToolCall{
		{ID: "q", Name: "ask_user", Arguments: json.RawMessage(`{"questions":[]}`)},
		{ID: "w", Name: "write", Arguments: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)
	assert.Equal(t, ToolAllow, decisions[0].Action)
	assert.Equal(t, ToolDeny, decisions[1].Action)
	assert.True(t, AllowsTool(raw, "ask_user"))
}
