package api

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type checkApprovalAPI struct {
	handler   http.Handler
	sessions  *sessionstore.Manager
	db        *sql.DB
	runID     string
	sessionID string
}

func newCheckApprovalAPI(t *testing.T) *checkApprovalAPI {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Checks", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Checks"})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	runID := "run-check-api"
	_, err = db.Exec(`INSERT INTO run_agent_flow
		(run_id,session_id,project_id,flow_version_id,manifest_digest,inputs_json,state,total_tokens_used,created_at,updated_at,definition_json,config_digest)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, session.ID, project.ID, "flow@v000001", "manifest", "{}", "running", 0, now, now, "{}", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO run_agent_flow_nodes (run_id,task_index,handle,terminal_state,created_at)
		VALUES (?,0,'gate','running',?)`, runID, now)
	require.NoError(t, err)
	runner := &store.CheckTaskRunner{DB: db}
	require.NoError(t, runner.CreateCheckApproval(ctx, runID, 0, "go test ./..."))
	server := &Server{
		Token: "test-token", SessionStores: sessions, CheckRunner: &store.CheckTaskRunner{},
	}
	return &checkApprovalAPI{handler: server.Handler(), sessions: sessions, db: db, runID: runID, sessionID: session.ID}
}

func TestCheckApprovalHTTPListAndDecide(t *testing.T) {
	fixture := newCheckApprovalAPI(t)
	base := "/v1/sessions/" + fixture.sessionID + "/graph-runs/" + fixture.runID + "/check-approvals"

	list := request(t, fixture.handler, http.MethodGet, base, nil, true)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var approvals []store.CheckApprovalRow
	decodeData(t, list, &approvals)
	require.Len(t, approvals, 1)
	assert.Equal(t, agentflow.CheckApprovalPending, approvals[0].Status)
	assert.Equal(t, "gate", approvals[0].TaskHandle)

	invalid := request(t, fixture.handler, http.MethodPost, base+"/0/decision",
		map[string]any{"decision": "maybe"}, true)
	assert.Equal(t, http.StatusBadRequest, invalid.Code)

	decided := request(t, fixture.handler, http.MethodPost, base+"/0/decision",
		map[string]any{"decision": "approved", "clientRequestId": "req-1"}, true)
	require.Equal(t, http.StatusOK, decided.Code, decided.Body.String())
	var result checkApprovalDecisionResult
	decodeData(t, decided, &result)
	assert.Equal(t, agentflow.CheckApprovalApproved, result.Status)
	assert.True(t, result.Applied)

	again := request(t, fixture.handler, http.MethodPost, base+"/0/decision",
		map[string]any{"decision": "rejected"}, true)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	var repeated checkApprovalDecisionResult
	decodeData(t, again, &repeated)
	assert.Equal(t, agentflow.CheckApprovalApproved, repeated.Status)
	assert.False(t, repeated.Applied)
}

func TestCheckApprovalHTTPRejectsTerminalRunAndCrossSession(t *testing.T) {
	fixture := newCheckApprovalAPI(t)
	base := "/v1/sessions/" + fixture.sessionID + "/graph-runs/" + fixture.runID + "/check-approvals"
	_, err := fixture.db.Exec(`UPDATE run_agent_flow SET state='completed' WHERE run_id=?`, fixture.runID)
	require.NoError(t, err)
	response := request(t, fixture.handler, http.MethodPost, base+"/0/decision",
		map[string]any{"decision": "approved"}, true)
	assert.Equal(t, http.StatusConflict, response.Code)
	assert.Contains(t, response.Body.String(), "graph_run_not_active")

	cross := request(t, fixture.handler, http.MethodGet,
		"/v1/sessions/00000000-0000-0000-0000-000000000000/graph-runs/"+fixture.runID+"/check-approvals", nil, true)
	assert.Equal(t, http.StatusNotFound, cross.Code)
}
