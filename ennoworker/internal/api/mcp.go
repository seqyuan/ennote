package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/mcpclient"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// MCPServer wraps the MCP stores and a resolver for credential values. It is
// the dependency boundary used by the API layer; the Worker wires concrete
// repos and the llm credential resolver.
type MCPServer struct {
	Profiles       *store.MCPProfileRepo
	Bindings       *store.MCPBindingRepo
	Catalogs       *store.MCPCatalogRepo
	Runs           *store.MCPRunRepo
	ResolveSecret  func(ref string) (string, error)
	Logger         *slog.Logger
	AllowedPrivate bool
	// Bundled is the embedded descriptor registry (metadata only).
	Bundled *mcpclient.BundledRegistry
	// DiscoverFn is injectable for tests; nil uses the real mcpDiscover.
	DiscoverFn func(ctx context.Context, binding *domain.MCPProjectBinding,
		version *domain.MCPServerProfileVersion) ([]domain.MCPCatalogEntry, error)
}

func (s *Server) mcp() *MCPServer { return s.MCP }

// --- Profile library ---

func (s *Server) listMCPServerProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.MCP.Profiles.ListProfiles(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	// Project-scoped (project_file) profiles are only visible to their owning
	// project; other projects must never see or bind them.
	projectID := r.URL.Query().Get("projectId")
	if projectID != "" {
		filtered := profiles[:0]
		for _, p := range profiles {
			if p.SourceKind == domain.MCPSourceManaged || p.SourceKind == domain.MCPSourceBundled ||
				(p.ProjectScope != nil && *p.ProjectScope == projectID) {
				filtered = append(filtered, p)
			}
		}
		profiles = filtered
	}
	writeData(w, http.StatusOK, profiles)
}

