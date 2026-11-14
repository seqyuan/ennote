package api

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func flowAPIFixture(t *testing.T) (*Server, http.Handler, *globalsource.Store) {
	t.Helper()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(context.Background(), fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-flow-secret",
	})
	require.NoError(t, err)
	_, err = models.CreateModel(context.Background(), fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "flow-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	sources := &globalsource.Store{HomeDir: home}
	hero := &heroesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer",
		Model:  heroesource.ModelBinding{Ref: "provider/flow-model", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 2, MaxToolCalls: 4,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 4, Prompt: "Review.",
	}
	_, _, err = sources.CreateHero(hero)
	require.NoError(t, err)
	_, err = sources.PublishHeroRevision("reviewer")
	require.NoError(t, err)
	server := &Server{
		Token: "test-token", GlobalSources: sources, Models: &store.ModelRepo{Files: models},
		CheckAllowlistAllowsAll: true,
	}
	return server, server.Handler(), sources
}

func flowWithHero(id string) *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: id, Budget: domain.FlowBudget{MaxTotalTokens: 20000},
		Tasks: map[string]domain.FlowTask{
			"review": {Type: domain.FlowTaskHero, Hero: "global/reviewer", Goal: "Review",
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"done": {Type: domain.FlowTaskHero, Hero: "global/reviewer", Depends: []string{"review"},
				Terminal: &domain.FlowTerminal{Status: "success"}},
		},
	}
}

// The deprecated /v1/flows surface delegates to /v1/graphs: listing is the one
// catalog, publishing a flow.yaml through the delegate keeps sourceKind=flow and
// lands the same revision the graph route would, and running is the one starter.
func TestGlobalFlowPublishAndVersions(t *testing.T) {
	_, handler, sources := flowAPIFixture(t)
	_, err := sources.CreateFlowFromDefinition(flowWithHero("review-flow"))
	require.NoError(t, err)

	list := request(t, handler, http.MethodGet, "/v1/flows", nil, true)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var summaries []globalGraphSummary
	decodeData(t, list, &summaries)
	require.Len(t, summaries, 1)
	assert.Equal(t, "review-flow", summaries[0].ID)
	assert.Equal(t, globalsource.LegacyFileFlow, summaries[0].LegacyFile)

	detail := request(t, handler, http.MethodGet, "/v1/flows/review-flow", nil, true)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	var flowDetail globalGraphDetail
	decodeData(t, detail, &flowDetail)
	require.NotNil(t, flowDetail.Definition)
	assert.Equal(t, "review-flow", flowDetail.Definition.ID)

	published := request(t, handler, http.MethodPost, "/v1/flows/review-flow/publish", nil, true)
	require.Equal(t, http.StatusCreated, published.Code, published.Body.String())
	var revision struct {
		Version    int    `json:"version"`
		Digest     string `json:"digest"`
		SourceKind string `json:"sourceKind"`
	}
	decodeData(t, published, &revision)
	assert.Equal(t, 1, revision.Version)
	assert.Equal(t, globalsource.SourceKindFlow, revision.SourceKind)
	assert.Contains(t, revision.Digest, "sha256:")

	// The graph route on the same object delegates to the same publish and dedups.
	again := request(t, handler, http.MethodPost, "/v1/graphs/review-flow/publish", nil, true)
	require.Equal(t, http.StatusCreated, again.Code, again.Body.String())
	var againRevision struct {
		Version int `json:"version"`
	}
	decodeData(t, again, &againRevision)
	assert.Equal(t, revision.Version, againRevision.Version)

	versions := request(t, handler, http.MethodGet, "/v1/flows/review-flow/versions", nil, true)
	require.Equal(t, http.StatusOK, versions.Code, versions.Body.String())
	var revisions []map[string]any
	decodeData(t, versions, &revisions)
	require.Len(t, revisions, 1)

	missing := request(t, handler, http.MethodGet, "/v1/flows/missing", nil, true)
	assert.Equal(t, http.StatusNotFound, missing.Code)

	unavailable := request(t, handler, http.MethodPost, "/v1/flows/review-flow/runs",
		map[string]any{"sessionId": "s"}, true)
	assert.Equal(t, http.StatusServiceUnavailable, unavailable.Code)
}

func TestGlobalFlowPublishRejectsInvalidDefinition(t *testing.T) {
	_, handler, sources := flowAPIFixture(t)
	definition := flowWithHero("bad-check")
	definition.Budget = domain.FlowBudget{}
	definition.Tasks = map[string]domain.FlowTask{
		"gate": {Type: domain.FlowTaskCheck, Command: "rm -rf /"},
	}
	_, err := sources.CreateFlowFromDefinition(definition)
	require.NoError(t, err)

	response := request(t, handler, http.MethodPost, "/v1/flows/bad-check/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)
	assert.Contains(t, response.Body.String(), "flow_validation_failed")
	assert.Contains(t, response.Body.String(), "flow_budget_required")
}

// The unified /v1/graphs catalog lists both a graph.yaml and a legacy flow.yaml
// object; the flow row is labelled by legacyFile.
func TestFlowCatalogAppearsInUnifiedGraphList(t *testing.T) {
	_, handler, sources := flowAPIFixture(t)
	_, _, err := sources.CreateGraphDefinition("graph-one", "Graph One")
	require.NoError(t, err)
	_, err = sources.CreateFlowFromDefinition(flowWithHero("flow-one"))
	require.NoError(t, err)

	response := request(t, handler, http.MethodGet, "/v1/graphs", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var summaries []globalGraphSummary
	decodeData(t, response, &summaries)
	require.Len(t, summaries, 2)

	byID := map[string]globalGraphSummary{}
	for _, summary := range summaries {
		byID[summary.ID] = summary
	}
	require.Contains(t, byID, "graph-one")
	assert.Empty(t, byID["graph-one"].LegacyFile)
	require.Contains(t, byID, "flow-one")
	assert.Equal(t, globalsource.LegacyFileFlow, byID["flow-one"].LegacyFile)
}
