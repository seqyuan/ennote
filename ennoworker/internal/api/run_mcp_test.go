package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runMCPResponse struct {
	RunID   string `json:"runId"`
	Servers []struct {
		ID                 string `json:"id"`
		BindingID          string `json:"bindingId"`
		BindingRevision    int    `json:"bindingRevision"`
		Required           bool   `json:"required"`
		UnavailableReason  string `json:"unavailableReason"`
		CatalogDigest      string `json:"catalogDigest"`
		NegotiatedProtocol string `json:"negotiatedProtocol"`
		Tools              []struct {
			ExposedName string `json:"exposedName"`
			RiskClass   string `json:"riskClass"`
		} `json:"tools"`
	} `json:"servers"`
}

func TestGetRunMCPReturnsTheFrozenSurface(t *testing.T) {
	_, handler, _, session, db := setupGraphRunsServer(t)
	ctx := context.Background()
	runID := insertRunRow(t, ctx, db, session.ID, "{}", "")

	repo := &store.MCPRunRepo{DB: db}
	_, err := repo.FreezeServerWithTools(ctx, store.RunMCPServerSnapshot{
		RunID: runID, BindingID: "binding-a", BindingRevision: 3, ProfileVersionID: "profile-a@v000001",
		ConfigDigest: "config-a", NegotiatedProtocol: "2025-06-18", CatalogDigest: "catalog-a",
		Required: true,
	}, []store.RunMCPToolSnapshot{{
		RemoteName: "search", ExposedName: "bio__search", Description: "Search",
		InputSchema: []byte(`{"type":"object"}`), SchemaDigest: "d1",
		RiskClass: domain.RiskExternal, SourceKind: domain.MCPSourceManaged,
	}})
	require.NoError(t, err)
	_, err = repo.FreezeServer(ctx, store.RunMCPServerSnapshot{
		RunID: runID, BindingID: "binding-b", BindingRevision: 1, ProfileVersionID: "profile-b@v000001",
		Required: false, UnavailableReason: "connection refused",
	})
	require.NoError(t, err)

	rec := request(t, handler, http.MethodGet, "/v1/runs/"+runID+"/mcp", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got runMCPResponse
	decodeData(t, rec, &got)
	assert.Equal(t, runID, got.RunID)
	require.Len(t, got.Servers, 2)
	assert.Equal(t, "binding-a", got.Servers[0].BindingID)
	assert.Equal(t, 3, got.Servers[0].BindingRevision)
	assert.Equal(t, "2025-06-18", got.Servers[0].NegotiatedProtocol)
	require.Len(t, got.Servers[0].Tools, 1)
	assert.Equal(t, "bio__search", got.Servers[0].Tools[0].ExposedName)
	assert.Equal(t, "external", got.Servers[0].Tools[0].RiskClass)
	// A required server that failed is a recorded fact, not an absence.
	assert.Equal(t, "connection refused", got.Servers[1].UnavailableReason)
	assert.Empty(t, got.Servers[1].Tools)
	assert.Contains(t, rec.Body.String(), `"tools":[]`, "tools must serialize as an array")
}

func TestGetRunMCPRejectsAnUnknownRun(t *testing.T) {
	_, handler, _, _, _ := setupGraphRunsServer(t)

	rec := request(t, handler, http.MethodGet, "/v1/runs/00000000-0000-4000-8000-000000000000/mcp", nil, true)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
