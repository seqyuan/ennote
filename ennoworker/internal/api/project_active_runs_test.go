package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The sidebar paints its running marker from this answer, so it has to name the
// same Sessions the per-Session endpoint would, and it has to survive a project
// with nothing running.
func TestProjectActiveRunsListsOnlySessionsWithAnActiveRun(t *testing.T) {
	ctx := context.Background()
	server, handler := setupServer(t, &fakeController{})

	ids, err := server.SessionStores.AllSessionIDs()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	session, err := server.SessionStores.FindByID(ctx, ids[0])
	require.NoError(t, err)
	require.NotNil(t, session)

	projectRuns := func(projectID string) []string {
		t.Helper()
		response := request(t, handler, http.MethodGet, "/v1/projects/"+projectID+"/active-runs", nil, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var answer struct {
			SessionIDs []string `json:"sessionIds"`
		}
		decodeData(t, response, &answer)
		require.NotNil(t, answer.SessionIDs, "an empty answer is [] and not null")
		return answer.SessionIDs
	}

	assert.Empty(t, projectRuns(session.ProjectID))

	// A queued Run is active: the composer treats it as in-flight and the sidebar
	// marks the row, so this endpoint must report it.
	bindAPISession(t, server, ctx, session.ID)
	submission, err := server.Runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "turn", Text: "start",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{session.ID}, projectRuns(session.ProjectID))

	// Same predicate as the per-Session endpoint, by construction.
	active := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/active-run", nil, true)
	require.Equal(t, http.StatusOK, active.Code, active.Body.String())
	var state domain.ActiveRunState
	decodeData(t, active, &state)
	assert.Equal(t, submission.Run.ID, state.Run.ID)

	// A project nobody knows is an empty answer, not an error: the sidebar asks for
	// every group it renders.
	assert.Empty(t, projectRuns("00000000-0000-0000-0000-000000000000"))

	unauthenticated := request(t, handler, http.MethodGet, "/v1/projects/"+session.ProjectID+"/active-runs", nil, false)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
}
