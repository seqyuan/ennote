package mcpclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ToolCallRecorder records the durable MCP request state machine
// (planned -> dispatched -> completed/failed/cancelled/outcome_unknown).
// The agent Loop's recorder fulfils this for Run execution; tests inject a
// no-op. Recording failures are non-fatal for the tool result.
type ToolCallRecorder interface {
	RecordMCPStep(runServerID, runToolID, toolCallID string, generation int,
		status domain.MCPRequestStatus, requestDigest, responseDigest, errorCode string)
}

// ResultPublisher publishes binary/structured content into Artifacts so the
// model only receives a bounded reference. Nil means no artifact support.
type ResultPublisher interface {
	PublishBytes(ctx context.Context, toolCallID, name string, mime string, data []byte) (domain.ArtifactReference, error)
}

// Tool is a frozen MCP tool adapter. It never retries: remote dispatch is
// exactly-once-at-most from the local perspective. All identity (name, schema,
// risk) is bound to the frozen snapshot; the underlying session is provided by
// a per-Run connection provider.
type Tool struct {
	// DefinitionSnapshot is the frozen domain definition.
	DefinitionSnapshot domain.ToolDefinition
	// ServerSlug is the immutable server slug (for provenance in results).
	ServerSlug string
	// RemoteName is the frozen remote tool name.
	RemoteName string
	// ReadOnlyHint mirrors the server annotation; informational only, never
	// used to lower local risk.
	ReadOnlyHint bool
	// Recorder persists MCP request state.
	Recorder ToolCallRecorder
	// Publisher is optional Artifact publishing for image/blob content.
	Publisher ResultPublisher
	// RunServerID / RunToolID bind to the frozen Run snapshot.
	RunServerID string
	RunToolID   string
	// ProfileVersionID binds the immutable server profile version; part of the
	// standing-approval identity so profile changes invalidate rules.
	ProfileVersionID string
	// ProjectID and BindingID/BindingRevision bind the per-Project binding;
	// part of the standing-approval identity so binding/credential changes
	// invalidate rules.
	ProjectID       string
	BindingID       string
	BindingRevision int
	// CatalogDigest binds the frozen catalog; catalog changes invalidate rules.
	CatalogDigest string
	// ConnectionProvider is read at call time from the connection provider.
	ConnectionProvider func() *Session
	// GenerationProvider reports the live connection generation (0 when absent).
	GenerationProvider func() int
	// SchemaDigest binds approval identity.
	SchemaDigest string
}

// StandingApprovalScope implements domain.StandingApprovalScopeProvider. The
// scope key binds the FULL frozen identity (profile version + binding + catalog
// digest + schema digest + remote name + project): any Binding revision change,
// credential/auth change, profile edit, or catalog change invalidates the rule.
func (t *Tool) StandingApprovalScope(arguments json.RawMessage) (domain.StandingApprovalScope, error) {
	if t.ProfileVersionID == "" || t.SchemaDigest == "" || t.RemoteName == "" || t.ProjectID == "" || t.BindingID == "" {
		return domain.StandingApprovalScope{}, fmt.Errorf("mcp tool identity is incomplete for standing approval")
	}
	key := fmt.Sprintf("%s:%s:%s:%d:%s:%s:%s",
		t.ProjectID, t.BindingID, t.ProfileVersionID, t.BindingRevision,
		t.CatalogDigest, t.SchemaDigest, t.RemoteName)
	if len(key) > 512 {
		return domain.StandingApprovalScope{}, fmt.Errorf("mcp standing scope key too long")
	}
	display := fmt.Sprintf("%s (%s/%s, profile %s)", t.RemoteName, t.ProjectID[:min(8, len(t.ProjectID))], t.BindingID[:min(8, len(t.BindingID))], t.ProfileVersionID[:min(8, len(t.ProfileVersionID))])
	if len(display) > 200 {
		display = display[:200]
	}
	return domain.StandingApprovalScope{
		Kind:         "mcp_tool",
		ScopeVersion: 2,
		Key:          key,
		Display:      display,
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Definition implements tools.Tool.
func (t *Tool) Definition() domain.ToolDefinition { return t.DefinitionSnapshot }

// ExecutionClass implements tools.ClassifiedTool. Third-party MCP tools are
// exclusive: they mutate external state and must not run on a read-only lane.
func (t *Tool) ExecutionClass() domain.ExecutionClass { return domain.ExecutionExclusive }

// RetryPolicy implements domain.RetryPolicyProvider. MCP calls never retry
// automatically: once dispatched, the remote outcome is unknown and replaying
// could duplicate external side effects.
func (t *Tool) RetryPolicy() domain.ToolRetryPolicy {
	return domain.ToolRetryPolicy{Mode: domain.ToolRetryNever, MaxRetries: 0}
}

// Execute implements tools.Tool. The session must be non-nil; the caller (the
// per-Run connection provider) guarantees the frozen connection is live.
func (t *Tool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	if t.ConnectionProvider == nil || t.ConnectionProvider() == nil {
		return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
			Content: "MCP connection is not available", IsError: true}, nil
	}
	generation := 0
	if t.GenerationProvider != nil {
		generation = t.GenerationProvider()
	}
	var arguments map[string]any
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
			t.record(call.ID, generation, domain.MCPRequestFailed, call.Arguments, nil, "invalid_arguments")
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
				Content: "invalid tool arguments: " + err.Error(), IsError: true}, nil
		}
	}
	if t.Recorder != nil {
		t.Recorder.RecordMCPStep(t.RunServerID, t.RunToolID, call.ID, generation,
			domain.MCPRequestDispatched, digestJSON(call.Arguments), "", "")
	}
	result, err := t.ConnectionProvider().CallTool(ctx, t.RemoteName, arguments)
	if err != nil {
		// Dispatch happened; the outcome is unknown or failed. We must not
		// resend. Cancellation is terminal; everything else is outcome_unknown
		// because the server may have executed the call.
		if ctx.Err() != nil {
			t.record(call.ID, generation, domain.MCPRequestCancelled, call.Arguments, nil, "cancelled")
			return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
				Content: "MCP call cancelled", IsError: true}, nil
		}
		t.record(call.ID, generation, domain.MCPRequestOutcomeUnknown, call.Arguments, nil, "transport_error")
		return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
			Content: "MCP call outcome unknown: " + err.Error(), IsError: true}, nil
	}
	projected, responseDigest, err := t.projectResult(ctx, call, result)
	if err != nil {
		t.record(call.ID, generation, domain.MCPRequestFailed, call.Arguments, nil, "projection_error")
		return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
			Content: "MCP result projection failed: " + err.Error(), IsError: true}, nil
	}
	t.record(call.ID, generation, domain.MCPRequestCompleted, call.Arguments, responseDigest, "")
	return projected, nil
}

