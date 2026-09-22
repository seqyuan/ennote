package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The frozen MCP surface is what lets a reader see which servers a Run reached
// and which tools they contributed, including a server that was required and
// failed. Without it a missing MCP tool call is unattributable.
func TestLoadRunMCPReturnsFrozenServersWithTheirTools(t *testing.T) {
	db := store.SetupDB(t)
	repo := &store.MCPRunRepo{DB: db}
	ctx := context.Background()

	healthyID, err := repo.FreezeServerWithTools(ctx, store.RunMCPServerSnapshot{
		RunID: "run-1", BindingID: "binding-a", BindingRevision: 2, ProfileVersionID: "profile-a@v000001",
		ConfigDigest: "config-a", NegotiatedProtocol: "2025-06-18", CatalogDigest: "catalog-a",
		ServerIdentityDigest: "identity-a", Required: true,
	}, []store.RunMCPToolSnapshot{
		{RemoteName: "search", ExposedName: "bio__search", Description: "Search sequences",
			InputSchema: []byte(`{"type":"object"}`), SchemaDigest: "d1",
			RiskClass: domain.RiskExternal, ExecutionClass: domain.ExecutionExclusive, SourceKind: domain.MCPSourceManaged},
		{RemoteName: "fetch", ExposedName: "bio__fetch", SchemaDigest: "d2",
			RiskClass: domain.RiskReadOnly, ExecutionClass: domain.ExecutionExclusive, SourceKind: domain.MCPSourceManaged},
	})
	require.NoError(t, err)
	require.NotEmpty(t, healthyID)

	// A required server that failed is still a frozen fact, with its reason.
	_, err = repo.FreezeServer(ctx, store.RunMCPServerSnapshot{
		RunID: "run-1", BindingID: "binding-b", BindingRevision: 1, ProfileVersionID: "profile-b@v000001",
		Required: true, UnavailableReason: "connection refused",
	})
	require.NoError(t, err)
	// A frozen row for a different Run must never leak into this one.
	_, err = repo.FreezeServer(ctx, store.RunMCPServerSnapshot{
		RunID: "run-2", BindingID: "binding-c", BindingRevision: 1, ProfileVersionID: "profile-c@v000001",
	})
	require.NoError(t, err)

	frozen, err := repo.LoadRunMCP(ctx, "run-1")
	require.NoError(t, err)
	assert.Equal(t, "run-1", frozen.RunID)
	require.Len(t, frozen.Servers, 2)

	healthy := frozen.Servers[0]
	assert.Equal(t, "binding-a", healthy.Server.BindingID)
	assert.Equal(t, 2, healthy.Server.BindingRevision)
	assert.Equal(t, "catalog-a", healthy.Server.CatalogDigest)
	assert.True(t, healthy.Server.Required)
	assert.Empty(t, healthy.Server.UnavailableReason)
	require.Len(t, healthy.Tools, 2)
	assert.Equal(t, "bio__search", healthy.Tools[0].ExposedName)
	assert.Equal(t, domain.RiskExternal, healthy.Tools[0].RiskClass)

	failed := frozen.Servers[1]
	assert.Equal(t, "binding-b", failed.Server.BindingID)
	assert.Equal(t, "connection refused", failed.Server.UnavailableReason)
	assert.Empty(t, failed.Tools)
}

func TestLoadRunMCPReportsAnEmptySurface(t *testing.T) {
	db := store.SetupDB(t)
	frozen, err := (&store.MCPRunRepo{DB: db}).LoadRunMCP(context.Background(), "run-without-mcp")
	require.NoError(t, err)
	assert.Empty(t, frozen.Servers)
}