func (s *Server) createMCPServerProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DisplayName string `json:"displayName"`
		Slug        string `json:"slug"`
		SourceKind  string `json:"sourceKind"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	// Only managed (user-maintained) profiles can be created via the API;
	// project_file profiles are materialized from .ennote/mcp.json and bundled
	// profiles come from the reviewed embedded registry — never via a raw API.
	if input.SourceKind != "" && input.SourceKind != domain.MCPSourceManaged {
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_profile",
			"only managed profiles can be created via the API", false)
		return
	}
	profile, err := s.MCP.Profiles.CreateProfile(r.Context(), store.CreateMCPProfileInput{
		DisplayName: input.DisplayName, Slug: input.Slug, SourceKind: domain.MCPSourceManaged,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_profile", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, profile)
}

func (s *Server) createMCPServerVersion(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("profileID")
	var input domain.MCPServerProfileVersion
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := validateMCPVersionInput(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_version", err.Error(), false)
		return
	}
	if err := s.MCP.Profiles.CreateVersion(r.Context(), profileID, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_version", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, input)
}

func (s *Server) listMCPProfileVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.MCP.Profiles.ListVersions(r.Context(), r.PathValue("profileID"))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, versions)
}

func (s *Server) deleteMCPServerProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.MCP.Profiles.Archive(r.Context(), r.PathValue("profileID")); err != nil {
		writeError(w, r, http.StatusNotFound, "mcp_profile_not_found", err.Error(), false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateMCPVersionInput(v *domain.MCPServerProfileVersion) error {
	switch v.Transport {
	case domain.MCPTransportStdio:
		if strings.TrimSpace(v.Executable) == "" {
			return fmt.Errorf("stdio server requires executable")
		}
		if len(v.Argv) > mcpclient.MaxStdioArgv {
			return fmt.Errorf("argv exceeds %d entries", mcpclient.MaxStdioArgv)
		}
		for name := range v.EnvLiterals {
			if mcpclient.IsSecretLikeEnvName(name) {
				return fmt.Errorf("environment variable %s must use a credential reference, not a literal value", name)
			}
		}
	case domain.MCPTransportStreamableHTTP, domain.MCPTransportLegacySSE:
		if strings.TrimSpace(v.Endpoint) == "" {
			return fmt.Errorf("HTTP server requires endpoint")
		}
		for name := range v.HeaderLiterals {
			if mcpclient.IsSecretLikeEnvName(name) {
				return fmt.Errorf("header %s must use a credential reference, not a literal value", name)
			}
		}
	default:
		return fmt.Errorf("unsupported transport: %q", v.Transport)
	}
	for name, ref := range v.EnvCredentials {
		if !validCredentialRef(ref) {
			return fmt.Errorf("invalid credential ref for env %s", name)
		}
	}
	for name, ref := range v.HeaderCreds {
		if !validCredentialRef(ref) {
			return fmt.Errorf("invalid credential ref for header %s", name)
		}
	}
	return nil
}

func validCredentialRef(ref string) bool {
	scheme, value, ok := strings.Cut(strings.TrimSpace(ref), ":")
	return ok && value != "" && (scheme == "env" || scheme == "file" || scheme == "keyring")
}

// --- Project bindings ---

func (s *Server) listMCPBindings(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	bindings, err := s.MCP.Bindings.ListByProject(r.Context(), projectID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, bindings)
}

func (s *Server) createMCPBinding(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if !s.projectExists(w, r, projectID) {
		return
	}
	var input struct {
		ProfileVersionID        string            `json:"profileVersionId"`
		DesiredEnabled          bool              `json:"desiredEnabled"`
		Required                *bool             `json:"required"`
		SelectedRemoteToolNames []string          `json:"selectedRemoteToolNames"`
		CredentialRefs          map[string]string `json:"credentialRefs"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ProfileVersionID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_binding", "profileVersionId is required", false)
		return
	}
	// The version's profile must be bindable to this project: managed/global
	// profiles are shared; project_file profiles are only bindable by their
	// owning project.
	version, err := s.MCP.Profiles.GetVersion(r.Context(), input.ProfileVersionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "mcp_version_not_found", "profile version not found", false)
		return
	}
	profile, err := s.MCP.Profiles.GetProfile(r.Context(), version.ProfileID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if profile.SourceKind == domain.MCPSourceProjectFile &&
		(profile.ProjectScope == nil || *profile.ProjectScope != projectID) {
		writeError(w, r, http.StatusForbidden, "mcp_binding_forbidden", "project-file profile is scoped to another project", false)
		return
	}
	binding, err := s.MCP.Bindings.EnsureBindingExists(r.Context(), projectID, input.ProfileVersionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	upd := store.MCPBindingUpdate{
		DesiredEnabled: &input.DesiredEnabled, Required: input.Required,
		SelectedRemoteToolNames: input.SelectedRemoteToolNames, CredentialRefs: input.CredentialRefs,
	}
	updated, err := s.MCP.Bindings.Update(r.Context(), binding.ID, upd)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_binding", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, updated)
}

func (s *Server) updateMCPBinding(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	binding := s.mcpBindingOwnedBy(w, r, projectID, r.PathValue("bindingID"))
	if binding == nil {
		return
	}
	var input struct {
		DesiredEnabled          *bool             `json:"desiredEnabled"`
		Required                *bool             `json:"required"`
		SelectedRemoteToolNames []string          `json:"selectedRemoteToolNames"`
		CredentialRefs          map[string]string `json:"credentialRefs"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	upd := store.MCPBindingUpdate{
		DesiredEnabled: input.DesiredEnabled, Required: input.Required,
		SelectedRemoteToolNames: input.SelectedRemoteToolNames, CredentialRefs: input.CredentialRefs,
	}
	updated, err := s.MCP.Bindings.Update(r.Context(), binding.ID, upd)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_binding", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, updated)
}

func (s *Server) deleteMCPBinding(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	binding := s.mcpBindingOwnedBy(w, r, projectID, r.PathValue("bindingID"))
	if binding == nil {
		return
	}
	if err := s.MCP.Bindings.Delete(r.Context(), binding.ID); err != nil {
		writeError(w, r, http.StatusNotFound, "mcp_binding_not_found", err.Error(), false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Test / catalog refresh (browse connections, never Run connections) ---

func (s *Server) testMCPBinding(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	binding := s.mcpBindingOwnedBy(w, r, projectID, r.PathValue("bindingID"))
	if binding == nil {
		return
	}
	version, err := s.MCP.Profiles.GetVersion(r.Context(), binding.ProfileVersionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	session, err := s.MCP.mcpBrowseConnect(r.Context(), version, binding)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "mcp_connect_failed", err.Error(), false)
		return
	}
	defer session.Close()
	writeData(w, http.StatusOK, map[string]any{"ok": true, "transport": version.Transport})
}

func (s *Server) catalogMCPBinding(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	binding := s.mcpBindingOwnedBy(w, r, projectID, r.PathValue("bindingID"))
	if binding == nil {
		return
	}
	version, err := s.MCP.Profiles.GetVersion(r.Context(), binding.ProfileVersionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	// Try the binding-scoped cache first (same revision + auth generation +
	// credential digest). Stale rows are treated as a miss.
	cached, err := s.MCP.Catalogs.GetCatalog(r.Context(), binding.ID, binding.Revision, 0,
		version.ID, "latest", s.MCP.bindingCredentialDigest(binding, version))
	if err == nil {
		writeData(w, http.StatusOK, cached.Tools)
		return
	}
	entries, err := s.MCP.mcpDiscover(r.Context(), binding, version)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "mcp_catalog_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, entries)
}

func (s *Server) refreshMCPCatalog(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	binding := s.mcpBindingOwnedBy(w, r, projectID, r.PathValue("bindingID"))
	if binding == nil {
		return
	}
	version, err := s.MCP.Profiles.GetVersion(r.Context(), binding.ProfileVersionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	entries, err := s.MCP.mcpDiscover(r.Context(), binding, version)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "mcp_catalog_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, entries)
}

// mcpBindingOwnedBy loads a binding and verifies it belongs to projectID,
// failing closed with 404 when it does not (cross-project mutation guard).
func (s *Server) mcpBindingOwnedBy(w http.ResponseWriter, r *http.Request, projectID, bindingID string) *domain.MCPProjectBinding {
	binding, err := s.MCP.Bindings.Get(r.Context(), bindingID)
	if err != nil || binding.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "mcp_binding_not_found", "binding not found", false)
		return nil
	}
	return binding
}

// projectExists verifies a project exists before a mutation can target it.
func (s *Server) projectExists(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if s.Projects == nil {
		return true
	}
	if _, err := s.Projects.FindWorkspaceByProjectID(r.Context(), projectID); err != nil {
		writeError(w, r, http.StatusNotFound, "project_not_found", "project not found", false)
		return false
	}
	return true
}

// effectiveVersion returns a copy of the profile version with the binding's
// credential refs merged over the profile defaults. Binding refs are the
// per-Project identity override; profile refs are the server defaults.
func (m *MCPServer) effectiveVersion(v *domain.MCPServerProfileVersion, binding *domain.MCPProjectBinding) *domain.MCPServerProfileVersion {
	if binding == nil || len(binding.CredentialRefs) == 0 {
		return v
	}
	clone := *v
	merged := make(map[string]string, len(v.EnvCredentials)+len(binding.CredentialRefs))
	for k, ref := range v.EnvCredentials {
		merged[k] = ref
	}
	for k, ref := range binding.CredentialRefs {
		merged[k] = ref
	}
	clone.EnvCredentials = merged
	return &clone
}

// bindingCredentialDigest hashes only the credential REFERENCE names + refs
// (never values) that will be applied for this binding. It is part of the
// catalog cache key so a server that varies its toolset by identity cannot
// leak a catalog across credential generations.
func (m *MCPServer) bindingCredentialDigest(binding *domain.MCPProjectBinding, version *domain.MCPServerProfileVersion) string {
	h := sha256.New()
	names := make([]string, 0, len(binding.CredentialRefs)+len(version.EnvCredentials)+len(version.HeaderCreds))
	for k := range binding.CredentialRefs {
		names = append(names, k)
	}
	for k := range version.EnvCredentials {
		names = append(names, k)
	}
	for k := range version.HeaderCreds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := binding.CredentialRefs[name]
		if ref == "" {
			ref = version.EnvCredentials[name]
		}
		if ref == "" {
			ref = version.HeaderCreds[name]
		}
		fmt.Fprintf(h, "%s=%s\n", name, ref)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// discover performs a bounded catalog discovery, honoring the test-injected
// DiscoverFn when present.
func (m *MCPServer) discover(ctx context.Context, binding *domain.MCPProjectBinding,
	version *domain.MCPServerProfileVersion) ([]domain.MCPCatalogEntry, error) {
	if m.DiscoverFn != nil {
		return m.DiscoverFn(ctx, binding, version)
	}
	return m.mcpDiscover(ctx, binding, version)
}

// mcpBrowseConnect dials a short-lived browse connection using the binding's
// credential refs merged over the profile version. Browse connections never
// enter a RunConnectionSet.
func (m *MCPServer) mcpBrowseConnect(ctx context.Context, version *domain.MCPServerProfileVersion,
	binding *domain.MCPProjectBinding) (*mcpclient.Session, error) {
	eff := m.effectiveVersion(version, binding)
	return mcpclient.Connect(ctx, eff, m.mcpConnectOption(binding))
}

func (m *MCPServer) mcpConnectOption(binding *domain.MCPProjectBinding) mcpclient.ConnectOption {
	return mcpclient.ConnectOption{
		ResolveSecret:          m.ResolveSecret,
		Logger:                 m.Logger,
		AllowedPrivateNetworks: m.AllowedPrivate,
	}
}

// mcpDiscover performs initialize + tools/list via a browse connection,
// normalizes the catalog, and stores it into the binding-scoped cache keyed
// by binding revision + credential digest.
func (m *MCPServer) mcpDiscover(ctx context.Context, binding *domain.MCPProjectBinding,
	version *domain.MCPServerProfileVersion) ([]domain.MCPCatalogEntry, error) {
	session, err := m.mcpBrowseConnect(ctx, version, binding)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	raw, err := session.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	profile, err := m.Profiles.GetProfile(ctx, version.ProfileID)
	if err != nil {
		return nil, err
	}
	entries, err := mcpclient.NormalizeCatalog(profile.Slug, raw)
	if err != nil {
		return nil, err
	}
	err = m.Catalogs.PutCatalog(ctx, store.MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision,
		ProfileVersionID: version.ID, ProtocolVersion: "latest",
		AuthGeneration: 0, CredentialDigest: m.bindingCredentialDigest(binding, version),
		CatalogDigest: store.DigestCatalog(entries),
		Tools:         entries, FetchedAt: time.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// --- Bundled catalog (static, no execution) ---

func (s *Server) listMCPBundledCatalog(w http.ResponseWriter, r *http.Request) {
	if s.MCP == nil || s.MCP.Bundled == nil {
		writeData(w, http.StatusOK, []any{})
		return
	}
	writeData(w, http.StatusOK, s.MCP.Bundled.List())
}

// --- Run freeze wiring ---

// FrozenRunServer pairs a frozen Run server snapshot with the connection
// version and the frozen tool snapshots so Run execution can build McpTool
// adapters directly from the snapshot.
type FrozenRunServer struct {
	Snapshot store.RunMCPServerSnapshot
	Version  *domain.MCPServerProfileVersion
	Tools    []store.RunMCPToolSnapshot
}

// FreezeRun resolves the project's enabled bindings, freezes immutable
// server + tool snapshots atomically, and returns them. It is idempotent per
// Run: when the Run already has frozen snapshots (approval resume / rewind),
// the existing snapshots are reused verbatim — binding changes after the first
// freeze never alter an active Run's capability set.
//
// Semantics:
//   - required servers are always connectivity-verified (discover) so a
//     required-but-unreachable server blocks Run start even with a warm cache;
//   - optional servers use a warm cache when available; on failure they freeze
//     an unavailable snapshot and Run start continues;
//   - only tools listed in the binding's selectedRemoteToolNames are frozen
//     (selected-only exposure; nothing is auto-augmented).
func (m *MCPServer) FreezeRun(ctx context.Context, runID, projectID string) ([]FrozenRunServer, error) {
	if m == nil {
		return nil, nil
	}
	// Resume / rewind: reuse already-frozen snapshots verbatim.
	existing, err := m.Runs.ListFrozenServers(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return m.loadFrozenServers(ctx, existing)
	}
	bindings, err := m.Bindings.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var frozen []FrozenRunServer
	for _, binding := range bindings {
		if !binding.DesiredEnabled {
			continue
		}
		if len(frozen) >= mcpclient.MaxServerCountPerRun {
			return nil, fmt.Errorf("mcp servers per run exceed limit %d", mcpclient.MaxServerCountPerRun)
		}
		version, err := m.Profiles.GetVersion(ctx, binding.ProfileVersionID)
		if err != nil {
			if binding.Required {
				return nil, fmt.Errorf("mcp binding %s: profile version unavailable: %w", binding.ID, err)
			}
			continue
		}
		profile, err := m.Profiles.GetProfile(ctx, version.ProfileID)
		if err != nil {
			return nil, fmt.Errorf("mcp binding %s: profile unavailable: %w", binding.ID, err)
		}
		// Required servers are always connectivity-verified: discover refreshes
		// the catalog AND proves the server is reachable right now.
		var entries []domain.MCPCatalogEntry
		if binding.Required {
			entries, err = m.discover(ctx, binding, version)
			if err != nil {
				return nil, fmt.Errorf("mcp server %s unavailable: %w", version.Executable+version.Endpoint, err)
			}
		} else {
			var cached *store.MCPCatalogCacheRow
			var cErr error
			if m.Catalogs != nil {
				cached, cErr = m.Catalogs.GetCatalog(ctx, binding.ID, binding.Revision, 0,
					version.ID, "latest", m.bindingCredentialDigest(binding, version))
				if cErr == nil {
					entries = cached.Tools
				}
			}
			if cErr != nil {
				entries, err = m.discover(ctx, binding, version)
				if err != nil {
					// Optional: freeze an unavailable snapshot and continue.
					snap := store.RunMCPServerSnapshot{
						RunID: runID, BindingID: binding.ID, BindingRevision: binding.Revision,
						ProfileVersionID: version.ID, ConfigDigest: version.ConfigDigest,
						NegotiatedProtocol: "2025-06-18", ServerIdentityDigest: version.ConfigDigest,
						Required: false, UnavailableReason: err.Error(),
					}
					if _, fErr := m.Runs.FreezeServer(ctx, snap); fErr == nil {
						frozen = append(frozen, FrozenRunServer{Snapshot: snap, Version: version})
					}
					continue
				}
			} else if m.Catalogs == nil {
				// No cache store configured (tests): discover directly.
				entries, err = m.discover(ctx, binding, version)
				if err != nil {
					if binding.Required {
						return nil, fmt.Errorf("mcp server %s unavailable: %w", version.Executable+version.Endpoint, err)
					}
					snap := store.RunMCPServerSnapshot{
						RunID: runID, BindingID: binding.ID, BindingRevision: binding.Revision,
						ProfileVersionID: version.ID, ConfigDigest: version.ConfigDigest,
						NegotiatedProtocol: "2025-06-18", ServerIdentityDigest: version.ConfigDigest,
						Required: false, UnavailableReason: err.Error(),
					}
					if _, fErr := m.Runs.FreezeServer(ctx, snap); fErr == nil {
						frozen = append(frozen, FrozenRunServer{Snapshot: snap, Version: version})
					}
					continue
				}
			} else {
				entries = cached.Tools
			}
		}
		// Selected-only: freeze exactly the tools the user selected. A binding
		// with zero selections freezes the server with an empty tool set.
		selected := map[string]bool{}
		for _, name := range binding.SelectedRemoteToolNames {
			selected[name] = true
		}
		var tools []store.RunMCPToolSnapshot
		for _, entry := range entries {
			if !selected[entry.RemoteName] {
				continue
			}
			if len(tools) >= mcpclient.MaxToolsPerRun {
				return nil, fmt.Errorf("mcp tools per run exceed limit %d", mcpclient.MaxToolsPerRun)
			}
			tools = append(tools, store.RunMCPToolSnapshot{
				RemoteName:     entry.RemoteName,
				ExposedName:    entry.ExposedName,
				Description:    entry.Description,
				InputSchema:    entry.InputSchema,
				OutputSchema:   entry.OutputSchema,
				SchemaDigest:   entry.Digest,
				RiskClass:      domain.RiskExternal,
				ExecutionClass: domain.ExecutionExclusive,
				SourceKind:     profile.SourceKind,
			})
		}
		snap := store.RunMCPServerSnapshot{
			RunID: runID, BindingID: binding.ID, BindingRevision: binding.Revision,
			ProfileVersionID: version.ID, ConfigDigest: version.ConfigDigest,
			NegotiatedProtocol: "2025-06-18", ServerIdentityDigest: version.ConfigDigest,
			CatalogDigest: store.DigestCatalog(entries), Required: binding.Required,
		}
		serverID, err := m.Runs.FreezeServerWithTools(ctx, snap, tools)
		if err != nil {
			return nil, fmt.Errorf("freeze mcp run snapshot: %w", err)
		}
		snap.ID = serverID
		for i := range tools {
			tools[i].RunServerID = serverID
		}
		frozen = append(frozen, FrozenRunServer{Snapshot: snap, Version: m.effectiveVersion(version, binding), Tools: tools})
	}
	return frozen, nil
}

// loadFrozenServers rehydrates already-frozen snapshots (resume/rewind). The
// connection version re-merges the binding's credential refs so the resumed
// Run connects with the same identity as the original freeze.
func (m *MCPServer) loadFrozenServers(ctx context.Context, snapshots []*store.RunMCPServerSnapshot) ([]FrozenRunServer, error) {
	var out []FrozenRunServer
	for _, snap := range snapshots {
		version, err := m.Profiles.GetVersion(ctx, snap.ProfileVersionID)
		if err != nil {
			return nil, err
		}
		tools, err := m.Runs.ListFrozenTools(ctx, snap.ID)
		if err != nil {
			return nil, err
		}
		eff := version
		if binding, bErr := m.Bindings.Get(ctx, snap.BindingID); bErr == nil {
			eff = m.effectiveVersion(version, binding)
		}
		out = append(out, FrozenRunServer{Snapshot: *snap, Version: eff, Tools: tools})
	}
	return out, nil
}

// --- Candidate discovery (side-effect-free) ---

// MCPCandidate is a discovered, not-yet-bound server declaration. Candidates
// are never executed or auto-enabled; a user must explicitly bind one.
type MCPCandidate struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"`
	SourceKind    string `json:"sourceKind"`
	SourceLocator string `json:"sourceLocator,omitempty"`
	Transport     string `json:"transport"`
	Executable    string `json:"executable,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	ConfigDigest  string `json:"configDigest"`
	// BoundVersionID is set when the candidate's config already matches a
	// materialized profile version; otherwise binding materializes it first.
	BoundVersionID string `json:"boundVersionId,omitempty"`
	// AlreadyBound indicates a project binding already exists for the version.
	AlreadyBound bool `json:"alreadyBound,omitempty"`
	// UpdateAvailable is true when the project file changed since the bound
	// version: a new immutable version exists that the user may review and
	// rebind. Existing bindings are never rewritten automatically.
	UpdateAvailable bool `json:"updateAvailable,omitempty"`
}

// listMCPCandidates merges bundled descriptors, managed profiles, and the
// project file. It performs no connection and no process spawn.
func (s *Server) listMCPCandidates(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	candidates, err := s.discoverCandidates(r.Context(), projectID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "mcp_discovery_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, candidates)
}

// refreshMCPDiscovery re-scans the project file and bundled descriptors.
func (s *Server) refreshMCPDiscovery(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	candidates, err := s.discoverCandidates(r.Context(), projectID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "mcp_discovery_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, candidates)
}

// discoverCandidates resolves the merged candidate list for a project.
func (s *Server) discoverCandidates(ctx context.Context, projectID string) ([]MCPCandidate, error) {
	candidates := []MCPCandidate{}
	// 1. Project file (.ennote/mcp.json) — parse only, never execute.
	record, err := s.Projects.FindWorkspaceByProjectID(ctx, projectID)
	if err != nil || record == nil {
		return nil, fmt.Errorf("project workspace not found")
	}
	filePath := mcpclient.FindProjectFile(record.HostPath)
	if file, err := mcpclient.ParseProjectFile(filePath); err != nil {
		return nil, err
	} else if file != nil {
		for _, cand := range file.Candidates() {
			c := MCPCandidate{
				Slug: cand.Slug, DisplayName: cand.DisplayName,
				SourceKind: domain.MCPSourceProjectFile, SourceLocator: filePath,
				Transport: cand.Version.Transport, Executable: cand.Version.Executable,
				Endpoint: cand.Version.Endpoint, ConfigDigest: cand.Version.ConfigDigest,
			}
			// Match an existing materialized profile/version so binding reuses
			// the immutable version instead of creating a duplicate.
			if profile, pErr := s.MCP.Profiles.FindProfileBySource(ctx, cand.Slug, domain.MCPSourceProjectFile, &projectID); pErr == nil {
				if v, vErr := s.MCP.Profiles.FindVersionByDigest(ctx, profile.ID, cand.Version.ConfigDigest); vErr == nil {
					c.BoundVersionID = v.ID
					if _, bErr := s.MCP.Bindings.GetByProjectVersion(ctx, projectID, v.ID); bErr == nil {
						c.AlreadyBound = true
					}
				} else {
					// The project file changed since the last bound version: flag
					// an update so the UI can offer an explicit rebind. Never
					// auto-rewrite existing bindings.
					versions, vErr := s.MCP.Profiles.ListVersions(ctx, profile.ID)
					if vErr == nil && len(versions) > 0 {
						c.BoundVersionID = versions[len(versions)-1].ID
						if _, bErr := s.MCP.Bindings.GetByProjectVersion(ctx, projectID, versions[len(versions)-1].ID); bErr == nil {
							c.AlreadyBound = true
							c.UpdateAvailable = true
						}
					}
				}
			}
			candidates = append(candidates, c)
		}
	}
	// 2. Managed profiles (global) not yet bound to this project.
	profiles, err := s.MCP.Profiles.ListProfiles(ctx)
	if err == nil {
		for _, profile := range profiles {
			if profile.SourceKind == domain.MCPSourceProjectFile || profile.LatestVersion == 0 {
				continue
			}
			versions, vErr := s.MCP.Profiles.ListVersions(ctx, profile.ID)
			if vErr != nil || len(versions) == 0 {
				continue
			}
			latest := versions[len(versions)-1]
			c := MCPCandidate{
				Slug: profile.Slug, DisplayName: profile.DisplayName,
				SourceKind: profile.SourceKind,
				Transport:  latest.Transport, Executable: latest.Executable,
				Endpoint: latest.Endpoint, ConfigDigest: latest.ConfigDigest,
				BoundVersionID: latest.ID,
			}
			if _, bErr := s.MCP.Bindings.GetByProjectVersion(ctx, projectID, latest.ID); bErr == nil {
				c.AlreadyBound = true
			}
			candidates = append(candidates, c)
		}
	}
	// 3. Bundled descriptors (static, embedded).
	candidates = append(candidates, s.bundledCandidates()...)
	return candidates, nil
}

// bundledCandidates returns the embedded bundled descriptors. The P1 catalog
// is intentionally empty: bundled runtime payloads are a Phase 3 decision and
// must never be presented as auto-executable.
// bundledCandidates converts the embedded descriptor registry into discoverable
// candidates (source_kind=bundled). They are metadata only: binding requires an
// explicit user action, and P1 ships an empty registry.
func (s *Server) bundledCandidates() []MCPCandidate {
	if s.MCP == nil || s.MCP.Bundled == nil {
		return nil
	}
	descriptors := s.MCP.Bundled.List()
	candidates := make([]MCPCandidate, 0, len(descriptors))
	for _, d := range descriptors {
		version := &domain.MCPServerProfileVersion{
			Transport: d.Transport, Executable: d.Command,
			Argv: d.Args, Endpoint: d.Endpoint,
		}
		c := MCPCandidate{
			Slug:         d.Slug,
			DisplayName:  d.DisplayName,
			SourceKind:   domain.MCPSourceBundled,
			Transport:    d.Transport,
			Executable:   d.Command,
			Endpoint:     d.Endpoint,
			ConfigDigest: mcpclient.ConfigDigest(version),
			AlreadyBound: false,
		}
		// Match an already-materialized bundled profile/version so binding
		// reuses the immutable version instead of creating a duplicate.
		if profile, err := s.MCP.Profiles.FindProfileBySource(context.Background(), d.Slug, domain.MCPSourceBundled, nil); err == nil {
			if v, vErr := s.MCP.Profiles.FindVersionByDigest(context.Background(), profile.ID, c.ConfigDigest); vErr == nil {
				c.BoundVersionID = v.ID
			}
		}
		candidates = append(candidates, c)
	}
	return candidates
}

// createMCPBindingFromCandidate materializes a discovered candidate into an
// immutable profile version and binds it to the project. It never executes the
// server and never auto-enables it; the binding is created disabled with zero
// selected tools.
func (s *Server) createMCPBindingFromCandidate(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if !s.projectExists(w, r, projectID) {
		return
	}
	var input struct {
		Slug       string `json:"slug"`
		SourceKind string `json:"sourceKind"`
		Transport  string `json:"transport"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Slug == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_candidate", "slug is required", false)
		return
	}
	switch input.SourceKind {
	case domain.MCPSourceBundled:
		s.createBundledBindingFromCandidate(w, r, projectID, input.Slug)
	case "", domain.MCPSourceProjectFile:
		s.createProjectFileBindingFromCandidate(w, r, projectID, input.Slug)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_mcp_candidate", "unsupported source kind", false)
	}
}

// createProjectFileBindingFromCandidate materializes from .ennote/mcp.json
// (the file is the authority; the client payload is a hint only).
func (s *Server) createProjectFileBindingFromCandidate(w http.ResponseWriter, r *http.Request, projectID, slug string) {
	record, err := s.Projects.FindWorkspaceByProjectID(r.Context(), projectID)
	if err != nil || record == nil {
		writeError(w, r, http.StatusNotFound, "workspace_not_found", "project workspace not found", false)
		return
	}
	filePath := mcpclient.FindProjectFile(record.HostPath)
	file, err := mcpclient.ParseProjectFile(filePath)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "mcp_discovery_failed", err.Error(), false)
		return
	}
	if file == nil {
		writeError(w, r, http.StatusNotFound, "mcp_project_file_missing", "no .ennote/mcp.json found", false)
		return
	}
	candidate, ok := findCandidate(file, slug)
	if !ok {
		writeError(w, r, http.StatusNotFound, "mcp_candidate_missing", "candidate not declared in .ennote/mcp.json", false)
		return
	}

	// Materialize profile + immutable version (reuse on digest match).
	profile, err := s.MCP.Profiles.FindProfileBySource(r.Context(), candidate.Slug, domain.MCPSourceProjectFile, &projectID)
	if err != nil {
		created, cErr := s.MCP.Profiles.CreateProfile(r.Context(), store.CreateMCPProfileInput{
			DisplayName: candidate.DisplayName, Slug: candidate.Slug,
			SourceKind: domain.MCPSourceProjectFile, ProjectScope: &projectID,
			SourceLocator: filePath,
		})
		if cErr != nil {
			writeInternal(w, r, cErr)
			return
		}
		profile = created
	}
	version, err := s.materializeVersion(r.Context(), profile, &candidate.Version)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	binding, err := s.MCP.Bindings.EnsureBindingExists(r.Context(), projectID, version.ID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, binding)
}

