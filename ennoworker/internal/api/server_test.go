package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/mcpclient"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeDiagnoser struct {
	diagnostic domain.ProviderDiagnostic
	err        error
	providerID string
	modelID    string
}

func (f *fakeDiagnoser) Diagnose(_ context.Context, providerID, modelID string) (domain.ProviderDiagnostic, error) {
	f.providerID = providerID
	f.modelID = modelID
	return f.diagnostic, f.err
}

type fakeController struct {
	mu        sync.Mutex
	enqueued  []string
	cancelled []string
}

func (f *fakeController) Enqueue(_ context.Context, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued = append(f.enqueued, runID)
	return nil
}
func (f *fakeController) Cancel(_ context.Context, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, runID)
	return nil
}

func setupServer(t *testing.T, control RunController) (*Server, http.Handler) {
	t.Helper()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	hub := events.NewHub()
	server := &Server{
		DB: db, Token: "test-token", Sandbox: "none",
		Projects: &store.ProjectRepo{DB: db}, Providers: &store.ProviderRepo{DB: db},
		Models: &store.ModelRepo{DB: db}, Roles: &store.RoleRepo{DB: db, KnownTools: map[string]bool{
			"read": true, "ls": true, "grep": true, "find": true, "bash": true, "write": true,
		}}, Policies: &store.PolicyRepo{DB: db},
		Artifacts: &artifacts.Service{DB: db, Root: t.TempDir()}, Sessions: &store.SessionRepo{DB: db},
		Branches: &store.BranchRepo{DB: db}, Messages: &store.MessageRepo{DB: db}, Compactions: &store.CompactionRepo{DB: db},
		Approvals: &store.ApprovalRepo{DB: db}, Delegations: &store.DelegationRepo{DB: db}, Runs: &store.RunRepo{DB: db},
		DelegationApprovals: &store.DelegationApprovalRepo{DB: db},
		Queue:               &store.QueueRepo{DB: db}, Events: &store.EventRepo{DB: db},
		Hub: hub, Control: control,
		MCP: &MCPServer{
			Profiles:      &store.MCPProfileRepo{DB: db},
			Bindings:      &store.MCPBindingRepo{DB: db},
			Catalogs:      &store.MCPCatalogRepo{DB: db},
			Runs:          &store.MCPRunRepo{DB: db},
			ResolveSecret: nil,
			Logger:        nil,
			Bundled:       mcpclient.NewBundledRegistry(),
		},
	}
	return server, server.Handler()
}

func request(t *testing.T, handler http.Handler, method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		req.Header.Set("Authorization", "Bearer test-token")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func decodeData(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope), recorder.Body.String())
	require.NoError(t, json.Unmarshal(envelope.Data, target), recorder.Body.String())
}

func TestArtifactMetadataPreviewDownloadAndSessionScope(t *testing.T) {
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "artifacts", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	otherProject, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "other", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	otherSession, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: otherProject.ID})
	require.NoError(t, err)

	table, err := server.Artifacts.Store(context.Background(), artifacts.PublishInput{
		ProjectID: project.ID, SessionID: session.ID, Name: "../results.csv",
	}, strings.NewReader("gene,value\nA,1\n"))
	require.NoError(t, err)
	base := "/v1/sessions/" + session.ID + "/artifacts/" + table.ID
	metadata := request(t, handler, http.MethodGet, base, nil, true)
	require.Equal(t, http.StatusOK, metadata.Code, metadata.Body.String())
	assert.NotContains(t, metadata.Body.String(), server.Artifacts.Root)
	assert.NotContains(t, metadata.Body.String(), "storagePath")

	preview := request(t, handler, http.MethodGet, base+"/preview", nil, true)
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	assert.Contains(t, preview.Body.String(), `"columns":["gene","value"]`)
	assert.Equal(t, "nosniff", preview.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "private, no-store", preview.Header().Get("Cache-Control"))

	download := request(t, handler, http.MethodGet, base+"/download", nil, true)
	require.Equal(t, http.StatusOK, download.Code, download.Body.String())
	assert.Equal(t, "gene,value\nA,1\n", download.Body.String())
	assert.Contains(t, download.Header().Get("Content-Disposition"), "results.csv")
	assert.NotContains(t, download.Header().Get("Content-Disposition"), "\r")
	assert.NotContains(t, download.Header().Get("Content-Disposition"), "\n")

	crossSession := request(t, handler, http.MethodGet,
		"/v1/sessions/"+otherSession.ID+"/artifacts/"+table.ID, nil, true)
	assert.Equal(t, http.StatusNotFound, crossSession.Code)

	htmlArtifact, err := server.Artifacts.Store(context.Background(), artifacts.PublishInput{
		ProjectID: project.ID, SessionID: session.ID, Name: "report.html",
	}, strings.NewReader(`<h1 onclick="alert(1)">Report</h1><script>parent.pwned=true</script>`))
	require.NoError(t, err)
	htmlPreview := request(t, handler, http.MethodGet,
		"/v1/sessions/"+session.ID+"/artifacts/"+htmlArtifact.ID+"/preview", nil, true)
	require.Equal(t, http.StatusOK, htmlPreview.Code, htmlPreview.Body.String())
	assert.Contains(t, htmlPreview.Header().Get("Content-Security-Policy"), "default-src 'none'")
	assert.Contains(t, htmlPreview.Header().Get("Content-Security-Policy"), "sandbox")
	assert.NotContains(t, htmlPreview.Body.String(), "script")
	assert.NotContains(t, htmlPreview.Body.String(), "onclick")

	require.NoError(t, os.WriteFile(filepath.Join(server.Artifacts.Root, table.StoragePath), []byte("corrupt"), 0o600))
	corrupt := request(t, handler, http.MethodGet, base+"/download", nil, true)
	assert.Equal(t, http.StatusConflict, corrupt.Code)
	assert.NotContains(t, corrupt.Body.String(), "corrupt\n")
}

