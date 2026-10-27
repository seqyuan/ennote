package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBashAndExecRejectEnnoteHomeInSandboxNone(t *testing.T) {
	manager, err := workspace.NewManager(t.TempDir(), t.TempDir(), "", workspace.SandboxNone)
	require.NoError(t, err)
	home := t.TempDir()
	manager.ProtectHome(home)
	ctx := context.Background()

	bash := &BashTool{Workspace: manager, Shell: "sh"}
	args, err := json.Marshal(map[string]string{"command": "cat " + filepath.Join(home, "config", "provider-auth.json")})
	require.NoError(t, err)
	result, err := bash.Execute(ctx, domain.ToolCall{ID: "call", Name: "bash", Arguments: args})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "cannot access the Ennote home")

	exec := &ExecTool{Workspace: manager}
	args, err = json.Marshal(map[string]any{"argv": []string{"cat", filepath.Join(home, "config", "provider-auth.json")}})
	require.NoError(t, err)
	result, err = exec.Execute(ctx, domain.ToolCall{ID: "call", Name: "exec", Arguments: args})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "cannot access the Ennote home")
}