// createBundledBindingFromCandidate materializes a bundled descriptor into a
// global bundled profile. Bundled descriptors are metadata only; binding
// creates a disabled binding that never auto-executes.
func (s *Server) createBundledBindingFromCandidate(w http.ResponseWriter, r *http.Request, projectID, slug string) {
	if s.MCP.Bundled == nil {
		writeError(w, r, http.StatusNotFound, "mcp_candidate_missing", "bundled registry unavailable", false)
		return
	}
	var descriptor *mcpclient.BundledDescriptor
	for _, d := range s.MCP.Bundled.List() {
		if d.Slug == slug {
			dd := d
			descriptor = &dd
			break
		}
	}
	if descriptor == nil {
		writeError(w, r, http.StatusNotFound, "mcp_candidate_missing", "bundled descriptor not found", false)
		return
	}
	profile, err := s.MCP.Profiles.FindProfileBySource(r.Context(), descriptor.Slug, domain.MCPSourceBundled, nil)
	if err != nil {
		created, cErr := s.MCP.Profiles.CreateProfile(r.Context(), store.CreateMCPProfileInput{
			DisplayName: descriptor.DisplayName, Slug: descriptor.Slug,
			SourceKind: domain.MCPSourceBundled, SourceLocator: "bundled:" + descriptor.Slug,
		})
		if cErr != nil {
			writeInternal(w, r, cErr)
			return
		}
		profile = created
	}
	version := &domain.MCPServerProfileVersion{
		Transport: descriptor.Transport, Executable: descriptor.Command,
		Argv: descriptor.Args, Endpoint: descriptor.Endpoint,
	}
	v, err := s.materializeVersion(r.Context(), profile, version)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	binding, err := s.MCP.Bindings.EnsureBindingExists(r.Context(), projectID, v.ID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, binding)
}

// materializeVersion creates a profile version, reusing an existing immutable
// version when the config digest matches.
func (s *Server) materializeVersion(ctx context.Context, profile *domain.MCPServerProfile,
	v *domain.MCPServerProfileVersion) (*domain.MCPServerProfileVersion, error) {
	if v.ConfigDigest == "" {
		v.ConfigDigest = mcpclient.ConfigDigest(v)
	}
	if existing, err := s.MCP.Profiles.FindVersionByDigest(ctx, profile.ID, v.ConfigDigest); err == nil {
		return existing, nil
	}
	v.ConfigDigest = ""
	v.ProfileID = profile.ID
	if err := s.MCP.Profiles.CreateVersion(ctx, profile.ID, v); err != nil {
		return nil, err
	}
	return v, nil
}

func findCandidate(file *mcpclient.ProjectFile, slug string) (*mcpclient.ProjectCandidate, bool) {
	for _, cand := range file.Candidates() {
		if cand.Slug == slug {
			return &cand, true
		}
	}
	return nil, false
}
