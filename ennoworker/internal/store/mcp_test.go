package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPProfileLifecycle(t *testing.T) {
	db := SetupDB(t)
	repo := &MCPProfileRepo{DB: db}

	profile, err := repo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "Pubmed", Slug: "pubmed", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	assert.Equal(t, "pubmed", profile.Slug)

	version := &domain.MCPServerProfileVersion{
		Transport: domain.MCPTransportStdio, Executable: "/bin/echo", Argv: []string{"-n", "hi"},
		TimeoutMS: 5000, NetworkPolicy: "default",
	}
	require.NoError(t, repo.CreateVersion(context.Background(), profile.ID, version))
	assert.Equal(t, 1, version.Version)
	assert.NotEmpty(t, version.ConfigDigest)

	// A second version bumps the number and keeps the first immutable.
	version2 := &domain.MCPServerProfileVersion{
		Transport: domain.MCPTransportStdio, Executable: "/bin/echo", Argv: []string{"-n", "bye"},
	}
	require.NoError(t, repo.CreateVersion(context.Background(), profile.ID, version2))
	assert.Equal(t, 2, version2.Version)

	versions, err := repo.ListVersions(context.Background(), profile.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, "/bin/echo", versions[0].Executable)
	assert.Equal(t, []string{"-n", "hi"}, versions[0].Argv)

	got, err := repo.GetVersion(context.Background(), version.ID)
	require.NoError(t, err)
	assert.Equal(t, version.ConfigDigest, got.ConfigDigest)

	profiles, err := repo.ListProfiles(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, 2, profiles[0].LatestVersion)

	require.NoError(t, repo.Archive(context.Background(), profile.ID))
	profiles, err = repo.ListProfiles(context.Background())
	require.NoError(t, err)
	assert.Len(t, profiles, 0)
}

