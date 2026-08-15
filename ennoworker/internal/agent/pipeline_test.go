package agent

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pipelineExec(name string) *ToolExecution {
	return &ToolExecution{
		RunID: "run-1", Iteration: 0, CallIndex: 0,
		Original:  domain.ToolCall{ID: "c1", Name: name},
		Effective: domain.ToolCall{ID: "c1", Name: name},
		RiskClass: domain.RiskShell,
	}
}

func TestPolicyChainDefaultAllowCarriesRiskClass(t *testing.T) {
	chain := NewPolicyChain()
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	decisions, terminate, err := frozen.Preflight(context.Background(), []*ToolExecution{pipelineExec("bash")})
	require.NoError(t, err)
	assert.False(t, terminate)
	assert.Equal(t, ToolAllow, decisions[0].Action)
	assert.Equal(t, domain.RiskShell, decisions[0].RiskClass)
}

func TestPolicyChainStickyDenyRejectsOverride(t *testing.T) {
	chain := NewPolicyChain()
	// Upstream wrapper registered FIRST; it delegates to the downstream deny and
	// then tries to override it with allow.
	_, err := chain.RegisterPre(func(ctx context.Context, exec *ToolExecution, next func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		_, _ = next(exec)
		return PreToolDecision{Action: ToolAllow}, nil
	}, false)
	require.NoError(t, err)
	_, err = chain.RegisterPre(func(_ context.Context, _ *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return PreToolDecision{Action: ToolDeny, Code: "inner", Reason: "no"}, nil
	}, false)
	require.NoError(t, err)

	frozen, err := chain.Freeze()
	require.NoError(t, err)
	_, _, err = frozen.Preflight(context.Background(), []*ToolExecution{pipelineExec("bash")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deny_override_attempted")
}

func TestPolicyChainRejectsShortCircuitAllow(t *testing.T) {
	chain := NewPolicyChain()
	_, err := chain.RegisterPre(func(_ context.Context, _ *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return PreToolDecision{Action: ToolAllow}, nil
	}, false)
	require.NoError(t, err)
	_, err = chain.RegisterPre(func(_ context.Context, _ *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return PreToolDecision{Action: ToolAllow}, nil
	}, false)
	require.NoError(t, err)

	frozen, err := chain.Freeze()
	require.NoError(t, err)
	_, _, err = frozen.Preflight(context.Background(), []*ToolExecution{pipelineExec("bash")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allow_short_circuit_attempted")
}

func TestPolicyChainDenyShortCircuitAllowed(t *testing.T) {
	chain := NewPolicyChain()
	_, err := chain.RegisterPre(func(_ context.Context, _ *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return PreToolDecision{Action: ToolDeny, Code: "first", Reason: "no"}, nil
	}, false)
	require.NoError(t, err)
	_, err = chain.RegisterPre(func(_ context.Context, _ *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return PreToolDecision{Action: ToolDeny, Code: "second", Reason: "no"}, nil
	}, false)
	require.NoError(t, err)

	frozen, err := chain.Freeze()
	require.NoError(t, err)
	decisions, _, err := frozen.Preflight(context.Background(), []*ToolExecution{pipelineExec("bash")})
	require.NoError(t, err)
	assert.Equal(t, ToolDeny, decisions[0].Action)
	assert.Equal(t, "first", decisions[0].Code) // first listener short-circuits
}

func TestPolicyChainFreezeRejectsRegistration(t *testing.T) {
	chain := NewPolicyChain()
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	_, err = chain.RegisterPre(func(_ context.Context, _ *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return PreToolDecision{Action: ToolAllow}, nil
	}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "frozen")

	decisions, _, err := frozen.Preflight(context.Background(), []*ToolExecution{pipelineExec("bash")})
	require.NoError(t, err)
	assert.Equal(t, ToolAllow, decisions[0].Action)
}

func TestPolicyChainDisposerNoOpAfterFreeze(t *testing.T) {
	chain := NewPolicyChain()
	dispose, err := chain.RegisterPre(func(_ context.Context, _ *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return PreToolDecision{Action: ToolDeny, Code: "kept", Reason: "no"}, nil
	}, false)
	require.NoError(t, err)

	frozen, err := chain.Freeze()
	require.NoError(t, err)
	dispose() // no-op after freeze

	decisions, _, err := frozen.Preflight(context.Background(), []*ToolExecution{pipelineExec("bash")})
	require.NoError(t, err)
	assert.Equal(t, ToolDeny, decisions[0].Action) // entry still present
}

type fixedDecisionPolicy struct{}

func (fixedDecisionPolicy) BeforeToolBatch(_ context.Context, _ ToolBatchContext, calls []domain.ToolCall) ([]ToolDecision, error) {
	decisions := make([]ToolDecision, len(calls))
	for i := range calls {
		decisions[i] = ToolDecision{Action: ToolAllow, RiskClass: domain.RiskShell}
	}
	return decisions, nil
}

func (fixedDecisionPolicy) AfterToolCall(_ context.Context, _ ToolCallContext, _ domain.ToolCall, result domain.ToolResult) (AfterToolDecision, error) {
	return AfterToolDecision{Result: result}, nil
}

func TestPolicyChainFromToolPolicyMatchesDirectCall(t *testing.T) {
	policy := fixedDecisionPolicy{}
	chain, err := NewPolicyChainFromToolPolicy(policy)
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	exec := pipelineExec("bash")
	decisions, terminate, err := frozen.Preflight(context.Background(), []*ToolExecution{exec})
	require.NoError(t, err)
	assert.False(t, terminate)

	direct, err := policy.BeforeToolBatch(context.Background(), ToolBatchContext{}, []domain.ToolCall{exec.Effective})
	require.NoError(t, err)
	assert.Equal(t, direct, decisions)
}
