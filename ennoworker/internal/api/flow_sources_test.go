package api

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
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
	role := &rolesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer",
		Model:  rolesource.ModelBinding{Ref: "provider/flow-model", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: rolesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 2, MaxToolCalls: 4,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 4, Prompt: "Review.",
	}
	_, _, err = sources.CreateRole(role)
	require.NoError(t, err)
	_, err = sources.PublishRoleRevision("reviewer")
	require.NoError(t, err)
	server := &Server{
		Token: "test-token", GlobalSources: sources, Models: &store.ModelRepo{Files: models},
		CheckAllowlistAllowsAll: true,
	}
	return server, server.Handler(), sources
}

func flowWithRole(id string) *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: id, Budget: domain.FlowBudget{MaxTotalTokens: 20000},
		Tasks: map[string]domain.FlowTask{
			"review": {Type: domain.FlowTaskRole, Role: "global/reviewer", Goal: "Review",
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"done": {Type: domain.FlowTaskRole, Role: "global/reviewer", Depends: []string{"review"},
				Terminal: &domain.FlowTerminal{Status: "success"}},
		},
	}
}

func TestGlobalFlowPublishAndVersions(t *testing.T) {
	_, handler, sources := flowAPIFixture(t)
	_, err := sources.CreateFlowFromDefinition(flowWithRole("review-flow"))
	require.NoError(t, err)

	list := request(t, handler, http.MethodGet, "/v1/flows", nil, true)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var summaries []globalFlowSummary
	decodeData(t, list, &summaries)
	require.Len(t, summaries, 1)
	assert.Equal(t, "review-flow", summaries[0].ID)
	assert.Equal(t, 2, summaries[0].TaskCount)

	detail := request(t, handler, http.MethodGet, "/v1/flows/review-flow", nil, true)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())

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
	definition := flowWithRole("bad-check")
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

func TestFlowCatalogDoesNotLeakIntoGraphList(t *testing.T) {
	_, handler, sources := flowAPIFixture(t)
	_, _, err := sources.CreateGraph("graph-one", "Graph One")
	require.NoError(t, err)
	_, err = sources.CreateFlowFromDefinition(flowWithRole("flow-one"))
	require.NoError(t, err)

	response := request(t, handler, http.MethodGet, "/v1/graphs", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var summaries []globalGraphSummary
	decodeData(t, response, &summaries)
	require.Len(t, summaries, 1)
	assert.Equal(t, "graph-one", summaries[0].ID)
}