func TestApprovalAPIRehydratesAndResolvesPendingBatch(t *testing.T) {
	control := &fakeController{}
	server, handler := setupServer(t, control)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "approval", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	submission, err := server.Runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "turn", Text: "update",
	})
	require.NoError(t, err)
	_, err = server.Runs.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	approval, err := server.Approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":1,"secret":"internal-only"}`), []domain.ApprovalItem{{
			ToolCallID: "call", ToolName: "write", RiskClass: domain.RiskLocalWrite,
			ArgumentsPreview: `{"path":"notes.txt"}`,
		}}, nil)
	require.NoError(t, err)

	activeResponse := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/active-run", nil, true)
	require.Equal(t, http.StatusOK, activeResponse.Code, activeResponse.Body.String())
	assert.NotContains(t, activeResponse.Body.String(), "internal-only")
	var active domain.ActiveRunState
	decodeData(t, activeResponse, &active)
	assert.Equal(t, domain.RunWaitingForApproval, active.Run.Status)
	require.NotNil(t, active.PendingApproval)
	assert.Equal(t, approval.ID, active.PendingApproval.ID)

	decision := request(t, handler, http.MethodPost, "/v1/approval-requests/"+approval.ID+"/decision",
		map[string]any{"decision": "approved", "clientRequestId": "decision"}, true)
	require.Equal(t, http.StatusOK, decision.Code, decision.Body.String())
	control.mu.Lock()
	assert.Equal(t, []string{submission.Run.ID}, control.enqueued)
	control.mu.Unlock()

	conflict := request(t, handler, http.MethodPost, "/v1/approval-requests/"+approval.ID+"/decision",
		map[string]any{"decision": "rejected", "clientRequestId": "opposite"}, true)
	assert.Equal(t, http.StatusConflict, conflict.Code)
	missing := request(t, handler, http.MethodGet, "/v1/sessions/missing/active-run", nil, true)
	assert.Equal(t, http.StatusNotFound, missing.Code)
}

func TestActiveParentProjectsDepthOneChildApproval(t *testing.T) {
	control := &fakeController{}
	server, handler := setupServer(t, control)
	ctx := context.Background()
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "child approval", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	submission, err := server.Runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "parent", Text: "delegate",
	})
	require.NoError(t, err)
	_, err = server.Runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	delegations := &store.DelegationRepo{DB: server.DB}
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "delegate-call", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{{Name: "inspect", RoleVersionID: "builtin-workspace-explorer-v3",
			AssignmentJSON: json.RawMessage(`{"task":"inspect"}`), OutputContract: "text-v1",
			Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxWallTimeMS: 120000}}},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: submission.Run.ID, ItemID: items[0].ID, SessionID: session.ID,
	})
	require.NoError(t, err)
	activityResponse := request(t, handler, http.MethodGet, "/v1/runs/"+submission.Run.ID+"/children", nil, true)
	require.Equal(t, http.StatusOK, activityResponse.Code, activityResponse.Body.String())
	var activity domain.DelegationActivityPage
	decodeData(t, activityResponse, &activity)
	require.Len(t, activity.Groups, 1)
	assert.Equal(t, "delegate-call", activity.Groups[0].ParentToolCallID)
	require.Len(t, activity.Groups[0].Children, 1)
	assert.Equal(t, child.ID, activity.Groups[0].Children[0].ChildRunID)
	assert.Equal(t, "workspace-explorer", activity.Groups[0].Children[0].RoleHandle)
	missingActivity := request(t, handler, http.MethodGet, "/v1/runs/missing/children", nil, true)
	assert.Equal(t, http.StatusNotFound, missingActivity.Code)

	_, err = server.Runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	approval, err := server.Approvals.Suspend(ctx, child.ID, 1, 1, "child-digest",
		json.RawMessage(`{"version":1}`), []domain.ApprovalItem{{
			CallIndex: 0, ToolCallID: "write-call", ToolName: "write", RiskClass: domain.RiskLocalWrite,
			ArgumentsPreview: `{"path":"notes.txt"}`,
		}}, nil)
	require.NoError(t, err)

	response := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/active-run", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var active domain.ActiveRunState
	decodeData(t, response, &active)
	assert.Equal(t, submission.Run.ID, active.Run.ID)
	assert.Equal(t, domain.RunWaitingChildren, active.Run.Status)
	require.NotNil(t, active.PendingApproval)
	assert.Equal(t, child.ID, active.PendingApproval.RunID)
	require.NotNil(t, active.PendingApproval.Attribution)
	assert.Equal(t, "workspace-explorer", active.PendingApproval.Attribution.Handle)

	decision := request(t, handler, http.MethodPost, "/v1/approval-requests/"+approval.ID+"/decision",
		map[string]any{"decision": "approved", "clientRequestId": "child-decision"}, true)
	require.Equal(t, http.StatusOK, decision.Code, decision.Body.String())
	control.mu.Lock()
	assert.Equal(t, []string{child.ID}, control.enqueued)
	control.mu.Unlock()
}

func TestHealthIsUnauthenticatedAndReportsDegradedSandbox(t *testing.T) {
	_, handler := setupServer(t, nil)
	live := request(t, handler, http.MethodGet, "/v1/health/live", nil, false)
	assert.Equal(t, http.StatusOK, live.Code)
	ready := request(t, handler, http.MethodGet, "/v1/health/ready", nil, false)
	assert.Equal(t, http.StatusOK, ready.Code)
	var data struct {
		Status   string `json:"status"`
		Degraded bool   `json:"degraded"`
	}
	decodeData(t, ready, &data)
	assert.Equal(t, "ready", data.Status)
	assert.True(t, data.Degraded)
}

func TestRuntimeIdentityRequiresWorkerAuthentication(t *testing.T) {
	server, handler := setupServer(t, nil)
	server.InstanceID = "runtime-instance"

	unauthorized := request(t, handler, http.MethodGet, "/v1/runtime", nil, false)
	assert.Equal(t, http.StatusUnauthorized, unauthorized.Code)
	authorized := request(t, handler, http.MethodGet, "/v1/runtime", nil, true)
	require.Equal(t, http.StatusOK, authorized.Code, authorized.Body.String())
	var data struct {
		InstanceID string `json:"instanceId"`
	}
	decodeData(t, authorized, &data)
	assert.Equal(t, "runtime-instance", data.InstanceID)
}

