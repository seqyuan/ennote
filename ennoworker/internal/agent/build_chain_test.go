package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildRunPolicyChainDeterminism pins I6/G5: two chains built from the same
// frozen effective-config inputs (policy snapshot + project hooks + delegation)
// produce byte-identical decisions, so a resumed run reconstructs the same
// chain without re-reading workspace files.
func TestBuildRunPolicyChainDeterminism(t *testing.T) {
	snapshot := domain.PolicySnapshot{ID: "p", Version: 1,
		Config: mustPolicyJSON(t, domain.ToolPolicyConfig{Mode: "auto"})}
	hooks := []domain.ProjectToolPolicyHook{
		{Kind: "deny", Code: "block_rm", Reason: "no", When: domain.ProjectHookWhen{ToolName: "bash", CommandContains: "rm -rf"}},
		{Kind: "rewrite", Arguments: json.RawMessage(`{"command":"safe"}`), When: domain.ProjectHookWhen{ToolName: "bash"}},
		{Kind: "project", RedactPatterns: []string{`secret-\d+`}},
	}
	delegation := func(_ context.Context, _ *ToolExecution, next func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return next(nil)
	}

	build := func() (*FrozenPolicyChain, error) {
		chain, err := BuildRunPolicyChain(snapshot, hooks, delegation)
		if err != nil {
			return nil, err
		}
		return chain.Freeze()
	}
	chain1, err := build()
	require.NoError(t, err)
	chain2, err := build()
	require.NoError(t, err)

	execs := []*ToolExecution{
		{RunID: "r", CallIndex: 0, Original: domain.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /tmp/x"}`)}, Effective: domain.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf /tmp/x"}`)}, RiskClass: domain.RiskShell},
		{RunID: "r", CallIndex: 1, Original: domain.ToolCall{ID: "c2", Name: "read", Arguments: json.RawMessage(`{}`)}, Effective: domain.ToolCall{ID: "c2", Name: "read", Arguments: json.RawMessage(`{}`)}, RiskClass: domain.RiskReadOnly},
	}

	dec1, term1, err := chain1.Preflight(context.Background(), execs)
	require.NoError(t, err)
	dec2, term2, err := chain2.Preflight(context.Background(), execs)
	require.NoError(t, err)
	assert.Equal(t, dec1, dec2)
	assert.Equal(t, term1, term2)

	// The deny hook denied the rm -rf call; the read call stayed allowed.
	assert.Equal(t, ToolDeny, dec1[0].Action)
	assert.Equal(t, "block_rm", dec1[0].Code)
	assert.Equal(t, ToolAllow, dec1[1].Action)

	// Post-chain determinism.
	result := domain.ToolResult{Content: "saw secret-99"}
	post1, err := chain1.Post(context.Background(), execs[1], result)
	require.NoError(t, err)
	post2, err := chain2.Post(context.Background(), execs[1], result)
	require.NoError(t, err)
	assert.Equal(t, post1, post2)
	assert.Equal(t, "saw [REDACTED]", post1.Result.Content)
}

// TestBuildRunPolicyChainRejectsInvalidHook pins fail-loud at assembly time.
func TestBuildRunPolicyChainRejectsInvalidHook(t *testing.T) {
	snapshot := domain.PolicySnapshot{ID: "p", Version: 1,
		Config: mustPolicyJSON(t, domain.ToolPolicyConfig{Mode: "auto"})}
	_, err := BuildRunPolicyChain(snapshot, []domain.ProjectToolPolicyHook{{Kind: "unknown"}}, nil)
	assert.Error(t, err)
}
