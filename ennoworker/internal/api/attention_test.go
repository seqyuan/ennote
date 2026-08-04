package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAttentionAPI seeds one background delegation completion so a real
// completion notification exists, and returns project + session ids.
func setupAttentionAPI(t *testing.T) (*Server, http.Handler, *store.DelegationRepo, string, string) {
	t.Helper()
	server, handler := setupServer(t, nil)
	server.DelegationApprovals = &store.DelegationApprovalRepo{DB: server.DB}
	server.Attention = &store.AttentionRepo{DB: server.DB}
	delegations := &store.DelegationRepo{DB: server.DB}
	runs := &store.RunRepo{DB: server.DB}
	ctx := context.Background()

	_, err := server.DB.Exec(`UPDATE settings SET value='1' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "attention-api", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "attention"})
	require.NoError(t, err)
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "att-parent", Text: "run"})
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
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "att result"},
	}))
	_ = group
	return server, handler, delegations, project.ID, session.ID
}

func TestListAttentionProjectAndSession(t *testing.T) {
	_, handler, _, projectID, sessionID := setupAttentionAPI(t)

	response := request(t, handler, http.MethodGet, "/v1/attention?projectId="+projectID+"&status=pending", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Items []domain.AttentionItem `json:"items"`
	}
	decodeData(t, response, &payload)
	require.NotEmpty(t, payload.Items)
	assert.Equal(t, domain.AttentionDelegationCompleted, payload.Items[0].Kind)
	require.NotNil(t, payload.Items[0].Action)
	assert.Equal(t, "none", payload.Items[0].Action.Kind)

	response = request(t, handler, http.MethodGet, "/v1/sessions/"+sessionID+"/attention?status=pending", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	decodeData(t, response, &payload)
	require.NotEmpty(t, payload.Items)
}

func TestDismissAttentionNotification(t *testing.T) {
	_, handler, _, projectID, _ := setupAttentionAPI(t)

	var items struct {
		Items []domain.AttentionItem `json:"items"`
	}
	response := request(t, handler, http.MethodGet, "/v1/attention?projectId="+projectID+"&status=pending", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	decodeData(t, response, &items)
	require.NotEmpty(t, items.Items)
	attentionID := items.Items[0].ID

	response = request(t, handler, http.MethodPost, "/v1/attention/"+attentionID+"/dismiss",
		map[string]any{"clientRequestId": "dismiss-1"}, true)
	require.Equal(t, http.StatusOK, response.Code)

	response = request(t, handler, http.MethodGet, "/v1/attention?projectId="+projectID+"&status=dismissed", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	decodeData(t, response, &items)
	require.Len(t, items.Items, 1)
}

func TestDismissAttentionRequiresClientRequestID(t *testing.T) {
	_, handler, _, projectID, _ := setupAttentionAPI(t)

	var items struct {
		Items []domain.AttentionItem `json:"items"`
	}
	response := request(t, handler, http.MethodGet, "/v1/attention?projectId="+projectID+"&status=pending", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	decodeData(t, response, &items)
	require.NotEmpty(t, items.Items)

	response = request(t, handler, http.MethodPost, "/v1/attention/"+items.Items[0].ID+"/dismiss",
		map[string]any{}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)

	response = request(t, handler, http.MethodPost, "/v1/attention/does-not-exist/dismiss",
		map[string]any{"clientRequestId": "x"}, true)
	assert.Equal(t, http.StatusNotFound, response.Code)
}
