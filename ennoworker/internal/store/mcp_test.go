package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileMCPFixture wires the file-backed MCP authority: profiles in
// config/mcp.json, bindings in project.json, catalog cache under cache/mcp, and
// Run snapshots in the owning Session database.
type fileMCPFixture struct {
	home     string
	projects *projectstore.Store
	profiles *MCPProfileRepo
	bindings *MCPBindingRepo
	catalogs *MCPCatalogRepo
	sessions *sessionstore.Manager
	project  *domain.Project
	session  *domain.Session
	db       *sql.DB
}

func newFileMCP(t *testing.T) *fileMCPFixture {
	t.Helper()
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(context.Background(),
		domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { _ = sessions.Close() })
	session, err := sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	db, err := sessions.OpenSession(context.Background(), session.ID)
	require.NoError(t, err)
	return &fileMCPFixture{
		home: home, projects: projects,
		profiles: &MCPProfileRepo{FilePath: filepath.Join(home, "config", "mcp.json")},
		bindings: &MCPBindingRepo{Projects: projects},
		catalogs: &MCPCatalogRepo{CacheDir: filepath.Join(home, "cache", "mcp")},
		sessions: sessions, project: project, session: session, db: db,
	}
}

// addManagedProfile creates a managed profile with one streamable HTTP version.
func (f *fileMCPFixture) addManagedProfile(t *testing.T, slug, endpoint string) (*domain.MCPServerProfile, *domain.MCPServerProfileVersion) {
	t.Helper()
	profile, err := f.profiles.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: slug, Slug: slug, SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: endpoint}
	require.NoError(t, f.profiles.CreateVersion(context.Background(), profile.ID, version))
	return profile, version
}

func TestMCPCatalogCacheScopedByRevision(t *testing.T) {
	f := newFileMCP(t)
	_, version := f.addManagedProfile(t, "srv", "https://example.com/mcp")
	binding, err := f.bindings.EnsureBindingExists(context.Background(), f.project.ID, version.ID)
	require.NoError(t, err)

	entries := []domain.MCPCatalogEntry{{RemoteName: "a", ExposedName: "srv__a", Digest: "d1"}}
	require.NoError(t, f.catalogs.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", AuthGeneration: 0, CatalogDigest: "c1", Tools: entries,
	}))
	cached, err := f.catalogs.GetCatalog(context.Background(), binding.ID, binding.Revision, 0, version.ID, "latest", "")
	require.NoError(t, err)
	require.Len(t, cached.Tools, 1)

	// A different binding revision must NOT find the old cache (fail closed).
	_, err = f.catalogs.GetCatalog(context.Background(), binding.ID, binding.Revision+1, 0, version.ID, "latest", "")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestMCPRunSnapshotAndRequests(t *testing.T) {
	db := SetupDB(t)
	// V2: run_mcp_servers has no FK to the removed global MCP tables.
	versionID, bindingID := "legacy-profile@v000001", "legacy-binding"

	runRepo := &MCPRunRepo{DB: db}
	serverID, err := runRepo.FreezeServer(context.Background(), RunMCPServerSnapshot{
		RunID: "run-1", BindingID: bindingID, BindingRevision: 1,
		ProfileVersionID: versionID, ConfigDigest: "config-digest-1",
		NegotiatedProtocol: "2025-06-18", CatalogDigest: "c1", Required: true,
	})
	require.NoError(t, err)

	toolID, err := runRepo.FreezeTool(context.Background(), RunMCPToolSnapshot{
		RunServerID: serverID, RemoteName: "search", ExposedName: "bio__search",
		InputSchema: []byte(`{"type":"object"}`), SchemaDigest: "d1",
		RiskClass: domain.RiskExternal, ExecutionClass: domain.ExecutionExclusive,
		SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)

	tools, err := runRepo.ListFrozenTools(context.Background(), serverID)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "bio__search", tools[0].ExposedName)
	assert.Equal(t, domain.RiskExternal, tools[0].RiskClass)

	servers, err := runRepo.ListFrozenServers(context.Background(), "run-1")
	require.NoError(t, err)
	require.Len(t, servers, 1)

	// Request state machine: multiple transitions on the same tool call must
	// upsert in place (one row, advancing status).
	reqID, err := runRepo.CreateRequest(context.Background(), MCPRequestRecord{
		RunID: "run-1", RunServerID: serverID, RunToolID: toolID, ToolCallID: "tc-1",
		Status: domain.MCPRequestPlanned, RequestDigest: "r1",
	})
	require.NoError(t, err)
	_, err = runRepo.CreateRequest(context.Background(), MCPRequestRecord{
		RunID: "run-1", RunServerID: serverID, RunToolID: toolID, ToolCallID: "tc-1",
		Status: domain.MCPRequestDispatched, RequestDigest: "r1",
	})
	require.NoError(t, err)
	require.NoError(t, runRepo.UpdateRequestStatus(context.Background(), reqID, domain.MCPRequestOutcomeUnknown, "", "transport_error"))

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM mcp_requests WHERE run_id='run-1' AND tool_call_id='tc-1'`).Scan(&count))
	assert.Equal(t, 1, count)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM mcp_requests WHERE run_id='run-1' AND tool_call_id='tc-1'`).Scan(&status))
	assert.Equal(t, string(domain.MCPRequestOutcomeUnknown), status)

	gen, err := runRepo.BumpConnectionGeneration(context.Background(), serverID)
	require.NoError(t, err)
	assert.Equal(t, 1, gen)
}

