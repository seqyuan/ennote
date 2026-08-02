package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishArtifactCopiesOnlyExplicitRegularFile(t *testing.T) {
	manager, sink := setupArtifactTool(t)
	require.NoError(t, os.WriteFile(filepath.Join(manager.Jail.Root(), "results.csv"), []byte("gene,value\nA,1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(manager.Jail.Root(), "private.tmp"), []byte("secret"), 0o600))
	tool := &PublishArtifactTool{Jail: manager.Jail, Sink: sink}
	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "call-1", Name: "publish_artifact",
		Arguments: json.RawMessage(`{"path":"/workspace/results.csv"}`)})
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)
	require.Len(t, result.Artifacts, 1)
	assert.Equal(t, domain.ArtifactKindTable, result.Artifacts[0].Kind)
	assert.Equal(t, "results.csv", result.Artifacts[0].Name)
	var count int
	require.NoError(t, sink.Service.DB.QueryRow(`SELECT COUNT(*) FROM artifacts`).Scan(&count))
	assert.Equal(t, 1, count, "unpublished Workspace files are not discovered or copied")
}

func TestPublishArtifactRejectsWorkspaceEscapeAndDirectory(t *testing.T) {
	manager, sink := setupArtifactTool(t)
	tool := &PublishArtifactTool{Jail: manager.Jail, Sink: sink}
	for _, raw := range []string{`{"path":"/etc/passwd"}`, `{"path":"/workspace"}`} {
		result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "call", Name: "publish_artifact", Arguments: json.RawMessage(raw)})
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Empty(t, result.Artifacts)
	}
}

func TestExecRetainsCompleteStdoutWhenPreviewTruncates(t *testing.T) {
	manager, sink := setupArtifactTool(t)
	tool := &ExecTool{Workspace: manager, Artifacts: sink, OutputLimit: 32, OutputArtifactLimit: 1024}
	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "call-output", Name: "exec",
		Arguments: json.RawMessage(`{"argv":["/bin/sh","-c","printf 'abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ'"]}`)})
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)
	require.Len(t, result.Artifacts, 1)
	assert.Equal(t, "stdout.txt", result.Artifacts[0].Name)
	assert.Contains(t, result.Content, "[output truncated]")
	assert.Contains(t, result.Content, "full output retained as artifact")
	_, data, err := sink.Service.ReadForSession(context.Background(), result.Artifacts[0].ArtifactID, "s")
	require.NoError(t, err)
	assert.Equal(t, "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ", string(data))
}

func TestExecReportsOversizedOutputWithoutPartialArtifact(t *testing.T) {
	manager, sink := setupArtifactTool(t)
	tool := &ExecTool{Workspace: manager, Artifacts: sink, OutputLimit: 8, OutputArtifactLimit: 16}
	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "call-output", Name: "exec",
		Arguments: json.RawMessage(`{"argv":["/bin/sh","-c","printf 'abcdefghijklmnopqrstuvwxyz'"]}`)})
	require.NoError(t, err)
	require.False(t, result.IsError, result.Content)
	assert.Empty(t, result.Artifacts)
	assert.Contains(t, result.Content, "exceeded the 16-byte artifact limit")
}

func setupArtifactTool(t *testing.T) (*workspace.Manager, *ArtifactSink) {
	t.Helper()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	now := "2026-07-28T00:00:00Z"
	_, err = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','project',?,?);
		INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?)`, now, now, now, now)
	require.NoError(t, err)
	workspaceRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	manager, err := workspace.NewManager(workspaceRoot, runtimeRoot, "", workspace.SandboxNone)
	require.NoError(t, err)
	service := &artifacts.Service{DB: db, Root: t.TempDir()}
	return manager, &ArtifactSink{Service: service, ProjectID: "p", SessionID: "s", RunID: "run-1"}
}
