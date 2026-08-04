package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/prompts"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

type RunController interface {
	Enqueue(context.Context, string) error
	Cancel(context.Context, string) error
}

type Server struct {
	DB                  *sql.DB
	Token               string
	Sandbox             string
	Projects            *store.ProjectRepo
	Providers           *store.ProviderRepo
	Models              *store.ModelRepo
	Roles               *store.RoleRepo
	Doctor              ProviderDiagnoser
	Policies            *store.PolicyRepo
	Artifacts           *artifacts.Service
	Sessions            *store.SessionRepo
	Branches            *store.BranchRepo
	Messages            *store.MessageRepo
	Compactions         *store.CompactionRepo
	Approvals           *store.ApprovalRepo
	StandingApprovals   *store.StandingApprovalRepo
	Delegations         *store.DelegationRepo
	DelegationApprovals *store.DelegationApprovalRepo
	Attention           *store.AttentionRepo
	Runs                *store.RunRepo
	Queue               *store.QueueRepo
	Events              *store.EventRepo
	Hub                 *events.Hub
	Control             RunController
	InstanceID          string
	PromptGate          PromptHookGate
	Prompts             *prompts.Service
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health/live", s.live)
	mux.HandleFunc("GET /v1/health/ready", s.ready)
	mux.HandleFunc("GET /v1/runtime", s.runtimeInfo)
	mux.HandleFunc("GET /v1/provider-profiles", s.listProviderProfiles)
	mux.HandleFunc("POST /v1/provider-profiles", s.createProviderProfile)
	mux.HandleFunc("POST /v1/provider-profiles/{providerID}/test", s.testProviderProfile)
	mux.HandleFunc("GET /v1/model-profiles", s.listModelProfiles)
	mux.HandleFunc("POST /v1/model-profiles", s.createModelProfile)
	mux.HandleFunc("PUT /v1/model-profiles/{modelID}/default", s.setDefaultModelProfile)
	mux.HandleFunc("GET /v1/policy-profiles", s.listPolicyProfiles)
	mux.HandleFunc("GET /v1/roles", s.listRoles)
	mux.HandleFunc("POST /v1/roles", s.createRole)
	mux.HandleFunc("GET /v1/roles/{roleID}", s.getRole)
	mux.HandleFunc("PATCH /v1/roles/{roleID}/draft", s.updateRoleDraft)
	mux.HandleFunc("POST /v1/roles/{roleID}/validate", s.validateRole)
	mux.HandleFunc("POST /v1/roles/{roleID}/publish", s.publishRole)
	mux.HandleFunc("POST /v1/roles/{roleID}/archive", s.archiveRole)
	mux.HandleFunc("GET /v1/roles/{roleID}/versions", s.listRoleVersions)
	mux.HandleFunc("GET /v1/roles/{roleID}/versions/{versionID}", s.getRoleVersion)
	mux.HandleFunc("POST /v1/policy-profiles", s.createPolicyProfile)
	mux.HandleFunc("PUT /v1/policy-profiles/{policyID}/default", s.setDefaultPolicyProfile)
	mux.HandleFunc("DELETE /v1/policy-profiles/{policyID}", s.deactivatePolicyProfile)
	mux.HandleFunc("GET /v1/projects", s.listProjects)
	mux.HandleFunc("POST /v1/projects", s.createProject)
	mux.HandleFunc("GET /v1/projects/{projectID}/workspace", s.getProjectWorkspace)
	mux.HandleFunc("GET /v1/projects/{projectID}/files", s.listProjectFiles)
	mux.HandleFunc("GET /v1/projects/{projectID}/files/content", s.readProjectFile)
	mux.HandleFunc("GET /v1/projects/{projectID}/sessions", s.listSessions)
	mux.HandleFunc("POST /v1/projects/{projectID}/sessions", s.createSession)
	mux.HandleFunc("POST /v1/projects/{projectID}/attachments/images", s.uploadImage)
	mux.HandleFunc("GET /v1/sessions/{sessionID}", s.getSession)
	mux.HandleFunc("PATCH /v1/sessions/{sessionID}", s.updateSession)
	mux.HandleFunc("POST /v1/sessions/{sessionID}/archive", s.archiveSession)
	mux.HandleFunc("POST /v1/sessions/{sessionID}/restore", s.restoreSession)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/messages", s.listSessionMessages)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/artifacts/{artifactID}", s.getArtifact)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/artifacts/{artifactID}/preview", s.previewArtifact)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/artifacts/{artifactID}/download", s.downloadArtifact)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/branches", s.listSessionBranches)
	mux.HandleFunc("POST /v1/sessions/{sessionID}/branches", s.createSessionBranch)
	mux.HandleFunc("POST /v1/sessions/{sessionID}/branches/{branchID}/activate", s.activateSessionBranch)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/recovery", s.getSessionRecovery)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/active-run", s.getSessionActiveRun)
	mux.HandleFunc("POST /v1/sessions/{sessionID}/turns", s.submitTurn)
	mux.HandleFunc("POST /v1/sessions/{sessionID}/invocations", s.submitInvocation)
	mux.HandleFunc("POST /v1/sessions/{sessionID}/compactions", s.createCompaction)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/compactions", s.listCompactions)
	mux.HandleFunc("GET /v1/compactions/{compactionID}", s.getCompaction)
	mux.HandleFunc("GET /v1/runs/{runID}", s.getRun)
	mux.HandleFunc("GET /v1/runs/{runID}/messages", s.listRunMessages)
	mux.HandleFunc("GET /v1/runs/{runID}/children", s.listRunChildren)
	mux.HandleFunc("GET /v1/delegations/{groupID}", s.inspectDelegation)
	mux.HandleFunc("POST /v1/delegations/{groupID}/retry", s.retryDelegation)
	mux.HandleFunc("POST /v1/delegations/{groupID}/cancel", s.cancelDelegation)
	mux.HandleFunc("GET /v1/delegation-handles/{handleID}", s.getDelegationHandle)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/delegation-handles", s.listSessionDelegationHandles)
	mux.HandleFunc("GET /v1/delivery-events", s.streamDeliveryEvents)
	mux.HandleFunc("GET /v1/attention", s.listAttention)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/attention", s.listSessionAttention)
	mux.HandleFunc("POST /v1/attention/{attentionID}/dismiss", s.dismissAttention)
	mux.HandleFunc("POST /v1/delegation-approvals/{approvalID}/decision", s.decideDelegationApproval)
	mux.HandleFunc("POST /v1/runs/{runID}/retry", s.retryRun)
	mux.HandleFunc("POST /v1/runs/{runID}/cancel", s.cancelRun)
	mux.HandleFunc("POST /v1/runs/{runID}/inputs", s.queueInput)
	mux.HandleFunc("POST /v1/approval-requests/{approvalID}/decision", s.decideApproval)
	mux.HandleFunc("GET /v1/sessions/{sessionID}/standing-approvals", s.listStandingApprovals)
	mux.HandleFunc("POST /v1/sessions/{sessionID}/standing-approvals/{ruleID}/revoke", s.revokeStandingApproval)
	mux.HandleFunc("GET /v1/runs/{runID}/events", s.streamEvents)

	// Prompt templates.
	mux.HandleFunc("GET /v1/projects/{projectID}/prompt-templates", s.listPromptTemplates)
	mux.HandleFunc("POST /v1/projects/{projectID}/prompt-templates/expand", s.expandPromptTemplate)
	mux.HandleFunc("GET /v1/prompt-templates", s.listGlobalPromptTemplates)
	mux.HandleFunc("POST /v1/prompt-templates", s.createPromptTemplate)
	mux.HandleFunc("GET /v1/prompt-templates/{name}", s.getPromptTemplate)
	mux.HandleFunc("PUT /v1/prompt-templates/{name}", s.updatePromptTemplate)
	mux.HandleFunc("DELETE /v1/prompt-templates/{name}", s.deletePromptTemplate)

	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = newID()
		}
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
		if !strings.HasPrefix(r.URL.Path, "/v1/health/") {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if s.Token == "" || len(provided) != len(s.Token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.Token)) != 1 {
				writeError(w, r, http.StatusUnauthorized, "unauthorized", "worker authentication failed", false)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, map[string]any{"status": "live"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil || s.DB.PingContext(r.Context()) != nil {
		writeError(w, r, http.StatusServiceUnavailable, "database_unavailable", "worker database is unavailable", true)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"status": "ready", "sandbox": s.Sandbox,
		"degraded": s.Sandbox == "none",
	})
}

