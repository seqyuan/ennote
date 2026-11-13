package api

import (
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func heroServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	server := &Server{Token: "test-token", GlobalSources: &globalsource.Store{HomeDir: t.TempDir()}}
	return server, server.Handler()
}

func heroDocument(t *testing.T) *heroesource.Document {
	t.Helper()
	document, err := heroesource.Parse([]byte(globalHeroFixture))
	require.NoError(t, err)
	return document
}

func createHero(t *testing.T, handler http.Handler, document *heroesource.Document) globalHeroDetail {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/v1/global-heroes", map[string]any{"document": document}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalHeroDetail
	decodeData(t, response, &created)
	return created
}

// Saving (create and PATCH) must reject an invalid document up front as 422
// with the field-path message, instead of letting it reach disk and fail only
// when a Run freezes.
func TestGlobalHeroSaveRejectsInvalidDocument(t *testing.T) {
	_, handler := heroServer(t)

	invalid := heroDocument(t)
	invalid.PermissionCeiling = ""
	response := request(t, handler, http.MethodPost, "/v1/global-heroes", map[string]any{"document": invalid}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "hero_validation_failed")
	assert.Contains(t, response.Body.String(), "permissionCeiling")

	// Nothing was written.
	response = request(t, handler, http.MethodGet, "/v1/global-heroes", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var summaries []globalHeroSummary
	decodeData(t, response, &summaries)
	assert.Empty(t, summaries)

	created := createHero(t, handler, heroDocument(t))
	invalid = heroDocument(t)
	invalid.MaxLoopIterations = 0
	response = request(t, handler, http.MethodPatch, "/v1/global-heroes/researcher",
		map[string]any{"expectedDigest": created.Digest, "document": invalid}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "hero_validation_failed")
	assert.Contains(t, response.Body.String(), "maxLoopIterations")
}

// A read-only Hero is allowed to be saved with a mutation tool in its draft,
// but publishing it must fail the same way a Run freeze would.
func TestGlobalHeroPublishRejectsMutationToolOnReadOnlyHero(t *testing.T) {
	_, handler := heroServer(t)

	document := heroDocument(t)
	document.AllowedTools = []string{"read", "bash"}
	createHero(t, handler, document)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes/researcher/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "hero_publish_failed")
	assert.Contains(t, response.Body.String(), "mutation tool")
	assert.Contains(t, response.Body.String(), "bash")
}

func TestGlobalHeroPublishRejectsUnknownSkill(t *testing.T) {
	server, handler := heroServer(t)
	server.SkillCatalog = func() map[string]string { return map[string]string{"known-skill": "digest"} }

	document := heroDocument(t)
	document.Skills = []heroesource.SkillBinding{{ID: "global/missing-skill", Mode: domain.HeroSkillAvailable}}
	createHero(t, handler, document)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes/researcher/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "hero_publish_failed")
	assert.Contains(t, response.Body.String(), "unknown skill")
	assert.Contains(t, response.Body.String(), "global/missing-skill")
}

// A global Hero has no owning Graph directory, so a graph-local Skill can never
// resolve; it must be rejected with a clear message even though a global Skill
// of the same spelling would be valid.
func TestGlobalHeroPublishRejectsGraphLocalSkill(t *testing.T) {
	server, handler := heroServer(t)
	server.SkillCatalog = func() map[string]string { return map[string]string{} }

	document := heroDocument(t)
	document.Skills = []heroesource.SkillBinding{{ID: "local/review-tools", Mode: domain.HeroSkillAvailable}}
	createHero(t, handler, document)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes/researcher/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "hero_publish_failed")
	assert.Contains(t, response.Body.String(), "graph-local Skill")
	assert.Contains(t, response.Body.String(), "local/review-tools")
}

// Without a Skill catalog the Worker cannot decide whether a Skill reference is
// real. That is the Worker's missing dependency, not the author's mistake, so
// it must answer 503 like Graph publish does.
func TestGlobalHeroPublishReportsMissingSkillCatalogAsUnavailable(t *testing.T) {
	server, handler := heroServer(t)
	require.Nil(t, server.SkillCatalog)

	document := heroDocument(t)
	document.Skills = []heroesource.SkillBinding{{ID: "global/whatever", Mode: domain.HeroSkillAvailable}}
	createHero(t, handler, document)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes/researcher/publish", nil, true)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "hero_validation_unavailable")
}

// The normal path stays intact: a valid Hero still saves and publishes.
func TestGlobalHeroSaveAndPublishStillSucceeds(t *testing.T) {
	_, handler := heroServer(t)
	created := createHero(t, handler, heroDocument(t))
	assert.Equal(t, "researcher", created.ID)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes/researcher/publish", nil, true)
	assert.Equal(t, http.StatusCreated, response.Code, response.Body.String())
}
