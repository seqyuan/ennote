package mcpclient

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func schemaObj(props map[string]string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
	}
}

func TestNormalizeCatalogBasic(t *testing.T) {
	raw := []*mcp.Tool{
		{Name: "search_articles", Description: "Search articles", InputSchema: schemaObj(map[string]string{"q": "string"})},
		{Name: "get_article", Description: "Get one article", InputSchema: schemaObj(map[string]string{"id": "string"})},
	}
	entries, err := NormalizeCatalog("pubmed", raw)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "pubmed__search_articles", entries[0].ExposedName)
	assert.Equal(t, "pubmed__get_article", entries[1].ExposedName)
	assert.NotEmpty(t, entries[0].Digest)
}

func TestNormalizeCatalogNameCollisionRejectsAll(t *testing.T) {
	raw := []*mcp.Tool{
		{Name: "a", InputSchema: schemaObj(nil)},
		{Name: "a", InputSchema: schemaObj(nil)},
	}
	_, err := NormalizeCatalog("s", raw)
	require.Error(t, err)
}

func TestNormalizeCatalogIllegalNameRejects(t *testing.T) {
	raw := []*mcp.Tool{{Name: "bad name;rm", InputSchema: schemaObj(nil)}}
	_, err := NormalizeCatalog("s", raw)
	require.Error(t, err)
}

func TestNormalizeCatalogEmptyRejects(t *testing.T) {
	_, err := NormalizeCatalog("s", nil)
	require.Error(t, err)
}

func TestNormalizeCatalogOversizeSchemaRejects(t *testing.T) {
	big := make(map[string]any)
	big["type"] = "object"
	big["properties"] = map[string]any{"x": map[string]any{"type": "string", "default": make([]byte, MaxToolSchemaBytes+1)}}
	raw := []*mcp.Tool{{Name: "big", InputSchema: big}}
	_, err := NormalizeCatalog("s", raw)
	require.Error(t, err)
}

func TestNormalizeCatalogExposedCollisionAcrossNames(t *testing.T) {
	// Different remote names that normalize to the same exposed name cannot
	// happen with our deterministic normalization, but guard anyway via a
	// direct duplicate check on the exposed map (covered by the two same-name
	// case). Here we assert distinct names produce distinct exposed names.
	raw := []*mcp.Tool{
		{Name: "x", InputSchema: schemaObj(nil)},
		{Name: "y", InputSchema: schemaObj(nil)},
	}
	entries, err := NormalizeCatalog("s", raw)
	require.NoError(t, err)
	assert.NotEqual(t, entries[0].ExposedName, entries[1].ExposedName)
}

func TestReadOnlyHintIsInformational(t *testing.T) {
	hint := true
	raw := []*mcp.Tool{
		{Name: "ro", InputSchema: schemaObj(nil), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: hint}},
	}
	entries, err := NormalizeCatalog("s", raw)
	require.NoError(t, err)
	assert.True(t, entries[0].ReadOnlyHint)
	// Local RiskClass stays conservative regardless of the hint.
	def := BuildToolDefinition(entries[0], domain.RiskExternal)
	assert.Equal(t, domain.RiskExternal, def.RiskClass)
}

func TestToolRetryNever(t *testing.T) {
	tool := &Tool{}
	assert.Equal(t, domain.ToolRetryNever, tool.RetryPolicy().Mode)
	assert.Zero(t, tool.RetryPolicy().MaxRetries)
}

func TestToolExecutionClassExclusive(t *testing.T) {
	tool := &Tool{}
	assert.Equal(t, domain.ExecutionExclusive, tool.ExecutionClass())
}

func TestSecretLikeEnvKeys(t *testing.T) {
	assert.True(t, SecretLikeEnvKeys["API_KEY"])
	assert.True(t, SecretLikeEnvKeys["ENNOTE_BOOTSTRAP_TOKEN"])
	assert.False(t, SecretLikeEnvKeys["FOO"])
}

func TestMCPToolStandingApprovalScopeBindsImmutableIdentity(t *testing.T) {
	tool := &Tool{
		RemoteName:       "search_articles",
		ProfileVersionID: "version-123",
		SchemaDigest:     "schema-digest-abc",
		ProjectID:        "project-1",
		BindingID:        "binding-1",
		BindingRevision:  3,
		CatalogDigest:    "catalog-1",
	}
	scope, err := tool.StandingApprovalScope(nil)
	require.NoError(t, err)
	assert.Equal(t, "mcp_tool", scope.Kind)
	assert.Equal(t, 2, scope.ScopeVersion)
	assert.Equal(t, "project-1:binding-1:version-123:3:catalog-1:schema-digest-abc:search_articles", scope.Key)
	assert.NotEmpty(t, scope.Display)

	// Any part of the frozen identity changing must produce a different key so
	// the standing rule automatically misses.
	variants := []func(*Tool){
		func(t *Tool) { t.ProfileVersionID = "version-NEW" },
		func(t *Tool) { t.SchemaDigest = "schema-digest-NEW" },
		func(t *Tool) { t.RemoteName = "other_tool" },
		func(t *Tool) { t.BindingID = "binding-2" },
		func(t *Tool) { t.BindingRevision = 4 },
		func(t *Tool) { t.CatalogDigest = "catalog-2" },
		func(t *Tool) { t.ProjectID = "project-2" },
	}
	for i, mutate := range variants {
		mutated := *tool
		mutate(&mutated)
		scope2, err := mutated.StandingApprovalScope(nil)
		require.NoError(t, err, "variant %d", i)
		assert.NotEqual(t, scope.Key, scope2.Key, "variant %d must invalidate the rule", i)
	}
}

func TestMCPToolStandingApprovalScopeIncompleteFails(t *testing.T) {
	tool := &Tool{RemoteName: "x"} // missing profile version + digest
	_, err := tool.StandingApprovalScope(nil)
	require.Error(t, err)
}