func TestMCPBindingLifecycle(t *testing.T) {
	db := SetupDB(t)
	profileRepo := &MCPProfileRepo{DB: db}
	bindingRepo := &MCPBindingRepo{DB: db}

	profile, err := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "GitHub", Slug: "github", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))

	binding, err := bindingRepo.EnsureBindingExists(context.Background(), "project-1", version.ID)
	require.NoError(t, err)
	assert.False(t, binding.DesiredEnabled)
	assert.True(t, binding.Required)

	enabled := true
	updated, err := bindingRepo.Update(context.Background(), binding.ID, MCPBindingUpdate{
		DesiredEnabled:          &enabled,
		SelectedRemoteToolNames: []string{"list_issues", "get_issue"},
		CredentialRefs:          map[string]string{"GITHUB_TOKEN": "env:GITHUB_TOKEN"},
	})
	require.NoError(t, err)
	assert.True(t, updated.DesiredEnabled)
	assert.Equal(t, 2, updated.Revision)
	assert.Equal(t, []string{"list_issues", "get_issue"}, updated.SelectedRemoteToolNames)

	byProject, err := bindingRepo.ListByProject(context.Background(), "project-1")
	require.NoError(t, err)
	require.Len(t, byProject, 1)

	// Invalid credential ref fails closed.
	badRef := true
	_, err = bindingRepo.Update(context.Background(), binding.ID, MCPBindingUpdate{
		DesiredEnabled: &badRef, CredentialRefs: map[string]string{"X": "plaintext-secret"},
	})
	require.Error(t, err)

	require.NoError(t, bindingRepo.Delete(context.Background(), binding.ID))
	_, err = bindingRepo.Get(context.Background(), binding.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestMCPCatalogCacheScopedByRevision(t *testing.T) {
	db := SetupDB(t)
	profileRepo := &MCPProfileRepo{DB: db}
	bindingRepo := &MCPBindingRepo{DB: db}
	catalogRepo := &MCPCatalogRepo{DB: db}

	profile, _ := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "Srv", Slug: "srv", SourceKind: domain.MCPSourceManaged,
	})
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	binding, _ := bindingRepo.EnsureBindingExists(context.Background(), "p1", version.ID)

	entries := []domain.MCPCatalogEntry{{RemoteName: "a", ExposedName: "srv__a", Digest: "d1"}}
	require.NoError(t, catalogRepo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", AuthGeneration: 0, CatalogDigest: "c1", Tools: entries,
	}))
	cached, err := catalogRepo.GetCatalog(context.Background(), binding.ID, binding.Revision, 0, version.ID, "latest", "")
	require.NoError(t, err)
	require.Len(t, cached.Tools, 1)

	// A different binding revision must NOT find the old cache (fail closed).
	_, err = catalogRepo.GetCatalog(context.Background(), binding.ID, binding.Revision+1, 0, version.ID, "latest", "")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestMCPRunSnapshotAndRequests(t *testing.T) {
	db := SetupDB(t)
	profileRepo := &MCPProfileRepo{DB: db}
	bindingRepo := &MCPBindingRepo{DB: db}
	catalogRepo := &MCPCatalogRepo{DB: db}
	runRepo := &MCPRunRepo{DB: db}

	profile, _ := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "Bio", Slug: "bio", SourceKind: domain.MCPSourceManaged,
	})
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStdio, Executable: "/bin/true"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	binding, _ := bindingRepo.EnsureBindingExists(context.Background(), "p1", version.ID)
	entries := []domain.MCPCatalogEntry{{RemoteName: "search", ExposedName: "bio__search", InputSchema: []byte(`{"type":"object"}`), Digest: "d1"}}
	require.NoError(t, catalogRepo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", CatalogDigest: "c1", Tools: entries,
	}))

	serverID, err := runRepo.FreezeServer(context.Background(), RunMCPServerSnapshot{
		RunID: "run-1", BindingID: binding.ID, BindingRevision: binding.Revision,
		ProfileVersionID: version.ID, ConfigDigest: version.ConfigDigest,
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

	// Exactly one row for tc-1, terminal status outcome_unknown.
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM mcp_requests WHERE run_id='run-1' AND tool_call_id='tc-1'`).Scan(&count))
	assert.Equal(t, 1, count)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM mcp_requests WHERE run_id='run-1' AND tool_call_id='tc-1'`).Scan(&status))
	assert.Equal(t, string(domain.MCPRequestOutcomeUnknown), status)

	// Generation bump.
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
	db := SetupDB(t)
	profileRepo := &MCPProfileRepo{DB: db}
	bindingRepo := &MCPBindingRepo{DB: db}
	catalogRepo := &MCPCatalogRepo{DB: db}

	profile, _ := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "Srv", Slug: "srv", SourceKind: domain.MCPSourceManaged,
	})
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	binding, _ := bindingRepo.EnsureBindingExists(context.Background(), "p1", version.ID)

	// Identity A (auth generation 0): catalog shows tool "a".
	require.NoError(t, catalogRepo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", AuthGeneration: 0, CatalogDigest: "c-a",
		Tools: []domain.MCPCatalogEntry{{RemoteName: "a", ExposedName: "srv__a", Digest: "da"}},
	}))
	// Identity B (auth generation 1): same binding+revision, different catalog.
	require.NoError(t, catalogRepo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", AuthGeneration: 1, CatalogDigest: "c-b",
		Tools: []domain.MCPCatalogEntry{{RemoteName: "b", ExposedName: "srv__b", Digest: "db"}},
	}))

	// Each generation reads only its own catalog — never the other identity's.
	gen0, err := catalogRepo.GetCatalog(context.Background(), binding.ID, binding.Revision, 0, version.ID, "latest", "")
	require.NoError(t, err)
	require.Len(t, gen0.Tools, 1)
	assert.Equal(t, "srv__a", gen0.Tools[0].ExposedName)

	gen1, err := catalogRepo.GetCatalog(context.Background(), binding.ID, binding.Revision, 1, version.ID, "latest", "")
	require.NoError(t, err)
	require.Len(t, gen1.Tools, 1)
	assert.Equal(t, "srv__b", gen1.Tools[0].ExposedName)
}

