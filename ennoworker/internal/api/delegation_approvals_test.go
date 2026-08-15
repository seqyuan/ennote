package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDelegationApprovalAPI creates a settled mixed group with a pending
// retry-budget approval and returns the server, repos, and approval id.
func setupDelegationApprovalAPI(t *testing.T) (*Server, http.Handler, *store.DelegationRepo, string) {
	t.Helper()
	server, handler := setupServer(t, nil)
	ctx := context.Background()

	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "api-delegation", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "approval"})
	require.NoError(t, err)
	sessionDB, err := server.SessionStores.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	delegations := &store.DelegationRepo{DB: sessionDB, Policies: apiPolicyStore(t)}
	server.DelegationApprovals = &store.DelegationApprovalRepo{DB: sessionDB}
	runs := &store.RunRepo{DB: sessionDB}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "api-parent", Text: "run"})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	second := apiExplorerItem(t, server.DB)
	second.Name = "fail"
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-1", Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{apiExplorerItem(t, server.DB), second},
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
// The RoleMeta is frozen from the fixture's published Role version so
// delegation validation never consults global role SQL (V2).
func apiExplorerItem(t *testing.T, db *sql.DB) store.CreateDelegationItemInput {
	t.Helper()
	item := store.CreateDelegationItemInput{
		Name: "explore", RoleVersionID: "builtin-workspace-explorer-v3",
		AssignmentJSON: json.RawMessage(`{"objective":"inspect"}`), OutputContract: "text-v1",
		Budget: domain.BudgetCeilingJSON{MaxModelCalls: 2, MaxToolCalls: 4, MaxTotalTokens: 10000,
			MaxOutputTokens: 2000, MaxCostMicros: 50000, MaxWallTimeMS: 60000},
	}
	item.RoleMeta = apiRoleMetaFromDB(t, db, "builtin-workspace-explorer-v3")
	return item
}

// apiRoleMetaFromDB freezes the builtin Workspace Explorer RoleMeta without
// consulting global role SQL (V2): identity + definition are hardcoded in
// lockstep with the fixture snapshot until B4 removes it.
func apiRoleMetaFromDB(t *testing.T, _ *sql.DB, _ string) *store.DelegationRoleMeta {
	t.Helper()
	var definition domain.RoleDefinition
	require.NoError(t, json.Unmarshal([]byte(apiExplorerRoleDefinitionV3), &definition))
	return &store.DelegationRoleMeta{
		ObjectID: "builtin-workspace-explorer", VersionID: "builtin-workspace-explorer-v3",
		Handle: "workspace-explorer", DisplayName: "Workspace Explorer",
		ConfigDigest: "sha256:c7cf36749030bd0626c24eea7ea325c2b70be64bd2f623b3c94b5fc8b81aa38b",
		Definition:   definition,
	}
}

const apiExplorerRoleDefinitionV3 = `{"schemaVersion":1,"rolePrompt":"You are the Workspace Explorer. Use read, ls, grep, and find to answer questions about workspace files. You may inspect git history and status with the git_readonly tool (status, diff, log, show, ls-files, blame). Every answer must be concise. End every turn by calling submit_result with a structured result. Never create, modify, or delete files, and never run arbitrary shell commands.","modelBinding":{"mode":"inherit","modelProfileId":"provider/model","thinkingEffort":"default","fallbackModelProfileIds":[],"overridableFields":[]},"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss","allowedTools":["read","ls","grep","find","git_readonly"],"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},"delegationPolicy":{"admission":"auto_within_budget","allowedCallerKinds":["host"],"allowedStrategies":["single","parallel"],"maxInvocationsPerParentRun":16,"maxConcurrentInstances":16,"budgetCeiling":{"maxModelCalls":6,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}},"outputContract":"text-v1","maxLoopIterations":8}`

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
	control := &fakeController{}
	server.Control = control

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
	assert.Equal(t, payload.ChildRunIDs, control.enqueued)
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

// apiPolicyStore returns a file-backed delegation policy store for api tests.
func apiPolicyStore(t *testing.T) *fileconfig.PolicyStore {
	t.Helper()
	return &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}
}
