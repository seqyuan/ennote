package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	manager, err := workspace.NewManager(root, t.TempDir(), t.TempDir(), workspace.SandboxNone)
	require.NoError(t, err)
	registry, err := NewDefaultRegistry(manager)
	require.NoError(t, err)
	return registry, root
}

func call(id, name, arguments string) domain.ToolCall {
	return domain.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(arguments)}
}

func TestWriteReadEditAndListTools(t *testing.T) {
	registry, root := testRegistry(t)
	ctx := context.Background()
	write := registry.Execute(ctx, call("w", "write", `{"path":"notes/a.txt","content":"hello world"}`))
	require.False(t, write.IsError, write.Content)
	data, err := os.ReadFile(filepath.Join(root, "notes", "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))

	read := registry.Execute(ctx, call("r", "read", `{"path":"/workspace/notes/a.txt"}`))
	assert.False(t, read.IsError, read.Content)
	assert.Equal(t, "hello world", read.Content)

	edit := registry.Execute(ctx, call("e", "edit", `{"path":"notes/a.txt","oldText":"world","newText":"agent"}`))
	assert.False(t, edit.IsError, edit.Content)
	data, _ = os.ReadFile(filepath.Join(root, "notes", "a.txt"))
	assert.Equal(t, "hello agent", string(data))

	list := registry.Execute(ctx, call("l", "ls", `{"path":"notes"}`))
	assert.False(t, list.IsError, list.Content)
	assert.Equal(t, "a.txt", list.Content)
}

func TestEditRequiresUniqueMatch(t *testing.T) {
	registry, root := testRegistry(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "dup.txt"), []byte("x x"), 0o644))
	result := registry.Execute(context.Background(), call("e", "edit", `{"path":"dup.txt","oldText":"x","newText":"y"}`))
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "matched 2 times")
	data, _ := os.ReadFile(filepath.Join(root, "dup.txt"))
	assert.Equal(t, "x x", string(data))
}

func TestToolsRejectSymlinkEscape(t *testing.T) {
	registry, root := testRegistry(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	for _, invocation := range []domain.ToolCall{
		call("r", "read", `{"path":"escape/secret"}`),
		call("w", "write", `{"path":"escape/new","content":"bad"}`),
	} {
		result := registry.Execute(context.Background(), invocation)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "escapes workspace")
	}
}

func TestGrepAndFindAreBounded(t *testing.T) {
	registry, root := testRegistry(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a\nfunc Alpha() {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "b.txt"), []byte("Alpha\n"), 0o644))
	grep := registry.Execute(context.Background(), call("g", "grep", `{"pattern":"Alpha","path":"src"}`))
	assert.False(t, grep.IsError, grep.Content)
	assert.Contains(t, grep.Content, "/workspace/src/a.go:2")
	find := registry.Execute(context.Background(), call("f", "find", `{"pattern":"*.go","path":"src"}`))
	assert.False(t, find.IsError, find.Content)
	assert.Equal(t, "/workspace/src/a.go", find.Content)
}

func TestBashUsesAllowlistedEnvironmentAndBoundsOutput(t *testing.T) {
	registry, _ := testRegistry(t)
	t.Setenv("ENNOTE_BOOTSTRAP_TOKEN", "must-not-leak")
	result := registry.Execute(context.Background(), call("b", "bash", `{"command":"env; printf '%05000d' 0"}`))
	assert.False(t, result.IsError, result.Content)
	assert.NotContains(t, result.Content, "must-not-leak")
	assert.NotContains(t, result.Content, "ENNOTE_BOOTSTRAP_TOKEN")
	assert.Less(t, len(result.Content), 1<<20)
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	registry, _ := testRegistry(t)
	started := time.Now()
	result := registry.Execute(context.Background(), call("b", "bash", `{"command":"sleep 30 & wait","timeoutSeconds":1}`))
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "timed out")
	assert.Less(t, time.Since(started), 3*time.Second)
}

func TestRegistryValidatesArgumentsAgainstSchema(t *testing.T) {
	registry, _ := testRegistry(t)
	assert.NoError(t, registry.ValidateArguments("read", json.RawMessage(`{"path":"README.md"}`)))
	assert.Error(t, registry.ValidateArguments("read", json.RawMessage(`{}`)))
	assert.Error(t, registry.ValidateArguments("read", json.RawMessage(`{"path":"x","unknown":true}`)))
	assert.Error(t, registry.ValidateArguments("missing", json.RawMessage(`{}`)))
}

func TestRegistryUnknownToolAndDefinitions(t *testing.T) {
	registry, _ := testRegistry(t)
	result := registry.Execute(context.Background(), call("x", "missing", `{}`))
	assert.True(t, result.IsError)
	definitions := registry.Definitions()
	var names []string
	for _, definition := range definitions {
		names = append(names, definition.Name)
		assert.True(t, json.Valid(definition.Parameters), definition.Name)
	}
	assert.Equal(t, "bash,edit,exec,find,grep,ls,read,write", strings.Join(names, ","))
}
