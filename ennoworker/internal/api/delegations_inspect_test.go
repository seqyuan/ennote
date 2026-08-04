package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDelegationGroupAPI returns a server with one settled mixed group (one
// success, one failure) whose parent tool call is recorded.
func setupDelegationGroupAPI(t *testing.T) (*Server, http.Handler, *store.DelegationRepo, string, string) {
	t.Helper()
	server, handler := setupServer(t, nil)
	server.DelegationApprovals = &store.DelegationApprovalRepo{DB: server.DB}
	delegations := &store.DelegationRepo{DB: server.DB}
	runs := &store.RunRepo{DB: server.DB}
	ctx := context.Background()

	_, err := server.DB.Exec(`UPDATE settings SET value='1' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "api-group", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "group"})
	require.NoError(t, err)
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "api-group-parent", Text: "run"})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	second := apiExplorerItem()
	second.Name = "fail"
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{apiExplorerItem(), second},
	}, session.ID)
	require.NoError(t, err)
	require.Len(t, children, 2)
	_, err = delegations.DB.Exec(`INSERT INTO tool_calls
		(id,run_id,seq,tool_call_id,tool_name,arguments_json,status,started_at)
		VALUES('tc-inspect',?,1,'call-1','delegate_roles','{}','completed',CURRENT_TIMESTAMP)`,
		submission.Run.ID)
	require.NoError(t, err)
	for index, child := range children {
		_, err = runs.Claim(ctx, child.ID)
		require.NoError(t, err)
		if index == 0 {
			require.NoError(t, runs.FinalizeChildSuccess(ctx, child.ID, domain.RunOutput{
				Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
					Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "ok"}}}},
				Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "found README"},
			}))
		} else {
			_, _, failErr := runs.FinalizeChildFailure(ctx, child.ID, "provider_unavailable", "boom")
			require.NoError(t, failErr)
		}
	}
	return server, handler, delegations, group.ID, items[1].ID
}

func TestInspectDelegationProjection(t *testing.T) {
	server, handler, _, groupID, failedItemID := setupDelegationGroupAPI(t)

	response := request(t, handler, http.MethodGet, "/v1/delegations/"+groupID, nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var inspection domain.DelegationInspection
	decodeData(t, response, &inspection)

	assert.Equal(t, groupID, inspection.Group.ID)
	assert.Equal(t, 0, inspection.CurrentGeneration)
	require.Len(t, inspection.Items, 2)
	require.Len(t, inspection.Generations, 1)
	assert.Equal(t, domain.DelegationGenerationSettled, inspection.Generations[0].Status)

	// Every item exposes its full attempt history with bounded results.
	attempts := map[string][]domain.DelegationAttemptSummary{}
	for _, item := range inspection.Items {
		attempts[item.ItemID] = item.Attempts
	}
	require.Len(t, attempts[failedItemID], 1)
	failed := attempts[failedItemID][0]
	assert.Equal(t, domain.DelegationAttemptFailed, failed.Status)
	assert.NotEmpty(t, failed.ResultDigest)
	assert.Equal(t, "provider_unavailable", failed.ErrorCode)

	// Valid actions derive from frozen state: retry is available for the
	// failed attempt.
	assert.Contains(t, inspection.ValidActions, "retry")

	// The projection never leaks private material.
	raw, err := json.Marshal(inspection)
	require.NoError(t, err)
	text := string(raw)
	for _, forbidden := range []string{"transcript", "credential", "apiKey", "rolePrompt", "systemPrompt"} {
		assert.NotContains(t, text, forbidden)
	}
	_ = server
}

func TestInspectDelegationUnknownGroup(t *testing.T) {
	_, handler, _, _, _ := setupDelegationGroupAPI(t)
	response := request(t, handler, http.MethodGet, "/v1/delegations/does-not-exist", nil, true)
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestRetryDelegationAPI(t *testing.T) {
	server, handler, delegations, groupID, failedItemID := setupDelegationGroupAPI(t)

	response := request(t, handler, http.MethodPost, "/v1/delegations/"+groupID+"/retry",
		map[string]any{"expectedGeneration": 0, "itemIds": []string{failedItemID}, "clientRequestId": "api-retry-1"}, true)
	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Generation  domain.DelegationGeneration `json:"generation"`
		ChildRunIDs []string                    `json:"childRunIds"`
	}
	decodeData(t, response, &payload)
	assert.Equal(t, 1, payload.Generation.Generation)
	require.Len(t, payload.ChildRunIDs, 1)
	require.Len(t, payload.Generation.ReusedAttempts, 1)

	// Stale expected generation conflicts.
	response = request(t, handler, http.MethodPost, "/v1/delegations/"+groupID+"/retry",
		map[string]any{"expectedGeneration": 0, "itemIds": []string{failedItemID}, "clientRequestId": "api-retry-stale"}, true)
	require.Equal(t, http.StatusConflict, response.Code)

	// Ineligible selection (the successful sibling) conflicts.
	items, err := delegations.ListItems(context.Background(), groupID)
	require.NoError(t, err)
	var successID string
	for _, item := range items {
		if item.Status == domain.DelegationItemTerminal {
			successID = item.ID
		}
	}
	response = request(t, handler, http.MethodPost, "/v1/delegations/"+groupID+"/retry",
		map[string]any{"expectedGeneration": 1, "itemIds": []string{successID}, "clientRequestId": "api-retry-ineligible"}, true)
	require.Equal(t, http.StatusConflict, response.Code)
	_ = server
}

func TestRetryDelegationAPIApprovalRequired(t *testing.T) {
	_, handler, _, groupID, failedItemID := setupDelegationGroupAPI(t)

	response := request(t, handler, http.MethodPost, "/v1/delegations/"+groupID+"/retry",
		map[string]any{"expectedGeneration": 0, "itemIds": []string{failedItemID}, "clientRequestId": "api-retry-budget",
			"budgetOverrides": map[string]any{failedItemID: map[string]any{
				"maxModelCalls": 4, "maxToolCalls": 8, "maxTotalTokens": 20000,
				"maxOutputTokens": 4000, "maxCostUsdMicros": 100000, "maxWallTimeMs": 120000,
			}}}, true)
	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Generation  domain.DelegationGeneration       `json:"generation"`
		Approval    *domain.DelegationApprovalRequest `json:"approval"`
		ChildRunIDs []string                          `json:"childRunIds"`
	}
	decodeData(t, response, &payload)
	assert.Equal(t, domain.DelegationGenerationAwaitingAuthorization, payload.Generation.Status)
	require.NotNil(t, payload.Approval)
	assert.Empty(t, payload.ChildRunIDs)
}

func TestCancelDelegationAPI(t *testing.T) {
	server, handler, delegations, groupID, _ := setupDelegationGroupAPI(t)

	// Create an active retry generation first so cancellation has work to do.
	items, err := delegations.ListItems(context.Background(), groupID)
	require.NoError(t, err)
	var failedID string
	for _, item := range items {
		if item.Status == domain.DelegationItemFailed {
			failedID = item.ID
		}
	}
	response := request(t, handler, http.MethodPost, "/v1/delegations/"+groupID+"/retry",
		map[string]any{"expectedGeneration": 0, "itemIds": []string{failedID}, "clientRequestId": "api-cancel-retry"}, true)
	require.Equal(t, http.StatusOK, response.Code)
	var retryPayload struct {
		ChildRunIDs []string `json:"childRunIds"`
	}
	decodeData(t, response, &retryPayload)
	require.Len(t, retryPayload.ChildRunIDs, 1)

	response = request(t, handler, http.MethodPost, "/v1/delegations/"+groupID+"/cancel", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	var status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`,
		retryPayload.ChildRunIDs[0]).Scan(&status))
	assert.Equal(t, "cancelled", status)
	_ = server
}
