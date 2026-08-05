package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testMCPServer builds a real MCP server exposing one echo tool and returns
// its HTTP handler bound to the given server instance.
func testMCPServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ennote-test", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "echo text"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
	})
	return s
}

// TestStreamableHTTPRoundTrip exercises the full Streamable HTTP transport
// over a real HTTP server: initialize, tools/list, and a tool call. This is
// the transport-level fixture the matrix item 15 requires (not just config
// validation).
func TestStreamableHTTPRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping HTTP fixture in short mode")
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testMCPServer() }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	version := &domain.MCPServerProfileVersion{
		Transport: domain.MCPTransportStreamableHTTP, Endpoint: ts.URL + "/mcp",
		TimeoutMS: 10000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := Connect(ctx, version, ConnectOption{})
	require.NoError(t, err)
	defer session.Close()

	tools, err := session.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)

	result, err := session.CallTool(ctx, "echo", map[string]any{"text": "hello http"})
	require.NoError(t, err)
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "hello http", text.Text)
}

// TestLegacySSERoundTrip exercises the legacy HTTP+SSE transport over a real
// SSE handler: initialize, tools/list, and a tool call.
func TestLegacySSERoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping SSE fixture in short mode")
	}
	handler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return testMCPServer() }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	version := &domain.MCPServerProfileVersion{
		Transport: domain.MCPTransportLegacySSE, Endpoint: ts.URL,
		TimeoutMS: 10000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := Connect(ctx, version, ConnectOption{})
	require.NoError(t, err)
	defer session.Close()

	tools, err := session.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)

	result, err := session.CallTool(ctx, "echo", map[string]any{"text": "hello sse"})
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

// TestHTTPHeaderInjection verifies literal and credential headers are attached
// to every outbound HTTP request (transport-level credential wiring).
func TestHTTPHeaderInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping HTTP fixture in short mode")
	}
	var sawAuth, sawXKey string
	var mu int
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return testMCPServer() }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			mu++
			sawAuth = r.Header.Get("Authorization")
		}
		if r.Header.Get("X-API-Key") != "" {
			sawXKey = r.Header.Get("X-API-Key")
		}
		inner.ServeHTTP(w, r)
	}))
	defer ts.Close()

	version := &domain.MCPServerProfileVersion{
		Transport: domain.MCPTransportStreamableHTTP, Endpoint: ts.URL + "/mcp",
		TimeoutMS: 10000,
		HeaderLiterals: map[string]string{"X-API-Key": "literal-value"},
		HeaderCreds:    map[string]string{"Authorization": "env:TEST_BEARER"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Setenv("TEST_BEARER", "Bearer secret-token")
	session, err := Connect(ctx, version, ConnectOption{})
	require.NoError(t, err)
	defer session.Close()

	_, err = session.ListTools(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret-token", sawAuth, "credential header must be attached")
	assert.Equal(t, "literal-value", sawXKey, "literal header must be attached")
	assert.NotZero(t, mu)
}
