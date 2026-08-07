package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// AgentFlowServer wires the Agent Flow authoring, binding, and run endpoints.
type AgentFlowServer struct {
	Profiles     *store.AgentFlowProfileRepo
	Bindings     *store.AgentFlowBindingRepo
	Runs         *store.AgentFlowRunRepo
	Projects     *store.ProjectRepo
	Sessions     *store.SessionRepo
	Checks       *store.CheckTaskRunner
	Discovery    *store.AgentFlowDiscovery
	Orchestrator *agentflow.Orchestrator
	// Skills is the publish-time skill catalog (name -> known).
	Skills map[string]bool
	// StartRun is wired by the Worker: creates+freezes+starts a flow run.
	StartRun func(ctx context.Context, projectID, flowVersionID, sessionID string,
		inputs, vars map[string]any) (*domain.RunAgentFlow, error)
	// StartRecovered is wired by the Worker: resumes an existing flow run.
	StartRecovered func(ctx context.Context, runID string) error
	Logger         *slog.Logger
}

func (s *Server) flows() *AgentFlowServer { return s.AgentFlows }

// --- Profile library ---

func (s *Server) listAgentFlows(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.AgentFlows.Profiles.ListProfiles(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	projectID := r.URL.Query().Get("projectId")
	if projectID != "" {
		filtered := profiles[:0]
		for _, p := range profiles {
			if p.SourceKind == domain.FlowSourceManaged ||
				(p.ProjectScope != nil && *p.ProjectScope == projectID) {
				filtered = append(filtered, p)
			}
		}
		profiles = filtered
	}
	writeData(w, http.StatusOK, profiles)
}

func (s *Server) createAgentFlow(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	profile, err := s.AgentFlows.Profiles.CreateProfile(r.Context(), store.CreateAgentFlowProfileInput{
		Name: input.Name, Slug: input.Slug, SourceKind: domain.FlowSourceManaged,
	})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, profile)
}

