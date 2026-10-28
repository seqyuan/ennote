package api

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/packinstall"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// packInstallFixture is a real Worker home: a file-native model catalog so a
// pack Role model ref resolves, the source store and user skills root the
// installer writes, a Session manager for the active-Run check, and HostHome
// pinned to the same home so the path confinement matches production.
type packInstallFixture struct {
	home    string
	sources *globalsource.Store
	service *packinstall.Service
	server  *Server
	handler http.Handler
	modelID string
}

func newPackInstallFixture(t *testing.T) *packInstallFixture {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	t.Setenv("ENNOTE_HOME", home)
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-pack-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "pack-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)

	sources := &globalsource.Store{HomeDir: home}
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { _ = sessions.Close() })
	service := &packinstall.Service{
		HomeDir: home, Sources: sources, Models: &store.ModelRepo{Files: models},
		SkillsRoot: filepath.Join(home, "skills"), Sessions: sessions,
	}
	server := &Server{Token: "test-token", HostHome: home, Packs: service}
	return &packInstallFixture{
		home: home, sources: sources, service: service, server: server,
		handler: server.Handler(), modelID: model.ID,
	}
}

// installPackFiles is the review fixture pack with the Role model ref bound to
// this fixture's model catalog, so the install has exactly one blocker source:
// whatever the test introduces.
func installPackFiles(modelRef string) map[string]string {
	files := packFixtureFiles()
	files["roles/qc-reviewer/role.md"] = `---
schemaVersion: 1
handle: qc-reviewer
name: QC Reviewer
model:
  ref: ` + modelRef + `
authority: read_only
---
Review the QC metrics.
`
	return files
}

func (f *packInstallFixture) writePack(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	return writePackFixture(t, filepath.Join(f.home, "sources", name), files)
}

func (f *packInstallFixture) install(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, f.handler, http.MethodPost, "/v1/packs/install", body, true)
}

func (f *packInstallFixture) requirePacksDirAbsent(t *testing.T) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(f.home, "packs"))
	assert.True(t, os.IsNotExist(err), "expected no pack state below ENNOTE_HOME, got %v", err)
}

// snapshotTree lists every entry below root so a dry run can be proven to have
// written nothing at all.
func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	entries := make([]string, 0, 16)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, relative)
		return nil
	}))
	sort.Strings(entries)
	return entries
}

func TestPackInstallRequiresAuth(t *testing.T) {
	fixture := newPackInstallFixture(t)
	source := fixture.writePack(t, "cell-qc", installPackFiles(fixture.modelID))

	body := map[string]any{"source": map[string]any{"kind": "path", "path": source}}
	for _, call := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/v1/packs/install", body},
		{http.MethodGet, "/v1/packs", nil},
		{http.MethodDelete, "/v1/packs/seqyuan/cell-qc-review", nil},
	} {
		response := request(t, fixture.handler, call.method, call.path, call.body, false)
		assert.Equal(t, http.StatusUnauthorized, response.Code, call.method+" "+call.path)
	}
	fixture.requirePacksDirAbsent(t)
}

func TestPackEndpointsAreUnavailableWithoutAnInstaller(t *testing.T) {
	home := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	server := &Server{Token: "test-token", HostHome: home}
	handler := server.Handler()
	require.Nil(t, server.Packs)

	install := request(t, handler, http.MethodPost, "/v1/packs/install",
		map[string]any{"source": map[string]any{"kind": "path", "path": home}}, true)
	assert.Equal(t, http.StatusServiceUnavailable, install.Code)
	assert.Contains(t, install.Body.String(), "pack_install_unavailable")

	list := request(t, handler, http.MethodGet, "/v1/packs", nil, true)
	assert.Equal(t, http.StatusServiceUnavailable, list.Code)
	assert.Contains(t, list.Body.String(), "pack_install_unavailable")

	remove := request(t, handler, http.MethodDelete, "/v1/packs/seqyuan/cell-qc-review", nil, true)
	assert.Equal(t, http.StatusServiceUnavailable, remove.Code)
	assert.Contains(t, remove.Body.String(), "pack_install_unavailable")
}

