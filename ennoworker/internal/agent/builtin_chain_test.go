package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixedRiskClassifier map[string]domain.RiskClass

func (m fixedRiskClassifier) RiskClass(name string) domain.RiskClass {
	if risk, ok := m[name]; ok {
		return risk
	}
	return domain.RiskSensitive
}

func mustPolicyJSON(t *testing.T, config domain.ToolPolicyConfig) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	return raw
}

// TestDefaultPolicyChainMatchesBuiltin proves the Stage 1 split reproduces the
// legacy BuiltinToolPolicy.BeforeToolBatch decision-by-decision across the five
// behavioural classes (Discuss gate, Ask-sensitive gate, allow_existing_behavior
// skip, allowlist deny, shell AST deny), including RiskClass propagation.
func TestDefaultPolicyChainMatchesBuiltin(t *testing.T) {
	risk := fixedRiskClassifier{
		"read":      domain.RiskReadOnly,
		"write":     domain.RiskLocalWrite,
		"bash":      domain.RiskShell,
		"exec":      domain.RiskShell,
		"web_fetch": domain.RiskExternal,
	}
	cases := []struct {
		name   string
		config domain.ToolPolicyConfig
		calls  []struct {
			name string
			args string
		}
	}{
		{
			name:   "discuss_denies_non_readonly",
			config: domain.ToolPolicyConfig{Mode: string(domain.PermissionDiscuss)},
			calls: []struct {
				name string
				args string
			}{
				{name: "read", args: `{}`},
				{name: "write", args: `{}`},
				{name: "bash", args: `{"command":"echo hi"}`},
			},
		},
		{
			name:   "ask_sensitive_and_approval",
			config: domain.ToolPolicyConfig{Mode: string(domain.PermissionAsk)},
			calls: []struct {
				name string
				args string
			}{
				{name: "read", args: `{}`},
				{name: "web_fetch", args: `{}`}, // external → require_approval
				{name: "bash", args: `{"command":"echo hi"}`},
			},
		},
		{
			name:   "allow_existing_behavior_skips_allowlist_and_shell",
			config: domain.ToolPolicyConfig{Mode: "allow_existing_behavior", AllowedTools: []string{"read"}},
			calls: []struct {
				name string
				args string
			}{
				{name: "read", args: `{}`},
				{name: "write", args: `{}`},                         // not allowlisted, but skipped
				{name: "bash", args: `{"command":"echo hi | cat"}`}, // pipe, but shell validation skipped
			},
		},
		{
			name:   "allowlist_deny",
			config: domain.ToolPolicyConfig{Mode: "auto", AllowedTools: []string{"read", "write"}},
			calls: []struct {
				name string
				args string
			}{
				{name: "read", args: `{}`},
				{name: "write", args: `{}`},
				{name: "bash", args: `{"command":"echo hi"}`},
			},
		},
		{
			name:   "shell_ast_deny",
			config: domain.ToolPolicyConfig{Mode: "auto"},
			calls: []struct {
				name string
				args string
			}{
				{name: "exec", args: `{"argv":["echo","hi"]}`},
				{name: "bash", args: `{"command":"echo hi | cat"}`}, // pipe denied
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := domain.PolicySnapshot{ID: "p", Version: 1, Config: mustPolicyJSON(t, tc.config)}
			builtin, err := NewBuiltinToolPolicy(snapshot, risk)
			require.NoError(t, err)
			chain, err := DefaultPolicyChain(snapshot)
			require.NoError(t, err)
			frozen, err := chain.Freeze()
			require.NoError(t, err)

			calls := make([]domain.ToolCall, len(tc.calls))
			execs := make([]*ToolExecution, len(tc.calls))
			for i, c := range tc.calls {
				calls[i] = domain.ToolCall{ID: "c", Name: c.name, Arguments: json.RawMessage(c.args)}
				execs[i] = &ToolExecution{
					RunID:     "r",
					CallIndex: i,
					Original:  calls[i],
					Effective: calls[i],
					RiskClass: risk.RiskClass(c.name),
				}
			}

			want, err := builtin.BeforeToolBatch(context.Background(), ToolBatchContext{}, calls)
			require.NoError(t, err)
			got, terminate, err := frozen.Preflight(context.Background(), execs)
			require.NoError(t, err)
			assert.False(t, terminate)
			assert.Equal(t, want, got)
		})
	}
}

// TestDefaultPolicyChainRedactMatchesBuiltin proves the post (redact) listener
// reproduces the legacy AfterToolCall projection.
func TestDefaultPolicyChainRedactMatchesBuiltin(t *testing.T) {
	risk := fixedRiskClassifier{}
	config := domain.ToolPolicyConfig{Mode: "auto", RedactPatterns: []string{`secret-\d+`}}
	snapshot := domain.PolicySnapshot{ID: "p", Version: 1, Config: mustPolicyJSON(t, config)}

	builtin, err := NewBuiltinToolPolicy(snapshot, risk)
	require.NoError(t, err)
	chain, err := DefaultPolicyChain(snapshot)
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	result := domain.ToolResult{Content: "found secret-123 and secret-456"}
	want, err := builtin.AfterToolCall(context.Background(), ToolCallContext{}, domain.ToolCall{}, result)
	require.NoError(t, err)
	got, err := frozen.Post(context.Background(), pipelineExec("read"), result)
	require.NoError(t, err)
	assert.Equal(t, want.Result, got.Result)
}
