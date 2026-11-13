package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupGlobalGraphServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	home := t.TempDir()
	server := &Server{Token: "test-token", GlobalSources: &globalsource.Store{HomeDir: home}}
	return server, server.Handler(), home
}

func TestGlobalGraphCatalogAndSemanticMutation(t *testing.T) {
	_, handler, home := setupGlobalGraphServer(t)

	response := request(t, handler, http.MethodGet, "/v1/graphs", nil, false)
	assert.Equal(t, http.StatusUnauthorized, response.Code)

	response = request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "rna-seq", "name": "RNA-seq"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalGraphDetail
	decodeData(t, response, &created)
	assert.Empty(t, created.Document.Tasks)
	assert.Equal(t, filepath.Join(home, "agents", "graphs", "rna-seq", "graph.yaml"), created.Path)

	response = request(t, handler, http.MethodGet, "/v1/graphs", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var summaries []globalGraphSummary
	decodeData(t, response, &summaries)
	require.Len(t, summaries, 1)
	assert.Equal(t, "rna-seq", summaries[0].ID)

	patch := map[string]any{
		"expectedDigest": created.Digest,
		"task": map[string]any{
			"id":    "align",
			"value": graphsource.Task{Name: "Align", Model: "anthropic/claude-sonnet-4", Thinking: "high", Goal: "Align reads"},
		},
	}
	response = request(t, handler, http.MethodPatch, "/v1/graphs/rna-seq", patch, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated globalGraphDetail
	decodeData(t, response, &updated)
	assert.Contains(t, updated.Document.Tasks, "align")
	assert.Contains(t, updated.Document.Graph, "align")

	response = request(t, handler, http.MethodPatch, "/v1/graphs/rna-seq", patch, true)
	assert.Equal(t, http.StatusConflict, response.Code)
	assert.Contains(t, response.Body.String(), "source_digest_conflict")
}

func TestGlobalGraphRunDerivesProjectFromSessionWithoutBinding(t *testing.T) {
	ctx := context.Background()
	projects := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Runtime Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	projectSessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { _ = projectSessions.Close() })
	session, err := projectSessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Runtime Session"})
	require.NoError(t, err)
	server := &Server{Token: "test-token", Sessions: &store.SessionRepo{Files: projectSessions}}
	var capturedProject, capturedSession, capturedVersion string
	server.GraphRuns = &stubGraphRunner{
		start: func(ctx context.Context, projectID, graphID string, version int, sessionID string, _, _ map[string]any) (*domain.RunAgentFlow, error) {
			capturedProject, capturedVersion, capturedSession = projectID, graphID+"@v000001", sessionID
			return &domain.RunAgentFlow{RunID: "run-global", ProjectID: projectID, SessionID: sessionID, FlowVersionID: graphID + "@v000001"}, nil
		},
	}

	handler := server.Handler()
	response := request(t, handler, http.MethodPost, "/v1/graphs/rna-seq/runs", map[string]any{"sessionId": session.ID}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	assert.Equal(t, project.ID, capturedProject)
	assert.Equal(t, session.ID, capturedSession)
	assert.Equal(t, "rna-seq@v000001", capturedVersion)
}

type stubGraphRunner struct {
	start func(ctx context.Context, projectID, graphID string, version int, sessionID string, inputs, vars map[string]any) (*domain.RunAgentFlow, error)
}

func (s *stubGraphRunner) Start(ctx context.Context, projectID, graphID string, version int, sessionID string, inputs, vars map[string]any) (*domain.RunAgentFlow, error) {
	if s.start == nil {
		return nil, fmt.Errorf("stub Start is not wired")
	}
	return s.start(ctx, projectID, graphID, version, sessionID, inputs, vars)
}

func TestGlobalGraphMutationRejectsUnknownDependencyWithoutWriting(t *testing.T) {
	_, handler, _ := setupGlobalGraphServer(t)
	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "rna-seq", "name": "RNA-seq"}, true)
	require.Equal(t, http.StatusCreated, response.Code)
	var created globalGraphDetail
	decodeData(t, response, &created)

	body := map[string]any{"expectedDigest": created.Digest, "dependencies": map[string]any{"taskId": "missing", "depends": []string{}}}
	response = request(t, handler, http.MethodPatch, "/v1/graphs/rna-seq", body, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)

	response = request(t, handler, http.MethodGet, "/v1/graphs/rna-seq", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var current globalGraphDetail
	decodeData(t, response, &current)
	assert.Equal(t, created.Digest, current.Digest)
	assert.Empty(t, current.Document.Tasks)
}

func TestGlobalGraphDeleteTaskPrunesDependencies(t *testing.T) {
	_, handler, home := setupGlobalGraphServer(t)

	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "rna-seq", "name": "RNA-seq"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalGraphDetail
	decodeData(t, response, &created)

	patch := func(patch map[string]any) globalGraphDetail {
		t.Helper()
		response = request(t, handler, http.MethodPatch, "/v1/graphs/rna-seq", patch, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var updated globalGraphDetail
		decodeData(t, response, &updated)
		created = updated
		return updated
	}

	task := func(id, name string) map[string]any {
		return map[string]any{"name": name, "model": "anthropic/claude-sonnet-4", "goal": "Do the work."}
	}

	patch(map[string]any{"expectedDigest": created.Digest, "task": map[string]any{"id": "aa", "value": task("aa", "A")}})
	patch(map[string]any{"expectedDigest": created.Digest, "task": map[string]any{"id": "bb", "value": task("bb", "B")}})
	patch(map[string]any{"expectedDigest": created.Digest, "dependencies": map[string]any{"taskId": "aa", "depends": []string{"bb"}}})

	updated := patch(map[string]any{"expectedDigest": created.Digest, "task": map[string]any{"id": "bb", "value": nil}})
	assert.NotContains(t, updated.Document.Tasks, "bb")
	assert.NotContains(t, updated.Document.Graph, "bb")
	require.Contains(t, updated.Document.Graph, "aa")
	assert.Empty(t, updated.Document.Graph["aa"])
	assert.NotNil(t, updated.Document.Graph["aa"], "the mutation response must return an empty dependency list, not null")

	// The written graph.yaml must re-parse cleanly with the same pruned shape.
	contents, err := os.ReadFile(filepath.Join(home, "agents", "graphs", "rna-seq", "graph.yaml"))
	require.NoError(t, err)
	document, err := graphsource.Parse(contents)
	require.NoError(t, err)
	assert.NotContains(t, document.Tasks, "bb")
	assert.NotContains(t, document.Graph, "bb")
	require.Contains(t, document.Graph, "aa")
	assert.Empty(t, document.Graph["aa"])
}

func TestGlobalGraphDeleteMissingTaskKeepsDocument(t *testing.T) {
	_, handler, _ := setupGlobalGraphServer(t)
	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "rna-seq", "name": "RNA-seq"}, true)
	require.Equal(t, http.StatusCreated, response.Code)
	var created globalGraphDetail
	decodeData(t, response, &created)

	response = request(t, handler, http.MethodPatch, "/v1/graphs/rna-seq", map[string]any{
		"expectedDigest": created.Digest,
		"task":           map[string]any{"id": "missing", "value": nil},
	}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated globalGraphDetail
	decodeData(t, response, &updated)
	assert.Equal(t, created.Digest, updated.Digest)
	assert.Empty(t, updated.Document.Tasks)
	assert.Empty(t, updated.Document.Graph)
}

// A Graph is a draft format, but publishing one must not be a way around the
// checks Flow publish already performs: an unresolved Skill reference or a writes
// scope the scheduler cannot parse fails the Run at freeze time, so the draft is
// rejected at publish instead of at the moment someone starts it.
func TestGlobalGraphPublishRejectsUnresolvedSkillAndBadWritesScope(t *testing.T) {
	server, handler, _ := setupGlobalGraphServer(t)
	server.SkillCatalog = func() map[string]string { return map[string]string{"bio-workspace": "digest"} }

	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "qc", "name": "QC"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalGraphDetail
	decodeData(t, response, &created)

	putTask := func(task map[string]any) globalGraphDetail {
		t.Helper()
		response = request(t, handler, http.MethodPatch, "/v1/graphs/qc",
			map[string]any{"expectedDigest": created.Digest, "task": map[string]any{"id": "align", "value": task}}, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var updated globalGraphDetail
		decodeData(t, response, &updated)
		created = updated
		return updated
	}

	putTask(map[string]any{
		"name": "Align", "model": "anthropic/claude-sonnet-4", "goal": "Align reads",
		"skills": []string{"global/missing-skill"},
	})
	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "unknown skill")

	putTask(map[string]any{
		"name": "Align", "model": "anthropic/claude-sonnet-4", "goal": "Align reads",
		"skills": []string{"global/bio-workspace"}, "writes": []string{"../escape.md"},
	})
	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "writes scope")

	putTask(map[string]any{
		"name": "Align", "model": "anthropic/claude-sonnet-4", "goal": "Align reads",
		"skills": []string{"global/bio-workspace"}, "writes": []string{"qc/**"},
	})
	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusCreated, response.Code, response.Body.String())
}

