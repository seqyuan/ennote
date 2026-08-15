package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/mcpclient"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPProfileAPI(t *testing.T) {
	_, handler := setupServer(t, &fakeController{})

	// Create profile.
	rec := request(t, handler, http.MethodPost, "/v1/mcp/server-profiles", map[string]any{
		"displayName": "Pubmed", "slug": "pubmed", "sourceKind": "managed",
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var profile domain.MCPServerProfile
	decodeData(t, rec, &profile)
	require.NotEmpty(t, profile.ID)

	// Create version (stdio).
	rec = request(t, handler, http.MethodPost, "/v1/mcp/server-profiles/"+profile.ID+"/versions", map[string]any{
		"transport": "stdio", "executable": "/bin/echo", "argv": []string{"-n", "hi"},
		"timeoutMs": 5000, "networkPolicy": "default",
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version domain.MCPServerProfileVersion
	decodeData(t, rec, &version)
	assert.Equal(t, 1, version.Version)

	// List versions.
	rec = request(t, handler, http.MethodGet, "/v1/mcp/server-profiles/"+profile.ID+"/versions", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var versions []domain.MCPServerProfileVersion
	decodeData(t, rec, &versions)
	require.Len(t, versions, 1)

	// List profiles.
	rec = request(t, handler, http.MethodGet, "/v1/mcp/server-profiles", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var profiles []domain.MCPServerProfile
	decodeData(t, rec, &profiles)
	require.Len(t, profiles, 1)
	assert.Equal(t, 1, profiles[0].LatestVersion)

	// Invalid version: secret-like literal env must be rejected.
	rec = request(t, handler, http.MethodPost, "/v1/mcp/server-profiles/"+profile.ID+"/versions", map[string]any{
		"transport": "stdio", "executable": "/bin/echo", "envLiterals": map[string]string{"API_KEY": "secret"},
	}, true)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// Archive.
	rec = request(t, handler, http.MethodDelete, "/v1/mcp/server-profiles/"+profile.ID, nil, true)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMCPBindingAPI(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	profileID := createMCPProfileWithVersion(t, handler)
	project, _, err := server.Projects.CreateWithWorkspace(t.Context(), domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)

	// Create binding (disabled by default).
	rec := request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/mcp/bindings", map[string]any{
		"profileVersionId": profileID,
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var binding domain.MCPProjectBinding
	decodeData(t, rec, &binding)
	assert.False(t, binding.DesiredEnabled)
	assert.True(t, binding.Required)

	// Enable with tool selection.
	rec = request(t, handler, http.MethodPatch, "/v1/projects/"+project.ID+"/mcp/bindings/"+binding.ID, map[string]any{
		"desiredEnabled": true, "selectedRemoteToolNames": []string{"search", "get"},
	}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	decodeData(t, rec, &binding)
	assert.True(t, binding.DesiredEnabled)
	assert.Equal(t, []string{"search", "get"}, binding.SelectedRemoteToolNames)
	assert.Equal(t, 3, binding.Revision) // 1 create + POST upsert + PATCH

	// List bindings.
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/mcp/bindings", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var bindings []domain.MCPProjectBinding
	decodeData(t, rec, &bindings)
	require.Len(t, bindings, 1)

	// Delete.
	rec = request(t, handler, http.MethodDelete, "/v1/projects/"+project.ID+"/mcp/bindings/"+binding.ID, nil, true)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMCPBindingAPIRequiresAuth(t *testing.T) {
	_, handler := setupServer(t, &fakeController{})
	rec := request(t, handler, http.MethodGet, "/v1/mcp/server-profiles", nil, false)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// createMCPProfileWithVersion creates a profile + stdio version and returns
// the version ID.
func createMCPProfileWithVersion(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := request(t, handler, http.MethodPost, "/v1/mcp/server-profiles", map[string]any{
		"displayName": "Srv", "slug": "srv", "sourceKind": "managed",
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var profile domain.MCPServerProfile
	decodeData(t, rec, &profile)
	rec = request(t, handler, http.MethodPost, "/v1/mcp/server-profiles/"+profile.ID+"/versions", map[string]any{
		"transport": "stdio", "executable": "/bin/true",
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version domain.MCPServerProfileVersion
	decodeData(t, rec, &version)
	return version.ID
}

func TestFreezeRunSnapshotsSemantics(t *testing.T) {
	profileRepo, bindingRepo, catalogRepo, runsRepo, projectID, _ := newAPIFileMCP(t)
	mcp := &MCPServer{
		Profiles: profileRepo, Bindings: bindingRepo, Catalogs: catalogRepo,
		Runs: runsRepo,
	}
	mcp.DiscoverFn = func(ctx context.Context, binding *domain.MCPProjectBinding,
		version *domain.MCPServerProfileVersion) ([]domain.MCPCatalogEntry, error) {
		return []domain.MCPCatalogEntry{{RemoteName: "search", ExposedName: "bio__search",
			InputSchema: []byte(`{"type":"object"}`), Digest: "d1"}}, nil
	}

	profile, err := profileRepo.CreateProfile(context.Background(), store.CreateMCPProfileInput{
		DisplayName: "Bio", Slug: "bio", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))

	// Disabled binding: no servers frozen.
	disabledBinding, err := bindingRepo.EnsureBindingExists(context.Background(), projectID, version.ID)
	require.NoError(t, err)
	servers, err := mcp.FreezeRun(context.Background(), "run-1", projectID)
	require.NoError(t, err)
	assert.Empty(t, servers)

	// Enabled binding with zero selected tools: required server is still
	// connectivity-verified (discover runs), the server freezes with an empty
	// tool set (selected-only exposure).
	enabled := true
	updatedBinding, err := bindingRepo.Update(context.Background(), disabledBinding.ID, store.MCPBindingUpdate{DesiredEnabled: &enabled})
	require.NoError(t, err)
	_ = updatedBinding
	servers, err = mcp.FreezeRun(context.Background(), "run-1", projectID)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	assert.True(t, servers[0].Snapshot.Required)
	assert.Empty(t, servers[0].Tools)

	// Selected tool on a NEW Run: frozen tool snapshot carries the exact
	// selected name and RiskExternal/ExecutionExclusive for third-party tools.
	// The binding's credential refs must also merge into the frozen connection
	// version.
	_, err = bindingRepo.Update(context.Background(), disabledBinding.ID, store.MCPBindingUpdate{
		SelectedRemoteToolNames: []string{"search"},
		CredentialRefs:          map[string]string{"GITHUB_TOKEN": "env:GITHUB_TOKEN"},
	})
	require.NoError(t, err)
	servers, err = mcp.FreezeRun(context.Background(), "run-2", projectID)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	require.Len(t, servers[0].Tools, 1)
	assert.Equal(t, "bio__search", servers[0].Tools[0].ExposedName)
	assert.Equal(t, domain.RiskExternal, servers[0].Tools[0].RiskClass)
	assert.Equal(t, domain.ExecutionExclusive, servers[0].Tools[0].ExecutionClass)
	assert.Equal(t, "env:GITHUB_TOKEN", servers[0].Version.EnvCredentials["GITHUB_TOKEN"],
		"binding credential refs must merge into the frozen connection version")

	// FreezeRun is idempotent per Run: a second call for run-2 reuses the
	// frozen snapshots and never duplicates run_mcp_servers rows.
	servers2, err := mcp.FreezeRun(context.Background(), "run-2", projectID)
	require.NoError(t, err)
	require.Len(t, servers2, 1)
	assert.Equal(t, servers[0].Snapshot.ID, servers2[0].Snapshot.ID)
	all, err := mcp.Runs.ListFrozenServers(context.Background(), "run-2")
	require.NoError(t, err)
	require.Len(t, all, 1)

	// Required + unreachable aborts Run init even with a warm cache.
	unreachable, err := profileRepo.CreateProfile(context.Background(), store.CreateMCPProfileInput{
		DisplayName: "Unreachable", Slug: "unreachable", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	uv := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://127.0.0.1:1/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), unreachable.ID, uv))
	ub, err := bindingRepo.EnsureBindingExists(context.Background(), projectID, uv.ID)
	require.NoError(t, err)
	_, err = bindingRepo.Update(context.Background(), ub.ID, store.MCPBindingUpdate{DesiredEnabled: &enabled})
	require.NoError(t, err)
	// Inject a failing discover to simulate an unreachable required server.
	mcp.DiscoverFn = func(ctx context.Context, binding *domain.MCPProjectBinding,
		version *domain.MCPServerProfileVersion) ([]domain.MCPCatalogEntry, error) {
		return nil, fmt.Errorf("connection refused")
	}
	_, err = mcp.FreezeRun(context.Background(), "run-3", projectID)
	require.Error(t, err)
}

func TestFreezeRunOptionalUnavailableFreezesSnapshot(t *testing.T) {
	profileRepo, bindingRepo, _, runsRepo, projectID, _ := newAPIFileMCP(t)
	mcp := &MCPServer{
		Profiles: profileRepo, Bindings: bindingRepo,
		Runs: runsRepo,
	}
	mcp.DiscoverFn = func(ctx context.Context, binding *domain.MCPProjectBinding,
		version *domain.MCPServerProfileVersion) ([]domain.MCPCatalogEntry, error) {
		return nil, fmt.Errorf("connection refused")
	}
	profile, err := profileRepo.CreateProfile(context.Background(), store.CreateMCPProfileInput{
		DisplayName: "Opt", Slug: "opt", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	binding, err := bindingRepo.EnsureBindingExists(context.Background(), projectID, version.ID)
	require.NoError(t, err)
	required := false
	_, err = bindingRepo.Update(context.Background(), binding.ID, store.MCPBindingUpdate{DesiredEnabled: ptr(true), Required: &required})
	require.NoError(t, err)

	servers, err := mcp.FreezeRun(context.Background(), "run-opt", projectID)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	assert.False(t, servers[0].Snapshot.Required)
	assert.NotEmpty(t, servers[0].Snapshot.UnavailableReason)
}

func ptr[V any](v V) *V { return &v }

func TestMCPProjectFileDiscovery(t *testing.T) {
	_, _, _, _, _, db := newAPIFileMCP(t)
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	projectRepo := projects
	server := &Server{
		DB: db, Token: "test-token", Projects: &store.ProjectRepo{Files: projects},
		MCP: &MCPServer{
			Profiles: &store.MCPProfileRepo{FilePath: filepath.Join(home, "config", "mcp.json")},
			Bindings: &store.MCPBindingRepo{Projects: projects},
			Catalogs: &store.MCPCatalogRepo{CacheDir: filepath.Join(home, "cache", "mcp")},
			Runs:     &store.MCPRunRepo{DB: db},
		},
	}
	handler := server.Handler()

	// Create a project whose workspace root contains a malicious-looking
	// .ennote/mcp.json. Discovery must only parse it: no process spawn, no
	// connection, no auto-enable.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".ennote"), 0o700))
	projectJSON := `{
	  "mcpServers": {
	    "pubmed": {
	      "type": "stdio",
	      "command": "should-not-run",
	      "args": ["--pwn"],
	      "envCredentials": {"API_KEY": "env:REAL_KEY"}
	    },
	    "geo": {
	      "type": "streamable_http",
	      "url": "https://example.com/mcp"
	    }
	  }
	}`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ennote", "mcp.json"), []byte(projectJSON), 0o600))
	project, _, err := projectRepo.CreateWithWorkspace(t.Context(), domain.CreateProjectInput{
		Name: "disc", HostPath: root,
	})
	require.NoError(t, err)

	// 1. Candidates listing: parse-only, both servers appear, nothing bound.
	rec := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/mcp/candidates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var candidates []MCPCandidate
	decodeData(t, rec, &candidates)
	require.Len(t, candidates, 2)
	bySlug := map[string]MCPCandidate{}
	for _, c := range candidates {
		bySlug[c.Slug] = c
	}
	assert.Equal(t, "pubmed", bySlug["pubmed"].Slug)
	assert.Equal(t, "should-not-run", bySlug["pubmed"].Executable)
	assert.Equal(t, "streamable_http", bySlug["geo"].Transport)
	assert.False(t, bySlug["pubmed"].AlreadyBound)
	assert.Empty(t, bySlug["pubmed"].BoundVersionID)

	// 2. Bind a candidate: materializes an immutable project_file version.
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/mcp/bindings/from-candidate",
		map[string]any{"slug": "pubmed", "transport": "stdio"}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var binding domain.MCPProjectBinding
	decodeData(t, rec, &binding)
	assert.False(t, binding.DesiredEnabled) // never auto-enabled
	assert.Empty(t, binding.SelectedRemoteToolNames)

	// 3. Re-discovery now marks the candidate bound.
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/mcp/candidates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	decodeData(t, rec, &candidates)
	bySlug = map[string]MCPCandidate{}
	for _, c := range candidates {
		bySlug[c.Slug] = c
	}
	assert.True(t, bySlug["pubmed"].AlreadyBound)
	assert.NotEmpty(t, bySlug["pubmed"].BoundVersionID)

	// 4. Project file change -> update_available on re-discovery (the bound
	// version is immutable; the new config gets a NEW version, never a rewrite).
	updatedJSON := `{
	  "mcpServers": {
	    "pubmed": {
	      "type": "stdio",
	      "command": "should-not-run",
	      "args": ["--pwn", "--new-flag"],
	      "envCredentials": {"API_KEY": "env:REAL_KEY"}
	    },
	    "geo": {
	      "type": "streamable_http",
	      "url": "https://example.com/mcp"
	    }
	  }
	}`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ennote", "mcp.json"), []byte(updatedJSON), 0o600))
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/mcp/candidates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	decodeData(t, rec, &candidates)
	bySlug = map[string]MCPCandidate{}
	for _, c := range candidates {
		bySlug[c.Slug] = c
	}
	assert.True(t, bySlug["pubmed"].AlreadyBound, "still bound to the old version")
	assert.True(t, bySlug["pubmed"].UpdateAvailable, "project file changed -> update available")

	// Rebind to the new version: creates a SECOND immutable version and a new
	// binding; the old binding is untouched.
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/mcp/bindings/from-candidate",
		map[string]any{"slug": "pubmed", "transport": "stdio"}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	// After rebind, a fresh discovery must report the candidate as bound to the
	// new version with NO update flag (the config now matches a bound version).
	var finalCandidates []MCPCandidate
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/mcp/candidates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	decodeData(t, rec, &finalCandidates)
	finalBySlug := map[string]MCPCandidate{}
	for _, c := range finalCandidates {
		finalBySlug[c.Slug] = c
	}
	assert.True(t, finalBySlug["pubmed"].AlreadyBound, "rebound to the new version")
	assert.False(t, finalBySlug["pubmed"].UpdateAvailable, "rebound to the new version")

	// 5. Malicious project file (secret literal in env) fails closed.
	badJSON := `{"mcpServers":{"x":{"type":"stdio","command":"/bin/true","env":{"API_KEY":"leak"}}}}`
	require.NoError(t, os.WriteFile(filepath.Join(root, ".ennote", "mcp.json"), []byte(badJSON), 0o600))
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/mcp/candidates", nil, true)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestMCPProjectFileMissing(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.MigrateFixtureSchema(db))
	projectRepo := newFileProjects(t)
	server := &Server{
		DB: db, Token: "test-token", Projects: projectRepo,
		MCP: &MCPServer{
			Profiles: &store.MCPProfileRepo{DB: db}, Bindings: &store.MCPBindingRepo{DB: db},
			Catalogs: &store.MCPCatalogRepo{DB: db}, Runs: &store.MCPRunRepo{DB: db},
		},
	}
	handler := server.Handler()
	root := t.TempDir()
	project, _, err := projectRepo.CreateWithWorkspace(t.Context(), domain.CreateProjectInput{Name: "empty", HostPath: root})
	require.NoError(t, err)

	rec := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/mcp/candidates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var candidates []MCPCandidate
	decodeData(t, rec, &candidates)
	assert.Empty(t, candidates)
}

func TestMCPBundledCatalogAndCandidates(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.MigrateFixtureSchema(db))
	projectRepo := newFileProjects(t)

	// P1 ships an empty bundled registry; the API plumbing still runs.
	bundled := mcpclient.NewBundledRegistry()

	server := &Server{
		DB: db, Token: "test-token", Projects: projectRepo,
		MCP: &MCPServer{
			Profiles: &store.MCPProfileRepo{DB: db}, Bindings: &store.MCPBindingRepo{DB: db},
			Catalogs: &store.MCPCatalogRepo{DB: db}, Runs: &store.MCPRunRepo{DB: db},
			Bundled: bundled,
		},
	}
	handler := server.Handler()
	root := t.TempDir()
	project, _, err := projectRepo.CreateWithWorkspace(t.Context(), domain.CreateProjectInput{Name: "bundled", HostPath: root})
	require.NoError(t, err)

	// Bundled catalog endpoint returns the descriptor list (empty in P1).
	rec := request(t, handler, http.MethodGet, "/v1/mcp/bundled-catalog", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Candidates endpoint includes bundled source kind entries.
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/mcp/candidates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var candidates []MCPCandidate
	decodeData(t, rec, &candidates)
	// Empty registry -> no bundled candidates (metadata-only, zero default).
	assert.Empty(t, candidates)
}

// newAPIFileMCP wires the file-native MCP repos plus a Session-schema DB for
// Run snapshots, mirroring production (V2).
func newAPIFileMCP(t *testing.T) (profiles *store.MCPProfileRepo, bindings *store.MCPBindingRepo,
	catalogs *store.MCPCatalogRepo, runs *store.MCPRunRepo, projectID string, db *sql.DB) {
	t.Helper()
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(t.Context(),
		domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)
	db, err = store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.MigrateSession(db))
	return &store.MCPProfileRepo{FilePath: filepath.Join(home, "config", "mcp.json")},
		&store.MCPBindingRepo{Projects: projects},
		&store.MCPCatalogRepo{CacheDir: filepath.Join(home, "cache", "mcp")},
		&store.MCPRunRepo{DB: db}, project.ID, db
}