func TestPackInstallRejectsUnconfinedSources(t *testing.T) {
	fixture := newPackInstallFixture(t)

	relative := fixture.install(t, map[string]any{"source": map[string]any{"kind": "path", "path": "packs/cell-qc"}})
	assert.Equal(t, http.StatusBadRequest, relative.Code)
	assert.Contains(t, relative.Body.String(), "pack_path_invalid")

	outside := installPackFiles(fixture.modelID)
	outsideRoot := writePackFixture(t, t.TempDir(), outside)
	require.NotContains(t, outsideRoot, fixture.home)
	forbidden := fixture.install(t, map[string]any{"source": map[string]any{"kind": "path", "path": outsideRoot}})
	assert.Equal(t, http.StatusForbidden, forbidden.Code)
	assert.Contains(t, forbidden.Body.String(), "pack_path_forbidden")

	unsupported := fixture.install(t, map[string]any{"source": map[string]any{"kind": "url", "path": "https://example.com/pack.enpack"}})
	assert.Equal(t, http.StatusBadRequest, unsupported.Code)
	assert.Contains(t, unsupported.Body.String(), "pack_format_unsupported")

	missing := fixture.install(t, map[string]any{"source": map[string]any{"kind": "path", "path": filepath.Join(fixture.home, "nope.enpack")}})
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Contains(t, missing.Body.String(), "pack_not_found")
	fixture.requirePacksDirAbsent(t)
}

func TestPackInstallDryRunWritesNothing(t *testing.T) {
	fixture := newPackInstallFixture(t)
	source := fixture.writePack(t, "cell-qc", installPackFiles(fixture.modelID))
	before := snapshotTree(t, fixture.home)

	response := fixture.install(t, map[string]any{
		"source": map[string]any{"kind": "path", "path": source},
		"dryRun": true,
	})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var plan packinstall.Plan
	decodeData(t, response, &plan)
	assert.Equal(t, packinstall.PlanVersion, plan.PlanVersion)
	assert.Equal(t, "seqyuan/cell-qc-review", plan.Pack.ID)
	require.NotEmpty(t, plan.Actions)
	assert.Empty(t, plan.Blockers)
	assert.Equal(t, packinstall.CommitPointStateFile, plan.CommitPoint)
	assert.False(t, plan.WillPublishFlowRevision)
	assert.Nil(t, plan.State)
	for _, action := range plan.Actions {
		assert.Equal(t, packinstall.ActionCreate, action.Action, action.Kind+" "+action.StoreID)
	}
	graph, ok := findPlanAction(plan.Actions, packinstall.ActionKindGraph, "qc-review")
	require.True(t, ok)
	assert.Equal(t, packinstall.ActionCreate, graph.Action)

	// A dry run must not create staging, state, store objects, or a Skill.
	assert.Equal(t, before, snapshotTree(t, fixture.home))
	fixture.requirePacksDirAbsent(t)
}

