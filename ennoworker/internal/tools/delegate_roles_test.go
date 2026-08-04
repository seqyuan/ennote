package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDelegateRolesProvider struct {
	result *DelegateRolesResult
	err    error
}

func (m *mockDelegateRolesProvider) ExecuteDelegation(_ context.Context, _, _, _ string, _ []DelegationSpec) (*DelegateRolesResult, error) {
	return m.result, m.err
}

func TestDelegateRolesToolRejectsEmptyDelegations(t *testing.T) {
	tool := &DelegateRolesTool{Provider: &mockDelegateRolesProvider{
		result: &DelegateRolesResult{Status: "delegated"},
	}}
	result, err := tool.Execute(context.Background(), domain.ToolCall{
		ID:        "call-1",
		Name:      "delegate_roles",
		Arguments: json.RawMessage(`{"delegations":[]}`),
	})
	require.NoError(t, err)
	assert.True(t, result.IsError, "empty delegations should be an error result")
}

func TestDelegateRolesToolRequiresProvider(t *testing.T) {
	tool := &DelegateRolesTool{}
	result, err := tool.Execute(context.Background(), domain.ToolCall{
		ID:        "call-1",
		Name:      "delegate_roles",
		Arguments: json.RawMessage(`{"delegations":[{"name":"r","roleHandle":"explorer","assignment":"inspect","budget":{"maxModelCalls":4,"maxToolCalls":4}}]}`),
	})
	require.NoError(t, err)
	assert.True(t, result.IsError, "missing provider should be an error result")
}

func TestDelegateRolesToolReturnsPlaceholderOnSuccess(t *testing.T) {
	expected := &DelegateRolesResult{
		Status:  "delegated",
		GroupID: "grp-1",
		Items: []DelegateRolesItemResult{
			{Name: "explore", ItemID: "item-1", ChildRunID: "child-1"},
		},
	}
	tool := &DelegateRolesTool{Provider: &mockDelegateRolesProvider{result: expected}}
	ctx := context.WithValue(context.Background(), delegateRolesRunIDKey, "parent-1")
	ctx = context.WithValue(ctx, delegateRolesSessionIDKey, "session-1")
	result, err := tool.Execute(ctx, domain.ToolCall{
		ID:        "call-1",
		Name:      "delegate_roles",
		Arguments: json.RawMessage(`{"delegations":[{"name":"explore","roleHandle":"workspace-explorer","assignment":"List files","outputContract":"text-v1","budget":{"maxModelCalls":4,"maxToolCalls":4,"maxTotalTokens":16000}}]}`),
	})
	require.NoError(t, err)
	assert.Equal(t, "call-1", result.ToolCallID)
	var parsed DelegateRolesResult
	require.NoError(t, json.Unmarshal([]byte(result.Content), &parsed))
	assert.Equal(t, "delegated", parsed.Status)
	assert.Equal(t, "grp-1", parsed.GroupID)
	assert.Len(t, parsed.Items, 1)
	assert.Equal(t, "child-1", parsed.Items[0].ChildRunID)
}
