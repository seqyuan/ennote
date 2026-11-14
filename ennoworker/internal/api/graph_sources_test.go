package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
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
	assert.Empty(t, created.Definition.Tasks)
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
			"value": domain.FlowTask{Hero: "global/reviewer", Goal: "Align reads"},
		},
	}
	response = request(t, handler, http.MethodPatch, "/v1/graphs/rna-seq", patch, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated globalGraphDetail
	decodeData(t, response, &updated)
	assert.Contains(t, updated.Definition.Tasks, "align")

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

// TestGlobalGraphRunSurfacesBindingErrors pins that a missing binding or a
// revision pin mismatch is reported with its own code, not flattened to
// graph_run_failed.
func TestGlobalGraphRunSurfacesBindingErrors(t *testing.T) {
	for _, code := range []string{"graph_binding_required", "graph_binding_revision_mismatch"} {
		t.Run(code, func(t *testing.T) {
			ctx := context.Background()
			projects := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects")}
			project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Runtime Project", HostPath: t.TempDir()})
			require.NoError(t, err)
			projectSessions := sessionstore.NewManager(projects.Root, projects)
			t.Cleanup(func() { _ = projectSessions.Close() })
			session, err := projectSessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Runtime Session"})
			require.NoError(t, err)
			server := &Server{Token: "test-token", Sessions: &store.SessionRepo{Files: projectSessions}}
			server.GraphRuns = &stubGraphRunner{start: func(context.Context, string, string, int, string, map[string]any, map[string]any) (*domain.RunAgentFlow, error) {
				return nil, fmt.Errorf("%s: refused", code)
			}}
			handler := server.Handler()
			response := request(t, handler, http.MethodPost, "/v1/graphs/qc/runs", map[string]any{"sessionId": session.ID}, true)
			assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
			assert.Contains(t, response.Body.String(), code)
			assert.NotContains(t, response.Body.String(), "graph_run_failed")
		})
	}
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
	assert.Empty(t, current.Definition.Tasks)
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

	task := func() domain.FlowTask { return domain.FlowTask{Hero: "global/reviewer", Goal: "Do the work."} }

	patch(map[string]any{"expectedDigest": created.Digest, "task": map[string]any{"id": "aa", "value": task()}})
	patch(map[string]any{"expectedDigest": created.Digest, "task": map[string]any{"id": "bb", "value": task()}})
	patch(map[string]any{"expectedDigest": created.Digest, "dependencies": map[string]any{"taskId": "aa", "depends": []string{"bb"}}})

	updated := patch(map[string]any{"expectedDigest": created.Digest, "task": map[string]any{"id": "bb", "value": nil}})
	assert.NotContains(t, updated.Definition.Tasks, "bb")
	require.Contains(t, updated.Definition.Tasks, "aa")
	assert.NotContains(t, updated.Definition.Tasks["aa"].Depends, "bb")

	// The written graph.yaml must re-parse cleanly with the same pruned shape.
	contents, err := os.ReadFile(filepath.Join(home, "agents", "graphs", "rna-seq", "graph.yaml"))
	require.NoError(t, err)
	definition, err := agentflow.ParseDefinition(contents)
	require.NoError(t, err)
	assert.NotContains(t, definition.Tasks, "bb")
	require.Contains(t, definition.Tasks, "aa")
	assert.NotContains(t, definition.Tasks["aa"].Depends, "bb")
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
	assert.Empty(t, updated.Definition.Tasks)
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

// graphWriterHeroDocument is graphHeroDocument but with mutation authority, so a
// Graph referencing it is a writer for the flow validator's writes-scope checks.
func graphWriterHeroDocument(handle, modelRef string) *heroesource.Document {
	document := graphHeroDocument(handle, modelRef)
	document.Authority = heroesource.AuthorityWriteWorkspace
	document.AllowedTools = []string{"read", "write"}
	return document
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
		"task": map[string]any{"id": "align", "value": domain.FlowTask{Hero: heroRef, Goal: "Align reads"}},
	}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
}

// graphPublishFixture wires a published global "reviewer" Hero and a stable
// model so a definition-shaped Graph can publish.
func graphPublishFixture(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
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
	return server, handler, home
}

// A Graph publish must run the same validator as a flow publish: an unresolved
// Skill reference or a writes scope the scheduler cannot parse fails at publish,
// not at Run start.
func TestGlobalGraphPublishRejectsUnresolvedSkillAndBadWritesScope(t *testing.T) {
	server, handler, home := graphPublishFixture(t)
	server.SkillCatalog = func() map[string]string { return map[string]string{"bio-workspace": "digest"} }
	_, _, err := server.GlobalSources.CreateHero(graphWriterHeroDocument("writer", "provider/stable"))
	require.NoError(t, err)
	_, err = server.GlobalSources.PublishHeroRevision("writer")
	require.NoError(t, err)

	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "qc", "name": "QC"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalGraphDetail
	decodeData(t, response, &created)

	putTask := func(task domain.FlowTask) globalGraphDetail {
		t.Helper()
		response = request(t, handler, http.MethodPatch, "/v1/graphs/qc",
			map[string]any{"expectedDigest": created.Digest, "task": map[string]any{"id": "align", "value": task}}, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var updated globalGraphDetail
		decodeData(t, response, &updated)
		created = updated
		return updated
	}

	putTask(domain.FlowTask{Hero: "global/reviewer", Goal: "Align reads", Skills: []string{"global/missing-skill"}})
	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "flow_validation_failed")
	assert.Contains(t, response.Body.String(), "unknown skill")

	putTask(domain.FlowTask{Hero: "global/writer", Goal: "Align reads", Skills: []string{"global/bio-workspace"}, Writes: []string{"../escape.md"}})
	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "flow_validation_failed")
	assert.Contains(t, response.Body.String(), "writes scope")

	putTask(domain.FlowTask{Hero: "global/writer", Goal: "Align reads", Skills: []string{"global/bio-workspace"}, Writes: []string{"qc/**"}})
	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	_ = home
}