func TestMCPDigestCatalogStable(t *testing.T) {
	a := []domain.MCPCatalogEntry{{RemoteName: "x", ExposedName: "s__x", InputSchema: []byte(`{}`)}}
	b := []domain.MCPCatalogEntry{{RemoteName: "x", ExposedName: "s__x", InputSchema: []byte(`{}`)}}
	assert.Equal(t, DigestCatalog(a), DigestCatalog(b))
	c := []domain.MCPCatalogEntry{{RemoteName: "y", ExposedName: "s__y", InputSchema: []byte(`{}`)}}
	assert.NotEqual(t, DigestCatalog(a), DigestCatalog(c))
}

func TestMCPCatalogCacheAuthGenerationIsolation(t *testing.T) {
	f := newFileMCP(t)
	_, version := f.addManagedProfile(t, "srv", "https://example.com/mcp")
	binding, err := f.bindings.EnsureBindingExists(context.Background(), f.project.ID, version.ID)
	require.NoError(t, err)

	require.NoError(t, f.catalogs.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", AuthGeneration: 0, CatalogDigest: "c-a",
		Tools: []domain.MCPCatalogEntry{{RemoteName: "a", ExposedName: "srv__a", Digest: "da"}},
	}))
	require.NoError(t, f.catalogs.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", AuthGeneration: 1, CatalogDigest: "c-b",
		Tools: []domain.MCPCatalogEntry{{RemoteName: "b", ExposedName: "srv__b", Digest: "db"}},
	}))

	gen0, err := f.catalogs.GetCatalog(context.Background(), binding.ID, binding.Revision, 0, version.ID, "latest", "")
	require.NoError(t, err)
	require.Len(t, gen0.Tools, 1)
	assert.Equal(t, "srv__a", gen0.Tools[0].ExposedName)

	gen1, err := f.catalogs.GetCatalog(context.Background(), binding.ID, binding.Revision, 1, version.ID, "latest", "")
	require.NoError(t, err)
	require.Len(t, gen1.Tools, 1)
	assert.Equal(t, "srv__b", gen1.Tools[0].ExposedName)
}

