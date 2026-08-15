package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/spill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingSpillStore struct {
	ref   spill.Ref
	err   error
	calls int
	last  spill.SaveInput
}

func (s *recordingSpillStore) SaveText(_ context.Context, input spill.SaveInput) (spill.Ref, error) {
	s.calls++
	s.last = input
	return s.ref, s.err
}

func TestSpillPostHookBelowThresholdPassthrough(t *testing.T) {
	store := &recordingSpillStore{}
	chain := NewPolicyChain()
	_, err := chain.RegisterPost(SpillPostHook(store, 100, func(_ *ToolExecution) spill.Owner { return spill.Owner{SessionID: "s"} }))
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	post, err := frozen.Post(context.Background(), pipelineExec("bash"), domain.ToolResult{Content: "small"})
	require.NoError(t, err)
	assert.Equal(t, "small", post.Result.Content)
	assert.Zero(t, store.calls)
}

func TestSpillPostHookSpillsOversized(t *testing.T) {
	store := &recordingSpillStore{ref: spill.Ref{Locator: "/spill/session-abc/1-bash.txt", Bytes: 200, RetrievalHint: "read or grep this file"}}
	chain := NewPolicyChain()
	_, err := chain.RegisterPost(SpillPostHook(store, 10, func(_ *ToolExecution) spill.Owner { return spill.Owner{SessionID: "s"} }))
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	big := strings.Repeat("x", 200)
	post, err := frozen.Post(context.Background(), pipelineExec("bash"), domain.ToolResult{Content: big})
	require.NoError(t, err)
	assert.Equal(t, 1, store.calls)
	assert.Contains(t, post.Result.Content, "/spill/session-abc/1-bash.txt")
	assert.Contains(t, post.Result.Content, "spilled 200 bytes")
	assert.NotEqual(t, big, post.Result.Content)
}

func TestSpillPostHookSaveFailureKeepsInline(t *testing.T) {
	store := &recordingSpillStore{err: errors.New("ENOSPC")}
	chain := NewPolicyChain()
	_, err := chain.RegisterPost(SpillPostHook(store, 10, func(_ *ToolExecution) spill.Owner { return spill.Owner{SessionID: "s"} }))
	require.NoError(t, err)
	frozen, err := chain.Freeze()
	require.NoError(t, err)

	big := strings.Repeat("y", 200)
	post, err := frozen.Post(context.Background(), pipelineExec("bash"), domain.ToolResult{Content: big})
	require.NoError(t, err)
	assert.Equal(t, 1, store.calls)
	assert.Equal(t, big, post.Result.Content) // best-effort: inline preserved
	assert.False(t, post.Result.IsError)
}

func TestSpillPreviewRuneSafe(t *testing.T) {
	content := strings.Repeat("字", 100)
	preview := spillPreview(content, 20)
	assert.Less(t, len([]rune(preview)), 100)
	assert.Contains(t, preview, "truncated")
}
