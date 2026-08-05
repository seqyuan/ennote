package api

import (
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMCPBindingCrossProjectIsolation verifies every binding-scoped endpoint
// fails closed when the path projectID does not own the binding (P0-4).
func TestMCPBindingCrossProjectIsolation(t *testing.T) {
	_, handler := setupServer(t, &fakeController{})
	profileID := createMCPProfileWithVersion(t, handler)

	// Create binding for project A.
	rec := request(t, handler, http.MethodPost, "/v1/projects/proj-a/mcp/bindings", map[string]any{
		"profileVersionId": profileID,
	}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var binding domain.MCPProjectBinding
	decodeData(t, rec, &binding)

	// Every binding-scoped endpoint under the WRONG project must 404.
	paths := []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPatch, "/v1/projects/proj-b/mcp/bindings/" + binding.ID, map[string]any{"desiredEnabled": true}},
		{http.MethodDelete, "/v1/projects/proj-b/mcp/bindings/" + binding.ID, nil},
		{http.MethodPost, "/v1/projects/proj-b/mcp/bindings/" + binding.ID + "/test", nil},
		{http.MethodGet, "/v1/projects/proj-b/mcp/bindings/" + binding.ID + "/catalog", nil},
		{http.MethodPost, "/v1/projects/proj-b/mcp/bindings/" + binding.ID + "/catalog/refresh", nil},
	}
	for _, tc := range paths {
		rec = request(t, handler, tc.method, tc.path, tc.body, true)
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s", tc.method, tc.path)
	}

	// The owning project can still mutate it.
	rec = request(t, handler, http.MethodPatch, "/v1/projects/proj-a/mcp/bindings/"+binding.ID,
		map[string]any{"desiredEnabled": true}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestMCPServerProfileCreationRejectsNonManaged verifies the API cannot be
// used to create project_file/bundled profiles directly (scope escape).
func TestMCPServerProfileCreationRejectsNonManaged(t *testing.T) {
	_, handler := setupServer(t, &fakeController{})
	rec := request(t, handler, http.MethodPost, "/v1/mcp/server-profiles", map[string]any{
		"displayName": "Bad", "slug": "bad", "sourceKind": "project_file",
	}, true)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