func (s *Server) getAgentFlow(w http.ResponseWriter, r *http.Request) {
	profile, err := s.AgentFlows.Profiles.GetProfile(r.Context(), r.PathValue("profileID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, profile)
}

func (s *Server) archiveAgentFlow(w http.ResponseWriter, r *http.Request) {
	if err := s.AgentFlows.Profiles.Archive(r.Context(), r.PathValue("profileID")); err != nil {
		writeError(w, r, http.StatusNotFound, "agent_flow_not_found", err.Error(), false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAgentFlowVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.AgentFlows.Profiles.ListVersions(r.Context(), r.PathValue("profileID"))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, versions)
}

func (s *Server) getAgentFlowVersion(w http.ResponseWriter, r *http.Request) {
	version, err := s.AgentFlows.Profiles.GetVersion(r.Context(), r.PathValue("versionID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, version)
}

// updateAgentFlowDraft stores the authoring YAML as the profile draft.
func (s *Server) updateAgentFlowDraft(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("profileID")
	var input struct {
		YAML             string `json:"yaml"`
		ExpectedRevision int    `json:"expectedRevision"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.YAML) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_draft", "yaml is required", false)
		return
	}
	def, err := agentflow.ParseDefinition([]byte(input.YAML))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_yaml", err.Error(), false)
		return
	}
	profile, err := s.AgentFlows.Profiles.UpdateDraft(r.Context(), profileID, def, input.YAML, input.ExpectedRevision)
	if err != nil {
		if errors.Is(err, store.ErrFlowDraftConflict) {
			writeError(w, r, http.StatusConflict, "agent_flow_draft_conflict", err.Error(), false)
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, profile)
}

func (s *Server) validateAgentFlowDraft(w http.ResponseWriter, r *http.Request) {
	validator := store.NewFlowValidator(s.AgentFlows.flowPublishOptions(r.Context(), r.PathValue("profileID")))
	result, err := s.AgentFlows.Profiles.ValidateDraft(r.Context(), r.PathValue("profileID"), validator)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) publishAgentFlow(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("profileID")
	expectedRevision := 0
	if r.ContentLength != 0 {
		var input struct {
			ExpectedRevision int `json:"expectedRevision"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		expectedRevision = input.ExpectedRevision
	}
	validator := store.NewFlowValidator(s.AgentFlows.flowPublishOptions(r.Context(), profileID))
	version, err := s.AgentFlows.Profiles.Publish(r.Context(), profileID, expectedRevision, validator)
	if err != nil {
		if errors.Is(err, store.ErrFlowDraftConflict) {
			writeError(w, r, http.StatusConflict, "agent_flow_draft_conflict", err.Error(), false)
			return
		}
		if errors.Is(err, store.ErrFlowValidation) {
			writeError(w, r, http.StatusUnprocessableEntity, "agent_flow_validation_failed", err.Error(), false)
			return
		}
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, version)
}

func (s *AgentFlowServer) flowPublishOptions(ctx context.Context, profileID string) store.FlowPublishOptions {
	opts := store.FlowPublishOptions{
		DB: s.Runs.DB, Skills: s.Skills, CheckAllowlist: defaultCheckAllowlist,
	}
	if strings.TrimSpace(profileID) != "" {
		var flowID string
		if err := s.Runs.DB.QueryRowContext(ctx,
			`SELECT id FROM agent_flow_profiles WHERE id=?`, profileID).Scan(&flowID); err == nil {
			opts.FlowID = flowID
		}
	}
	return opts
}

var defaultCheckAllowlist = []string{"go", "python3", "python", "sh", "bash", "node", "git", "make", "pytest", "Rscript", "echo"}

// --- Project candidates / bindings ---

func (s *Server) listAgentFlowCandidates(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	workspace, err := s.AgentFlows.Projects.FindWorkspaceByProjectID(r.Context(), projectID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	candidates, err := s.AgentFlows.Discovery.DiscoverCandidates(r.Context(), workspace.HostPath, projectID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "agent_flow_discovery_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, candidates)
}

// bindAgentFlowCandidate materializes a project_file candidate (new immutable
// version when the project file changed) and creates a disabled binding.
func (s *Server) bindAgentFlowCandidate(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	workspace, err := s.AgentFlows.Projects.FindWorkspaceByProjectID(r.Context(), projectID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	var input struct {
		Slug string `json:"slug"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	candidates, err := s.AgentFlows.Discovery.DiscoverCandidates(r.Context(), workspace.HostPath, projectID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "agent_flow_discovery_failed", err.Error(), false)
		return
	}
	var candidate *store.AgentFlowCandidate
	for i := range candidates {
		if candidates[i].Slug == input.Slug {
			candidate = &candidates[i]
			break
		}
	}
	if candidate == nil || candidate.Definition == nil {
		writeError(w, r, http.StatusNotFound, "agent_flow_candidate_not_found", "candidate not found or failed to parse", false)
		return
	}
	validator := store.NewFlowValidator(store.FlowPublishOptions{
		DB: s.AgentFlows.Runs.DB, ProjectID: projectID,
		Skills: s.AgentFlows.flowSkills(), CheckAllowlist: defaultCheckAllowlist,
	})
	validation := validator.Validate(r.Context(), candidate.Definition)
	if !validation.Valid {
		writeError(w, r, http.StatusUnprocessableEntity, "agent_flow_validation_failed",
			"project file flow is invalid: "+validation.Diagnostics[0].Code, false)
		return
	}
	profile, err := s.AgentFlows.Profiles.FindProfileBySource(r.Context(), candidate.Slug,
		domain.FlowSourceProjectFile, &projectID)
	if err == nil {
		// Profile exists; create the new immutable version below.
	} else if errors.Is(err, sql.ErrNoRows) {
		profile, err = s.AgentFlows.Profiles.CreateProfile(r.Context(), store.CreateAgentFlowProfileInput{
			Name: candidate.Slug, Slug: candidate.Slug, SourceKind: domain.FlowSourceProjectFile,
			ProjectScope: &projectID, SourceLocator: candidate.SourceLocator,
		})
		if err != nil {
			writeInternal(w, r, err)
			return
		}
	} else {
		writeInternal(w, r, err)
		return
	}
	version, err := s.AgentFlows.Profiles.CreateVersion(r.Context(), profile.ID, candidate.Definition)
	if err != nil {
		writeError(w, r, http.StatusConflict, "agent_flow_version_conflict", err.Error(), false)
		return
	}
	binding, err := s.AgentFlows.Bindings.EnsureBindingExists(r.Context(), projectID, version.ID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, binding)
}

func (s *Server) listAgentFlowBindings(w http.ResponseWriter, r *http.Request) {
	bindings, err := s.AgentFlows.Bindings.ListByProject(r.Context(), r.PathValue("projectID"))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, bindings)
}

func (s *Server) createAgentFlowBinding(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if !s.projectExists(w, r, projectID) {
		return
	}
	var input struct {
		FlowVersionID  string `json:"flowVersionId"`
		DesiredEnabled bool   `json:"desiredEnabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.FlowVersionID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_binding", "flowVersionId is required", false)
		return
	}
	version, err := s.AgentFlows.Profiles.GetVersion(r.Context(), input.FlowVersionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "agent_flow_version_not_found", "flow version not found", false)
		return
	}
	profile, err := s.AgentFlows.Profiles.GetProfile(r.Context(), version.ProfileID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if profile.SourceKind == domain.FlowSourceProjectFile &&
		(profile.ProjectScope == nil || *profile.ProjectScope != projectID) {
		writeError(w, r, http.StatusForbidden, "agent_flow_binding_forbidden", "project-file flow is scoped to another project", false)
		return
	}
	binding, err := s.AgentFlows.Bindings.EnsureBindingExists(r.Context(), projectID, input.FlowVersionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if input.DesiredEnabled {
		binding, err = s.AgentFlows.Bindings.Update(r.Context(), binding.ID, true)
		if err != nil {
			writeInternal(w, r, err)
			return
		}
	}
	writeData(w, http.StatusCreated, binding)
}

func (s *Server) updateAgentFlowBinding(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	binding := s.flowBindingOwnedBy(w, r, projectID, r.PathValue("bindingID"))
	if binding == nil {
		return
	}
	var input struct {
		DesiredEnabled *bool `json:"desiredEnabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.DesiredEnabled == nil {
		writeData(w, http.StatusOK, binding)
		return
	}
	updated, err := s.AgentFlows.Bindings.Update(r.Context(), binding.ID, *input.DesiredEnabled)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, updated)
}

func (s *Server) deleteAgentFlowBinding(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	binding := s.flowBindingOwnedBy(w, r, projectID, r.PathValue("bindingID"))
	if binding == nil {
		return
	}
	if err := s.AgentFlows.Bindings.Delete(r.Context(), binding.ID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// flowBindingOwnedBy loads a binding and verifies project ownership.
func (s *Server) flowBindingOwnedBy(w http.ResponseWriter, r *http.Request, projectID, bindingID string) *domain.ProjectAgentFlowBinding {
	binding, err := s.AgentFlows.Bindings.Get(r.Context(), bindingID)
	if err != nil || binding.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "agent_flow_binding_not_found", "binding not found", false)
		return nil
	}
	return binding
}

// --- Runs ---

func (s *Server) runAgentFlow(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	bindingID := r.PathValue("bindingID")
	binding := s.flowBindingOwnedBy(w, r, projectID, bindingID)
	if binding == nil {
		return
	}
	if !binding.DesiredEnabled {
		writeError(w, r, http.StatusConflict, "agent_flow_not_enabled",
			"the flow binding must be enabled before it can run", false)
		return
	}
	var input struct {
		SessionID       string         `json:"sessionId"`
		Inputs          map[string]any `json:"inputs"`
		Vars            map[string]any `json:"vars"`
		ClientRequestID string         `json:"clientRequestId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.SessionID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_run", "sessionId is required", false)
		return
	}
	session, err := s.AgentFlows.Sessions.FindByID(r.Context(), input.SessionID)
	if err != nil || session.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "session_not_found", "session not found in this project", false)
		return
	}
	if s.AgentFlows.StartRun == nil {
		writeError(w, r, http.StatusServiceUnavailable, "agent_flow_unavailable", "flow runner is not configured", false)
		return
	}
	run, err := s.AgentFlows.StartRun(r.Context(), projectID, binding.FlowVersionID, input.SessionID,
		input.Inputs, input.Vars)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "agent_flow_run_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, run)
}

func (s *Server) cancelAgentFlowRun(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	runID := r.PathValue("runID")
	run, err := s.AgentFlows.Runs.GetRun(r.Context(), runID)
	if err != nil || run.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "agent_flow_run_not_found", "flow run not found", false)
		return
	}
	if run.State.Terminal() {
		writeData(w, http.StatusOK, run)
		return
	}
	if err := s.AgentFlows.Runs.SetCancelRequested(r.Context(), runID); err != nil {
		writeInternal(w, r, err)
		return
	}
	// Hard-cancel any active child so its settlement is visible to the poll.
	if s.Control != nil {
		nodes, listErr := s.AgentFlows.Runs.ListNodes(r.Context(), runID)
		if listErr == nil {
			for _, node := range nodes {
				if node.TerminalState == domain.FlowNodeRunning && node.ChildRunID != "" {
					_ = s.Control.Cancel(r.Context(), node.ChildRunID)
				}
			}
		}
	}
	writeData(w, http.StatusOK, run)
}

func (s *Server) resumeAgentFlowRun(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	runID := r.PathValue("runID")
	run, err := s.AgentFlows.Runs.GetRun(r.Context(), runID)
	if err != nil || run.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "agent_flow_run_not_found", "flow run not found", false)
		return
	}
	resumed, err := s.AgentFlows.Runs.ResumeFlowRun(r.Context(), runID)
	if err != nil {
		writeError(w, r, http.StatusConflict, "agent_flow_resume_failed", err.Error(), false)
		return
	}
	if s.AgentFlows.StartRecovered != nil {
		if err := s.AgentFlows.StartRecovered(r.Context(), runID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "agent_flow_resume_failed", err.Error(), false)
			return
		}
	}
	writeData(w, http.StatusOK, resumed)
}

