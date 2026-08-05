package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noRiskTool is a tool that intentionally omits RiskClass to exercise the
// mandatory-risk registration contract.
type noRiskTool struct{}

func (noRiskTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "no_risk", Description: "missing risk", Parameters: json.RawMessage(`{"type":"object"}`)}
}

func (noRiskTool) Execute(_ context.Context, _ domain.ToolCall) (domain.ToolResult, error) {
	return domain.ToolResult{}, nil
}

// invalidRiskTool declares an illegal RiskClass value.
type invalidRiskTool struct{}

func (invalidRiskTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "bad_risk", Description: "bad risk", Parameters: json.RawMessage(`{"type":"object"}`), RiskClass: domain.RiskClass("future")}
}

func (invalidRiskTool) Execute(_ context.Context, _ domain.ToolCall) (domain.ToolResult, error) {
	return domain.ToolResult{}, nil
}

// declaredRiskTool declares a valid RiskExternal classification.
type declaredRiskTool struct{}

func (declaredRiskTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "declared_external", Description: "external", Parameters: json.RawMessage(`{"type":"object"}`), RiskClass: domain.RiskExternal}
}

func (declaredRiskTool) Execute(_ context.Context, _ domain.ToolCall) (domain.ToolResult, error) {
	return domain.ToolResult{}, nil
}

func TestRegisterMissingRiskFailsClosed(t *testing.T) {
	registry, err := NewRegistry(noRiskTool{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid risk class")
	assert.Nil(t, registry)
}

func TestRegisterInvalidRiskFailsClosed(t *testing.T) {
	registry, err := NewRegistry(invalidRiskTool{})
	require.Error(t, err)
	assert.Nil(t, registry)
}

func TestRegisteredRiskRoundTrip(t *testing.T) {
	registry, err := NewRegistry(declaredRiskTool{})
	require.NoError(t, err)
	assert.Equal(t, domain.RiskExternal, registry.RiskClass("declared_external"))
}

func TestUnknownAndRestrictedToolsResolveSensitive(t *testing.T) {
	registry, err := NewRegistry(declaredRiskTool{})
	require.NoError(t, err)
	// Unknown/hallucinated tool.
	assert.Equal(t, domain.RiskSensitive, registry.RiskClass("hallucinated_tool"))
	// Restrict removes the tool; it must fail closed afterwards.
	registry.Restrict([]string{"other"})
	assert.Equal(t, domain.RiskSensitive, registry.RiskClass("declared_external"))
	assert.Len(t, registry.Definitions(), 0)
}

func TestDefaultRegistryRiskValues(t *testing.T) {
	root := t.TempDir()
	manager, err := workspace.NewManager(root, t.TempDir(), t.TempDir(), workspace.SandboxNone)
	require.NoError(t, err)
	registry, err := NewDefaultRegistry(manager)
	require.NoError(t, err)
	expect := map[string]domain.RiskClass{
		"read": domain.RiskReadOnly, "ls": domain.RiskReadOnly, "grep": domain.RiskReadOnly,
		"find": domain.RiskReadOnly, "git_readonly": domain.RiskReadOnly,
		"write": domain.RiskLocalWrite, "edit": domain.RiskLocalWrite,
		"bash": domain.RiskShell, "exec": domain.RiskShell,
		"web_fetch": domain.RiskExternal,
	}
	for name, want := range expect {
		assert.Equal(t, want, registry.RiskClass(name), name)
	}
}

// mcpScopeTool wraps the mcpclient.Tool to exercise Registry's standing-scope
// forwarding without importing the mcpclient package's tool adapter directly.
type mcpScopeTool struct {
	scope domain.StandingApprovalScope
}

func (t *mcpScopeTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{Name: "pubmed__search", Description: "external mcp",
		Parameters: json.RawMessage(`{"type":"object"}`), RiskClass: domain.RiskExternal}
}
func (t *mcpScopeTool) Execute(_ context.Context, _ domain.ToolCall) (domain.ToolResult, error) {
	return domain.ToolResult{}, nil
}
func (t *mcpScopeTool) StandingApprovalScope(_ json.RawMessage) (domain.StandingApprovalScope, error) {
	return t.scope, nil
}

func TestRegistryForwardsStandingScopeForExternalMCPTool(t *testing.T) {
	scope := domain.StandingApprovalScope{Kind: "mcp_tool", ScopeVersion: 1,
		Key: "version-1:schema-a:search", Display: "search (version-1)"}
	registry, err := NewRegistry(&mcpScopeTool{scope: scope})
	require.NoError(t, err)
	assert.Equal(t, domain.RiskExternal, registry.RiskClass("pubmed__search"))

	resolved, ok, err := registry.ResolveStandingApprovalScope("pubmed__search", nil)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "mcp_tool", resolved.Kind)
	assert.Equal(t, scope.Key, resolved.Key)
}
