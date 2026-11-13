package api

import (
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolvedGlobalHeroEndpoint pins the design 六 P1 debug dump endpoint: the
// resolved hero is the latest published revision with an audit source, never
// the mutable draft.
func TestResolvedGlobalHeroEndpoint(t *testing.T) {
	home := t.TempDir()
	server := &Server{Token: "test-token", GlobalSources: &globalsource.Store{HomeDir: home}}
	handler := server.Handler()
	document, err := heroesource.Parse([]byte(globalHeroFixture))
	require.NoError(t, err)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes", map[string]any{"document": document}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())

	// Before publish: resolution must fail (draft is not the effective hero).
	response = request(t, handler, http.MethodGet, "/v1/global-heroes/researcher/resolved", nil, true)
	require.Equal(t, http.StatusNotFound, response.Code)

	// Publish, then resolve.
	response = request(t, handler, http.MethodPost, "/v1/global-heroes/researcher/publish", nil, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())

	response = request(t, handler, http.MethodGet, "/v1/global-heroes/researcher/resolved", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var resolved struct {
		HeroID   string                `json:"heroId"`
		Source   string                `json:"source"`
		Revision globalsource.Revision `json:"revision"`
		Document *heroesource.Document `json:"document"`
	}
	decodeData(t, response, &resolved)
	assert.Equal(t, "researcher", resolved.HeroID)
	assert.Equal(t, "global", resolved.Source)
	assert.Equal(t, "v000001", resolved.Revision.ID())
	assert.Equal(t, "Researcher", resolved.Document.Name)
}