// A Worker without a Skill catalog cannot decide whether a Skill reference is
// real. That is the Worker's missing dependency, not the author's mistake, so it
// must answer 503 like Flow publish does rather than 422 "unknown skill".
func TestGlobalGraphPublishReportsMissingSkillCatalogAsUnavailable(t *testing.T) {
	server, handler, _ := setupGlobalGraphServer(t)
	require.Nil(t, server.SkillCatalog)

	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "qc", "name": "QC"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalGraphDetail
	decodeData(t, response, &created)

	response = request(t, handler, http.MethodPatch, "/v1/graphs/qc", map[string]any{
		"expectedDigest": created.Digest,
		"task": map[string]any{"id": "align", "value": map[string]any{
			"name": "Align", "model": "anthropic/claude-sonnet-4", "goal": "Align reads",
			"skills": []string{"global/bio-workspace"},
		}},
	}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	decodeData(t, response, &created)

	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "graph_validation_unavailable")

	// A local/<id> Skill is read from the Graph's own directory, so the missing
	// catalog says nothing about it: the publish is judged on the document, not on
	// a dependency this reference never needed.
	response = request(t, handler, http.MethodPatch, "/v1/graphs/qc", map[string]any{
		"expectedDigest": created.Digest,
		"task": map[string]any{"id": "align", "value": map[string]any{
			"name": "Align", "model": "anthropic/claude-sonnet-4", "goal": "Align reads",
			"skills": []string{"local/missing-local-skill"},
		}},
	}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "unknown skill")
	assert.NotContains(t, response.Body.String(), "graph_validation_unavailable")
}

