package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain handles the stdio-server subprocess mode: when the marker
// environment variable is set, the test binary acts as an MCP stdio server.
func TestMain(m *testing.M) {
	if os.Getenv("ENNOTE_MCP_TEST_SERVER") == "1" {
		runTestMCPServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runTestMCPServer serves a tiny MCP server over stdio using the SDK.
func runTestMCPServer() {
	server := mcp.NewServer(&mcp.Implementation{Name: "ennote-test-server", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", Description: "Echo arguments back",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + args.Text}}}, nil
		})
	server.AddTool(&mcp.Tool{Name: "fail", Description: "Always fails",
		InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "boom"}}, IsError: true}, nil
		})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = server.Connect(ctx, &mcp.StdioTransport{}, nil)
	// In notify mode, fire a tools/list_changed notification shortly after the
	// client initializes so the client-side future-catalog staleness handler
	// can be observed.
	if os.Getenv("ENNOTE_MCP_TEST_NOTIFY") == "1" {
		go func() {
			time.Sleep(400 * time.Millisecond)
			server.RemoveTools("fail")
		}()
	}
	<-ctx.Done()
}

// selfExec returns the current test binary path for subprocess reuse.
func selfExec(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	return exe
}

func TestStdioConnectAndCallTool(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration in short mode")
	}
	version := &domain.MCPServerProfileVersion{
		Transport:   domain.MCPTransportStdio,
		Executable:  selfExec(t),
		Argv:        nil,
		EnvLiterals: map[string]string{"ENNOTE_MCP_TEST_SERVER": "1"},
		TimeoutMS:   10000,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := Connect(ctx, version, ConnectOption{Logger: nil})
	require.NoError(t, err)
	defer session.Close()

	tools, err := session.ListTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "echo", tools[0].Name)

	result, err := session.CallTool(ctx, "echo", map[string]any{"text": "hello"})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "echo:hello", text.Text)

	failResult, err := session.CallTool(ctx, "fail", map[string]any{})
	require.NoError(t, err)
	assert.True(t, failResult.IsError)
}

func TestStdioEnvironmentIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration in short mode")
	}
	// Secret env name with literal value must fail closed at config time.
	version := &domain.MCPServerProfileVersion{
		Transport:   domain.MCPTransportStdio,
		Executable:  selfExec(t),
		EnvLiterals: map[string]string{"API_KEY": "should-never-work"},
	}
	_, err := Connect(context.Background(), version, ConnectOption{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential reference")
}

func TestStdioCredentialResolutionInjected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stdio integration in short mode")
	}
	version := &domain.MCPServerProfileVersion{
		Transport:      domain.MCPTransportStdio,
		Executable:     selfExec(t),
		EnvCredentials: map[string]string{"MY_SECRET": "env:REAL_SECRET"},
		EnvLiterals:    map[string]string{"ENNOTE_MCP_TEST_SERVER": "1"},
	}
	opts := ConnectOption{ResolveSecret: func(ref string) (string, error) {
		if ref == "env:REAL_SECRET" {
			return "resolved-value", nil
		}
		return "", fmt.Errorf("unknown ref %s", ref)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// The server should start and the resolved credential be injected. We only
	// assert that connect succeeds and the session can close cleanly.
	session, err := Connect(ctx, version, opts)
	require.NoError(t, err)
	session.Close()
}
