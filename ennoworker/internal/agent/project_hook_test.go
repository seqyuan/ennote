package agent

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

func TestCompileProjectHookDenyShortCircuits(t *testing.T) {
	compiled, err := CompileProjectHook(domain.ProjectToolPolicyHook{
		Kind:   "deny",
		Code:   "dangerous_command",
		Reason: "no",
		When:   domain.ProjectHookWhen{ToolName: "bash"},
	})
	require.NoError(t, err)
	chain := NewPolicyChain()
	_, err = chain.RegisterPre(compiled.Pre, false)
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	exec := pipelineExec("bash")
	decisions, _, err := frozen.Preflight(context.Background(), []*ToolExecution{exec})
	require.NoError(t, err)
	assert.Equal(t, ToolDeny, decisions[0].Action)
	assert.Equal(t, "dangerous_command", decisions[0].Code)

	// Non-matching tool delegates to the default allow.
	exec = pipelineExec("read")
	decisions, _, err = frozen.Preflight(context.Background(), []*ToolExecution{exec})
	require.NoError(t, err)
	assert.Equal(t, ToolAllow, decisions[0].Action)
}

func TestCompileProjectHookRewritePreservesDownstreamDeny(t *testing.T) {
	rewrite, err := CompileProjectHook(domain.ProjectToolPolicyHook{
		Kind:      "rewrite",
		Arguments: json.RawMessage(`{"command":"safe"}`),
		When:      domain.ProjectHookWhen{ToolName: "bash"},
	})
	require.NoError(t, err)

	chain := NewPolicyChain()
	// Deny listener downstream of the rewrite listener.
	_, err = chain.RegisterPre(rewrite.Pre, false)
	require.NoError(t, err)
	_, err = chain.RegisterPre(func(_ context.Context, _ *ToolExecution, _ func(*ToolExecution) (PreToolDecision, error)) (PreToolDecision, error) {
		return PreToolDecision{Action: ToolDeny, Code: "inner", Reason: "no"}, nil
	}, false)
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	decisions, _, err := frozen.Preflight(context.Background(), []*ToolExecution{pipelineExec("bash")})
	require.NoError(t, err)
	// The rewrite hook delegated and preserved the downstream deny (I7).
	assert.Equal(t, ToolDeny, decisions[0].Action)
	assert.Equal(t, "inner", decisions[0].Code)
}

func TestCompileProjectHookRewriteAppliesOnAllow(t *testing.T) {
	rewrite, err := CompileProjectHook(domain.ProjectToolPolicyHook{
		Kind:      "rewrite",
		Arguments: json.RawMessage(`{"command":"rewritten"}`),
		When:      domain.ProjectHookWhen{ToolName: "bash"},
	})
	require.NoError(t, err)

	chain := NewPolicyChain()
	_, err = chain.RegisterPre(rewrite.Pre, false)
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	exec := pipelineExec("bash")
	decisions, _, err := frozen.Preflight(context.Background(), []*ToolExecution{exec})
	require.NoError(t, err)
	assert.Equal(t, ToolAllow, decisions[0].Action)
	assert.JSONEq(t, `{"command":"rewritten"}`, string(decisions[0].Arguments))
}

func TestCompileProjectHookProjectRedacts(t *testing.T) {
	compiled, err := CompileProjectHook(domain.ProjectToolPolicyHook{
		Kind:           "project",
		RedactPatterns: []string{`secret-\d+`},
	})
	require.NoError(t, err)

	chain := NewPolicyChain()
	_, err = chain.RegisterPost(compiled.Post)
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	result := domain.ToolResult{Content: "saw secret-42 and secret-7"}
	post, err := frozen.Post(context.Background(), pipelineExec("bash"), result)
	require.NoError(t, err)
	assert.Equal(t, "saw [REDACTED] and [REDACTED]", post.Result.Content)
}

func TestCompileProjectHookRejectsInvalid(t *testing.T) {
	_, err := CompileProjectHook(domain.ProjectToolPolicyHook{Kind: "unknown"})
	assert.Error(t, err)
	_, err = CompileProjectHook(domain.ProjectToolPolicyHook{Kind: "deny"})
	assert.Error(t, err) // missing code
	_, err = CompileProjectHook(domain.ProjectToolPolicyHook{Kind: "rewrite", Arguments: json.RawMessage(`{bad`)})
	assert.Error(t, err)
	_, err = CompileProjectHook(domain.ProjectToolPolicyHook{Kind: "project", RedactPatterns: []string{`(`}})
	assert.Error(t, err)
}

func TestLoadWorkspaceToolPolicyHooks(t *testing.T) {
	root := t.TempDir()
	// Absent file.
	hooks, err := LoadWorkspaceToolPolicyHooks(root)
	require.NoError(t, err)
	assert.Nil(t, hooks)

	// Present + valid.
	dir := filepath.Join(root, ".ennote")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{
		"toolPolicyHooks": [
			{"kind":"deny","code":"block_rm","when":{"toolName":"bash","commandContains":"rm -rf"}},
			{"kind":"project","redactPatterns":["secret-\\d+"]}
		]
	}`), 0o600))

	hooks, err = LoadWorkspaceToolPolicyHooks(root)
	require.NoError(t, err)
	require.Len(t, hooks, 2)
	assert.Equal(t, "block_rm", hooks[0].Code)
	assert.Equal(t, "rm -rf", hooks[0].When.CommandContains)
	assert.Equal(t, "project", hooks[1].Kind)

	// Malformed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"toolPolicyHooks":{invalid`), 0o600))
	_, err = LoadWorkspaceToolPolicyHooks(root)
	assert.Error(t, err)
}