func (s *Server) runtimeInfo(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, map[string]any{"instanceId": s.InstanceID})
}

func (s *Server) listProviderProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.Providers.List(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, profiles)
}

func (s *Server) createProviderProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name          string              `json:"name"`
		ProviderType  domain.ProviderType `json:"providerType"`
		BaseURL       string              `json:"baseUrl"`
		CredentialRef string              `json:"credentialRef"`
		Proxy         string              `json:"proxy"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	profile, err := s.Providers.Create(r.Context(), store.CreateProviderInput{
		Name: input.Name, ProviderType: input.ProviderType, BaseURL: input.BaseURL,
		CredentialRef: input.CredentialRef, Proxy: input.Proxy,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_provider_profile", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, profile)
}

func (s *Server) listModelProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.Models.List(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, profiles)
}

func (s *Server) createModelProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProviderID                    string                  `json:"providerId"`
		ModelName                     string                  `json:"modelName"`
		DisplayName                   string                  `json:"displayName"`
		ContextWindow                 int                     `json:"contextWindow"`
		MaxOutputTokens               int                     `json:"maxOutputTokens"`
		InputCostUSDMicrosPerMillion  int64                   `json:"inputCostUsdMicrosPerMillion"`
		OutputCostUSDMicrosPerMillion int64                   `json:"outputCostUsdMicrosPerMillion"`
		SupportsVision                bool                    `json:"supportsVision"`
		SupportsToolUse               bool                    `json:"supportsToolUse"`
		SupportsThinking              bool                    `json:"supportsThinking"`
		ThinkingDialect               domain.ThinkingDialect  `json:"thinkingDialect"`
		SupportedThinkingEfforts      []domain.ThinkingEffort `json:"supportedThinkingEfforts"`
		IsDefault                     bool                    `json:"isDefault"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	profile, err := s.Models.Create(r.Context(), store.CreateModelInput{
		ProviderID: input.ProviderID, ModelName: input.ModelName, DisplayName: input.DisplayName,
		ContextWindow: input.ContextWindow, MaxOutputTokens: input.MaxOutputTokens,
		InputCostUSDMicrosPerMillion:  input.InputCostUSDMicrosPerMillion,
		OutputCostUSDMicrosPerMillion: input.OutputCostUSDMicrosPerMillion,
		SupportsVision:                input.SupportsVision, SupportsToolUse: input.SupportsToolUse,
		SupportsThinking: input.SupportsThinking, ThinkingDialect: input.ThinkingDialect,
		SupportedThinkingEfforts: input.SupportedThinkingEfforts, IsDefault: input.IsDefault,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_model_profile", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, profile)
}

func (s *Server) setDefaultModelProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.Models.SetDefault(r.Context(), r.PathValue("modelID")); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_model_profile", err.Error(), false)
		return
	}
	profiles, err := s.Models.List(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	for _, profile := range profiles {
		if profile.ID == r.PathValue("modelID") {
			writeData(w, http.StatusOK, profile)
			return
		}
	}
	writeError(w, r, http.StatusNotFound, "model_profile_not_found", "model profile not found", false)
}