func TestPackInstallCreatesDraftAndState(t *testing.T) {
	fixture := newPackInstallFixture(t)
	source := fixture.writePack(t, "cell-qc", installPackFiles(fixture.modelID))

	response := fixture.install(t, map[string]any{"source": map[string]any{"kind": "path", "path": source}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var plan packinstall.Plan
	decodeData(t, response, &plan)
	assert.Empty(t, plan.Blockers)
	assert.Empty(t, plan.Reinstall)
	require.NotNil(t, plan.State, "a real install reports the committed state")
	state := plan.State
	assert.Equal(t, packinstall.InstallStateDraft, state.InstallState)
	assert.Equal(t, "seqyuan/cell-qc-review", state.Pack.ID)
	assert.Equal(t, source, state.Source.Path)
	assert.False(t, state.Signed)
	require.Len(t, state.Flows, 1)
	assert.Equal(t, "qc-review", state.Flows[0].StoreID)
	// The default-disabled guarantee: the installer never publishes a revision.
	assert.Nil(t, state.Flows[0].PublishedRevision)
	require.Len(t, state.Roles, 1)
	assert.Equal(t, "qc-review/qc-reviewer", state.Roles[0].StoreID)
	assert.Empty(t, state.Roles[0].Revision)
	require.Len(t, state.Skills, 1)
	assert.Equal(t, "cell-qc-tables", state.Skills[0].StoreID)

	// The Graph exists as an ordinary draft and has no published revision.
	_, _, err := fixture.sources.ReadGraph("qc-review")
	require.NoError(t, err)
	revisions, err := fixture.sources.ListGraphRevisions("qc-review")
	require.NoError(t, err)
	assert.Empty(t, revisions)
	_, err = os.Stat(filepath.Join(fixture.home, "skills", "cell-qc-tables", "SKILL.md"))
	require.NoError(t, err)

	// State file and manifest copy are the provenance record.
	statePath := filepath.Join(fixture.home, "packs", "state", "seqyuan", "cell-qc-review.json")
	_, err = os.Stat(statePath)
	require.NoError(t, err)
	manifestCopy := filepath.Join(fixture.home, "packs", "state", "seqyuan", "cell-qc-review", "manifest.yaml")
	copied, err := os.ReadFile(manifestCopy)
	require.NoError(t, err)
	original, err := os.ReadFile(filepath.Join(source, "pack.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(original), string(copied))
	// Staging is cleaned up after a committed install: the root may exist, but no
	// transaction directory survives.
	staged, err := os.ReadDir(filepath.Join(fixture.home, "packs", "staging"))
	if err == nil {
		assert.Empty(t, staged)
	} else {
		assert.True(t, os.IsNotExist(err), "unexpected staging read error: %v", err)
	}

	// Re-planning the same bytes is an idempotent no-op, not a conflict.
	again := fixture.install(t, map[string]any{
		"source": map[string]any{"kind": "path", "path": source},
		"dryRun": true,
	})
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	var replan packinstall.Plan
	decodeData(t, again, &replan)
	assert.Equal(t, packinstall.ReinstallNoop, replan.Reinstall)
	assert.Empty(t, replan.Blockers)

	// A real reinstall is also a no-op and keeps the committed state.
	reinstall := fixture.install(t, map[string]any{"source": map[string]any{"kind": "path", "path": source}})
	require.Equal(t, http.StatusOK, reinstall.Code, reinstall.Body.String())
	var reinstalled packinstall.Plan
	decodeData(t, reinstall, &reinstalled)
	assert.Equal(t, packinstall.ReinstallNoop, reinstalled.Reinstall)
	assert.Equal(t, state.InstalledAt, reinstalled.State.InstalledAt)
}

func TestPackInstallRefusesFullFlowAndMCPFormat(t *testing.T) {
	fixture := newPackInstallFixture(t)
	flowPack := map[string]string{
		"pack.yaml": `schema_version: 1
id: seqyuan/qc-flow
name: QC Flow
version: 0.1.0
flows:
  - id: qc-review
    path: flows/qc-review/flow.yaml
    format: flow
`,
		"flows/qc-review/flow.yaml": `schemaVersion: 1
id: qc-review
budget:
  max_total_tokens: 1000
tasks:
  load:
    role: qc-reviewer
    goal: Load the samplesheet
`,
	}
	source := fixture.writePack(t, "flow-only", flowPack)

	response := fixture.install(t, map[string]any{"source": map[string]any{"kind": "path", "path": source}})
	assert.Equal(t, http.StatusUnprocessableEntity, response.Code)
	assert.Contains(t, response.Body.String(), packinstall.CodeFlowFormatUnsupported)
	fixture.requirePacksDirAbsent(t)

	// A dry run reports the same blocker instead of failing the request.
	before := snapshotTree(t, fixture.home)
	dryRun := fixture.install(t, map[string]any{
		"source": map[string]any{"kind": "path", "path": source},
		"dryRun": true,
	})
	require.Equal(t, http.StatusOK, dryRun.Code, dryRun.Body.String())
	var plan packinstall.Plan
	decodeData(t, dryRun, &plan)
	require.Len(t, plan.Blockers, 1)
	assert.Equal(t, packinstall.CodeFlowFormatUnsupported, plan.Blockers[0].Code)
	assert.Equal(t, packinstall.SeverityError, plan.Blockers[0].Severity)
	assert.Equal(t, before, snapshotTree(t, fixture.home))

	// A declared MCP profile ref is likewise refused, not ignored.
	mcpPack := installPackFiles(fixture.modelID)
	mcpPack["pack.yaml"] += "mcp:\n  - ref: pubmed@1\n"
	mcpSource := fixture.writePack(t, "mixed-mcp", mcpPack)
	mcp := fixture.install(t, map[string]any{"source": map[string]any{"kind": "path", "path": mcpSource}})
	assert.Equal(t, http.StatusUnprocessableEntity, mcp.Code)
	assert.Contains(t, mcp.Body.String(), packinstall.CodeMCPUnsupported)
	fixture.requirePacksDirAbsent(t)
}

func TestPackListAndRemove(t *testing.T) {
	fixture := newPackInstallFixture(t)
	source := fixture.writePack(t, "cell-qc", installPackFiles(fixture.modelID))

	empty := request(t, fixture.handler, http.MethodGet, "/v1/packs", nil, true)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	var emptyList []packinstall.StateSummary
	decodeData(t, empty, &emptyList)
	assert.Empty(t, emptyList)

	response := fixture.install(t, map[string]any{"source": map[string]any{"kind": "path", "path": source}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	list := request(t, fixture.handler, http.MethodGet, "/v1/packs", nil, true)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var summaries []packinstall.StateSummary
	decodeData(t, list, &summaries)
	require.Len(t, summaries, 1)
	assert.Equal(t, "seqyuan/cell-qc-review", summaries[0].Pack.ID)
	assert.Equal(t, packinstall.InstallStateDraft, summaries[0].InstallState)
	assert.Equal(t, 1, summaries[0].FlowCount)
	assert.Equal(t, 1, summaries[0].RoleCount)
	assert.Equal(t, 1, summaries[0].SkillCount)
	assert.Equal(t, source, summaries[0].Source.Path)

	// Editing the installed Graph draft makes an unforced remove refuse, and
	// force deletes it anyway.
	_, digest, err := fixture.sources.ReadGraph("qc-review")
	require.NoError(t, err)
	updated, _, err := fixture.sources.UpdateGraph("qc-review", digest, func(document *graphsource.Document) error {
		task := document.Tasks["load"]
		task.Goal = "Load a different samplesheet"
		document.Tasks["load"] = task
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, updated)

	blocked := request(t, fixture.handler, http.MethodDelete, "/v1/packs/seqyuan/cell-qc-review", nil, true)
	assert.Equal(t, http.StatusConflict, blocked.Code, blocked.Body.String())
	assert.Contains(t, blocked.Body.String(), packinstall.CodeInstallBlocked)

	_, _, err = fixture.sources.ReadGraph("qc-review")
	require.NoError(t, err, "a refused removal must not delete anything")

	forced := request(t, fixture.handler, http.MethodDelete, "/v1/packs/seqyuan/cell-qc-review?force=1", nil, true)
	assert.Equal(t, http.StatusNoContent, forced.Code, forced.Body.String())
	_, _, err = fixture.sources.ReadGraph("qc-review")
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, _, err = fixture.sources.ReadRole("qc-reviewer")
	assert.ErrorIs(t, err, os.ErrNotExist)

	missing := request(t, fixture.handler, http.MethodDelete, "/v1/packs/seqyuan/cell-qc-review", nil, true)
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Contains(t, missing.Body.String(), packinstall.CodeNotInstalled)
}

func TestPackInstallErrorStatusMapping(t *testing.T) {
	// Busy is not reachable through the handler from outside packinstall (the
	// installer's mutex is internal), so pin the mapping directly: a concurrent
	// install must answer 409, and every refusal must keep its stable code.
	for _, testCase := range []struct {
		code   string
		status int
	}{
		{packinstall.CodeInstallBusy, http.StatusConflict},
		{packinstall.CodeAlreadyInstalled, http.StatusConflict},
		{packinstall.CodeVersionConflict, http.StatusConflict},
		{packinstall.CodeNotInstalled, http.StatusNotFound},
		{packinstall.CodeFlowFormatUnsupported, http.StatusUnprocessableEntity},
		{packinstall.CodeMCPUnsupported, http.StatusUnprocessableEntity},
		{packinstall.CodeDependencyUnsupported, http.StatusUnprocessableEntity},
		{packinstall.CodeInstallBlocked, http.StatusUnprocessableEntity},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/packs/install", nil)
		writePackInstallError(recorder, request, &packinstall.Error{Code: testCase.code, Message: "refused"})
		assert.Equal(t, testCase.status, recorder.Code, testCase.code)
		assert.Contains(t, recorder.Body.String(), testCase.code)
	}
}

func TestPackIDFromRouteRejectsUnsafeSegments(t *testing.T) {
	for _, segments := range [][2]string{
		{"", "name"}, {"publisher", ""}, {".", "name"}, {"publisher", ".."},
		{"pub/lish", "name"}, {"publisher", `na\me`}, {"..", ".."},
	} {
		_, ok := packIDFromRoute(segments[0], segments[1])
		assert.False(t, ok, "%q/%q must be rejected", segments[0], segments[1])
	}
	packID, ok := packIDFromRoute("seqyuan", "cell-qc-review")
	assert.True(t, ok)
	assert.Equal(t, "seqyuan/cell-qc-review", packID)
}

func findPlanAction(actions []packinstall.Action, kind, storeID string) (packinstall.Action, bool) {
	for _, action := range actions {
		if action.Kind == kind && action.StoreID == storeID {
			return action, true
		}
	}
	return packinstall.Action{}, false
}
