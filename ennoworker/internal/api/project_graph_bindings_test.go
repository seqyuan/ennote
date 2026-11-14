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
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func graphAPIFixture(t *testing.T) (*Server, http.Handler, *globalsource.Store) {
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

func graphWithHero(id string) *domain.FlowDefinition {
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

func TestProjectGraphBindingEnablesRevision(t *testing.T) {
	server, handler, sources := graphAPIFixture(t)
	projects := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects")}
	server.Projects = &store.ProjectRepo{Files: projects}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Project", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	_, err = sources.CreateGraphFromDefinition(graphWithHero("review-graph"))
	require.NoError(t, err)
	revision, err := sources.PublishGraphRevision("review-graph")
	require.NoError(t, err)

	missing := request(t, handler, http.MethodGet,
		"/v1/projects/"+project.ID+"/graph-bindings", nil, true)
	require.Equal(t, http.StatusOK, missing.Code, missing.Body.String())
	var empty []projectstore.GraphBinding
	decodeData(t, missing, &empty)
	assert.Empty(t, empty)

	invalid := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/review-graph",
		map[string]any{"sourceKind": "graph", "revision": "v999999", "enabled": true}, true)
	assert.Equal(t, http.StatusUnprocessableEntity, invalid.Code)
	assert.Contains(t, invalid.Body.String(), "source_revision_not_found")

	enabled := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/review-graph",
		map[string]any{"sourceKind": "graph", "revision": revision.ID(), "enabled": true}, true)
	require.Equal(t, http.StatusOK, enabled.Code, enabled.Body.String())
	var binding projectstore.GraphBinding
	decodeData(t, enabled, &binding)
	assert.True(t, binding.Enabled)
	assert.Equal(t, revision.ID(), binding.Revision)

	// Enabling with no revision pins the latest published revision, never a
	// moving "latest" reference.
	pinned := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/review-graph",
		map[string]any{"sourceKind": "graph", "enabled": true}, true)
	require.Equal(t, http.StatusOK, pinned.Code, pinned.Body.String())
	var pinnedBinding projectstore.GraphBinding
	decodeData(t, pinned, &pinnedBinding)
	assert.Equal(t, revision.ID(), pinnedBinding.Revision)

	removed := request(t, handler, http.MethodDelete,
		"/v1/projects/"+project.ID+"/graph-bindings/review-graph?sourceKind=graph", nil, true)
	assert.Equal(t, http.StatusNoContent, removed.Code)

	_, err = projects.FindGraphBinding(context.Background(), project.ID, globalsource.SourceKindGraph, "review-graph")
	require.Error(t, err)
}

func TestProjectGraphBindingRejectsUnknownSource(t *testing.T) {
	server, handler, _ := graphAPIFixture(t)
	projects := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects")}
	server.Projects = &store.ProjectRepo{Files: projects}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Project", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	response := request(t, handler, http.MethodPut,
		"/v1/projects/"+project.ID+"/graph-bindings/missing",
		map[string]any{"sourceKind": "graph", "enabled": true}, true)
	assert.Equal(t, http.StatusNotFound, response.Code)
}
