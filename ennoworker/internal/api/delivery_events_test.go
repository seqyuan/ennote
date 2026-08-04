package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDeliveryServer creates a background delegation with a settled child so
// one completion and one delivery event exist.
func setupDeliveryServer(t *testing.T) (*Server, http.Handler, *store.DelegationRepo, string, string) {
	t.Helper()
	server, handler := setupServer(t, nil)
	server.DelegationApprovals = &store.DelegationApprovalRepo{DB: server.DB}
	delegations := &store.DelegationRepo{DB: server.DB}
	runs := &store.RunRepo{DB: server.DB}
	ctx := context.Background()

	_, err := server.DB.Exec(`UPDATE settings SET value='1' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "delivery", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "delivery"})
	require.NoError(t, err)
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "delivery-parent", Text: "run"})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "bg", Strategy: domain.DelegationStrategySingle,
		ExecutionMode: domain.DelegationExecutionBackground,
		Items:         []store.CreateDelegationItemInput{apiExplorerItem()},
	}, session.ID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "delivered result"},
	}))
	handle, err := delegations.HandleForGroup(ctx, group.ID)
	require.NoError(t, err)
	return server, handler, delegations, handle.ID, session.ID
}

func TestGetDelegationHandleWithCompletion(t *testing.T) {
	_, handler, _, handleID, _ := setupDeliveryServer(t)

	response := request(t, handler, http.MethodGet, "/v1/delegation-handles/"+handleID, nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Handle     domain.DelegationHandle      `json:"handle"`
		Completion *domain.DelegationCompletion `json:"completion"`
	}
	decodeData(t, response, &payload)
	assert.Equal(t, "background", string(payload.Handle.ExecutionMode))
	require.NotNil(t, payload.Completion)
	assert.Equal(t, "completed", payload.Completion.Kind)
	for _, forbidden := range []string{"transcript", "credential", "apiKey", "assignment"} {
		assert.NotContains(t, response.Body.String(), forbidden)
	}
}

func TestListSessionDelegationHandles(t *testing.T) {
	_, handler, _, _, sessionID := setupDeliveryServer(t)

	response := request(t, handler, http.MethodGet, "/v1/sessions/"+sessionID+"/delegation-handles", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var page struct {
		Items []domain.DelegationHandle `json:"items"`
	}
	decodeData(t, response, &page)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "background", string(page.Items[0].ExecutionMode))
}

// collectDeliveryFrames drives the SSE handler with a timeout context and
// returns the delivery data frames it emitted.
func collectDeliveryFrames(t *testing.T, handler http.Handler, path, lastEventID string) []domain.DeliveryEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://test"+path, nil)
	require.NoError(t, err)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req) // returns when ctx expires
	events := make([]domain.DeliveryEvent, 0)
	var data strings.Builder
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			data.WriteString(strings.TrimPrefix(line, "data: "))
			continue
		}
		if line == "" && data.Len() > 0 {
			var event domain.DeliveryEvent
			if err := json.Unmarshal([]byte(data.String()), &event); err == nil {
				events = append(events, event)
			}
			data.Reset()
		}
	}
	return events
}

func TestDeliveryEventsStreamReplaysWithSameIDs(t *testing.T) {
	_, handler, _, _, sessionID := setupDeliveryServer(t)

	first := collectDeliveryFrames(t, handler, "/v1/delivery-events?sessionId="+sessionID, "")
	require.Len(t, first, 1)
	eventID := first[0].EventID
	require.Greater(t, eventID, int64(0))

	// Reconnect with Last-Event-ID yields no duplicate logical event.
	second := collectDeliveryFrames(t, handler, "/v1/delivery-events?sessionId="+sessionID, formatID(eventID))
	assert.Empty(t, second)

	// after cursor yields the same.
	third := collectDeliveryFrames(t, handler,
		"/v1/delivery-events?sessionId="+sessionID+"&after="+formatID(eventID), "")
	assert.Empty(t, third)
}

func TestDeliveryEventsRequireSession(t *testing.T) {
	_, handler, _, _, _ := setupDeliveryServer(t)
	response := request(t, handler, http.MethodGet, "/v1/delivery-events", nil, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)
}

func formatID(value int64) string {
	encoded, _ := json.Marshal(value)
	return strings.Trim(string(encoded), `"`)
}