// A Worker without a model catalog cannot resolve Hero references, so publish
// answers 503 flow_validation_unavailable like the flow handler, not a 422 that
// blames the author.
func TestGlobalGraphPublishReportsMissingModelsAsUnavailable(t *testing.T) {
	server, handler, _ := setupGlobalGraphServer(t)
	require.Nil(t, server.Models)

	createHeroGraph(t, handler, "global/reviewer")

	response := request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "flow_validation_unavailable")
}

// A Graph without a name is refused at publish so the catalog never falls back
// to a raw id for an object created after this change.
func TestGlobalGraphPublishRequiresName(t *testing.T) {
	server, handler, home := graphPublishFixture(t)
	_ = server
	_ = home

	createHeroGraph(t, handler, "global/reviewer")

	response := request(t, handler, http.MethodGet, "/v1/graphs/qc", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var detail globalGraphDetail
	decodeData(t, response, &detail)
	response = request(t, handler, http.MethodPatch, "/v1/graphs/qc",
		map[string]any{"expectedDigest": detail.Digest, "name": ""}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "graph_name_required")
}

// A global Hero that exists only as a draft was never resolvable at Run time,
// so publishing a Graph that references it must fail up front.
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
	assert.Contains(t, response.Body.String(), "is not published")
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
// lets the Graph publish as a graph revision.
func TestGlobalGraphPublishSucceedsWithPublishedGlobalHero(t *testing.T) {
	server, handler, home := graphPublishFixture(t)
	_ = server
	_ = home

	createHeroGraph(t, handler, "global/reviewer")

	response := request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	assert.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var revision globalsource.Revision
	decodeData(t, response, &revision)
	assert.Equal(t, globalsource.SourceKindGraph, revision.SourceKind)
}

// Every Graph detail response reports latestVersion from the same published
// revisions, so a create, an update, a read and a publish cannot disagree.
func TestGlobalGraphResponsesReportLatestVersion(t *testing.T) {
	server, handler, home := graphPublishFixture(t)
	_ = server
	_ = home

	response := request(t, handler, http.MethodPost, "/v1/graphs", map[string]any{"id": "qc", "name": "QC"}, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created globalGraphDetail
	decodeData(t, response, &created)
	assert.Zero(t, created.LatestVersion, "a Graph without a revision has no latest version")

	response = request(t, handler, http.MethodPatch, "/v1/graphs/qc",
		map[string]any{"expectedDigest": created.Digest,
			"task": map[string]any{"id": "merge", "value": domain.FlowTask{Hero: "global/reviewer", Goal: "Merge results."}}}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var applied globalGraphDetail
	decodeData(t, response, &applied)
	assert.Zero(t, applied.LatestVersion)
	require.Contains(t, applied.Definition.Tasks, "merge")

	response = request(t, handler, http.MethodPost, "/v1/graphs/qc/publish", nil, true)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())

	response = request(t, handler, http.MethodGet, "/v1/graphs/qc", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var read globalGraphDetail
	decodeData(t, response, &read)
	assert.Equal(t, 1, read.LatestVersion)

	response = request(t, handler, http.MethodPatch, "/v1/graphs/qc",
		map[string]any{"expectedDigest": read.Digest, "name": "QC 2"}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var updated globalGraphDetail
	decodeData(t, response, &updated)
	assert.Equal(t, read.LatestVersion, updated.LatestVersion)
}