func (s *Server) listPolicyProfiles(w http.ResponseWriter, r *http.Request) {
	kind := domain.PolicyKind(r.URL.Query().Get("kind"))
	profiles, err := s.Policies.List(r.Context(), kind)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, profiles)
}

func (s *Server) createPolicyProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   string            `json:"name"`
		Kind   domain.PolicyKind `json:"kind"`
		Config json.RawMessage   `json:"config"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	profile, err := s.Policies.CreateVersion(r.Context(), store.CreatePolicyInput{
		Name: input.Name, Kind: input.Kind, Config: input.Config})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_policy_profile", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, profile)
}

func (s *Server) setDefaultPolicyProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.Policies.SetDefault(r.Context(), r.PathValue("policyID")); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_policy_profile", err.Error(), false)
		return
	}
	profile, err := s.Policies.FindByID(r.Context(), r.PathValue("policyID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "policy_profile_not_found", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, profile)
}

func (s *Server) deactivatePolicyProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.Policies.Deactivate(r.Context(), r.PathValue("policyID")); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrPolicyNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, r, status, "policy_profile_not_found", err.Error(), false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact_service_unavailable", "artifact service is unavailable", true)
		return
	}
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_image", err.Error(), false)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_image", "file is required", false)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 12<<20))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_image", err.Error(), false)
		return
	}
	artifact, err := s.Artifacts.StoreImage(r.Context(), r.PathValue("projectID"), r.FormValue("sessionId"), header.Filename, data)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_image", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, artifact)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Projects.List(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, projects)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		HostPath    string `json:"hostPath"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.HostPath) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_project", "name and hostPath are required", false)
		return
	}
	project, workspace, err := s.Projects.CreateWithWorkspace(r.Context(), domain.CreateProjectInput{
		Name: input.Name, Description: input.Description, HostPath: input.HostPath,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"project": project, "workspace": workspace})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = store.SessionStatusActive
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	sessions, err := s.Sessions.SearchByProject(r.Context(), r.PathValue("projectID"), status, query)
	if err != nil {
		if errors.Is(err, store.ErrSessionSearchInvalid) {
			writeError(w, r, http.StatusBadRequest, "invalid_session_search", err.Error(), false)
			return
		}
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, sessions)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title                     string  `json:"title"`
		DefaultModelProfileID     *string `json:"defaultModelProfileId"`
		CompactionPolicyProfileID *string `json:"compactionPolicyProfileId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	session, err := s.Sessions.Create(r.Context(), domain.CreateSessionInput{
		ProjectID: r.PathValue("projectID"), Title: input.Title,
		DefaultModelProfileID:     input.DefaultModelProfileID,
		CompactionPolicyProfileID: input.CompactionPolicyProfileID,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_session", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, session)
}

func (s *Server) updateSession(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title                     *string         `json:"title"`
		DefaultModelProfileID     json.RawMessage `json:"defaultModelProfileId"`
		CompactionPolicyProfileID json.RawMessage `json:"compactionPolicyProfileId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Title == nil && len(input.DefaultModelProfileID) == 0 && len(input.CompactionPolicyProfileID) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_session", "a session update is required", false)
		return
	}
	var session *domain.Session
	var err error
	if input.Title != nil {
		session, err = s.Sessions.UpdateTitle(r.Context(), r.PathValue("sessionID"), *input.Title)
	}
	if err == nil && len(input.DefaultModelProfileID) > 0 {
		modelID, decodeErr := nullableString(input.DefaultModelProfileID)
		if decodeErr != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_session", "defaultModelProfileId must be a string or null", false)
			return
		}
		session, err = s.Sessions.UpdateDefaultModel(r.Context(), r.PathValue("sessionID"), modelID)
	}
	if err == nil && len(input.CompactionPolicyProfileID) > 0 {
		policyID, decodeErr := nullableString(input.CompactionPolicyProfileID)
		if decodeErr != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_session", "compactionPolicyProfileId must be a string or null", false)
			return
		}
		session, err = s.Sessions.UpdateCompactionPolicy(r.Context(), r.PathValue("sessionID"), policyID)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_session", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, session)
}

