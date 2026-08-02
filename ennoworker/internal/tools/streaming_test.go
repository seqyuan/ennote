package tools

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingSink struct {
	mu     sync.Mutex
	chunks []domain.ToolOutputUpdate
}

func (s *recordingSink) TryEmit(u domain.ToolOutputUpdate) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = append(s.chunks, u)
	return true
}

func (s *recordingSink) all() []domain.ToolOutputUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.ToolOutputUpdate(nil), s.chunks...)
}

func TestBashExecuteStreamingEmitsChunks(t *testing.T) {
	dir := t.TempDir()
	manager, err := workspace.NewManager(dir, dir, dir, workspace.SandboxNone)
	require.NoError(t, err)
	tool := &BashTool{Workspace: manager, Shell: "sh", OutputLimit: 1024}

	sink := &recordingSink{}
	call := domain.ToolCall{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hello; echo world >&2"}`)}
	result, err := tool.ExecuteStreaming(context.Background(), call, sink)
	require.NoError(t, err)
	assert.False(t, result.IsError)

	chunks := sink.all()
	assert.NotEmpty(t, chunks, "streaming should emit output chunks")
	var stdoutText, stderrText string
	for _, c := range chunks {
		assert.Equal(t, "call-1", c.ToolCallID)
		switch c.Stream {
		case "stdout":
			stdoutText += string(c.Data)
		case "stderr":
			stderrText += string(c.Data)
		}
	}
	assert.Contains(t, stdoutText, "hello")
	assert.Contains(t, stderrText, "world")
}

func TestExecExecuteStreamingEmitsChunks(t *testing.T) {
	dir := t.TempDir()
	manager, err := workspace.NewManager(dir, dir, dir, workspace.SandboxNone)
	require.NoError(t, err)
	tool := &ExecTool{Workspace: manager, OutputLimit: 1024}

	sink := &recordingSink{}
	call := domain.ToolCall{ID: "call-2", Name: "exec", Arguments: json.RawMessage(`{"argv":["/bin/echo","exec-hello"]}`)}
	result, err := tool.ExecuteStreaming(context.Background(), call, sink)
	require.NoError(t, err)
	assert.False(t, result.IsError)

	chunks := sink.all()
	var stdoutText string
	for _, c := range chunks {
		if c.Stream == "stdout" {
			stdoutText += string(c.Data)
		}
	}
	assert.Contains(t, stdoutText, "exec-hello")
}

func TestRegistryExecuteStreamingFallsBack(t *testing.T) {
	// A non-streaming tool (read) should still work via ExecuteStreaming fallback.
	dir := t.TempDir()
	manager, err := workspace.NewManager(dir, dir, dir, workspace.SandboxNone)
	require.NoError(t, err)
	tool, err := NewRegistry(&ReadTool{Jail: manager.Jail, MaxBytes: 4096})
	require.NoError(t, err)

	sink := &recordingSink{}
	file := dir + "/sample.txt"
	require.NoError(t, os.WriteFile(file, []byte("line one\nline two\n"), 0o600))
	call := domain.ToolCall{ID: "call-3", Name: "read", Arguments: json.RawMessage(`{"path":"sample.txt"}`)}
	result, err := tool.ExecuteStreaming(context.Background(), call, sink)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content, "line one")
}
