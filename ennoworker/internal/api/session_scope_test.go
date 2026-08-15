package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwnerFromPathCoversSessionResources(t *testing.T) {
	tests := map[string]requestOwner{
		"/v1/sessions/session-id/messages":                {kind: "session", id: "session-id"},
		"/v1/runs/run-id/events":                          {kind: "run", id: "run-id"},
		"/v1/delegations/group-id/retry":                  {kind: "delegation_group", id: "group-id"},
		"/v1/approval-requests/approval-id/decision":      {kind: "approval", id: "approval-id"},
		"/v1/projects/project-id/agent-flows/runs/run-id": {kind: "flow_run", id: "run-id"},
	}
	for path, expected := range tests {
		actual := ownerFromPath(path)
		require.NotNil(t, actual, path)
		assert.Equal(t, expected, *actual, path)
	}
	assert.Nil(t, ownerFromPath("/v1/projects/project-id/sessions"))
	assert.Nil(t, ownerFromPath("/v1/attention"))
}

func TestScopeForRequestBindsRepositoriesToOwningSession(t *testing.T) {
	projects := &projectstore.Store{Root: filepath.Join(t.TempDir(), "projects")}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	manager := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	session, err := manager.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	db, err := manager.OpenSession(context.Background(), session.ID)
	require.NoError(t, err)
	messageID := uuid.NewString()
	require.NoError(t, func() error {
		_, err := db.Exec(`INSERT INTO messages(id,session_id,role,status,created_at) VALUES(?,?,'user','complete',?)`,
			messageID, session.ID, time.Now().UTC().Format(time.RFC3339Nano))
		return err
	}())

	server := &Server{
		SessionStores: manager,
		Sessions:      &store.SessionRepo{Files: manager}, Messages: &store.MessageRepo{},
		Runs: &store.RunRepo{}, Branches: &store.BranchRepo{}, Events: &store.EventRepo{},
	}
	request := httptest.NewRequest("GET", "/v1/sessions/"+session.ID+"/messages", nil)
	scoped, err := server.scopeForRequest(request)
	require.NoError(t, err)
	assert.Same(t, db, scoped.DB)
	assert.Same(t, db, scoped.Messages.DB)
	assert.Same(t, db, scoped.Runs.DB)
	var count int
	require.NoError(t, scoped.Messages.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE id=?`, messageID).Scan(&count))
	assert.Equal(t, 1, count)
}