func (s *Server) archiveSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.Sessions.Archive(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, session)
}

func (s *Server) restoreSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.Sessions.Restore(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, session)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	session, err := s.Sessions.FindByID(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if session == nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", "session not found", false)
		return
	}
	writeData(w, http.StatusOK, session)
}

func (s *Server) listSessionMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	session, err := s.Sessions.FindByID(r.Context(), sessionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if session == nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", "session not found", false)
		return
	}

	limit := 50
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_message_limit", "limit must be between 1 and 100", false)
			return
		}
		limit = parsed
	}

	beforeMessageID := ""
	if rawCursor := r.URL.Query().Get("before"); rawCursor != "" {
		cursor, decodeErr := decodeMessageCursor(rawCursor)
		if decodeErr != nil || cursor.SessionID != sessionID {
			writeError(w, r, http.StatusBadRequest, "invalid_message_cursor", "message cursor is invalid for this session", false)
			return
		}
		beforeMessageID = cursor.MessageID
	}

	leafID := ""
	if session.ActiveLeafMessageID != nil {
		leafID = *session.ActiveLeafMessageID
	}
	page, err := s.Messages.Page(r.Context(), sessionID, leafID, beforeMessageID, limit)
	if err != nil {
		if errors.Is(err, store.ErrMessageCursorInvalid) {
			writeError(w, r, http.StatusBadRequest, "invalid_message_cursor", "message cursor is not on the active session branch", false)
			return
		}
		writeInternal(w, r, err)
		return
	}

	nextCursor := ""
	if page.HasMore {
		nextCursor, err = encodeMessageCursor(sessionID, page.NextBeforeMessageID)
		if err != nil {
			writeInternal(w, r, err)
			return
		}
	}
	writeData(w, http.StatusOK, struct {
		Messages            []domain.Message `json:"messages"`
		NextCursor          string           `json:"nextCursor,omitempty"`
		HasMore             bool             `json:"hasMore"`
		ActiveLeafMessageID string           `json:"activeLeafMessageId,omitempty"`
	}{page.Messages, nextCursor, page.HasMore, leafID})
}

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact_service_unavailable", "artifact service is unavailable", true)
		return
	}
	artifact, err := s.Artifacts.GetForSession(r.Context(), r.PathValue("artifactID"), r.PathValue("sessionID"))
	if err != nil {
		s.writeArtifactError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, artifact)
}

