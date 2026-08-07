package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDelegateTasksProvider struct {
	result *DelegateTasksResult
	err    error
}

func (m *mockDelegateTasksProvider) ExecuteDelegation(_ context.Context, _, _, _ string, _ []domain.TaskSpec) (*DelegateTasksResult, error) {
	return m.result, m.err
}

func TestDelegateTasksToolRejectsEmptyTasks(t *testing.T) {
	tool := &DelegateTasksTool{Provider: &mockDelegateTasksProvider{
		result: &DelegateTasksResult{Status: "delegated"},
	}}
	result, err := tool.Execute(context.Background(), domain.ToolCall{
		ID:        "call-1",
		Name:      "delegate_tasks",
		Arguments: json.RawMessage(`{"tasks":[]}`),
	})
	require.NoError(t, err)
	assert.True(t, result.IsError, "empty tasks should be an error result")
}

func TestDelegateTasksToolRequiresProvider(t *testing.T) {
	tool := &DelegateTasksTool{}
	result, err := tool.Execute(context.Background(), domain.ToolCall{
		ID:        "call-1",
		Name:      "delegate_tasks",
		Arguments: json.RawMessage(`{"tasks":[{"name":"r","role":"explorer","goal":"inspect","budget":{"maxModelCalls":4,"maxToolCalls":4}}]}`),
	})
	require.NoError(t, err)
	assert.True(t, result.IsError, "missing provider should be an error result")
}

func TestDelegateTasksToolReturnsPlaceholderOnSuccess(t *testing.T) {
	expected := &DelegateTasksResult{
		Status:  "delegated",
		GroupID: "grp-1",
		Items: []DelegateTasksItemResult{
			{Name: "explore", ItemID: "item-1", ChildRunID: "child-1"},
		},
	}
	tool := &DelegateTasksTool{Provider: &mockDelegateTasksProvider{result: expected}}
	ctx := context.WithValue(context.Background(), delegateRolesRunIDKey, "parent-1")
	ctx = context.WithValue(ctx, delegateRolesSessionIDKey, "session-1")
	result, err := tool.Execute(ctx, domain.ToolCall{
		ID:   "call-1",
		Name: "delegate_tasks",
		Arguments: json.RawMessage(`{"tasks":[{"name":"explore","role":"workspace-explorer","goal":"List files",
			"skills":["workspace-nav"],"outputContract":"text-v1","budget":{"maxModelCalls":4,"maxToolCalls":4,"maxTotalTokens":16000}}]}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "call-1", result.ToolCallID)
	var parsed DelegateTasksResult
	require.NoError(t, json.Unmarshal([]byte(result.Content), &parsed))
	assert.Equal(t, "delegated", parsed.Status)
	assert.Equal(t, "grp-1", parsed.GroupID)
	assert.Len(t, parsed.Items, 1)
	assert.Equal(t, "child-1", parsed.Items[0].ChildRunID)
}

// The legacy delegate_roles argument shape (delegations + roleHandle/assignment)
// must keep resolving after the rename so replayed tool calls of resumed runs
// work; the tool normalizes the legacy fields onto the unified TaskSpec.
func TestDelegateTasksToolAcceptsLegacyDelegateRolesShape(t *testing.T) {
	var captured []domain.TaskSpec
	tool := &DelegateTasksTool{Provider: delegateTasksCaptureProvider{capture: &captured}}
	ctx := context.WithValue(context.Background(), delegateRolesRunIDKey, "parent-1")
	ctx = context.WithValue(ctx, delegateRolesSessionIDKey, "session-1")
	result, err := tool.Execute(ctx, domain.ToolCall{
		ID:   "call-1",
		Name: "delegate_roles", // replayed persisted call under the old name
		Arguments: json.RawMessage(`{"delegations":[{"name":"explore","roleHandle":"workspace-explorer",
			"assignment":"List files","budget":{"maxModelCalls":4,"maxToolCalls":4}}]}`),
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, captured, 1)
	assert.Equal(t, "workspace-explorer", captured[0].Role)
	assert.Equal(t, "List files", captured[0].Goal)
	assert.Empty(t, captured[0].RoleHandle, "legacy fields are normalized away")
	assert.Empty(t, captured[0].Assignment)
}

type delegateTasksCaptureProvider struct {
	capture *[]domain.TaskSpec
}

func (m delegateTasksCaptureProvider) ExecuteDelegation(_ context.Context, _, _, _ string, specs []domain.TaskSpec) (*DelegateTasksResult, error) {
	*m.capture = append(*m.capture, specs...)
	return &DelegateTasksResult{Status: "delegated", GroupID: "grp-1"}, nil
}
