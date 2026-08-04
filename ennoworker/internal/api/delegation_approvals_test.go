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

// setupDelegationApprovalAPI creates a settled mixed group with a pending
// retry-budget approval and returns the server, repos, and approval id.
func setupDelegationApprovalAPI(t *testing.T) (*Server, http.Handler, *store.DelegationRepo, string) {
	t.Helper()
	server, handler := setupServer(t, nil)
	delegations := &store.DelegationRepo{DB: server.DB}
	server.DelegationApprovals = &store.DelegationApprovalRepo{DB: server.DB}
	runs := &store.RunRepo{DB: server.DB}
	ctx := context.Background()

	_, err := server.DB.Exec(`UPDATE settings SET value='1' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "api-delegation", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "approval"})
	require.NoError(t, err)
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "api-parent", Text: "run"})
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
	for index, child := range children {
		_, err = runs.Claim(ctx, child.ID)
		require.NoError(t, err)
		if index == 0 {
			require.NoError(t, runs.FinalizeChildSuccess(ctx, child.ID, domain.RunOutput{
				Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
					Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "ok"}}}},
				Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "ok"},
			}))
		} else {
			_, _, failErr := runs.FinalizeChildFailure(ctx, child.ID, "provider_unavailable", "boom")
			require.NoError(t, failErr)
		}
	}
	failedItemID := items[1].ID
	_, _, approval, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
		ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "api-request-1",
		BudgetOverrides: map[string]domain.BudgetCeilingJSON{
			failedItemID: {MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, approval)
	return server, handler, delegations, approval.ID
}

// apiExplorerItem mirrors the store-level explorer fixture for the api package.
func apiExplorerItem() store.CreateDelegationItemInput {
	return store.CreateDelegationItemInput{
		Name: "explore", RoleVersionID: "builtin-workspace-explorer-v2",
		AssignmentJSON: json.RawMessage(`{"objective":"inspect"}`), OutputContract: "text-v1",
		Budget: domain.BudgetCeilingJSON{MaxModelCalls: 2, MaxToolCalls: 4, MaxTotalTokens: 10000,
			MaxOutputTokens: 2000, MaxCostMicros: 50000, MaxWallTimeMS: 60000},
	}
}

func TestDecideDelegationApprovalRejectsAndRewinds(t *testing.T) {
	server, handler, delegations, approvalID := setupDelegationApprovalAPI(t)

	response := request(t, handler, http.MethodPost, "/v1/delegation-approvals/"+approvalID+"/decision",
		map[string]any{"decision": "rejected", "clientRequestId": "api-reject"}, true)
	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Approval domain.DelegationApprovalRequest `json:"approval"`
	}
	decodeData(t, response, &payload)
	assert.Equal(t, "rejected", payload.Approval.Status)

	var groupID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT group_id FROM delegation_approval_requests WHERE id=?`,
		approvalID).Scan(&groupID))
	var cursor int
	require.NoError(t, delegations.DB.QueryRow(`SELECT current_generation FROM delegation_groups WHERE id=?`,
		groupID).Scan(&cursor))
	assert.Equal(t, 0, cursor, "rejection rewinds the group cursor")
	_ = server
}

func TestDecideDelegationApprovalApprovesAndMaterializes(t *testing.T) {
	server, handler, delegations, approvalID := setupDelegationApprovalAPI(t)

	response := request(t, handler, http.MethodPost, "/v1/delegation-approvals/"+approvalID+"/decision",
		map[string]any{"decision": "approved", "clientRequestId": "api-approve"}, true)
	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Approval    domain.DelegationApprovalRequest `json:"approval"`
		ChildRunIDs []string                         `json:"childRunIds"`
	}
	decodeData(t, response, &payload)
	assert.Equal(t, "approved", payload.Approval.Status)
	require.Len(t, payload.ChildRunIDs, 1)
	var status string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`,
		payload.ChildRunIDs[0]).Scan(&status))
	assert.Equal(t, "queued", status)
	_ = server
}

func TestDecideDelegationApprovalValidation(t *testing.T) {
	_, handler, _, approvalID := setupDelegationApprovalAPI(t)

	response := request(t, handler, http.MethodPost, "/v1/delegation-approvals/"+approvalID+"/decision",
		map[string]any{"decision": "maybe", "clientRequestId": "bad"}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)

	response = request(t, handler, http.MethodPost, "/v1/delegation-approvals/"+approvalID+"/decision",
		map[string]any{"decision": "approved"}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)

	response = request(t, handler, http.MethodPost, "/v1/delegation-approvals/does-not-exist/decision",
		map[string]any{"decision": "approved", "clientRequestId": "x"}, true)
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func TestDecideDelegationApprovalConflict(t *testing.T) {
	server, handler, delegations, approvalID := setupDelegationApprovalAPI(t)

	response := request(t, handler, http.MethodPost, "/v1/delegation-approvals/"+approvalID+"/decision",
		map[string]any{"decision": "approved", "clientRequestId": "first"}, true)
	require.Equal(t, http.StatusOK, response.Code)

	response = request(t, handler, http.MethodPost, "/v1/delegation-approvals/"+approvalID+"/decision",
		map[string]any{"decision": "rejected", "clientRequestId": "second"}, true)
	require.Equal(t, http.StatusConflict, response.Code)

	response = request(t, handler, http.MethodPost, "/v1/delegation-approvals/"+approvalID+"/decision",
		map[string]any{"decision": "approved", "clientRequestId": "first"}, true)
	require.Equal(t, http.StatusOK, response.Code)
	var childCount int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&childCount))
	assert.Equal(t, 1, childCount)
	_ = server
}
