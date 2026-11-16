package api

import (
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireArtifactsContract is a valid v1 report contract that demands an
// artifact, the shape a Hero uses to promise a deliverable.
func requireArtifactsContract() *domain.HeroReportContract {
	return &domain.HeroReportContract{
		Version:          domain.HeroReportContractV1,
		SummaryMaxBytes:  512,
		RequireArtifacts: true,
	}
}

// A read-only Hero can never call publish_artifact (it is a mutation tool, so
// the rule above refuses it), so a report contract that requires an artifact is
// a contradiction: the Hero publishes, does the work, and then every Run fails
// with report_contract_violation. That must be refused where the Hero is
// published, not discovered at Run time.
func TestGlobalHeroPublishRejectsRequiredArtifactsOnReadOnlyHero(t *testing.T) {
	_, handler := heroServer(t)

	document := heroDocument(t)
	require.Equal(t, domain.HeroAuthorityReadOnly, document.Authority.Domain())
	require.NotContains(t, document.AllowedTools, "publish_artifact")
	document.ReportContract = requireArtifactsContract()
	createHero(t, handler, document)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes/researcher/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	body := response.Body.String()
	assert.Contains(t, body, "hero_publish_failed")
	assert.Contains(t, body, "researcher", "the refusal must name the Hero")
	assert.Contains(t, body, "requireArtifacts")
	assert.Contains(t, body, "read_only")
	assert.Contains(t, body, "publish_artifact", "the refusal must name the contradiction")

	// Nothing was published, so there is still no effective revision.
	response = request(t, handler, http.MethodGet, "/v1/global-heroes/researcher/resolved", nil, true)
	assert.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
}

// The rule targets the contradiction, not requireArtifacts: the same contract on
// a Hero that may publish artifacts still publishes.
func TestGlobalHeroPublishAcceptsRequiredArtifactsWhenTheHeroMayPublish(t *testing.T) {
	_, handler := heroServer(t)

	document := heroDocument(t)
	document.Authority = "mutation"
	document.PermissionCeiling = domain.PermissionAuto
	document.AllowedTools = []string{"read", "write", "publish_artifact"}
	document.ReportContract = requireArtifactsContract()
	createHero(t, handler, document)

	response := request(t, handler, http.MethodPost, "/v1/global-heroes/researcher/publish", nil, true)
	assert.Equal(t, http.StatusCreated, response.Code, response.Body.String())
}
