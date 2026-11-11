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

// A widget is published only on request. The same file, published without the
// kind, stays static_html — the script-free kind — so a model that never thought
// about interactivity cannot end up with it.
func TestPublishArtifactWidgetRequiresExplicitIntent(t *testing.T) {
	manager, sink := setupArtifactTool(t)
	page := `<!doctype html><svg viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg><script>void 0</script>`
	require.NoError(t, os.WriteFile(filepath.Join(manager.Jail.Root(), "canvas.html"), []byte(page), 0o600))
	tool := &PublishArtifactTool{Jail: manager.Jail, Sink: sink}

	call := func(arguments string) domain.ToolResult {
		result, err := tool.Execute(context.Background(), domain.ToolCall{
			ID: "call-widget", Name: "publish_artifact", Arguments: json.RawMessage(arguments)})
		require.NoError(t, err)
		return result
	}

	inferred := call(`{"path":"/workspace/canvas.html"}`)
	require.False(t, inferred.IsError, inferred.Content)
	require.Len(t, inferred.Artifacts, 1)
	assert.Equal(t, domain.ArtifactKindStaticHTML, inferred.Artifacts[0].Kind)

	published := call(`{"path":"/workspace/canvas.html","kind":"widget"}`)
	require.False(t, published.IsError, published.Content)
	require.Len(t, published.Artifacts, 1)
	assert.Equal(t, domain.ArtifactKindWidget, published.Artifacts[0].Kind)
	assert.Contains(t, published.Content, "kind=widget")

	auto := call(`{"path":"/workspace/canvas.html","kind":"auto"}`)
	require.False(t, auto.IsError, auto.Content)
	assert.Equal(t, domain.ArtifactKindStaticHTML, auto.Artifacts[0].Kind)

	// Spelling is normalised, and that cannot weaken the decision: naming the kind at
	// all is what grants script execution, so "Widget" is the same act as "widget".
	for _, raw := range []string{
		`{"path":"/workspace/canvas.html","kind":"Widget"}`,
		`{"path":"/workspace/canvas.html","kind":" widget "}`,
		`{"path":"/workspace/canvas.html","kind":"wIDGET"}`,
	} {
		named := call(raw)
		require.False(t, named.IsError, named.Content)
		assert.Equal(t, domain.ArtifactKindWidget, named.Artifacts[0].Kind, raw)
	}
}

// The artifact name is the model's to choose; the file it points at is not. A
// widget published from a .txt would otherwise pass the extension check on the
// renamed artifact and blame the wrong file in every later error message.
func TestPublishArtifactWidgetChecksTheSourceExtension(t *testing.T) {
	manager, sink := setupArtifactTool(t)
	require.NoError(t, os.WriteFile(filepath.Join(manager.Jail.Root(), "canvas.txt"), []byte("<p>x</p>"), 0o600))
	tool := &PublishArtifactTool{Jail: manager.Jail, Sink: sink}
	result, err := tool.Execute(context.Background(), domain.ToolCall{ID: "call-src", Name: "publish_artifact",
		Arguments: json.RawMessage(`{"path":"/workspace/canvas.txt","name":"canvas.html","kind":"widget"}`)})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, ".html file")
	assert.Empty(t, result.Artifacts)
}

func TestPublishArtifactRejectsUnknownKind(t *testing.T) {
	manager, sink := setupArtifactTool(t)
	require.NoError(t, os.WriteFile(filepath.Join(manager.Jail.Root(), "canvas.html"), []byte("<p>x</p>"), 0o600))
	tool := &PublishArtifactTool{Jail: manager.Jail, Sink: sink}
	for _, raw := range []string{
		`{"path":"/workspace/canvas.html","kind":"static_html"}`,
		`{"path":"/workspace/canvas.html","kind":"image"}`,
		`{"path":"/workspace/canvas.html","kind":"dashboard"}`,
	} {
		result, err := tool.Execute(context.Background(), domain.ToolCall{
			ID: "call-kind", Name: "publish_artifact", Arguments: json.RawMessage(raw)})
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Empty(t, result.Artifacts)
	}
}

// The widget kind must not become an excuse to lower the risk class: publishing is
// still a Workspace write that a policy can gate on.
func TestPublishArtifactKeepsItsExecutionAndRiskClass(t *testing.T) {
	tool := &PublishArtifactTool{}
	assert.Equal(t, domain.ExecutionWorkspaceWrite, tool.ExecutionClass())
	definition := tool.Definition()
	assert.Equal(t, domain.RiskLocalWrite, definition.RiskClass)
	assert.Contains(t, string(definition.Parameters), `"widget"`)
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
	require.NoError(t, store.MigrateFixtureSchema(db))
	now := "2026-07-28T00:00:00Z"
	_, err = db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?)`, now, now)
	require.NoError(t, err)
	workspaceRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	manager, err := workspace.NewManager(workspaceRoot, runtimeRoot, "", workspace.SandboxNone)
	require.NoError(t, err)
	service := &artifacts.Service{DB: db, Root: t.TempDir()}
	return manager, &ArtifactSink{Service: service, ProjectID: "p", SessionID: "s", RunID: "run-1"}
}