func TestProviderDoctorAPIUsesExplicitModelWithoutRunPersistence(t *testing.T) {
	server, handler := setupServer(t, nil)
	doctor := &fakeDiagnoser{diagnostic: domain.ProviderDiagnostic{
		ProviderID: "provider", ModelProfileID: "model", ModelName: "test-model", Status: "ready",
		Stages: []domain.ProviderDiagnosticStage{{Name: "generation", Status: "passed", Message: "ok"}},
	}}
	server.Doctor = doctor

	response := request(t, handler, http.MethodPost, "/v1/provider-profiles/provider/test", map[string]any{"modelProfileId": "model"}, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var diagnostic domain.ProviderDiagnostic
	decodeData(t, response, &diagnostic)
	assert.Equal(t, "ready", diagnostic.Status)
	assert.Equal(t, "provider", doctor.providerID)
	assert.Equal(t, "model", doctor.modelID)
	for _, table := range []string{"agent_runs", "messages", "model_calls"} {
		var count int
		require.NoError(t, server.DB.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
		assert.Zero(t, count, table)
	}
}

func TestProviderDoctorAPIIsUnavailableWithoutService(t *testing.T) {
	_, handler := setupServer(t, nil)
	response := request(t, handler, http.MethodPost, "/v1/provider-profiles/provider/test", map[string]any{}, true)
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func apiRoleDefinition(modelID string) domain.RoleDefinition {
	return domain.RoleDefinition{
		SchemaVersion: 1, RolePrompt: "Review evidence independently.",
		ModelBinding: domain.RoleModelBinding{Mode: domain.RoleModelFixed, ModelProfileID: modelID,
			ThinkingEffort: domain.ThinkingDefault, FallbackModelProfileIDs: []string{}, OverridableFields: []string{}},
		Skills: domain.RoleSkills{Entries: []domain.RoleSkillEntry{}}, Authority: domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		ContextPolicy: domain.RoleContextPolicy{DefaultMode: domain.RoleContextRoom,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom}, OwnExecutionContinuity: domain.RoleContinuityNone},
		DelegationPolicy: domain.RoleDelegationPolicy{Admission: domain.DelegationApprovalRequired,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8,
	}
}

func TestRoleCRUDPublishAndCatalogAPI(t *testing.T) {
	server, handler := setupServer(t, nil)
	ctx := context.Background()
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Roles", HostPath: t.TempDir()})
	require.NoError(t, err)
	provider, err := server.Providers.Create(ctx, store.CreateProviderInput{Name: "Provider",
		ProviderType: domain.ProviderOpenAICompatible, BaseURL: "https://provider.test", CredentialRef: "env:ROLE_KEY"})
	require.NoError(t, err)
	model, err := server.Models.Create(ctx, store.CreateModelInput{ProviderID: provider.ID, ModelName: "role-model",
		ContextWindow: 32000, MaxOutputTokens: 2048, SupportsToolUse: true})
	require.NoError(t, err)
	definition := apiRoleDefinition(model.ID)
	created := request(t, handler, http.MethodPost, "/v1/roles", map[string]any{
		"handle": "security-reviewer", "name": "Security Reviewer", "description": "Independent review",
		"positioning": "Use after trust-boundary changes.", "icon": "shield-check", "color": "red",
		"scope": "project", "projectId": project.ID, "definition": definition,
	}, true)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var role domain.RoleIdentity
	decodeData(t, created, &role)

	catalog := request(t, handler, http.MethodGet, "/v1/roles?projectId="+project.ID+"&status=active", nil, true)
	require.Equal(t, http.StatusOK, catalog.Code, catalog.Body.String())
	assert.NotContains(t, catalog.Body.String(), "Review evidence independently")
	var page struct {
		Items []domain.RoleSummary `json:"items"`
	}
	decodeData(t, catalog, &page)
	require.Len(t, page.Items, 2)

	validated := request(t, handler, http.MethodPost, "/v1/roles/"+role.ID+"/validate", map[string]any{}, true)
	require.Equal(t, http.StatusOK, validated.Code, validated.Body.String())
	var validation domain.RoleValidationResult
	decodeData(t, validated, &validation)
	assert.True(t, validation.Valid)

	published := request(t, handler, http.MethodPost, "/v1/roles/"+role.ID+"/publish",
		map[string]any{"expectedRevision": 0}, true)
	require.Equal(t, http.StatusCreated, published.Code, published.Body.String())
	var version domain.RoleVersion
	decodeData(t, published, &version)
	assert.Equal(t, 1, version.Version)

	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Direct Role"})
	require.NoError(t, err)
	_, err = server.DB.Exec(`UPDATE settings SET value='2' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	invoked := request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/invocations", map[string]any{
		"text":   "Review authorization boundaries.",
		"target": map[string]any{"kind": "role", "objectId": role.ID, "versionId": version.ID, "contextMode": "room"},
	}, true)
	require.Equal(t, http.StatusAccepted, invoked.Code, invoked.Body.String())
	var invocation domain.TurnSubmission
	decodeData(t, invoked, &invocation)
	assert.Equal(t, domain.CommitFormatSpeakerV2, invocation.Run.CommitFormatVersion)
	assert.Contains(t, string(invocation.Run.SpeakerSnapshot), `"handle":"security-reviewer"`)
	var addresseeKind, addresseeObjectID string
	require.NoError(t, server.DB.QueryRow(`SELECT addressee_kind,addressee_object_id FROM messages WHERE id=?`,
		invocation.UserMessageID).Scan(&addresseeKind, &addresseeObjectID))
	assert.Equal(t, "role", addresseeKind)
	assert.Equal(t, role.ID, addresseeObjectID)

	definition.RolePrompt = "Review authorization boundaries."
	updated := request(t, handler, http.MethodPatch, "/v1/roles/"+role.ID+"/draft", map[string]any{
		"expectedRevision": 0, "positioning": "Updated positioning", "definition": definition,
	}, true)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	decodeData(t, updated, &role)
	assert.Equal(t, 1, role.DraftRevision)
	assert.Equal(t, "Updated positioning", role.Positioning)

	history := request(t, handler, http.MethodGet, "/v1/roles/"+role.ID+"/versions", nil, true)
	require.Equal(t, http.StatusOK, history.Code, history.Body.String())
	var versions []domain.RoleVersion
	decodeData(t, history, &versions)
	require.Len(t, versions, 1)
	assert.Equal(t, version.ID, versions[0].ID)

	archived := request(t, handler, http.MethodPost, "/v1/roles/"+role.ID+"/archive", map[string]any{}, true)
	assert.Equal(t, http.StatusOK, archived.Code, archived.Body.String())
}

func TestProfileManagementAndSessionDefaultModel(t *testing.T) {
	_, handler := setupServer(t, nil)
	literal := request(t, handler, http.MethodPost, "/v1/provider-profiles", map[string]any{
		"name": "unsafe", "providerType": "openai-compatible", "baseUrl": "https://provider.test", "credentialRef": "sk-literal",
	}, true)
	assert.Equal(t, http.StatusBadRequest, literal.Code)

	createdProvider := request(t, handler, http.MethodPost, "/v1/provider-profiles", map[string]any{
		"name": "DeepSeek", "providerType": "openai-compatible", "baseUrl": "https://api.deepseek.com", "credentialRef": "env:DEEPSEEK_API_KEY",
	}, true)
	require.Equal(t, http.StatusCreated, createdProvider.Code, createdProvider.Body.String())
	var provider domain.ProviderProfile
	decodeData(t, createdProvider, &provider)
	assert.Equal(t, "env:DEEPSEEK_API_KEY", provider.CredentialRef)

	createdModel := request(t, handler, http.MethodPost, "/v1/model-profiles", map[string]any{
		"providerId": provider.ID, "modelName": "deepseek-chat", "displayName": "DeepSeek Chat",
		"contextWindow": 64000, "maxOutputTokens": 4096, "supportsToolUse": true, "isDefault": true,
	}, true)
	require.Equal(t, http.StatusCreated, createdModel.Code, createdModel.Body.String())
	var model domain.ModelProfile
	decodeData(t, createdModel, &model)
	assert.True(t, model.IsDefault)

	listed := request(t, handler, http.MethodGet, "/v1/model-profiles", nil, true)
	require.Equal(t, http.StatusOK, listed.Code)
	var models []domain.ModelProfile
	decodeData(t, listed, &models)
	require.Len(t, models, 1)
	assert.Equal(t, model.ID, models[0].ID)
	assert.True(t, models[0].IsDefault)

	projectResponse := request(t, handler, http.MethodPost, "/v1/projects", map[string]any{
		"name": "profiles", "hostPath": t.TempDir(),
	}, true)
	var project struct {
		Project domain.Project `json:"project"`
	}
	decodeData(t, projectResponse, &project)
	sessionResponse := request(t, handler, http.MethodPost, "/v1/projects/"+project.Project.ID+"/sessions", map[string]any{
		"title": "model session", "defaultModelProfileId": model.ID,
	}, true)
	require.Equal(t, http.StatusCreated, sessionResponse.Code, sessionResponse.Body.String())
	var session domain.Session
	decodeData(t, sessionResponse, &session)
	require.NotNil(t, session.DefaultModelProfileID)
	assert.Equal(t, model.ID, *session.DefaultModelProfileID)

	cleared := request(t, handler, http.MethodPatch, "/v1/sessions/"+session.ID, map[string]any{
		"defaultModelProfileId": nil,
	}, true)
	require.Equal(t, http.StatusOK, cleared.Code, cleared.Body.String())
	session = domain.Session{}
	decodeData(t, cleared, &session)
	assert.Nil(t, session.DefaultModelProfileID)
}

func TestPolicyProfileAPIUsesStrictVersionedConfiguration(t *testing.T) {
	_, handler := setupServer(t, nil)
	created := request(t, handler, http.MethodPost, "/v1/policy-profiles", map[string]any{
		"name": "safe-tools", "kind": "tool", "config": map[string]any{
			"mode": "restricted", "allowedTools": []string{"read"},
		},
	}, true)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var profile domain.PolicyProfile
	decodeData(t, created, &profile)
	assert.Equal(t, 1, profile.Version)
	assert.Equal(t, domain.PolicyKindTool, profile.Kind)

	invalid := request(t, handler, http.MethodPost, "/v1/policy-profiles", map[string]any{
		"name": "unsafe", "kind": "tool", "config": map[string]any{"mode": "restricted", "unknown": true},
	}, true)
	assert.Equal(t, http.StatusBadRequest, invalid.Code)

	listed := request(t, handler, http.MethodGet, "/v1/policy-profiles?kind=tool", nil, true)
	require.Equal(t, http.StatusOK, listed.Code)
	var profiles []domain.PolicyProfile
	decodeData(t, listed, &profiles)
	assert.NotEmpty(t, profiles)

	deactivated := request(t, handler, http.MethodDelete, "/v1/policy-profiles/"+profile.ID, nil, true)
	assert.Equal(t, http.StatusNoContent, deactivated.Code)
}

func TestBusinessAPIRequiresBearerToken(t *testing.T) {
	_, handler := setupServer(t, nil)
	response := request(t, handler, http.MethodGet, "/v1/projects", nil, false)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Contains(t, response.Body.String(), `"code":"unauthorized"`)
	assert.NotEmpty(t, response.Header().Get("X-Request-ID"))
}

func TestProjectAndSessionCRUD(t *testing.T) {
	_, handler := setupServer(t, nil)
	created := request(t, handler, http.MethodPost, "/v1/projects", map[string]any{
		"name": "Lung", "description": "scRNA", "hostPath": t.TempDir(),
	}, true)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var result struct {
		Project   domain.Project          `json:"project"`
		Workspace domain.ProjectWorkspace `json:"workspace"`
	}
	decodeData(t, created, &result)
	assert.Equal(t, "/workspace", result.Workspace.VirtualPath)

	sessionResponse := request(t, handler, http.MethodPost, "/v1/projects/"+result.Project.ID+"/sessions", map[string]any{"title": "Analysis"}, true)
	require.Equal(t, http.StatusCreated, sessionResponse.Code, sessionResponse.Body.String())
	var session domain.Session
	decodeData(t, sessionResponse, &session)
	assert.Equal(t, result.Project.ID, session.ProjectID)

	listed := request(t, handler, http.MethodGet, "/v1/projects/"+result.Project.ID+"/sessions", nil, true)
	var sessions []domain.Session
	decodeData(t, listed, &sessions)
	require.Len(t, sessions, 1)
	assert.Equal(t, session.ID, sessions[0].ID)

	renamed := request(t, handler, http.MethodPatch, "/v1/sessions/"+session.ID, map[string]any{"title": "Renamed analysis"}, true)
	require.Equal(t, http.StatusOK, renamed.Code, renamed.Body.String())
	var renamedSession domain.Session
	decodeData(t, renamed, &renamedSession)
	assert.Equal(t, "Renamed analysis", renamedSession.Title)
}

func TestProjectWorkspaceFilesAPI(t *testing.T) {
	server, handler := setupServer(t, nil)
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("# Ennote\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o600))
	project, workspaceRecord, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "files", HostPath: root,
	})
	require.NoError(t, err)

	workspaceResponse := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/workspace", nil, true)
	require.Equal(t, http.StatusOK, workspaceResponse.Code, workspaceResponse.Body.String())
	var returnedWorkspace domain.ProjectWorkspace
	decodeData(t, workspaceResponse, &returnedWorkspace)
	assert.Equal(t, workspaceRecord.ID, returnedWorkspace.ID)

	listed := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/files?path="+url.QueryEscape("/workspace"), nil, true)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var entries []workspaceFileEntry
	decodeData(t, listed, &entries)
	require.Len(t, entries, 2)
	assert.True(t, entries[0].IsDir)
	assert.Equal(t, "/workspace/src", entries[0].Path)
	assert.Equal(t, "/workspace/README.md", entries[1].Path)
	assert.NotContains(t, listed.Body.String(), root)

	contentPath := "/v1/projects/" + project.ID + "/files/content?path=" + url.QueryEscape("/workspace/README.md")
	content := request(t, handler, http.MethodGet, contentPath, nil, true)
	require.Equal(t, http.StatusOK, content.Code, content.Body.String())
	assert.Equal(t, "# Ennote\n", content.Body.String())
	assert.Contains(t, content.Header().Get("Content-Type"), "text/markdown")
	assert.Equal(t, "nosniff", content.Header().Get("X-Content-Type-Options"))

	rangeRequest := httptest.NewRequest(http.MethodGet, contentPath, nil)
	rangeRequest.Header.Set("Authorization", "Bearer test-token")
	rangeRequest.Header.Set("Range", "bytes=0-2")
	rangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(rangeResponse, rangeRequest)
	assert.Equal(t, http.StatusPartialContent, rangeResponse.Code)
	assert.Equal(t, "# E", rangeResponse.Body.String())

	traversal := request(t, handler, http.MethodGet,
		"/v1/projects/"+project.ID+"/files/content?path="+url.QueryEscape("/workspace/../outside.txt"), nil, true)
	assert.Equal(t, http.StatusBadRequest, traversal.Code)
	assert.Contains(t, traversal.Body.String(), "workspace_path_escape")

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape.txt")); err == nil {
		symlink := request(t, handler, http.MethodGet,
			"/v1/projects/"+project.ID+"/files/content?path="+url.QueryEscape("/workspace/escape.txt"), nil, true)
		assert.Equal(t, http.StatusBadRequest, symlink.Code)
		assert.NotContains(t, symlink.Body.String(), "secret")
	}
}

func TestSessionSearchArchiveRestoreAPI(t *testing.T) {
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)
	alpha, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Alpha atlas"})
	require.NoError(t, err)
	_, err = server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Beta atlas"})
	require.NoError(t, err)

	search := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/sessions?status=active&q=alpha", nil, true)
	require.Equal(t, http.StatusOK, search.Code, search.Body.String())
	var active []domain.Session
	decodeData(t, search, &active)
	require.Len(t, active, 1)
	assert.Equal(t, alpha.ID, active[0].ID)

	archived := request(t, handler, http.MethodPost, "/v1/sessions/"+alpha.ID+"/archive", map[string]any{}, true)
	require.Equal(t, http.StatusOK, archived.Code, archived.Body.String())
	var archivedSession domain.Session
	decodeData(t, archived, &archivedSession)
	assert.Equal(t, store.SessionStatusArchived, archivedSession.Status)
	archivedList := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/sessions?status=archived&q=ALPHA", nil, true)
	var archivedValues []domain.Session
	decodeData(t, archivedList, &archivedValues)
	require.Len(t, archivedValues, 1)

	conflict := request(t, handler, http.MethodPost, "/v1/sessions/"+alpha.ID+"/archive", map[string]any{}, true)
	assert.Equal(t, http.StatusConflict, conflict.Code)
	assert.Contains(t, conflict.Body.String(), `"code":"session_state_conflict"`)
	restored := request(t, handler, http.MethodPost, "/v1/sessions/"+alpha.ID+"/restore", map[string]any{}, true)
	assert.Equal(t, http.StatusOK, restored.Code, restored.Body.String())
	invalid := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/sessions?status=all", nil, true)
	assert.Equal(t, http.StatusBadRequest, invalid.Code)
	assert.Contains(t, invalid.Body.String(), `"code":"invalid_session_search"`)

	busy, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Busy"})
	require.NoError(t, err)
	_, err = server.Runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{SessionID: busy.ID, ClientRequestID: "busy", Text: "run"})
	require.NoError(t, err)
	busyResponse := request(t, handler, http.MethodPost, "/v1/sessions/"+busy.ID+"/archive", map[string]any{}, true)
	assert.Equal(t, http.StatusConflict, busyResponse.Code)
	assert.Contains(t, busyResponse.Body.String(), `"code":"session_busy"`)
}

func TestSubmitTurnQueueSteerAndCancel(t *testing.T) {
	controller := &fakeController{}
	_, handler := setupServer(t, controller)
	project := request(t, handler, http.MethodPost, "/v1/projects", map[string]any{"name": "P", "hostPath": t.TempDir()}, true)
	var created struct {
		Project domain.Project `json:"project"`
	}
	decodeData(t, project, &created)
	sessionResponse := request(t, handler, http.MethodPost, "/v1/projects/"+created.Project.ID+"/sessions", map[string]any{"title": "S"}, true)
	var session domain.Session
	decodeData(t, sessionResponse, &session)

	turnReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session.ID+"/turns", strings.NewReader(`{"text":"start"}`))
	turnReq.Header.Set("Authorization", "Bearer test-token")
	turnReq.Header.Set("Content-Type", "application/json")
	turnReq.Header.Set("Idempotency-Key", "turn-1")
	turnResponse := httptest.NewRecorder()
	handler.ServeHTTP(turnResponse, turnReq)
	require.Equal(t, http.StatusAccepted, turnResponse.Code, turnResponse.Body.String())
	var submission domain.TurnSubmission
	decodeData(t, turnResponse, &submission)
	require.Len(t, controller.enqueued, 1)
	assert.Equal(t, submission.Run.ID, controller.enqueued[0])

	queued := request(t, handler, http.MethodPost, "/v1/runs/"+submission.Run.ID+"/inputs", map[string]any{
		"kind": "steer", "text": "change direction", "clientRequestId": "steer-1",
	}, true)
	require.Equal(t, http.StatusAccepted, queued.Code, queued.Body.String())
	var item domain.QueuedInput
	decodeData(t, queued, &item)
	assert.Equal(t, domain.QueuedInputSteer, item.Kind)

	cancelled := request(t, handler, http.MethodPost, "/v1/runs/"+submission.Run.ID+"/cancel", map[string]any{}, true)
	require.Equal(t, http.StatusOK, cancelled.Code, cancelled.Body.String())
	assert.Equal(t, []string{submission.Run.ID}, controller.cancelled)
}

func TestBranchNavigationAPIAndRunRecovery(t *testing.T) {
	control := &fakeController{}
	server, handler := setupServer(t, control)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Recovery", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	root, err := server.Messages.CreateUserMessage(context.Background(), session.ID, "", "root")
	require.NoError(t, err)
	leaf, err := server.Messages.CreateUserMessage(context.Background(), session.ID, root.ID, "leaf")
	require.NoError(t, err)
	require.NoError(t, server.Sessions.ActivateLeaf(context.Background(), session.ID, leaf.ID))
	mainID := *session.ActiveBranchID

	listed := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/branches", nil, true)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var initial []domain.SessionBranch
	decodeData(t, listed, &initial)
	require.Len(t, initial, 1)

	created := request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/branches",
		map[string]any{"fromMessageId": root.ID}, true)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var navigation domain.BranchNavigation
	decodeData(t, created, &navigation)
	require.Len(t, navigation.Branches, 2)
	assert.Equal(t, root.ID, *navigation.Session.ActiveLeafMessageID)

	activated := request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/branches/"+mainID+"/activate", map[string]any{}, true)
	require.Equal(t, http.StatusOK, activated.Code, activated.Body.String())
	decodeData(t, activated, &navigation)
	assert.Equal(t, leaf.ID, *navigation.Session.ActiveLeafMessageID)

	submission, err := server.Runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "failed", Text: "retry me",
	})
	require.NoError(t, err)
	_, err = server.Runs.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, server.Runs.Fail(context.Background(), submission.Run.ID, "provider_unavailable", "temporary"))

	recoveryResponse := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/recovery", nil, true)
	require.Equal(t, http.StatusOK, recoveryResponse.Code, recoveryResponse.Body.String())
	var recovery domain.RunRecovery
	decodeData(t, recoveryResponse, &recovery)
	assert.True(t, recovery.Retryable)

	retried := request(t, handler, http.MethodPost, "/v1/runs/"+submission.Run.ID+"/retry",
		map[string]any{"clientRequestId": "retry-api"}, true)
	require.Equal(t, http.StatusAccepted, retried.Code, retried.Body.String())
	var retry domain.RunRetrySubmission
	decodeData(t, retried, &retry)
	assert.Equal(t, 2, retry.Run.Attempt)
	control.mu.Lock()
	assert.Equal(t, []string{retry.Run.ID}, control.enqueued)
	control.mu.Unlock()

	replayed := request(t, handler, http.MethodPost, "/v1/runs/"+submission.Run.ID+"/retry",
		map[string]any{"clientRequestId": "retry-api"}, true)
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	control.mu.Lock()
	assert.Len(t, control.enqueued, 1)
	control.mu.Unlock()

	blocked := request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/branches",
		map[string]any{"fromMessageId": root.ID}, true)
	assert.Equal(t, http.StatusConflict, blocked.Code)
	assert.Contains(t, blocked.Body.String(), string(domain.ErrorSessionBusy))
}

func TestManualCompactionAPIIsIdempotentAndListable(t *testing.T) {
	controller := &fakeController{}
	server, handler := setupServer(t, controller)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	message, err := (&store.MessageRepo{DB: server.DB}).CreateUserMessage(context.Background(), session.ID, "", "history")
	require.NoError(t, err)
	require.NoError(t, server.Sessions.ActivateLeaf(context.Background(), session.ID, message.ID))
	body := map[string]any{"baseMessageId": message.ID, "clientRequestId": "compact-api", "instructions": "keep sample ids"}

	created := request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/compactions", body, true)
	require.Equal(t, http.StatusAccepted, created.Code, created.Body.String())
	var first domain.CompactionSubmission
	decodeData(t, created, &first)
	require.Len(t, controller.enqueued, 1)
	assert.Equal(t, first.RunID, controller.enqueued[0])

	replayed := request(t, handler, http.MethodPost, "/v1/sessions/"+session.ID+"/compactions", body, true)
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	var second domain.CompactionSubmission
	decodeData(t, replayed, &second)
	assert.Equal(t, first.CompactionID, second.CompactionID)
	assert.True(t, second.Existing)

	listed := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/compactions", nil, true)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	var checkpoints []domain.ContextCompaction
	decodeData(t, listed, &checkpoints)
	require.Len(t, checkpoints, 1)
	assert.Equal(t, first.CompactionID, checkpoints[0].ID)
	assert.Empty(t, checkpoints[0].Summary)
}

func TestSessionMessagesAPIIsBranchAwareAndPaginated(t *testing.T) {
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "History", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	var lineage []*domain.Message
	parentID := ""
	for _, text := range []string{"one", "two", "three"} {
		message, createErr := server.Messages.CreateUserMessage(context.Background(), session.ID, parentID, text)
		require.NoError(t, createErr)
		lineage = append(lineage, message)
		parentID = message.ID
	}
	require.NoError(t, server.Sessions.ActivateLeaf(context.Background(), session.ID, lineage[2].ID))

	first := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/messages?limit=2", nil, true)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	var firstPage struct {
		Messages            []domain.Message `json:"messages"`
		NextCursor          string           `json:"nextCursor"`
		HasMore             bool             `json:"hasMore"`
		ActiveLeafMessageID string           `json:"activeLeafMessageId"`
	}
	decodeData(t, first, &firstPage)
	require.Len(t, firstPage.Messages, 2)
	assert.Equal(t, []string{lineage[1].ID, lineage[2].ID}, messageIDsForAPI(firstPage.Messages))
	assert.True(t, firstPage.HasMore)
	assert.NotEmpty(t, firstPage.NextCursor)
	assert.Equal(t, lineage[2].ID, firstPage.ActiveLeafMessageID)
	assert.Equal(t, domain.ContentText, firstPage.Messages[0].Parts[0].Kind)
	assert.Equal(t, domain.SpeakerUser, firstPage.Messages[0].SpeakerKind)
	assert.Equal(t, domain.VisibilityPublic, firstPage.Messages[0].Visibility)
	require.NotNil(t, firstPage.Messages[0].AddresseeKind)
	assert.Equal(t, "host", *firstPage.Messages[0].AddresseeKind)
	assert.Contains(t, first.Body.String(), `"type":"text"`)
	assert.NotContains(t, first.Body.String(), `"Kind"`)

	second := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/messages?limit=2&before="+firstPage.NextCursor, nil, true)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	var secondPage struct {
		Messages []domain.Message `json:"messages"`
		HasMore  bool             `json:"hasMore"`
	}
	decodeData(t, second, &secondPage)
	require.Len(t, secondPage.Messages, 1)
	assert.Equal(t, lineage[0].ID, secondPage.Messages[0].ID)
	assert.False(t, secondPage.HasMore)

	malformed := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/messages?before=not-a-cursor", nil, true)
	assert.Equal(t, http.StatusBadRequest, malformed.Code)
	assert.Contains(t, malformed.Body.String(), "invalid_message_cursor")

	invalidLimit := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/messages?limit=101", nil, true)
	assert.Equal(t, http.StatusBadRequest, invalidLimit.Code)
	assert.Contains(t, invalidLimit.Body.String(), "invalid_message_limit")

	missing := request(t, handler, http.MethodGet, "/v1/sessions/missing/messages", nil, true)
	assert.Equal(t, http.StatusNotFound, missing.Code)

	emptyCheckpoints := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/compactions", nil, true)
	assert.JSONEq(t, `{"data":[]}`, emptyCheckpoints.Body.String())
}

func TestSessionMessagesAPIRejectsCursorFromSiblingBranch(t *testing.T) {
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Branches", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	root, err := server.Messages.CreateUserMessage(context.Background(), session.ID, "", "root")
	require.NoError(t, err)
	active, err := server.Messages.CreateUserMessage(context.Background(), session.ID, root.ID, "active")
	require.NoError(t, err)
	sibling, err := server.Messages.CreateUserMessage(context.Background(), session.ID, root.ID, "sibling")
	require.NoError(t, err)
	require.NoError(t, server.Sessions.ActivateLeaf(context.Background(), session.ID, active.ID))

	cursor, err := encodeMessageCursor(session.ID, sibling.ID)
	require.NoError(t, err)
	response := request(t, handler, http.MethodGet, "/v1/sessions/"+session.ID+"/messages?before="+cursor, nil, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), "invalid_message_cursor")
}

func TestRunAPIExposesSafeFrozenPromptMetadata(t *testing.T) {
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{
		Name: "Prompt metadata", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	submission, err := server.Runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "prompt-metadata", Text: "hello",
	})
	require.NoError(t, err)
	_, err = server.DB.Exec(`UPDATE agent_runs SET system_prompt_snapshot_json=?,system_prompt_digest=? WHERE id=?`,
		`{"version":1,"agentProfileId":"agent-safe","agentPrompt":"private prompt body","platformVersion":"host-v1","digest":"digest-safe"}`,
		"digest-safe", submission.Run.ID)
	require.NoError(t, err)

	response := request(t, handler, http.MethodGet, "/v1/runs/"+submission.Run.ID, nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	metadata, ok := envelope.Data["systemPrompt"].(map[string]any)
	require.True(t, ok, response.Body.String())
	assert.Equal(t, "digest-safe", metadata["digest"])
	assert.Equal(t, "agent-safe", metadata["agentProfileId"])
	assert.Equal(t, "host-v1", metadata["platformVersion"])
	assert.NotContains(t, response.Body.String(), "private prompt body")
	assert.NotContains(t, response.Body.String(), "agentPrompt")
	assert.NotContains(t, response.Body.String(), "system_prompt_snapshot_json")
}

func TestRunMessagesAPIExposesPrivateShadowAndLegacyFallback(t *testing.T) {
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Transcript", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	assert.Equal(t, domain.SessionModeHosted, session.Mode)
	_, err = server.DB.Exec(`UPDATE settings SET value='1' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	submission, err := server.Runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "transcript", Text: "run",
	})
	require.NoError(t, err)
	_, err = server.Runs.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, server.Runs.FinalizeSuccess(context.Background(), submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "first"}}},
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "final"}}},
	}}))

	response := request(t, handler, http.MethodGet, "/v1/runs/"+submission.Run.ID+"/messages?limit=1", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var page struct {
		RunID             string              `json:"runId"`
		FormatVersion     int                 `json:"formatVersion"`
		Source            string              `json:"source"`
		Messages          []domain.RunMessage `json:"messages"`
		HasMore           bool                `json:"hasMore"`
		NextBeforeOrdinal int                 `json:"nextBeforeOrdinal"`
	}
	decodeData(t, response, &page)
	assert.Equal(t, submission.Run.ID, page.RunID)
	assert.Equal(t, 1, page.FormatVersion)
	assert.Equal(t, "shadow", page.Source)
	require.Len(t, page.Messages, 1)
	assert.Equal(t, 1, page.Messages[0].Ordinal)
	assert.True(t, page.HasMore)

	_, err = server.DB.Exec(`DELETE FROM run_messages WHERE run_id=?`, submission.Run.ID)
	require.NoError(t, err)
	legacy := request(t, handler, http.MethodGet, "/v1/runs/"+submission.Run.ID+"/messages", nil, true)
	require.Equal(t, http.StatusOK, legacy.Code, legacy.Body.String())
	decodeData(t, legacy, &page)
	assert.Equal(t, "legacy", page.Source)

	_, err = server.DB.Exec(`INSERT INTO run_messages(id,run_id,ordinal,role,payload_json,visibility,created_at)
		VALUES('corrupt',?,0,'assistant','[{"type":"text","text":"private-secret"}]','private','2026-08-03T00:00:00Z')`, submission.Run.ID)
	require.NoError(t, err)
	corrupt := request(t, handler, http.MethodGet, "/v1/runs/"+submission.Run.ID+"/messages", nil, true)
	assert.Equal(t, http.StatusInternalServerError, corrupt.Code)
	assert.NotContains(t, corrupt.Body.String(), "private-secret")

	missing := request(t, handler, http.MethodGet, "/v1/runs/missing/messages", nil, true)
	assert.Equal(t, http.StatusNotFound, missing.Code)
}

