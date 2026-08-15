package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// disposerTestTool is a minimal tool used to exercise the disposer contract.
type disposerTestTool struct{}

func (disposerTestTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "disposer_target", Description: "dispose test",
		Parameters: json.RawMessage(`{"type":"object"}`), RiskClass: domain.RiskLocalWrite}
}

func (disposerTestTool) Execute(_ context.Context, _ domain.ToolCall) (domain.ToolResult, error) {
	return domain.ToolResult{}, nil
}

func TestRegisterDisposeRemovesTool(t *testing.T) {
	registry, err := NewRegistry()
	require.NoError(t, err)

	dispose, err := registry.Register(disposerTestTool{})
	require.NoError(t, err)
	require.NotNil(t, dispose)

	// Before dispose: resolvable through every lookup path.
	assert.Equal(t, domain.RiskLocalWrite, registry.RiskClass("disposer_target"))
	assert.NoError(t, registry.ValidateArguments("disposer_target", json.RawMessage(`{}`)))
	result, execErr := registry.Execute(context.Background(),
		domain.ToolCall{ID: "c1", Name: "disposer_target"})
	require.NoError(t, execErr)
	assert.False(t, result.IsError)

	dispose()

	// After dispose: fails closed everywhere.
	assert.Equal(t, domain.RiskSensitive, registry.RiskClass("disposer_target"))
	assert.Equal(t, domain.ExecutionExclusive, registry.ExecutionClass("disposer_target"))
	assert.Error(t, registry.ValidateArguments("disposer_target", json.RawMessage(`{}`)))
	result, _ = registry.Execute(context.Background(), domain.ToolCall{ID: "c2", Name: "disposer_target"})
	assert.True(t, result.IsError)
	for _, def := range registry.Definitions() {
		assert.NotEqual(t, "disposer_target", def.Name)
	}
}

func TestRegisterDisposeIdempotent(t *testing.T) {
	registry, err := NewRegistry()
	require.NoError(t, err)
	dispose, err := registry.Register(disposerTestTool{})
	require.NoError(t, err)

	dispose()
	assert.NotPanics(t, dispose) // second call is a no-op
	assert.Equal(t, domain.RiskSensitive, registry.RiskClass("disposer_target"))
}

func TestRegisterAliasDisposeKeepsCanonical(t *testing.T) {
	registry, err := NewRegistry(disposerTestTool{})
	require.NoError(t, err)

	aliasDispose, err := registry.RegisterAlias("legacy_alias", "disposer_target", `{"type":"object"}`)
	require.NoError(t, err)
	require.NotNil(t, aliasDispose)

	// Alias resolves before dispose.
	result, execErr := registry.Execute(context.Background(),
		domain.ToolCall{ID: "c1", Name: "legacy_alias"})
	require.NoError(t, execErr)
	assert.False(t, result.IsError)

	aliasDispose()

	// Alias is gone, canonical remains.
	result, _ = registry.Execute(context.Background(), domain.ToolCall{ID: "c2", Name: "legacy_alias"})
	assert.True(t, result.IsError)
	result, execErr = registry.Execute(context.Background(),
		domain.ToolCall{ID: "c3", Name: "disposer_target"})
	require.NoError(t, execErr)
	assert.False(t, result.IsError)
}

func TestRegisterDuplicateReturnsNilDisposer(t *testing.T) {
	registry, err := NewRegistry(disposerTestTool{})
	require.NoError(t, err)

	dispose, err := registry.Register(disposerTestTool{})
	require.Error(t, err)
	assert.Nil(t, dispose)
}