// graphHeroCatalog wires a file-backed Model catalog into a Graph test server
// and returns the underlying store so a test can add or remove models.
func graphHeroCatalog(t *testing.T, server *Server, home string) *fileconfig.ModelStore {
	t.Helper()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(context.Background(), fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-graph-secret",
	})
	require.NoError(t, err)
	server.Models = &store.ModelRepo{Files: models}
	return models
}

// graphHeroDocument is a minimal portable Hero whose only model is modelRef.
func graphHeroDocument(handle, modelRef string) *heroesource.Document {
	return &heroesource.Document{
		SchemaVersion: 1, Handle: handle, Name: "Hero",
		Model:  heroesource.ModelBinding{Ref: modelRef, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 2, MaxToolCalls: 4,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 4, Prompt: "Do the work.",
	}
}

// createHeroGraph builds a one-Task Graph whose Task runs through heroRef, so a
// later publish exercises the Hero model-reference check.
func createHeroGraph(t *testing.T, handler http.Handler, heroRef string) {
	t.Helper()
	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "qc", "name": "QC"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalGraphDetail
	decodeData(t, response, &created)

	response = request(t, handler, http.MethodPatch, "/v1/graphs/qc", map[string]any{
		"expectedDigest": created.Digest,
		"task": map[string]any{"id": "align", "value": map[string]any{
			"name": "Align", "hero": heroRef, "goal": "Align reads",
		}},
	}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

// A global Hero that exists only as a draft was never resolvable at Run time
// (graphrun reads the published revision), so publishing a Graph that references
// it must fail up front with a message that names the difference.
func TestGlobalGraphPublishRejectsUnpublishedGlobalHero(t *testing.T) {
	server, handler, home := setupGlobalGraphServer(t)
	models := graphHeroCatalog(t, server, home)
	_, err := models.CreateModel(context.Background(), fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "stable", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault},
	})
	require.NoError(t, err)

	_, _, err = server.GlobalSources.CreateHero(graphHeroDocument("reviewer", "provider/stable"))
	require.NoError(t, err)

	createHeroGraph(t, handler, "global/reviewer")

	response := request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "has no published revision")
	assert.Contains(t, response.Body.String(), "reviewer")
	assert.Contains(t, response.Body.String(), "align")
}

// A published Hero is the document a Run actually resolves, so publish-time model
// validation must read the published revision, not the draft. When the draft has
// moved to a still-valid model but the published revision points at a model that
// no longer resolves, publishing must fail.
func TestGlobalGraphPublishValidatesPublishedHeroRevisionNotDraft(t *testing.T) {
	server, handler, home := setupGlobalGraphServer(t)
	models := graphHeroCatalog(t, server, home)
	for _, name := range []string{"published", "draft"} {
		_, err := models.CreateModel(context.Background(), fileconfig.CreateModelInput{
			ProviderID: "provider", ModelName: name, ContextWindow: 32768, MaxOutputTokens: 4096,
			SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
			SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault},
		})
		require.NoError(t, err)
	}

	_, heroDigest, err := server.GlobalSources.CreateHero(graphHeroDocument("reviewer", "provider/published"))
	require.NoError(t, err)
	_, err = server.GlobalSources.PublishHeroRevision("reviewer")
	require.NoError(t, err)

	// Move only the draft to a different, still-resolvable model.
	_, _, err = server.GlobalSources.UpdateHero("reviewer", heroDigest, func(document *heroesource.Document) error {
		document.Model.Ref = "provider/draft"
		return nil
	})
	require.NoError(t, err)

	// The published revision still references provider/published, which is gone.
	err = models.DeleteModel(context.Background(), "provider/published")
	require.NoError(t, err)

	createHeroGraph(t, handler, "global/reviewer")

	response := request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "provider/published")
}

// The normal path stays intact: a published global Hero with a resolvable model
// lets the Graph publish.
func TestGlobalGraphPublishSucceedsWithPublishedGlobalHero(t *testing.T) {
	server, handler, home := setupGlobalGraphServer(t)
	models := graphHeroCatalog(t, server, home)
	_, err := models.CreateModel(context.Background(), fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "stable", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault},
	})
	require.NoError(t, err)

	_, _, err = server.GlobalSources.CreateHero(graphHeroDocument("reviewer", "provider/stable"))
	require.NoError(t, err)
	_, err = server.GlobalSources.PublishHeroRevision("reviewer")
	require.NoError(t, err)

	createHeroGraph(t, handler, "global/reviewer")

	response := request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusCreated, response.Code, response.Body.String())
}