func messageIDsForAPI(messages []domain.Message) []string {
	ids := make([]string, len(messages))
	for index := range messages {
		ids[index] = messages[index].ID
	}
	return ids
}

func TestSSEReplaysAfterCursorWithoutDuplicates(t *testing.T) {
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := server.Sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	submission, err := server.Runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "turn", Text: "start"})
	require.NoError(t, err)
	_, err = server.Runs.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	_, err = server.Events.Append(context.Background(), submission.Run.ID,
		domain.PendingEvent{EventType: "text_delta", Payload: json.RawMessage(`{"text":"hi"}`)},
		domain.PendingEvent{EventType: "tool_call_skipped", Payload: json.RawMessage(`{"toolCallId":"call-1","toolName":"read","reason":"output_truncated","argumentsFragment":"{\\\"path\\\":"}`)},
	)
	require.NoError(t, err)
	require.NoError(t, server.Runs.Succeed(context.Background(), submission.Run.ID))
	all, err := server.Events.After(context.Background(), submission.Run.ID, 0, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 4)

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+submission.Run.ID+"/events", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Last-Event-ID", strconv.FormatInt(all[0].EventID, 10))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	assert.NotContains(t, body, "id: "+strconv.FormatInt(all[0].EventID, 10)+"\n")
	for _, event := range all[1:] {
		assert.Contains(t, body, "id: "+strconv.FormatInt(event.EventID, 10)+"\n")
	}
	assert.Contains(t, body, `"type":"tool_call_skipped"`)
	assert.Contains(t, body, `"argumentsFragment":"{\\\"path\\\":"`)
	assert.Contains(t, body, `"type":"run_succeeded"`)
}