func TestMCPTwoProjectsSameSlugIsolated(t *testing.T) {
	db := SetupDB(t)
	profileRepo := &MCPProfileRepo{DB: db}
	bindingRepo := &MCPBindingRepo{DB: db}
	catalogRepo := &MCPCatalogRepo{DB: db}

	// Two managed profiles with the same slug are allowed (different ids).
	profileA, err := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "Shared", Slug: "shared", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	profileB, err := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "Shared", Slug: "shared", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	assert.NotEqual(t, profileA.ID, profileB.ID)

	versionA := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://a.example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profileA.ID, versionA))
	versionB := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://b.example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profileB.ID, versionB))

	// Each project binds its own version.
	bindingA, err := bindingRepo.EnsureBindingExists(context.Background(), "proj-1", versionA.ID)
	require.NoError(t, err)
	bindingB, err := bindingRepo.EnsureBindingExists(context.Background(), "proj-2", versionB.ID)
	require.NoError(t, err)

	// Separate catalogs, no cross-project reuse.
	require.NoError(t, catalogRepo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: bindingA.ID, BindingRevision: 1, ProfileVersionID: versionA.ID,
		ProtocolVersion: "latest", CatalogDigest: "ca",
		Tools: []domain.MCPCatalogEntry{{RemoteName: "x", ExposedName: "shared__x", Digest: "dx"}},
	}))
	require.NoError(t, catalogRepo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: bindingB.ID, BindingRevision: 1, ProfileVersionID: versionB.ID,
		ProtocolVersion: "latest", CatalogDigest: "cb",
		Tools: []domain.MCPCatalogEntry{{RemoteName: "y", ExposedName: "shared__y", Digest: "dy"}},
	}))

	gotA, err := catalogRepo.GetCatalog(context.Background(), bindingA.ID, 1, 0, versionA.ID, "latest", "")
	require.NoError(t, err)
	gotB, err := catalogRepo.GetCatalog(context.Background(), bindingB.ID, 1, 0, versionB.ID, "latest", "")
	require.NoError(t, err)
	assert.Equal(t, "shared__x", gotA.Tools[0].ExposedName)
	assert.Equal(t, "shared__y", gotB.Tools[0].ExposedName)
}

func TestMCPRequestsSurviveStoreReopen(t *testing.T) {
	// Worker restart semantics: the mcp_requests rows are durable; a restarted
	// Worker must NOT replay them (no automatic MCP retry). Verify persistence
	// across a fresh DB handle.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mcp.db")
	db1, err := Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, Migrate(db1))

	// Build the FK chain: profile -> version -> binding -> run server -> tool.
	profileRepo := &MCPProfileRepo{DB: db1}
	bindingRepo := &MCPBindingRepo{DB: db1}
	catalogRepo := &MCPCatalogRepo{DB: db1}
	runRepo1 := &MCPRunRepo{DB: db1}
	profile, _ := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "P", Slug: "p", SourceKind: domain.MCPSourceManaged,
	})
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStdio, Executable: "/bin/true"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	binding, _ := bindingRepo.EnsureBindingExists(context.Background(), "p1", version.ID)
	require.NoError(t, catalogRepo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", CatalogDigest: "c", Tools: []domain.MCPCatalogEntry{{RemoteName: "t", ExposedName: "p__t", Digest: "d"}},
	}))
	serverID, err := runRepo1.FreezeServer(context.Background(), RunMCPServerSnapshot{
		RunID: "run-1", BindingID: binding.ID, BindingRevision: binding.Revision,
		ProfileVersionID: version.ID, ConfigDigest: version.ConfigDigest,
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

	// Reopen: the record persists with its terminal status.
	db2, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db2.Close() })
	var status string
	require.NoError(t, db2.QueryRow(`SELECT status FROM mcp_requests WHERE id=?`, reqID).Scan(&status))
	assert.Equal(t, string(domain.MCPRequestOutcomeUnknown), status)
}
