package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freezeServerToolForRequests(t *testing.T, db *sql.DB, runID string) (serverID, toolID string) {
	t.Helper()
	repo := &MCPRunRepo{DB: db}
	profileRepo := &MCPProfileRepo{DB: db}
	profile, err := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "S", Slug: "s", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	serverID, sErr := repo.FreezeServer(context.Background(), RunMCPServerSnapshot{
		RunID: runID, BindingID: "b1", BindingRevision: 1, ProfileVersionID: version.ID,
		ConfigDigest: "c1", NegotiatedProtocol: "2025-06-18", Required: true,
	})
	require.NoError(t, sErr)
	toolID, fErr := repo.FreezeTool(context.Background(), RunMCPToolSnapshot{
		RunServerID: serverID, RemoteName: "a", ExposedName: "s__a",
		InputSchema: []byte(`{"type":"object"}`), SchemaDigest: "d",
		RiskClass: domain.RiskExternal, ExecutionClass: domain.ExecutionExclusive,
	})
	require.NoError(t, fErr)
	return serverID, toolID
}

func TestMCPRequestStateMachineRejectsIllegalTransitions(t *testing.T) {
	db := SetupDB(t)
	repo := &MCPRunRepo{DB: db}
	serverID, toolID := freezeServerToolForRequests(t, db, "r1")

	_, err := repo.CreateRequest(context.Background(), MCPRequestRecord{
		RunID: "r1", RunServerID: serverID, RunToolID: toolID, ToolCallID: "tc-1",
		Status: domain.MCPRequestCompleted, RequestDigest: "d1",
	})
	require.NoError(t, err)

	// Terminal -> dispatched is illegal: a completed call must never be
	// replayed/recorded as dispatched (exactly-once terminalization).
	_, err = repo.CreateRequest(context.Background(), MCPRequestRecord{
		RunID: "r1", RunServerID: serverID, RunToolID: toolID, ToolCallID: "tc-1",
		Status: domain.MCPRequestDispatched, RequestDigest: "d1",
	})
	require.Error(t, err)
	var terr *MCPRequestTransitionError
	require.ErrorAs(t, err, &terr)
	assert.Equal(t, domain.MCPRequestCompleted, terr.From)
	assert.Equal(t, domain.MCPRequestDispatched, terr.To)

	// Idempotent re-record of the same terminal status is allowed.
	_, err = repo.CreateRequest(context.Background(), MCPRequestRecord{
		RunID: "r1", RunServerID: serverID, RunToolID: toolID, ToolCallID: "tc-1",
		Status: domain.MCPRequestCompleted, RequestDigest: "d1",
	})
	require.NoError(t, err)
}