func TestUnknownJSONFieldIsRejected(t *testing.T) {
	_, handler := setupServer(t, nil)
	response := request(t, handler, http.MethodPost, "/v1/projects", map[string]any{"name": "P", "hostPath": t.TempDir(), "unknown": true}, true)
	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), "invalid_json")
}

func TestFlowScopedRoleAPI(t *testing.T) {
	server, handler := setupServer(t, nil)
	ctx := context.Background()
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "FlowRoles", HostPath: t.TempDir()})
	require.NoError(t, err)
	provider, err := server.Providers.Create(ctx, store.CreateProviderInput{Name: "Provider",
		ProviderType: domain.ProviderOpenAICompatible, BaseURL: "https://provider.test", CredentialRef: "env:FR_KEY"})
	require.NoError(t, err)
	model, err := server.Models.Create(ctx, store.CreateModelInput{ProviderID: provider.ID, ModelName: "fr-model",
		ContextWindow: 32000, MaxOutputTokens: 2048, SupportsToolUse: true})
	require.NoError(t, err)
	profile, err := (&store.AgentFlowProfileRepo{DB: server.DB}).CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "FR Graph", Slug: "fr-graph", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)

	definition := apiRoleDefinition(model.ID)
	created := request(t, handler, http.MethodPost, "/v1/roles", map[string]any{
		"handle": "graph-local", "name": "Graph Local", "description": "inside the graph only",
		"positioning": "", "icon": "bot", "color": "neutral",
		"scope": "flow", "flowId": profile.ID, "definition": definition,
	}, true)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var role domain.RoleIdentity
	decodeData(t, created, &role)
	require.NotNil(t, role.FlowID)
	assert.Equal(t, profile.ID, *role.FlowID)
	assert.Equal(t, domain.RoleScopeFlow, role.Scope)

	// Generic project catalog hides the flow role.
	catalog := request(t, handler, http.MethodGet, "/v1/roles?projectId="+project.ID+"&status=active", nil, true)
	require.Equal(t, http.StatusOK, catalog.Code, catalog.Body.String())
	assert.NotContains(t, catalog.Body.String(), "graph-local")
	// Flow-scoped listing returns it.
	flowCatalog := request(t, handler, http.MethodGet, "/v1/roles?scope=flow&flowId="+profile.ID+"&status=active", nil, true)
	require.Equal(t, http.StatusOK, flowCatalog.Code, flowCatalog.Body.String())
	assert.Contains(t, flowCatalog.Body.String(), "graph-local")

	// Missing flowId on a flow role is rejected by the store contract.
	bad := request(t, handler, http.MethodPost, "/v1/roles", map[string]any{
		"handle": "bad-flow-role", "name": "Bad", "description": "",
		"positioning": "", "icon": "bot", "color": "neutral",
		"scope": "flow", "definition": definition,
	}, true)
	require.Equal(t, http.StatusBadRequest, bad.Code, bad.Body.String())
}