func (t *Tool) record(toolCallID string, generation int, status domain.MCPRequestStatus,
	requestJSON, responseJSON json.RawMessage, errorCode string) {
	if t.Recorder == nil {
		return
	}
	var reqDigest, respDigest string
	if len(requestJSON) > 0 {
		reqDigest = digestJSON(requestJSON)
	}
	if len(responseJSON) > 0 {
		respDigest = digestJSON(responseJSON)
	}
	t.Recorder.RecordMCPStep(t.RunServerID, t.RunToolID, toolCallID, generation,
		status, reqDigest, respDigest, errorCode)
}

func digestJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	_ = json.Compact(&buf, raw)
	return fmt.Sprintf("%x", sha256Sum(buf.Bytes()))
}

// projectResult maps MCP content blocks into a bounded ToolResult. Binary and
// image content is published to Artifacts; text is joined with provenance.
// The total projected size is bounded across ALL blocks, not per block.
func (t *Tool) projectResult(ctx context.Context, call domain.ToolCall, result *mcp.CallToolResult) (domain.ToolResult, []byte, error) {
	var text []string
	var artifacts []domain.ArtifactReference
	var total int
	for _, block := range result.Content {
		switch c := block.(type) {
		case *mcp.TextContent:
			if len(c.Text) > MaxResultTextBytes {
				return domain.ToolResult{}, nil, fmt.Errorf("text result exceeds size limit")
			}
			text = append(text, c.Text)
			total += len(c.Text)
			if total > MaxResultTextBytes {
				return domain.ToolResult{}, nil, fmt.Errorf("aggregate text result exceeds size limit")
			}
		case *mcp.ImageContent:
			if len(c.Data) > 8*1024*1024 {
				return domain.ToolResult{}, nil, fmt.Errorf("image result exceeds size limit")
			}
			ref, err := t.publishBytes(ctx, call.ID, "mcp-image-"+t.RemoteName, c.MIMEType, c.Data)
			if err != nil {
				return domain.ToolResult{}, nil, err
			}
			artifacts = append(artifacts, ref)
			text = append(text, fmt.Sprintf("[image published as artifact %s]", ref.Name))
			total += len(text[len(text)-1])
			if total > MaxResultTextBytes {
				return domain.ToolResult{}, nil, fmt.Errorf("aggregate result exceeds size limit")
			}
		case *mcp.AudioContent:
			if len(c.Data) > 8*1024*1024 {
				return domain.ToolResult{}, nil, fmt.Errorf("audio result exceeds size limit")
			}
			ref, err := t.publishBytes(ctx, call.ID, "mcp-audio-"+t.RemoteName, c.MIMEType, c.Data)
			if err != nil {
				return domain.ToolResult{}, nil, err
			}
			artifacts = append(artifacts, ref)
			text = append(text, fmt.Sprintf("[audio published as artifact %s]", ref.Name))
			total += len(text[len(text)-1])
			if total > MaxResultTextBytes {
				return domain.ToolResult{}, nil, fmt.Errorf("aggregate result exceeds size limit")
			}
		case *mcp.EmbeddedResource:
			ref, err := t.publishEmbedded(ctx, call.ID, c)
			if err != nil {
				return domain.ToolResult{}, nil, err
			}
			if ref != nil {
				artifacts = append(artifacts, *ref)
				text = append(text, fmt.Sprintf("[embedded resource published as artifact %s]", ref.Name))
				total += len(text[len(text)-1])
				if total > MaxResultTextBytes {
					return domain.ToolResult{}, nil, fmt.Errorf("aggregate result exceeds size limit")
				}
			}
		case *mcp.ResourceLink:
			// A resource link carries a URI, not content. Surface it as a
			// bounded reference instead of failing closed; never fetch it.
			uri := c.URI
			if len(uri) > 2048 {
				return domain.ToolResult{}, nil, fmt.Errorf("resource link URI exceeds size limit")
			}
			if !validResourceURI(uri) {
				return domain.ToolResult{}, nil, fmt.Errorf("resource link URI uses a disallowed scheme")
			}
			line := fmt.Sprintf("[resource link: %s]", uri)
			text = append(text, line)
			total += len(line)
			if total > MaxResultTextBytes {
				return domain.ToolResult{}, nil, fmt.Errorf("aggregate result exceeds size limit")
			}
		default:
			// Unrecognized content type: fail closed, never inject raw JSON.
			return domain.ToolResult{}, nil, fmt.Errorf("unrecognized MCP content type")
		}
	}
	var content string
	if result.StructuredContent != nil {
		structured, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return domain.ToolResult{}, nil, err
		}
		if len(structured) > MaxResultTextBytes {
			return domain.ToolResult{}, nil, fmt.Errorf("structured result exceeds size limit")
		}
		content = string(structured)
	} else {
		content = strings.Join(text, "\n")
	}
	responseDigest, _ := json.Marshal(map[string]any{"content": content, "structured": result.StructuredContent != nil, "isError": result.IsError})
	return domain.ToolResult{
		ToolCallID: call.ID, ToolName: call.Name, Content: content,
		IsError: result.IsError, Artifacts: artifacts,
	}, responseDigest, nil
}

