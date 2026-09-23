package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func catalogRow() store.MCPCatalogCacheRow {
	return store.MCPCatalogCacheRow{
		BindingID: "binding", BindingRevision: 1, ProfileVersionID: "profile@v000001",
		ProtocolVersion: "latest", AuthGeneration: 0, CredentialDigest: "cred",
		ServerIdentityDigest: "identity", CatalogDigest: "catalog",
		Tools:     []domain.MCPCatalogEntry{{RemoteName: "search", ExposedName: "bio__search", Digest: "d1"}},
		FetchedAt: time.Now().UTC(),
	}
}

// The cache used to have a SQL form whose table no migration declared: every read
// failed and callers treated the failure as a cache miss, so a SQL-configured repo
// cached nothing and said nothing. A repo with no cache directory is now a wiring
// error on every operation.
func TestCatalogCacheRequiresADirectory(t *testing.T) {
	repo := &store.MCPCatalogRepo{}
	ctx := context.Background()

	require.ErrorIs(t, repo.PutCatalog(ctx, catalogRow()), store.ErrMCPCatalogCacheDirMissing)
	_, err := repo.GetCatalog(ctx, "binding", 1, 0, "profile@v000001", "latest", "cred")
	require.ErrorIs(t, err, store.ErrMCPCatalogCacheDirMissing)
	require.ErrorIs(t, repo.MarkCatalogStale(ctx, "binding", 0), store.ErrMCPCatalogCacheDirMissing)

	// Whitespace is not a directory.
	blank := &store.MCPCatalogRepo{CacheDir: "   "}
	require.ErrorIs(t, blank.PutCatalog(ctx, catalogRow()), store.ErrMCPCatalogCacheDirMissing)
}

// The file form is the whole cache: a row written must read back with its tools
// and the handshake facts, and a different credential digest must miss.
func TestCatalogCacheFileFormRoundTrips(t *testing.T) {
	repo := &store.MCPCatalogRepo{CacheDir: t.TempDir()}
	ctx := context.Background()
	row := catalogRow()
	row.NegotiatedProtocol = "2026-07-28"
	row.Instructions = "Search before you fetch."

	require.NoError(t, repo.PutCatalog(ctx, row))

	cached, err := repo.GetCatalog(ctx, row.BindingID, row.BindingRevision, row.AuthGeneration,
		row.ProfileVersionID, row.ProtocolVersion, row.CredentialDigest)
	require.NoError(t, err)
	require.Len(t, cached.Tools, 1)
	assert.Equal(t, "bio__search", cached.Tools[0].ExposedName)
	assert.Equal(t, "2026-07-28", cached.NegotiatedProtocol)
	assert.Equal(t, "Search before you fetch.", cached.Instructions)

	// A different credential generation must not read another identity's catalog.
	_, err = repo.GetCatalog(ctx, row.BindingID, row.BindingRevision, row.AuthGeneration,
		row.ProfileVersionID, row.ProtocolVersion, "other-credential")
	require.Error(t, err)

	// A binding revision bump must miss too: a cached catalog belongs to one
	// revision, and a stale one must never serve a newer binding.
	_, err = repo.GetCatalog(ctx, row.BindingID, row.BindingRevision+1, row.AuthGeneration,
		row.ProfileVersionID, row.ProtocolVersion, row.CredentialDigest)
	require.Error(t, err)

	require.NoError(t, repo.MarkCatalogStale(ctx, row.BindingID, row.AuthGeneration))
	_, err = repo.GetCatalog(ctx, row.BindingID, row.BindingRevision, row.AuthGeneration,
		row.ProfileVersionID, row.ProtocolVersion, row.CredentialDigest)
	require.Error(t, err, "a stale catalog must be treated as a miss")
}
