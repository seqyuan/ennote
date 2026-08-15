package domain

import (
	"encoding/json"
	"time"
)

// MCP transport kinds supported in P1.
const (
	MCPTransportStdio          = "stdio"
	MCPTransportStreamableHTTP = "streamable_http"
	MCPTransportLegacySSE      = "legacy_sse"
)

// MCP profile source kinds.
const (
	MCPSourceManaged     = "managed"
	MCPSourceProjectFile = "project_file"
	MCPSourceBundled     = "bundled"
)

// MCPServerProfile is the stable identity of an MCP server definition.
type MCPServerProfile struct {
	ID            string    `json:"id"`
	DisplayName   string    `json:"displayName"`
	Slug          string    `json:"slug"`
	SourceKind    string    `json:"sourceKind"`
	ProjectScope  *string   `json:"projectScope,omitempty"`
	SourceLocator string    `json:"sourceLocator,omitempty"`
	Lifecycle     string    `json:"lifecycleStatus"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	LatestVersion int       `json:"latestVersion"`
}

// MCPServerProfileVersion is an immutable connection configuration.
type MCPServerProfileVersion struct {
	ID             string            `json:"id"`
	ProfileID      string            `json:"profileId"`
	Version        int               `json:"version"`
	Transport      string            `json:"transport"`
	Executable     string            `json:"executable,omitempty"`
	Argv           []string          `json:"argv,omitempty"`
	Endpoint       string            `json:"endpoint,omitempty"`
	EnvLiterals    map[string]string `json:"envLiterals,omitempty"`
	EnvCredentials map[string]string `json:"envCredentials,omitempty"` // {envName: credentialRef}
	HeaderLiterals map[string]string `json:"headerLiterals,omitempty"`
	HeaderCreds    map[string]string `json:"headerCredentials,omitempty"`
	CWD            string            `json:"cwd,omitempty"`
	TimeoutMS      int               `json:"timeoutMs"`
	NetworkPolicy  string            `json:"networkPolicy"`
	ConfigDigest   string            `json:"configDigest"`
	CreatedAt      time.Time         `json:"createdAt"`
}

// MCPProjectBinding is the per-Project enablement and exact tool selection.
type MCPProjectBinding struct {
	ID                      string            `json:"id"`
	ProjectID               string            `json:"projectId"`
	ProfileVersionID        string            `json:"profileVersionId"`
	DesiredEnabled          bool              `json:"desiredEnabled"`
	Required                bool              `json:"required"`
	SelectedRemoteToolNames []string          `json:"selectedRemoteToolNames"`
	CredentialRefs          map[string]string `json:"credentialRefs,omitempty"` // {envName: credentialRef}
	Revision                int               `json:"revision"`
	CreatedAt               time.Time         `json:"createdAt"`
	UpdatedAt               time.Time         `json:"updatedAt"`
}

// MCPCatalogEntry is a normalized, bounded tool definition from a server.
type MCPCatalogEntry struct {
	RemoteName   string          `json:"remoteName"`
	ExposedName  string          `json:"exposedName"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	ReadOnlyHint bool            `json:"readOnlyHint,omitempty"`
	// Digest binds the schema; used for approval identity and staleness.
	Digest string `json:"digest"`
}

// MCPRequestStatus tracks the durable lifecycle of one MCP tool call.
type MCPRequestStatus string

const (
	MCPRequestPlanned        MCPRequestStatus = "planned"
	MCPRequestDispatched     MCPRequestStatus = "dispatched"
	MCPRequestCompleted      MCPRequestStatus = "completed"
	MCPRequestFailed         MCPRequestStatus = "failed"
	MCPRequestCancelled      MCPRequestStatus = "cancelled"
	MCPRequestOutcomeUnknown MCPRequestStatus = "outcome_unknown"
)