func TestMCPCatalogStaleRowIsAMiss(t *testing.T) {
	db := SetupDB(t)
	repo := &MCPCatalogRepo{DB: db}
	profileRepo := &MCPProfileRepo{DB: db}
	bindingRepo := &MCPBindingRepo{DB: db}

	profile, err := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "S", Slug: "s", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	binding, err := bindingRepo.EnsureBindingExists(context.Background(), "p1", version.ID)
	require.NoError(t, err)

	entries := []domain.MCPCatalogEntry{{RemoteName: "a", ExposedName: "s__a", InputSchema: []byte(`{}`), Digest: "d"}}
	require.NoError(t, repo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", CatalogDigest: "c", Tools: entries, FetchedAt: time.Now().UTC(),
	}))
	cached, err := repo.GetCatalog(context.Background(), binding.ID, binding.Revision, 0, version.ID, "latest", "")
	require.NoError(t, err)
	require.Len(t, cached.Tools, 1)

	// After a tools/list_changed notification marks the row stale, the same
	// lookup must MISS so the next Run refreshes.
	require.NoError(t, repo.MarkCatalogStale(context.Background(), binding.ID, 0))
	_, err = repo.GetCatalog(context.Background(), binding.ID, binding.Revision, 0, version.ID, "latest", "")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestMCPCatalogCredentialDigestIsolation(t *testing.T) {
	db := SetupDB(t)
	repo := &MCPCatalogRepo{DB: db}
	profileRepo := &MCPProfileRepo{DB: db}
	bindingRepo := &MCPBindingRepo{DB: db}

	profile, err := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "S", Slug: "s", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	binding, err := bindingRepo.EnsureBindingExists(context.Background(), "p1", version.ID)
	require.NoError(t, err)

	// Same binding revision but different credential refs must not share a
	// catalog: a server may vary its toolset by identity.
	require.NoError(t, repo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", CredentialDigest: "cred-A",
		CatalogDigest: "cA", Tools: []domain.MCPCatalogEntry{{RemoteName: "a", ExposedName: "s__a", Digest: "d"}},
	}))
	require.NoError(t, repo.PutCatalog(context.Background(), MCPCatalogCacheRow{
		BindingID: binding.ID, BindingRevision: binding.Revision, ProfileVersionID: version.ID,
		ProtocolVersion: "latest", CredentialDigest: "cred-B",
		CatalogDigest: "cB", Tools: []domain.MCPCatalogEntry{{RemoteName: "b", ExposedName: "s__b", Digest: "d"}},
	}))
	gotA, err := repo.GetCatalog(context.Background(), binding.ID, binding.Revision, 0, version.ID, "latest", "cred-A")
	require.NoError(t, err)
	assert.Equal(t, "s__a", gotA.Tools[0].ExposedName)
	gotB, err := repo.GetCatalog(context.Background(), binding.ID, binding.Revision, 0, version.ID, "latest", "cred-B")
	require.NoError(t, err)
	assert.Equal(t, "s__b", gotB.Tools[0].ExposedName)
}

func TestMCPBindingPatchPreservesUntouchedFields(t *testing.T) {
	db := SetupDB(t)
	profileRepo := &MCPProfileRepo{DB: db}
	bindingRepo := &MCPBindingRepo{DB: db}

	profile, err := profileRepo.CreateProfile(context.Background(), CreateMCPProfileInput{
		DisplayName: "S", Slug: "s", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{Transport: domain.MCPTransportStreamableHTTP, Endpoint: "https://example.com/mcp"}
	require.NoError(t, profileRepo.CreateVersion(context.Background(), profile.ID, version))
	binding, err := bindingRepo.EnsureBindingExists(context.Background(), "p1", version.ID)
	require.NoError(t, err)

	// Full desired-state update.
	_, err = bindingRepo.Update(context.Background(), binding.ID, MCPBindingUpdate{
		DesiredEnabled:          ptrBool(true),
		SelectedRemoteToolNames: []string{"a", "b"},
		CredentialRefs:          map[string]string{"GITHUB_TOKEN": "env:GITHUB_TOKEN"},
	})
	require.NoError(t, err)

	// A PATCH that only flips desiredEnabled must NOT clear selections or refs.
	updated, err := bindingRepo.Update(context.Background(), binding.ID, MCPBindingUpdate{DesiredEnabled: ptrBool(false)})
	require.NoError(t, err)
	assert.False(t, updated.DesiredEnabled)
	assert.Equal(t, []string{"a", "b"}, updated.SelectedRemoteToolNames)
	assert.Equal(t, map[string]string{"GITHUB_TOKEN": "env:GITHUB_TOKEN"}, updated.CredentialRefs)

	// A PATCH that only changes selection must NOT clear credential refs.
	updated, err = bindingRepo.Update(context.Background(), binding.ID, MCPBindingUpdate{
		SelectedRemoteToolNames: []string{"a"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, updated.SelectedRemoteToolNames)
	assert.Equal(t, map[string]string{"GITHUB_TOKEN": "env:GITHUB_TOKEN"}, updated.CredentialRefs)
	assert.False(t, updated.DesiredEnabled)
}

func ptrBool(v bool) *bool { return &v }
