package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeWorkspaceToolPolicyConfig(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, ".ennote")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0o600))
}

// TestApplyWorkspaceToolPolicyFreshTrusted pins the Stage 2a/2b wiring: a
// trusted workspace's patch replaces the tool policy config (whole row) and its
// hooks are frozen into the effective config — without touching the dispatcher.
func TestApplyWorkspaceToolPolicyFreshTrusted(t *testing.T) {
	executor := &agentExecutor{}
	run := &domain.AgentRun{ID: "r1"}
	effective := &domain.EffectiveRunConfig{ToolPolicy: domain.PolicySnapshot{ID: "policy-1", Version: 1, Config: json.RawMessage(`{"mode":"auto"}`)}}
	root := t.TempDir()
	writeWorkspaceToolPolicyConfig(t, root, `{
		"toolPolicyPatch": {"id":"policy-1","set":{"mode":"auto","allowedTools":["read","custom"]}},
		"toolPolicyHooks": [{"kind":"deny","code":"block_rm","reason":"no","when":{"toolName":"bash","commandContains":"rm -rf"}}]
	}`)

	err := executor.applyWorkspaceToolPolicy(context.Background(), run, effective, root, true)
	require.NoError(t, err)

	var config domain.ToolPolicyConfig
	require.NoError(t, json.Unmarshal(effective.ToolPolicy.Config, &config))
	assert.Equal(t, []string{"read", "custom"}, config.AllowedTools) // whole-row replacement
	require.Len(t, effective.ToolPolicyHooks, 1)
	assert.Equal(t, "block_rm", effective.ToolPolicyHooks[0].Code)
}

// TestApplyWorkspaceToolPolicyNoopWhenUntrustedOrResumed pins the trust gate and
// the fresh-run guard: neither an untrusted workspace nor a resumed run may
// re-derive the tool policy from files (G5).
func TestApplyWorkspaceToolPolicyNoopWhenUntrustedOrResumed(t *testing.T) {
	executor := &agentExecutor{}
	root := t.TempDir()
	writeWorkspaceToolPolicyConfig(t, root, `{
		"toolPolicyPatch": {"id":"policy-1","set":{"mode":"auto","allowedTools":["read"]}},
		"toolPolicyHooks": [{"kind":"deny","code":"block_rm"}]
	}`)

	// Untrusted: no-op.
	run := &domain.AgentRun{ID: "r1"}
	effective := &domain.EffectiveRunConfig{ToolPolicy: domain.PolicySnapshot{ID: "policy-1", Version: 1, Config: json.RawMessage(`{"mode":"auto"}`)}}
	require.NoError(t, executor.applyWorkspaceToolPolicy(context.Background(), run, effective, root, false))
	var config domain.ToolPolicyConfig
	require.NoError(t, json.Unmarshal(effective.ToolPolicy.Config, &config))
	assert.Empty(t, config.AllowedTools)
	assert.Empty(t, effective.ToolPolicyHooks)

	// Resumed (BaseMessageID set): no-op even when trusted.
	run = &domain.AgentRun{ID: "r2", BaseMessageID: "m1"}
	require.NoError(t, executor.applyWorkspaceToolPolicy(context.Background(), run, effective, root, true))
	require.NoError(t, json.Unmarshal(effective.ToolPolicy.Config, &config))
	assert.Empty(t, config.AllowedTools)
	assert.Empty(t, effective.ToolPolicyHooks)
}

// TestApplyWorkspaceToolPolicyPatchIDMismatchFails pins fail-loud on a patch
// targeting a different policy profile.
func TestApplyWorkspaceToolPolicyPatchIDMismatchFails(t *testing.T) {
	executor := &agentExecutor{}
	run := &domain.AgentRun{ID: "r1"}
	effective := &domain.EffectiveRunConfig{ToolPolicy: domain.PolicySnapshot{ID: "policy-1", Version: 1, Config: json.RawMessage(`{"mode":"auto"}`)}}
	root := t.TempDir()
	writeWorkspaceToolPolicyConfig(t, root, `{"toolPolicyPatch":{"id":"other-policy","set":{"mode":"auto"}}}`)

	err := executor.applyWorkspaceToolPolicy(context.Background(), run, effective, root, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match effective profile")
}

// TestApplyWorkspaceToolPolicyMalformedHookFails pins eager validation: a
// malformed hook fails the freeze, never reaching the chain build.
func TestApplyWorkspaceToolPolicyMalformedHookFails(t *testing.T) {
	executor := &agentExecutor{}
	run := &domain.AgentRun{ID: "r1"}
	effective := &domain.EffectiveRunConfig{ToolPolicy: domain.PolicySnapshot{ID: "policy-1", Version: 1, Config: json.RawMessage(`{"mode":"auto"}`)}}
	root := t.TempDir()
	writeWorkspaceToolPolicyConfig(t, root, `{"toolPolicyHooks":[{"kind":"unknown"}]}`)

	err := executor.applyWorkspaceToolPolicy(context.Background(), run, effective, root, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown project hook kind")
}