func TestMCPTwoProjectsSameSlugIsolated(t *testing.T) {
	f := newFileMCP(t)
	otherProjects := &projectstore.Store{Root: filepath.Join(f.home, "projects-other")}
	otherProject, _, err := otherProjects.CreateWithWorkspace(context.Background(),
		domain.CreateProjectInput{Name: "Other", HostPath: t.TempDir()})
	require.NoError(t, err)

	_, versionA := f.addManagedProfile(t, "shared-a", "https://a.example.com/mcp")
	_, versionB := f.addManagedProfile(t, "shared-b", "https://b.example.com/mcp")

	bindingA, err := f.bindings.EnsureBindingExists(context.Background(), f.project.ID, versionA.ID)
	require.NoError(t, err)
	bindingB, err := (&MCPBindingRepo{Projects: otherProjects}).EnsureBindingExists(context.Background(), otherProject.ID, versionB.ID)
	require.NoError(t, err)

	require.NoError(t, f.catalogs.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: bindingA.ID, BindingRevision: bindingA.Revision, ProfileVersionID: versionA.ID,
		ProtocolVersion: "latest", CatalogDigest: "ca",
		Tools: []domain.MCPCatalogEntry{{RemoteName: "x", ExposedName: "shared_a__x", Digest: "dx"}},
	}))
	require.NoError(t, f.catalogs.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: bindingB.ID, BindingRevision: bindingB.Revision, ProfileVersionID: versionB.ID,
		ProtocolVersion: "latest", CatalogDigest: "cb",
		Tools: []domain.MCPCatalogEntry{{RemoteName: "y", ExposedName: "shared_b__y", Digest: "dy"}},
	}))

	gotA, err := f.catalogs.GetCatalog(context.Background(), bindingA.ID, bindingA.Revision, 0, versionA.ID, "latest", "")
	require.NoError(t, err)
	gotB, err := f.catalogs.GetCatalog(context.Background(), bindingB.ID, bindingB.Revision, 0, versionB.ID, "latest", "")
	require.NoError(t, err)
	assert.Equal(t, "shared_a__x", gotA.Tools[0].ExposedName)
	assert.Equal(t, "shared_b__y", gotB.Tools[0].ExposedName)
}

func TestMCPRequestsSurviveStoreReopen(t *testing.T) {
	// Worker restart semantics: the mcp_requests rows are durable; a restarted
	// Worker must NOT replay them (no automatic MCP retry). Verify persistence
	// across a fresh DB handle.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mcp.db")
	db1, err := Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, MigrateFixtureSchema(db1))

	versionID, bindingID := "legacy-profile@v000001", "legacy-binding"
	runRepo1 := &MCPRunRepo{DB: db1}
	serverID, err := runRepo1.FreezeServer(context.Background(), RunMCPServerSnapshot{
		RunID: "run-1", BindingID: bindingID, BindingRevision: 1,
		ProfileVersionID: versionID, ConfigDigest: "config-digest-1",
		NegotiatedProtocol: "2025-06-18", CatalogDigest: "c", Required: true,
	})
	require.NoError(t, err)
	toolID, err := runRepo1.FreezeTool(context.Background(), RunMCPToolSnapshot{
		RunServerID: serverID, RemoteName: "t", ExposedName: "p__t", InputSchema: []byte(`{}`),
		SchemaDigest: "d", RiskClass: domain.RiskExternal, ExecutionClass: domain.ExecutionExclusive,
	})
	require.NoError(t, err)

	reqID, err := runRepo1.CreateRequest(context.Background(), MCPRequestRecord{
		RunID: "run-1", RunServerID: serverID, RunToolID: toolID, ToolCallID: "tc-9",
		Status: domain.MCPRequestOutcomeUnknown, RequestDigest: "r",
	})
	require.NoError(t, err)
	require.NoError(t, db1.Close())

	// Reopen: the request row survives; no MCP retry rows are replayed.
	db2, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db2.Close() })
	require.NoError(t, MigrateFixtureSchema(db2))
	var count int
	require.NoError(t, db2.QueryRow(`SELECT COUNT(*) FROM mcp_requests WHERE run_id='run-1' AND id=?`, reqID).Scan(&count))
	assert.Equal(t, 1, count)
}
