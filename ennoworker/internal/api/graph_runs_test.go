package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGraphRunsServer wires a session-scoped Graph Runs server over an
// isolated Home (file project store + session store).
func setupGraphRunsServer(t *testing.T) (*Server, http.Handler, *domain.Project, *domain.Session, *sql.DB) {
	t.Helper()
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(context.Background(),
		domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { _ = sessions.Close() })
	session, err := sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	db, err := sessions.OpenSession(context.Background(), session.ID)
	require.NoError(t, err)
	server := &Server{Token: "test-token", SessionStores: sessions}
	return server, server.Handler(), project, session, db
}

func TestSessionGraphRunsLifecycle(t *testing.T) {
	_, handler, project, session, sessionDB := setupGraphRunsServer(t)
	ctx := context.Background()

	definitionJSON, _ := json.Marshal(&domain.FlowDefinition{
		SchemaVersion: 1, ID: "pipeline", Budget: domain.FlowBudget{MaxTotalTokens: 1000},
		Tasks: map[string]domain.FlowTask{"produce": {Type: domain.FlowTaskRole, Role: "r@1", Goal: "run"}},
	})
	repo := &store.AgentFlowRunRepo{DB: sessionDB}
	run, err := repo.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: project.ID, FlowVersionID: "pipeline@v000001",
		DefinitionJSON: definitionJSON, ConfigDigest: "sha256:" + "a",
		InputsJSON: json.RawMessage(`{"inputs":{},"vars":{}}`),
	}, []store.FlowNodeFreeze{{
		TaskIndex: 0, Handle: "produce", RoleVersionID: "r@v000001",
		GoalDigest: "g", GoalText: "run", BudgetJSON: json.RawMessage(`{}`),
	}})
	require.NoError(t, err)

	// List returns the run, newest first.
	rec := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/graph-runs", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var listed []domain.RunAgentFlow
	decodeData(t, rec, &listed)
	require.Len(t, listed, 1)
	assert.Equal(t, run.RunID, listed[0].RunID)
	assert.Equal(t, domain.FlowStatePending, listed[0].State)

	// Detail returns run + nodes + frozen flow version.
	rec = request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/graph-runs/"+run.RunID, nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var detail graphRunDetail
	decodeData(t, rec, &detail)
	require.NotNil(t, detail.Run)
	require.Len(t, detail.Nodes, 1)
	assert.Equal(t, "produce", detail.Nodes[0].Handle)
	assert.Equal(t, 1, detail.FlowVersion)

	// Cross-session access is fail-closed.
	rec = request(t, handler, http.MethodGet, "/v1/sessions/other-session/graph-runs/"+run.RunID, nil, true)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// Cancel requests cancellation (pending run still returns 200).
	rec = request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/graph-runs/"+run.RunID+"/cancel", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var cancelRequested int
	require.NoError(t, sessionDB.QueryRow(`SELECT cancel_requested FROM run_agent_flow WHERE run_id=?`, run.RunID).Scan(&cancelRequested))
	assert.Equal(t, 1, cancelRequested)

	// Resume a failed run resets it to pending.
	_, err = repo.UpdateFlowState(ctx, run.RunID, domain.FlowStateFailed, 0, "boom")
	require.NoError(t, err)
	rec = request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/graph-runs/"+run.RunID+"/resume", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resumed domain.RunAgentFlow
	decodeData(t, rec, &resumed)
	assert.Equal(t, domain.FlowStatePending, resumed.State)

	// A running run is not resumable.
	_, err = repo.UpdateFlowState(ctx, run.RunID, domain.FlowStateRunning, 0, "")
	require.NoError(t, err)
	rec = request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/graph-runs/"+run.RunID+"/resume", nil, true)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}
