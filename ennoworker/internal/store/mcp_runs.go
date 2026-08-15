package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// MCPCatalogRepo persists the binding-scoped catalog cache and freezes Run
// snapshots. The cache key includes binding revision and auth generation so a
// server that returns a different toolset per identity cannot leak a catalog
// across Projects or credential generations.
type MCPCatalogRepo struct {
	DB       *sql.DB
	CacheDir string
}

// MCPRunRepo persists frozen Run server/tool snapshots and request state.
type MCPRunRepo struct{ DB *sql.DB }

// MCPCatalogCacheRow is one cached normalized catalog. The cache key binds
// binding revision AND credential digest so a server that returns a different
// toolset per identity can never leak a catalog across Projects or credential
// generations. CredentialDigest hashes only reference names, never values.
type MCPCatalogCacheRow struct {
	BindingID            string
	BindingRevision      int
	ProfileVersionID     string
	ProtocolVersion      string
	AuthGeneration       int
	CredentialDigest     string
	ServerIdentityDigest string
	CatalogDigest        string
	Tools                []domain.MCPCatalogEntry
	AnnotationsJSON      string
	FetchedAt            time.Time
}

// PutCatalog stores or replaces the binding-scoped catalog cache row.
func (r *MCPCatalogRepo) PutCatalog(ctx context.Context, row MCPCatalogCacheRow) error {
	if r.CacheDir != "" {
		return r.putCatalogFile(row)
	}
	toolsJSON, err := json.Marshal(row.Tools)
	if err != nil {
		return err
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO mcp_catalog_cache
		(binding_id, binding_revision, profile_version_id, protocol_version, auth_generation,
		 credential_digest, server_identity_digest, catalog_digest, tools_json, annotations_json, fetched_at, stale_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		ON CONFLICT(binding_id, binding_revision, profile_version_id, protocol_version, auth_generation, credential_digest)
		DO UPDATE SET server_identity_digest=excluded.server_identity_digest,
			catalog_digest=excluded.catalog_digest, tools_json=excluded.tools_json,
			annotations_json=excluded.annotations_json, fetched_at=excluded.fetched_at, stale_at=NULL`,
		row.BindingID, row.BindingRevision, row.ProfileVersionID, row.ProtocolVersion, row.AuthGeneration,
		row.CredentialDigest, row.ServerIdentityDigest, row.CatalogDigest, string(toolsJSON), row.AnnotationsJSON, roleTime(row.FetchedAt))
	return err
}

// GetCatalog fetches a NON-STALE cache row for the exact binding revision +
// auth generation + credential digest. Stale rows (tools/list_changed) are
// treated as a miss so future Runs must refresh.
func (r *MCPCatalogRepo) GetCatalog(ctx context.Context, bindingID string, bindingRevision, authGeneration int,
	profileVersionID, protocolVersion, credentialDigest string) (*MCPCatalogCacheRow, error) {
	if r.CacheDir != "" {
		return r.getCatalogFile(bindingID, bindingRevision, authGeneration, profileVersionID, protocolVersion, credentialDigest)
	}
	row := &MCPCatalogCacheRow{}
	var toolsJSON, fetchedAt string
	var staleAt sql.NullString
	err := r.DB.QueryRowContext(ctx, `SELECT binding_id, binding_revision, profile_version_id, protocol_version,
		auth_generation, credential_digest, server_identity_digest, catalog_digest, tools_json, annotations_json, fetched_at
		FROM mcp_catalog_cache
		WHERE binding_id=? AND binding_revision=? AND profile_version_id=? AND protocol_version=? AND auth_generation=?
		  AND credential_digest=? AND (stale_at IS NULL OR stale_at = '')`,
		bindingID, bindingRevision, profileVersionID, protocolVersion, authGeneration, credentialDigest).
		Scan(&row.BindingID, &row.BindingRevision, &row.ProfileVersionID, &row.ProtocolVersion,
			&row.AuthGeneration, &row.CredentialDigest, &row.ServerIdentityDigest, &row.CatalogDigest, &toolsJSON,
			&row.AnnotationsJSON, &fetchedAt)
	if err != nil {
		return nil, err
	}
	_ = staleAt
	row.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetchedAt)
	if err := json.Unmarshal([]byte(toolsJSON), &row.Tools); err != nil {
		return nil, err
	}
	return row, nil
}

// MarkCatalogStale marks a cached catalog stale so future Runs must refresh.
func (r *MCPCatalogRepo) MarkCatalogStale(ctx context.Context, bindingID string, authGeneration int) error {
	if r.CacheDir != "" {
		return r.markCatalogFilesStale(bindingID, authGeneration)
	}
	_, err := r.DB.ExecContext(ctx,
		`UPDATE mcp_catalog_cache SET stale_at=? WHERE binding_id=? AND auth_generation=?`,
		roleTime(time.Now().UTC()), bindingID, authGeneration)
	return err
}

// RunMCPServerSnapshot is a frozen per-Run server record.
type RunMCPServerSnapshot struct {
	ID                   string
	RunID                string
	BindingID            string
	BindingRevision      int
	ProfileVersionID     string
	ConfigDigest         string
	NegotiatedProtocol   string
	ServerIdentityDigest string
	CatalogDigest        string
	Required             bool
	UnavailableReason    string
	ConnectionGeneration int
	CreatedAt            time.Time
}

// RunMCPToolSnapshot is a frozen per-Run tool record.
type RunMCPToolSnapshot struct {
	ID             string
	RunServerID    string
	RemoteName     string
	ExposedName    string
	Description    string
	InputSchema    json.RawMessage
	OutputSchema   json.RawMessage
	SchemaDigest   string
	RiskClass      domain.RiskClass
	ExecutionClass domain.ExecutionClass
	SourceKind     string
}

// FreezeServer writes a Run server snapshot and returns its id.
func (r *MCPRunRepo) FreezeServer(ctx context.Context, s RunMCPServerSnapshot) (string, error) {
	return r.freezeServerTx(ctx, nil, s)
}

func (r *MCPRunRepo) freezeServerTx(ctx context.Context, tx *sql.Tx, s RunMCPServerSnapshot) (string, error) {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	if tx != nil {
		_, err := tx.ExecContext(ctx, `INSERT INTO run_mcp_servers
			(id, run_id, binding_id, binding_revision, profile_version_id, config_digest,
			 negotiated_protocol, server_identity_digest, catalog_digest, required,
			 unavailable_reason, connection_generation, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, s.RunID, s.BindingID, s.BindingRevision, s.ProfileVersionID, s.ConfigDigest,
			s.NegotiatedProtocol, s.ServerIdentityDigest, s.CatalogDigest, s.Required,
			emptyAsNull(s.UnavailableReason), s.ConnectionGeneration, roleTime(s.CreatedAt))
		return s.ID, err
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO run_mcp_servers
		(id, run_id, binding_id, binding_revision, profile_version_id, config_digest,
		 negotiated_protocol, server_identity_digest, catalog_digest, required,
		 unavailable_reason, connection_generation, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.RunID, s.BindingID, s.BindingRevision, s.ProfileVersionID, s.ConfigDigest,
		s.NegotiatedProtocol, s.ServerIdentityDigest, s.CatalogDigest, s.Required,
		emptyAsNull(s.UnavailableReason), s.ConnectionGeneration, roleTime(s.CreatedAt))
	if err != nil {
		return "", fmt.Errorf("freeze mcp run server: %w", err)
	}
	return s.ID, nil
}

// FreezeServerWithTools atomically writes a Run server snapshot and its frozen
// tool snapshots in one transaction. Either all rows commit or none do: a Run
// never sees a partially frozen MCP snapshot.
func (r *MCPRunRepo) FreezeServerWithTools(ctx context.Context, s RunMCPServerSnapshot,
	tools []RunMCPToolSnapshot) (string, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	serverID, err := r.freezeServerTx(ctx, tx, s)
	if err != nil {
		return "", fmt.Errorf("freeze mcp run server: %w", err)
	}
	for i := range tools {
		tools[i].RunServerID = serverID
		toolID, err := r.freezeToolTx(ctx, tx, tools[i])
		if err != nil {
			return "", err
		}
		// freezeToolTx takes a value copy and auto-assigns an id when empty;
		// the caller must get the assigned id back or the frozen snapshot's
		// RunToolID is empty and mcp_requests FK writes fail.
		tools[i].ID = toolID
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return serverID, nil
}

// FreezeTool writes a frozen Run tool record.
func (r *MCPRunRepo) FreezeTool(ctx context.Context, t RunMCPToolSnapshot) (string, error) {
	return r.freezeToolTx(ctx, nil, t)
}

func (r *MCPRunRepo) freezeToolTx(ctx context.Context, tx *sql.Tx, t RunMCPToolSnapshot) (string, error) {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	inputJSON, _ := json.Marshal(t.InputSchema)
	outputJSON, _ := json.Marshal(t.OutputSchema)
	if string(outputJSON) == "null" {
		outputJSON = nil
	}
	if tx != nil {
		_, err := tx.ExecContext(ctx, `INSERT INTO run_mcp_tools
			(id, run_server_id, remote_name, exposed_name, description, input_schema_json,
			 output_schema_json, schema_digest, risk_class, execution_class, source_kind)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.RunServerID, t.RemoteName, t.ExposedName, t.Description, string(inputJSON),
			nullableBytes(outputJSON), t.SchemaDigest, string(t.RiskClass), string(t.ExecutionClass), t.SourceKind)
		return t.ID, err
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO run_mcp_tools
		(id, run_server_id, remote_name, exposed_name, description, input_schema_json,
		 output_schema_json, schema_digest, risk_class, execution_class, source_kind)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.RunServerID, t.RemoteName, t.ExposedName, t.Description, string(inputJSON),
		nullableBytes(outputJSON), t.SchemaDigest, string(t.RiskClass), string(t.ExecutionClass), t.SourceKind)
	if err != nil {
		return "", fmt.Errorf("freeze mcp run tool: %w", err)
	}
	return t.ID, nil
}

// ListFrozenTools returns the frozen tool snapshots for a Run's server.
func (r *MCPRunRepo) ListFrozenTools(ctx context.Context, runServerID string) ([]RunMCPToolSnapshot, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, run_server_id, remote_name, exposed_name, description,
		input_schema_json, output_schema_json, schema_digest, risk_class, execution_class, source_kind
		FROM run_mcp_tools WHERE run_server_id = ?`, runServerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tools []RunMCPToolSnapshot
	for rows.Next() {
		t := RunMCPToolSnapshot{}
		var inputJSON, risk, execClass string
		var outputJSON sql.NullString
		if err := rows.Scan(&t.ID, &t.RunServerID, &t.RemoteName, &t.ExposedName, &t.Description,
			&inputJSON, &outputJSON, &t.SchemaDigest, &risk, &execClass, &t.SourceKind); err != nil {
			return nil, err
		}
		t.InputSchema = json.RawMessage(inputJSON)
		if outputJSON.Valid {
			t.OutputSchema = json.RawMessage(outputJSON.String)
		}
		t.RiskClass = domain.RiskClass(risk)
		t.ExecutionClass = domain.ExecutionClass(execClass)
		tools = append(tools, t)
	}
	return tools, rows.Err()
}

// ListFrozenServers returns Run server snapshots with their frozen tools.
func (r *MCPRunRepo) ListFrozenServers(ctx context.Context, runID string) ([]*RunMCPServerSnapshot, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, run_id, binding_id, binding_revision, profile_version_id,
		config_digest, negotiated_protocol, server_identity_digest, catalog_digest, required,
		unavailable_reason, connection_generation, created_at
		FROM run_mcp_servers WHERE run_id = ? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var servers []*RunMCPServerSnapshot
	for rows.Next() {
		s := &RunMCPServerSnapshot{}
		var reason sql.NullString
		var createdAt string
		if err := rows.Scan(&s.ID, &s.RunID, &s.BindingID, &s.BindingRevision, &s.ProfileVersionID,
			&s.ConfigDigest, &s.NegotiatedProtocol, &s.ServerIdentityDigest, &s.CatalogDigest,
			&s.Required, &reason, &s.ConnectionGeneration, &createdAt); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		if reason.Valid {
			s.UnavailableReason = reason.String
		}
		servers = append(servers, s)
	}
	return servers, rows.Err()
}

// MCPRequestRecord is one durable MCP tool call state.
type MCPRequestRecord struct {
	ID                   string
	RunID                string
	RunServerID          string
	RunToolID            string
	ToolCallID           string
	ConnectionGeneration int
	ProtocolRequestID    string
	Status               domain.MCPRequestStatus
	RequestDigest        string
	ResponseDigest       string
	ErrorCode            string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// allowedMCPStatusTransitions defines the legal MCP request state machine.
// Self-transitions are allowed for idempotent re-recording (e.g. a recorder
// retry writing the same terminal status). Terminal states are never left.
var allowedMCPStatusTransitions = map[domain.MCPRequestStatus]map[domain.MCPRequestStatus]bool{
	domain.MCPRequestPlanned: {
		domain.MCPRequestPlanned:        true,
		domain.MCPRequestDispatched:     true,
		domain.MCPRequestFailed:         true,
		domain.MCPRequestCancelled:      true,
		domain.MCPRequestOutcomeUnknown: true,
	},
	domain.MCPRequestDispatched: {
		domain.MCPRequestDispatched:     true,
		domain.MCPRequestCompleted:      true,
		domain.MCPRequestFailed:         true,
		domain.MCPRequestCancelled:      true,
		domain.MCPRequestOutcomeUnknown: true,
	},
	domain.MCPRequestCompleted:      {domain.MCPRequestCompleted: true},
	domain.MCPRequestFailed:         {domain.MCPRequestFailed: true},
	domain.MCPRequestCancelled:      {domain.MCPRequestCancelled: true},
	domain.MCPRequestOutcomeUnknown: {domain.MCPRequestOutcomeUnknown: true},
}

// MCPRequestTransitionError rejects an illegal state transition.
type MCPRequestTransitionError struct {
	From domain.MCPRequestStatus
	To   domain.MCPRequestStatus
}

func (e *MCPRequestTransitionError) Error() string {
	return fmt.Sprintf("illegal mcp request state transition %s -> %s", e.From, e.To)
}

// CreateRequest upserts a request row keyed by (run_id, tool_call_id) so the
// state machine (planned -> dispatched -> terminal) advances in place. Illegal
// transitions (e.g. terminal -> dispatched) are rejected so a request can only
// terminalize exactly once. Returns the row id (existing or new).
func (r *MCPRunRepo) CreateRequest(ctx context.Context, req MCPRequestRecord) (string, error) {
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if req.CreatedAt.IsZero() {
		req.CreatedAt = now
	}
	req.UpdatedAt = now
	// Reuse an existing row for the same (run_id, tool_call_id) so repeated
	// state transitions do not create duplicates.
	var existing string
	err := r.DB.QueryRowContext(ctx, `SELECT id FROM mcp_requests WHERE run_id=? AND tool_call_id=?`,
		req.RunID, req.ToolCallID).Scan(&existing)
	if err == nil {
		req.ID = existing
		// CAS: only advance if the current status permits the new status.
		var current string
		if err := r.DB.QueryRowContext(ctx, `SELECT status FROM mcp_requests WHERE id=?`, req.ID).Scan(&current); err != nil {
			return "", err
		}
		from := domain.MCPRequestStatus(current)
		if !allowedMCPStatusTransitions[from][req.Status] {
			return "", &MCPRequestTransitionError{From: from, To: req.Status}
		}
		_, err = r.DB.ExecContext(ctx, `UPDATE mcp_requests
			SET status=?, response_digest=COALESCE(?, response_digest), error_code=?,
			    connection_generation=?, updated_at=?
			WHERE id=?`,
			string(req.Status), nullableString(&req.ResponseDigest), nullableString(&req.ErrorCode),
			req.ConnectionGeneration, roleTime(req.UpdatedAt), req.ID)
		return req.ID, err
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO mcp_requests
		(id, run_id, run_server_id, run_tool_id, tool_call_id, connection_generation,
		 protocol_request_id, status, request_digest, response_digest, error_code, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.RunID, req.RunServerID, req.RunToolID, req.ToolCallID, req.ConnectionGeneration,
		nullableString(&req.ProtocolRequestID), string(req.Status), req.RequestDigest,
		nullableString(&req.ResponseDigest), nullableString(&req.ErrorCode), roleTime(req.CreatedAt), roleTime(req.UpdatedAt))
	if err != nil {
		return "", fmt.Errorf("create mcp request: %w", err)
	}
	return req.ID, nil
}

// UpdateRequestStatus transitions a request state machine through the CAS
// check. Illegal transitions return an error.
func (r *MCPRunRepo) UpdateRequestStatus(ctx context.Context, id string, status domain.MCPRequestStatus,
	responseDigest, errorCode string) error {
	var current string
	if err := r.DB.QueryRowContext(ctx, `SELECT status FROM mcp_requests WHERE id=?`, id).Scan(&current); err != nil {
		return err
	}
	from := domain.MCPRequestStatus(current)
	if !allowedMCPStatusTransitions[from][status] {
		return &MCPRequestTransitionError{From: from, To: status}
	}
	_, err := r.DB.ExecContext(ctx, `UPDATE mcp_requests
		SET status=?, response_digest=COALESCE(?, response_digest), error_code=?, updated_at=?
		WHERE id=?`,
		string(status), nullableString(&responseDigest), nullableString(&errorCode), roleTime(time.Now().UTC()), id)
	return err
}

// BumpConnectionGeneration increments the connection generation for a Run
// server snapshot (safe reconnect).
func (r *MCPRunRepo) BumpConnectionGeneration(ctx context.Context, runServerID string) (int, error) {
	_, err := r.DB.ExecContext(ctx,
		`UPDATE run_mcp_servers SET connection_generation = connection_generation + 1 WHERE id=?`, runServerID)
	if err != nil {
		return 0, err
	}
	var gen int
	if err := r.DB.QueryRowContext(ctx, `SELECT connection_generation FROM run_mcp_servers WHERE id=?`,
		runServerID).Scan(&gen); err != nil {
		return 0, err
	}
	return gen, nil
}

func nullableBytes(v []byte) any {
	if v == nil {
		return nil
	}
	return string(v)
}

func emptyAsNull(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// DigestCatalog computes the catalog digest over normalized tool definitions.
func DigestCatalog(tools []domain.MCPCatalogEntry) string {
	h := sha256.New()
	for _, t := range tools {
		fmt.Fprintf(h, "name=%s\nexposed=%s\ndesc=%s\nschema=%s\n", t.RemoteName, t.ExposedName, t.Description, t.InputSchema)
	}
	return hex.EncodeToString(h.Sum(nil))
}
