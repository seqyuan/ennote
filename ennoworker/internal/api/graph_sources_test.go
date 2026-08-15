package api

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
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