func (s *Server) previewArtifact(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact_service_unavailable", "artifact service is unavailable", true)
		return
	}
	artifact, err := s.Artifacts.GetForSession(r.Context(), r.PathValue("artifactID"), r.PathValue("sessionID"))
	if err != nil {
		s.writeArtifactError(w, r, err)
		return
	}
	setArtifactHeaders(w)
	switch artifact.Kind {
	case domain.ArtifactKindImage, "image_attachment":
		artifact, data, err := s.Artifacts.ReadForSession(r.Context(), artifact.ID, r.PathValue("sessionID"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", artifact.MIMEType)
		w.Header().Set("Content-Disposition", contentDisposition("inline", artifact.Name))
		http.ServeContent(w, r, artifact.Name, artifact.CreatedAt, bytes.NewReader(data))
	case domain.ArtifactKindTable:
		_, preview, err := s.Artifacts.PreviewTable(r.Context(), artifact.ID, r.PathValue("sessionID"), r.URL.Query().Get("sheet"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, preview)
	case domain.ArtifactKindStaticHTML:
		_, preview, err := s.Artifacts.PreviewHTML(r.Context(), artifact.ID, r.PathValue("sessionID"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", contentDisposition("inline", artifact.Name))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, preview)
	case domain.ArtifactKindText:
		_, preview, err := s.Artifacts.PreviewText(r.Context(), artifact.ID, r.PathValue("sessionID"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, preview)
	default:
		writeError(w, r, http.StatusUnsupportedMediaType, "artifact_preview_unsupported", "artifact does not have a safe preview", false)
	}
}

func (s *Server) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact_service_unavailable", "artifact service is unavailable", true)
		return
	}
	artifact, data, err := s.Artifacts.ReadForSession(r.Context(), r.PathValue("artifactID"), r.PathValue("sessionID"))
	if err != nil {
		s.writeArtifactError(w, r, err)
		return
	}
	setArtifactHeaders(w)
	w.Header().Set("Content-Type", artifact.MIMEType)
	w.Header().Set("Content-Disposition", contentDisposition("attachment", artifact.Name))
	http.ServeContent(w, r, artifact.Name, artifact.CreatedAt, bytes.NewReader(data))
}

func setArtifactHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
}

func contentDisposition(disposition, name string) string {
	value := mime.FormatMediaType(disposition, map[string]string{"filename": name})
	if value == "" {
		return disposition
	}
	return value
}

func (s *Server) writeArtifactError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, artifacts.ErrArtifactNotFound):
		writeError(w, r, http.StatusNotFound, "artifact_not_found", "artifact not found", false)
	case errors.Is(err, artifacts.ErrArtifactCorrupt):
		writeError(w, r, http.StatusConflict, "artifact_corrupt", "artifact content is missing or corrupt", false)
	case errors.Is(err, artifacts.ErrPreviewUnsupported):
		writeError(w, r, http.StatusUnsupportedMediaType, "artifact_preview_unsupported", "artifact does not have a safe preview", false)
	default:
		writeInternal(w, r, err)
	}
}

func (s *Server) listSessionBranches(w http.ResponseWriter, r *http.Request) {
	branches, err := s.Branches.List(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, branches)
}

