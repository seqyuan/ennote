package mcpclient

import (
	"context"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePublisher struct {
	mu     sync.Mutex
	calls  []published
	closed bool
}

type published struct {
	name string
	mime string
	size int
}

func (p *fakePublisher) PublishBytes(_ context.Context, _ string, name, mime string, data []byte) (domain.ArtifactReference, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, published{name: name, mime: mime, size: len(data)})
	return domain.ArtifactReference{ArtifactID: "art-1", Name: name, MIMEType: mime, SizeBytes: int64(len(data))}, nil
}

func toolWithPublisher(pub ResultPublisher) *Tool {
	return &Tool{
		DefinitionSnapshot: domain.ToolDefinition{Name: "s__t", RiskClass: domain.RiskExternal},
		RemoteName:         "t",
		Publisher:          pub,
		ConnectionProvider: func() *Session { return nil }, // not used for projection
	}
}

func TestProjectResultTextBounded(t *testing.T) {
	pub := &fakePublisher{}
	tool := toolWithPublisher(pub)
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "hello world"},
		&mcp.TextContent{Text: "second line"},
	}}
	out, _, err := tool.projectResult(context.Background(), domain.ToolCall{ID: "tc", Name: "s__t"}, result)
	require.NoError(t, err)
	assert.Equal(t, "hello world\nsecond line", out.Content)
	assert.False(t, out.IsError)
	assert.Empty(t, out.Artifacts)
	assert.Empty(t, pub.calls)
}

func TestProjectResultImagePublishesArtifact(t *testing.T) {
	pub := &fakePublisher{}
	tool := toolWithPublisher(pub)
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: []byte("fake-image-bytes"), MIMEType: "image/png"},
	}}
	out, _, err := tool.projectResult(context.Background(), domain.ToolCall{ID: "tc", Name: "s__t"}, result)
	require.NoError(t, err)
	require.Len(t, out.Artifacts, 1)
	assert.Equal(t, "image/png", out.Artifacts[0].MIMEType)
	assert.Contains(t, out.Content, "image published as artifact")
	require.Len(t, pub.calls, 1)
	assert.Equal(t, "image/png", pub.calls[0].mime)
	assert.Equal(t, 16, pub.calls[0].size)
}

func TestProjectResultStructuredContent(t *testing.T) {
	tool := toolWithPublisher(&fakePublisher{})
	result := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: "raw text"}},
		StructuredContent: map[string]any{"hits": 3},
	}
	out, _, err := tool.projectResult(context.Background(), domain.ToolCall{ID: "tc", Name: "s__t"}, result)
	require.NoError(t, err)
	// Structured content takes precedence and is canonical JSON.
	assert.JSONEq(t, `{"hits":3}`, out.Content)
}

func TestProjectResultOversizeTextFailsClosed(t *testing.T) {
	tool := toolWithPublisher(&fakePublisher{})
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: string(make([]byte, MaxResultTextBytes+1))},
	}}
	_, _, err := tool.projectResult(context.Background(), domain.ToolCall{ID: "tc", Name: "s__t"}, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size limit")
}

func TestProjectResultUnknownContentFailsClosed(t *testing.T) {
	tool := toolWithPublisher(&fakePublisher{})
	// A nil/unknown content block must fail closed, never inject raw JSON.
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "ok"},
		nil,
	}}
	_, _, err := tool.projectResult(context.Background(), domain.ToolCall{ID: "tc", Name: "s__t"}, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized")
}

func TestProjectResultResourceLinkBounded(t *testing.T) {
	tool := toolWithPublisher(nil)
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ResourceLink{URI: "https://example.com/resource/42"},
	}}
	out, _, err := tool.projectResult(context.Background(), domain.ToolCall{ID: "tc", Name: "s__t"}, result)
	require.NoError(t, err)
	assert.Contains(t, out.Content, "[resource link: https://example.com/resource/42]")

	// A disallowed scheme fails closed.
	bad := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ResourceLink{URI: "javascript:alert(1)"},
	}}
	_, _, err = tool.projectResult(context.Background(), domain.ToolCall{ID: "tc", Name: "s__t"}, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")
}