func (t *Tool) publishBytes(ctx context.Context, toolCallID, name, mime string, data []byte) (domain.ArtifactReference, error) {
	if t.Publisher == nil {
		return domain.ArtifactReference{}, fmt.Errorf("artifact publishing is unavailable")
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	if !validMIME(mime) {
		return domain.ArtifactReference{}, fmt.Errorf("invalid MIME type %q", mime)
	}
	return t.Publisher.PublishBytes(ctx, toolCallID, name, mime, data)
}

func (t *Tool) publishEmbedded(ctx context.Context, toolCallID string, c *mcp.EmbeddedResource) (*domain.ArtifactReference, error) {
	if c.Resource == nil {
		return nil, nil
	}
	if data := c.Resource.Text; data != "" {
		if len(data) > MaxResultTextBytes {
			return nil, fmt.Errorf("embedded resource text exceeds size limit")
		}
		if t.Publisher == nil {
			return nil, fmt.Errorf("artifact publishing is unavailable")
		}
		ref, err := t.Publisher.PublishBytes(ctx, toolCallID, "mcp-resource-"+t.RemoteName, "text/plain", []byte(data))
		if err != nil {
			return nil, err
		}
		return &ref, nil
	}
	if len(c.Resource.Blob) > 0 {
		blob, err := base64.StdEncoding.DecodeString(string(c.Resource.Blob))
		if err != nil {
			return nil, fmt.Errorf("decode embedded blob: %w", err)
		}
		if len(blob) > 8*1024*1024 {
			return nil, fmt.Errorf("embedded blob exceeds size limit")
		}
		if t.Publisher == nil {
			return nil, fmt.Errorf("artifact publishing is unavailable")
		}
		ref, err := t.Publisher.PublishBytes(ctx, toolCallID, "mcp-resource-"+t.RemoteName,
			c.Resource.MIMEType, blob)
		if err != nil {
			return nil, err
		}
		return &ref, nil
	}
	return nil, nil
}

func validMIME(mime string) bool {
	if len(mime) == 0 || len(mime) > 128 {
		return false
	}
	for _, r := range mime {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '/' || r == '+' || r == '-' || r == '.' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// validResourceURI restricts resource-link URIs to http(s) and the mcp scheme
// so an arbitrary server-provided scheme cannot smuggle content into the model
// context. The link is displayed as a bounded reference; it is never fetched.
func validResourceURI(uri string) bool {
	scheme, _, ok := strings.Cut(uri, ":")
	if !ok {
		return false
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	switch scheme {
	case "http", "https", "mcp", "file":
		return true
	default:
		return false
	}
}