func (s *Server) createSessionBranch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		FromMessageID string `json:"fromMessageId"`
		Label         string `json:"label"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	navigation, err := s.Branches.Create(r.Context(), r.PathValue("sessionID"), input.FromMessageID, input.Label)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, navigation)
}

func (s *Server) activateSessionBranch(w http.ResponseWriter, r *http.Request) {
	navigation, err := s.Branches.Activate(r.Context(), r.PathValue("sessionID"), r.PathValue("branchID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, navigation)
}

func (s *Server) getSessionRecovery(w http.ResponseWriter, r *http.Request) {
	recovery, err := s.Runs.FindRecoveryBySession(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, recovery)
}

func (s *Server) submitTurn(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text    string `json:"text"`
		Content []struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			ArtifactID string `json:"artifactId"`
		} `json:"content"`
		BaseMessageID string          `json:"baseMessageId"`
		Config        json.RawMessage `json:"config"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	requestID := r.Header.Get("Idempotency-Key")
	if requestID == "" {
		requestID = newID()
	}
	parts := make([]domain.ContentBlock, 0, len(input.Content))
	for _, part := range input.Content {
		switch part.Type {
		case "text":
			parts = append(parts, domain.ContentBlock{Kind: domain.ContentText, Text: part.Text})
		case "image":
			parts = append(parts, domain.ContentBlock{Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: part.ArtifactID}})
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_turn_content", "unsupported content part type", false)
			return
		}
	}
	// UserPromptSubmit hook gate: may block the submission before any run is
	// created. On infrastructure failure we fail-open (allow) but log.
	if s.PromptGate != nil {
		outcome := s.PromptGate.CheckPrompt(r.Context(), r.PathValue("sessionID"), input.Text, parts)
		if outcome.Error != nil {
			slog.Warn("prompt hook gate failed (fail-open)", "session_id", r.PathValue("sessionID"), "error", outcome.Error)
		}
		if outcome.Blocked {
			writeError(w, r, http.StatusForbidden, "prompt_blocked_by_hook", outcome.Reason, false)
			return
		}
	}
	submission, err := s.Runs.SubmitTurn(r.Context(), domain.SubmitTurnInput{
		SessionID: r.PathValue("sessionID"), ClientRequestID: requestID,
		BaseMessageID: input.BaseMessageID, Text: input.Text, Parts: parts, RequestedConfig: input.Config,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !submission.Existing && s.Control != nil {
		if err := s.Control.Enqueue(context.Background(), submission.Run.ID); err != nil {
			_ = s.Runs.Fail(context.Background(), submission.Run.ID, "run_enqueue_failed", err.Error())
			writeError(w, r, http.StatusInternalServerError, "run_enqueue_failed", "run could not be scheduled", true)
			return
		}
	}
	status := http.StatusAccepted
	if submission.Existing {
		status = http.StatusOK
	}
	writeData(w, status, submission)
}

func (s *Server) submitInvocation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text    string `json:"text"`
		Content []struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			ArtifactID string `json:"artifactId"`
		} `json:"content"`
		BaseMessageID string          `json:"baseMessageId"`
		Config        json.RawMessage `json:"config"`
		Target        struct {
			Kind        domain.InvocationTargetKind  `json:"kind"`
			ObjectID    string                       `json:"objectId"`
			VersionID   string                       `json:"versionId"`
			ContextMode domain.InvocationContextMode `json:"contextMode"`
			ReplyTo     []string                     `json:"replyTo"`
		} `json:"target"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	requestID := r.Header.Get("Idempotency-Key")
	if requestID == "" {
		requestID = newID()
	}
	parts := make([]domain.ContentBlock, 0, len(input.Content))
	for _, part := range input.Content {
		switch part.Type {
		case "text":
			parts = append(parts, domain.ContentBlock{Kind: domain.ContentText, Text: part.Text})
		case "image":
			parts = append(parts, domain.ContentBlock{Kind: domain.ContentImage, Image: &domain.ImageRef{ArtifactID: part.ArtifactID}})
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_invocation_content", "unsupported content part type", false)
			return
		}
	}
	if s.PromptGate != nil {
		outcome := s.PromptGate.CheckPrompt(r.Context(), r.PathValue("sessionID"), input.Text, parts)
		if outcome.Error != nil {
			slog.Warn("prompt hook gate failed (fail-open)", "session_id", r.PathValue("sessionID"), "error", outcome.Error)
		}
		if outcome.Blocked {
			writeError(w, r, http.StatusForbidden, "prompt_blocked_by_hook", outcome.Reason, false)
			return
		}
	}
	var submission *domain.TurnSubmission
	var err error
	switch input.Target.Kind {
	case domain.InvocationTargetHost:
		submission, err = s.Runs.SubmitTurn(r.Context(), domain.SubmitTurnInput{
			SessionID: r.PathValue("sessionID"), ClientRequestID: requestID, BaseMessageID: input.BaseMessageID,
			Text: input.Text, Parts: parts, RequestedConfig: input.Config,
		})
	case domain.InvocationTargetRole:
		submission, err = s.Runs.SubmitInvocation(r.Context(), domain.SubmitInvocationInput{
			SessionID: r.PathValue("sessionID"), ClientRequestID: requestID, BaseMessageID: input.BaseMessageID,
			Text: input.Text, Parts: parts, RequestedConfig: input.Config,
			Target: domain.RoleInvocationTarget{Kind: input.Target.Kind, ObjectID: input.Target.ObjectID,
				VersionID: input.Target.VersionID, ContextMode: input.Target.ContextMode, ReplyTo: input.Target.ReplyTo},
		})
	default:
		writeError(w, r, http.StatusBadRequest, string(domain.ErrorInvocationTargetInvalid), "unsupported invocation target", false)
		return
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !submission.Existing && s.Control != nil {
		if err := s.Control.Enqueue(context.Background(), submission.Run.ID); err != nil {
			_ = s.Runs.Fail(context.Background(), submission.Run.ID, "run_enqueue_failed", err.Error())
			writeError(w, r, http.StatusInternalServerError, "run_enqueue_failed", "run could not be scheduled", true)
			return
		}
	}
	status := http.StatusAccepted
	if submission.Existing {
		status = http.StatusOK
	}
	writeData(w, status, submission)
}

func (s *Server) createCompaction(w http.ResponseWriter, r *http.Request) {
	if s.Compactions == nil {
		writeError(w, r, http.StatusServiceUnavailable, "compaction_unavailable", "context compaction is unavailable", true)
		return
	}
	var input struct {
		BaseMessageID   string `json:"baseMessageId"`
		ClientRequestID string `json:"clientRequestId"`
		Instructions    string `json:"instructions"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = r.Header.Get("Idempotency-Key")
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = newID()
	}
	submission, err := s.Compactions.CreateManual(r.Context(), domain.ManualCompactionInput{
		SessionID: r.PathValue("sessionID"), BaseMessageID: input.BaseMessageID,
		ClientRequestID: input.ClientRequestID, Instructions: input.Instructions,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !submission.Existing && s.Control != nil {
		if err := s.Control.Enqueue(context.Background(), submission.RunID); err != nil {
			_ = s.Runs.Fail(context.Background(), submission.RunID, "run_enqueue_failed", err.Error())
			writeError(w, r, http.StatusInternalServerError, "run_enqueue_failed", "compaction could not be scheduled", true)
			return
		}
	}
	status := http.StatusAccepted
	if submission.Existing {
		status = http.StatusOK
	}
	writeData(w, status, submission)
}

func (s *Server) listCompactions(w http.ResponseWriter, r *http.Request) {
	values, err := s.Compactions.List(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if values == nil {
		values = []domain.ContextCompaction{}
	}
	writeData(w, http.StatusOK, values)
}

func (s *Server) getCompaction(w http.ResponseWriter, r *http.Request) {
	value, err := s.Compactions.Get(r.Context(), r.PathValue("compactionID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, value)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.Runs.Get(r.Context(), r.PathValue("runID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, run)
}

func (s *Server) retryRun(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ClientRequestID string `json:"clientRequestId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = r.Header.Get("Idempotency-Key")
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = newID()
	}
	submission, err := s.Runs.Retry(r.Context(), r.PathValue("runID"), input.ClientRequestID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !submission.Existing && s.Control != nil {
		if err := s.Control.Enqueue(context.Background(), submission.Run.ID); err != nil {
			_ = s.Runs.Fail(context.Background(), submission.Run.ID, "run_enqueue_failed", err.Error())
			writeError(w, r, http.StatusInternalServerError, "run_enqueue_failed", "retry could not be scheduled", true)
			return
		}
	}
	status := http.StatusAccepted
	if submission.Existing {
		status = http.StatusOK
	}
	writeData(w, status, submission)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	var err error
	if s.Control != nil {
		err = s.Control.Cancel(r.Context(), runID)
	} else {
		err = s.Runs.Cancel(r.Context(), runID)
	}
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	run, err := s.Runs.Get(r.Context(), runID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, run)
}

func (s *Server) queueInput(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Kind            domain.QueuedInputKind `json:"kind"`
		Text            string                 `json:"text"`
		ClientRequestID string                 `json:"clientRequestId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = r.Header.Get("Idempotency-Key")
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = newID()
	}
	item, err := s.Queue.Enqueue(r.Context(), r.PathValue("runID"), input.ClientRequestID, input.Kind, input.Text)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusAccepted, item)
}

func (s *Server) writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrSessionNotFound):
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error(), false)
	case errors.Is(err, store.ErrSessionStateConflict):
		writeError(w, r, http.StatusConflict, "session_state_conflict", err.Error(), false)
	case errors.Is(err, store.ErrRunNotFound):
		writeError(w, r, http.StatusNotFound, "run_not_found", err.Error(), false)
	case errors.Is(err, store.ErrBranchNotFound):
		writeError(w, r, http.StatusNotFound, "branch_not_found", err.Error(), false)
	case errors.Is(err, store.ErrBranchPointNotActive):
		writeError(w, r, http.StatusConflict, "branch_point_not_active", err.Error(), false)
	case errors.Is(err, store.ErrRunRetryUnsafe):
		writeError(w, r, http.StatusConflict, "run_retry_unsafe", err.Error(), false)
	case errors.Is(err, store.ErrRunRetryStale), errors.Is(err, store.ErrRunRetryConflict):
		writeError(w, r, http.StatusConflict, "run_retry_stale", err.Error(), false)
	case errors.Is(err, store.ErrSessionBusy), errors.Is(err, store.ErrSessionRunActive):
		writeError(w, r, http.StatusConflict, string(domain.ErrorSessionBusy), err.Error(), false)
	case errors.Is(err, store.ErrSessionCompacting):
		writeError(w, r, http.StatusConflict, string(domain.ErrorSessionCompacting), err.Error(), false)
	case errors.Is(err, store.ErrCompactionNotFound):
		writeError(w, r, http.StatusNotFound, "compaction_not_found", err.Error(), false)
	case domain.ErrorCodeOf(err) == domain.ErrorCompactionNotAllowed:
		writeError(w, r, http.StatusConflict, string(domain.ErrorCompactionNotAllowed), err.Error(), false)
	case domain.ErrorCodeOf(err) == domain.ErrorCompactionConfigInvalid,
		domain.ErrorCodeOf(err) == domain.ErrorCompactionModelUnavailable:
		writeError(w, r, http.StatusBadRequest, string(domain.ErrorCodeOf(err)), err.Error(), false)
	case domain.ErrorCodeOf(err) == domain.ErrorCompactionCheckpointInvalid:
		writeError(w, r, http.StatusConflict, string(domain.ErrorCompactionCheckpointInvalid), err.Error(), false)
	case domain.ErrorCodeOf(err) == domain.ErrorInvocationTargetInvalid:
		writeError(w, r, http.StatusBadRequest, string(domain.ErrorInvocationTargetInvalid), err.Error(), false)
	case domain.ErrorCodeOf(err) == domain.ErrorCommitFormatNotEnabled:
		writeError(w, r, http.StatusConflict, string(domain.ErrorCommitFormatNotEnabled), err.Error(), false)
	case errors.Is(err, store.ErrRunNotActive), errors.Is(err, store.ErrInvalidRunState):
		writeError(w, r, http.StatusConflict, "run_not_active", err.Error(), false)
	case errors.Is(err, store.ErrDelegationGroupNotFound):
		writeError(w, r, http.StatusNotFound, "delegation_group_not_found", err.Error(), false)
	case errors.Is(err, store.ErrDelegationItemNotFound):
		writeError(w, r, http.StatusNotFound, "delegation_item_not_found", err.Error(), false)
	case errors.Is(err, store.ErrDelegationConflict):
		writeError(w, r, http.StatusConflict, "delegation_conflict", err.Error(), false)
	default:
		writeInternal(w, r, err)
	}
}