func (s *Server) listAgentFlowRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.AgentFlows.Runs.ListProjectRuns(r.Context(), r.PathValue("projectID"), 50)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, runs)
}

func (s *Server) getAgentFlowRun(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	runID := r.PathValue("runID")
	run, err := s.AgentFlows.Runs.GetRun(r.Context(), runID)
	if err != nil || run.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "agent_flow_run_not_found", "flow run not found", false)
		return
	}
	nodes, err := s.AgentFlows.Runs.ListNodes(r.Context(), runID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	version, err := s.AgentFlows.Profiles.GetVersion(r.Context(), run.FlowVersionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	var def domain.FlowDefinition
	_ = json.Unmarshal(version.DefinitionJSON, &def)
	writeData(w, http.StatusOK, map[string]any{"run": run, "nodes": nodes, "flowVersion": version.Version})
}

// --- Check approvals ---

func (s *Server) listAgentFlowCheckApprovals(w http.ResponseWriter, r *http.Request) {
	approvals, err := s.AgentFlows.Checks.ListPendingCheckApprovals(r.Context(), r.PathValue("projectID"))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, approvals)
}

func (s *Server) decideAgentFlowCheckApproval(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	runID := r.PathValue("runID")
	taskIndex, err := strconv.Atoi(r.PathValue("taskIndex"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_task_index", "task index is invalid", false)
		return
	}
	run, err := s.AgentFlows.Runs.GetRun(r.Context(), runID)
	if err != nil || run.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "agent_flow_run_not_found", "flow run not found", false)
		return
	}
	var input struct {
		Approved        bool   `json:"approved"`
		ClientRequestID string `json:"clientRequestId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	status, err := s.AgentFlows.Checks.DecideCheckApproval(r.Context(), runID, taskIndex, input.Approved, input.ClientRequestID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"status": status})
}

func (s *AgentFlowServer) flowSkills() map[string]bool {
	return s.Skills
}

// invokeAgentFlow resolves a flow by name[@version] inside the project's
// enabled bindings and starts a run in the given session. This is the
// Host-side orchestration address (@flow / /invoke_agent_flow): flows never
// enter Room speaker addressing. Resolution is fail-closed: an unbound,
// disabled, or version-mismatched flow is rejected with a clear error.
func (s *Server) invokeAgentFlow(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if !s.projectExists(w, r, projectID) {
		return
	}
	var input struct {
		SessionID string         `json:"sessionId"`
		Name      string         `json:"name"`
		Version   int            `json:"version,omitempty"`
		Inputs    map[string]any `json:"inputs"`
		Vars      map[string]any `json:"vars"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_invoke", "flow name is required", false)
		return
	}
	if input.SessionID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_invoke", "sessionId is required", false)
		return
	}
	session, err := s.AgentFlows.Sessions.FindByID(r.Context(), input.SessionID)
	if err != nil || session.ProjectID != projectID {
		writeError(w, r, http.StatusNotFound, "session_not_found", "session not found in this project", false)
		return
	}
	bindings, err := s.AgentFlows.Bindings.ListByProject(r.Context(), projectID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	// Resolve an enabled binding whose flow definition matches name[@version].
	var resolvedVersionID string
	for _, binding := range bindings {
		if !binding.DesiredEnabled {
			continue
		}
		version, err := s.AgentFlows.Profiles.GetVersion(r.Context(), binding.FlowVersionID)
		if err != nil {
			continue
		}
		var def domain.FlowDefinition
		if err := json.Unmarshal(version.DefinitionJSON, &def); err != nil || def.ID != name {
			continue
		}
		if input.Version > 0 && version.Version != input.Version {
			continue
		}
		resolvedVersionID = version.ID
		break
	}
	if resolvedVersionID == "" {
		if input.Version > 0 {
			writeError(w, r, http.StatusNotFound, "agent_flow_invoke_not_found",
				fmt.Sprintf("flow %s@%d is not bound and enabled in this project", name, input.Version), false)
		} else {
			writeError(w, r, http.StatusNotFound, "agent_flow_invoke_not_found",
				fmt.Sprintf("flow %s is not bound and enabled in this project", name), false)
		}
		return
	}
	if s.AgentFlows.StartRun == nil {
		writeError(w, r, http.StatusServiceUnavailable, "agent_flow_unavailable", "flow runner is not configured", false)
		return
	}
	run, err := s.AgentFlows.StartRun(r.Context(), projectID, resolvedVersionID, input.SessionID, input.Inputs, input.Vars)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "agent_flow_run_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, run)
}

