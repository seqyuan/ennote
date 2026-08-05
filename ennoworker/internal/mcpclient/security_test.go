package mcpclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSecretLikeEnvNameCatchesCommonNames(t *testing.T) {
	for _, name := range []string{
		"API_KEY", "GITHUB_TOKEN", "OPENAI_API_KEY", "PUBMED_API_KEY",
		"ANTHROPIC_API_KEY", "MY_PASSWORD", "SERVICE_CREDENTIAL", "PRIVATE_KEY",
		"ACCESS_TOKEN", "ENNOTE_BOOTSTRAP_TOKEN",
	} {
		assert.True(t, IsSecretLikeEnvName(name), "%s must be secret-like", name)
	}
	for _, name := range []string{"LOG_LEVEL", "PATH", "HOME", "ENNOTE_MCP_TEST_SERVER", "PORT"} {
		assert.False(t, IsSecretLikeEnvName(name), "%s must not be secret-like", name)
	}
}

func TestValidateEndpointNoUserinfoRejectsEmbeddedCredentials(t *testing.T) {
	err := validateEndpointNoUserinfo("https://user:pass@example.com/mcp")
	require.Error(t, err)
	assert.NoError(t, validateEndpointNoUserinfo("https://example.com/mcp"))
	assert.NoError(t, validateEndpointNoUserinfo("http://127.0.0.1:8080/mcp"))
}

func TestValidateResolvedHostRejectsPrivateDNS(t *testing.T) {
	// Localhost literal is always allowed.
	assert.NoError(t, validateResolvedHost("127.0.0.1", false, "default"))
	assert.NoError(t, validateResolvedHost("localhost", false, "default"))
	// Private literal requires an explicit network policy.
	err := validateResolvedHost("10.0.0.5", false, "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private network")
	// Explicit allowPrivate permits it.
	assert.NoError(t, validateResolvedHost("10.0.0.5", true, "default"))
}

func TestDispatchRecordUsesDigestNotRawArguments(t *testing.T) {
	// digestJSON must produce a 64-hex digest and never embed raw content, so
	// the dispatch record cannot leak arguments (tokens / user content) into
	// SQLite even if a caller passes the raw JSON.
	raw := json.RawMessage(`{"query":"patient PII","token":"secret-value"}`)
	digest := digestJSON(raw)
	assert.Len(t, digest, 64)
	assert.NotContains(t, digest, "secret-value")
	assert.NotContains(t, digest, "patient")
}

func TestProjectResultAggregateTotalIsBounded(t *testing.T) {
	tool := toolWithPublisher(nil)
	half := MaxResultTextBytes - 1024
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: string(make([]byte, half))},
		&mcp.TextContent{Text: string(make([]byte, half))},
	}}
	_, _, err := tool.projectResult(context.Background(), domain.ToolCall{ID: "tc", Name: "s__t"}, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aggregate")
}