type requestIDKey struct{}

type messageCursor struct {
	Version   int    `json:"v"`
	SessionID string `json:"sessionId"`
	MessageID string `json:"messageId"`
}

func encodeMessageCursor(sessionID, messageID string) (string, error) {
	if sessionID == "" || messageID == "" {
		return "", fmt.Errorf("message cursor identifiers are required")
	}
	encoded, err := json.Marshal(messageCursor{Version: 1, SessionID: sessionID, MessageID: messageID})
	if err != nil {
		return "", fmt.Errorf("encode message cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeMessageCursor(value string) (messageCursor, error) {
	var cursor messageCursor
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursor, fmt.Errorf("decode message cursor: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return cursor, fmt.Errorf("parse message cursor: %w", err)
	}
	if cursor.Version != 1 || cursor.SessionID == "" || cursor.MessageID == "" {
		return cursor, fmt.Errorf("unsupported or incomplete message cursor")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return cursor, fmt.Errorf("message cursor must contain one JSON value")
	}
	return cursor, nil
}

func nullableString(raw json.RawMessage) (*string, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error(), false)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must contain one JSON value", false)
		return false
	}
	return true
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	_ = err
	writeError(w, r, http.StatusInternalServerError, "internal_error", "internal worker error", true)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool) {
	requestID, _ := r.Context().Value(requestIDKey{}).(string)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": code, "message": message, "requestId": requestID, "retryable": retryable,
	}})
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(bytes[:])
}

func parseCursor(value string) int64 {
	cursor, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return cursor
}

func eventJSON(event domain.RunEvent) []byte {
	encoded, _ := json.Marshal(map[string]any{
		"eventId": event.EventID, "runId": event.RunID, "seq": event.Seq,
		"type": event.EventType, "payload": json.RawMessage(event.Payload),
		"createdAt": event.CreatedAt.Format(time.RFC3339Nano),
	})
	return encoded
}

func isTerminalEvent(eventType string) bool {
	return eventType == "run_succeeded" || eventType == "run_failed" || eventType == "run_cancelled" || eventType == "run_interrupted"
}