// --- Phase 3: file sharing (import/export; YAML is the shared unit) ---

// checkAgentFlowDependencies validates arbitrary flow YAML and resolves its
// dependency manifest against the target environment. It never persists.
func (s *Server) checkAgentFlowDependencies(w http.ResponseWriter, r *http.Request) {
	var input struct {
		YAML      string `json:"yaml"`
		ProjectID string `json:"projectId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	def, diagnostics, opts, ok := s.parseFlowForImport(w, r, input.YAML, input.ProjectID)
	if !ok {
		return
	}
	_ = diagnostics
	validator := store.NewFlowValidator(opts)
	result := validator.Validate(r.Context(), def)
	dependencies, err := store.CheckFlowDependencies(r.Context(), opts, def)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"valid": result.Valid, "diagnostics": result.Diagnostics,
		"configDigest": result.ConfigDigest, "dependencies": dependencies,
	})
}

// importAgentFlow validates flow YAML, resolves its dependencies, and creates
// (or updates) a managed profile DRAFT. It NEVER publishes, binds, or
// authorizes anything: publishing stays an explicit user action.
func (s *Server) importAgentFlow(w http.ResponseWriter, r *http.Request) {
	var input struct {
		YAML      string `json:"yaml"`
		ProjectID string `json:"projectId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	def, diagnostics, opts, ok := s.parseFlowForImport(w, r, input.YAML, input.ProjectID)
	if !ok {
		_ = diagnostics
		return
	}
	validator := store.NewFlowValidator(opts)
	result := validator.Validate(r.Context(), def)
	dependencies, err := store.CheckFlowDependencies(r.Context(), opts, def)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if !result.Valid {
		writeError(w, r, http.StatusUnprocessableEntity, "agent_flow_validation_failed",
			"imported flow is invalid: "+result.Diagnostics[0].Code, false)
		return
	}
	profile, err := s.AgentFlows.Profiles.FindProfileBySource(r.Context(), def.ID,
		domain.FlowSourceManaged, nil)
	if errors.Is(err, sql.ErrNoRows) {
		profile, err = s.AgentFlows.Profiles.CreateProfile(r.Context(), store.CreateAgentFlowProfileInput{
			Name: def.ID, Slug: def.ID, SourceKind: domain.FlowSourceManaged,
		})
		if err != nil {
			writeInternal(w, r, err)
			return
		}
	} else if err != nil {
		writeInternal(w, r, err)
		return
	}
	// Draft CAS with idempotency: identical digest -> revision unchanged
	// (no write at all); a changed draft bumps the revision.
	expectedRevision := 0
	alreadyDrafted := false
	existingDraft, draftErr := s.AgentFlows.Profiles.GetDraft(r.Context(), profile.ID)
	switch {
	case draftErr == nil:
		existingDigest, err := agentflow.ConfigDigest(&existingDraft.Definition)
		if err != nil {
			writeInternal(w, r, err)
			return
		}
		newDigest, err := agentflow.ConfigDigest(def)
		if err != nil {
			writeInternal(w, r, err)
			return
		}
		if existingDigest == newDigest {
			alreadyDrafted = true
		} else {
			expectedRevision = existingDraft.Revision
		}
	case errors.Is(draftErr, sql.ErrNoRows):
		// no draft yet: expectedRevision stays 0
	default:
		writeInternal(w, r, draftErr)
		return
	}
	revision := 0
	if alreadyDrafted {
		revision = existingDraft.Revision
	} else {
		updated, err := s.AgentFlows.Profiles.UpdateDraft(r.Context(), profile.ID, def, input.YAML, expectedRevision)
		if errors.Is(err, store.ErrFlowDraftConflict) {
			writeError(w, r, http.StatusConflict, "agent_flow_draft_conflict", err.Error(), false)
			return
		}
		if err != nil {
			writeInternal(w, r, err)
			return
		}
		revision = updated.DraftRevision
	}
	writeData(w, http.StatusCreated, map[string]any{
		"profileId": profile.ID, "slug": profile.Slug, "draftRevision": revision,
		"alreadyDrafted": alreadyDrafted, "dependencies": dependencies,
		"configDigest": result.ConfigDigest,
	})
}

// exportAgentFlow returns the flow YAML: the authoring draft verbatim, or a
// normalized YAML generated from an immutable published version.
func (s *Server) exportAgentFlow(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("profileID")
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "draft"
	}
	switch source {
	case "draft":
		draft, err := s.AgentFlows.Profiles.GetDraft(r.Context(), profileID)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Header().Set("Content-Disposition", `attachment; filename="`+draft.Definition.ID+`.yaml"`)
		_, _ = w.Write([]byte(draft.YAML))
	case "version":
		versionID := r.URL.Query().Get("versionID")
		if versionID == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_export", "versionID is required for version export", false)
			return
		}
		version, err := s.AgentFlows.Profiles.GetVersion(r.Context(), versionID)
		if err != nil {
			s.writeStoreError(w, r, err)
			return
		}
		var def domain.FlowDefinition
		if err := json.Unmarshal(version.DefinitionJSON, &def); err != nil {
			writeInternal(w, r, err)
			return
		}
		encoded, err := agentflow.DefinitionToYAML(&def)
		if err != nil {
			writeInternal(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Header().Set("Content-Disposition", `attachment; filename="`+def.ID+`.yaml"`)
		_, _ = w.Write(encoded)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_export", "source must be draft or version", false)
	}
}

// parseFlowForImport parses and validates flow YAML, returning the definition,
// its diagnostics, and the publish options scoped to the optional project.
func (s *Server) parseFlowForImport(w http.ResponseWriter, r *http.Request, yamlText, projectID string) (
	*domain.FlowDefinition, []agentflow.ValidationDiagnostic, store.FlowPublishOptions, bool) {
	if strings.TrimSpace(yamlText) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_import", "yaml is required", false)
		return nil, nil, store.FlowPublishOptions{}, false
	}
	def, err := agentflow.ParseDefinition([]byte(yamlText))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_agent_flow_yaml", err.Error(), false)
		return nil, nil, store.FlowPublishOptions{}, false
	}
	opts := s.AgentFlows.flowPublishOptions(r.Context(), "")
	if strings.TrimSpace(projectID) != "" {
		opts.ProjectID = strings.TrimSpace(projectID)
	}
	return def, nil, opts, true
}
