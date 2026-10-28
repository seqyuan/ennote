package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// globalFlowSummary is the list projection of one file-native Agent Flow
// draft. Flow sources share agents/graphs/<id> with Graph sources but are
// exposed through /v1/flows so the existing Graph UI never receives a document
// it cannot represent.
type globalFlowSummary struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	Digest        string `json:"digest,omitempty"`
	LatestVersion int    `json:"latestVersion"`
	TaskCount     int    `json:"taskCount"`
	CheckTasks    int    `json:"checkTasks"`
	FanOutTasks   int    `json:"fanOutTasks"`
	Error         string `json:"error,omitempty"`
}

type globalFlowDetail struct {
	ID            string                 `json:"id"`
	Path          string                 `json:"path"`
	Digest        string                 `json:"digest"`
	LatestVersion int                    `json:"latestVersion"`
	Definition    *domain.FlowDefinition `json:"definition"`
}

// FlowRunStarter starts one full Agent Flow from a project-bound immutable
// revision. graphrun.Service implements it; tests stub it to assert routing.
type FlowRunStarter interface {
	StartFlow(ctx context.Context, projectID, flowID string, version int, sessionID string,
		inputs, vars map[string]any) (*domain.RunAgentFlow, error)
}

func (s *Server) listGlobalFlows(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	entries, err := sources.ListFlows()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "flow_catalog_failed", err.Error(), true)
		return
	}
	result := make([]globalFlowSummary, 0, len(entries))
	for _, entry := range entries {
		summary := globalFlowSummary{
			ID: entry.ID, Path: entry.Path, Digest: entry.Digest, Error: entry.Error,
		}
		if entry.Flow != nil {
			summary.TaskCount = len(entry.Flow.Tasks)
			for _, task := range entry.Flow.Tasks {
				if task.Type == domain.FlowTaskCheck {
					summary.CheckTasks++
				}
				if task.FanOut != nil {
					summary.FanOutTasks++
				}
			}
		}
		if revisions, revisionErr := sources.ListFlowRevisions(entry.ID); revisionErr == nil && len(revisions) > 0 {
			summary.LatestVersion = revisions[len(revisions)-1].Version
		}
		result = append(result, summary)
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) getGlobalFlow(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	id := r.PathValue("flowID")
	definition, digest, err := sources.ReadFlow(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "flow_not_found", err.Error(), false)
		return
	}
	latestVersion := 0
	if revisions, revisionErr := sources.ListFlowRevisions(id); revisionErr == nil && len(revisions) > 0 {
		latestVersion = revisions[len(revisions)-1].Version
	}
	writeData(w, http.StatusOK, globalFlowDetail{
		ID: id, Path: sources.GraphsDir() + "/" + id + "/flow.yaml",
		Digest: digest, LatestVersion: latestVersion, Definition: definition,
	})
}

// publishGlobalFlow validates the draft with the real Agent Flow validator and
// writes one immutable revision. Publish is deliberately separate from install
// and from project binding: publishing never enables a flow.
func (s *Server) publishGlobalFlow(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	id := r.PathValue("flowID")
	definition, _, err := sources.ReadFlow(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "flow_not_found", err.Error(), false)
		return
	}
	if s.Models == nil {
		writeError(w, r, http.StatusServiceUnavailable, "flow_validation_unavailable", "model catalog is unavailable", true)
		return
	}
	validator := store.NewFlowValidator(store.FlowPublishOptions{
		FlowID:                  id,
		Sources:                 sources,
		Models:                  s.Models,
		SkillCatalog:            s.SkillCatalog,
		CheckAllowlist:          s.CheckAllowlist,
		CheckAllowlistAllowsAll: s.CheckAllowlistAllowsAll,
	})
	result := validator.Validate(r.Context(), definition)
	if !result.Valid {
		messages := make([]string, 0, len(result.Diagnostics))
		for _, diagnostic := range result.Diagnostics {
			messages = append(messages, diagnostic.Code+": "+diagnostic.Message)
		}
		writeError(w, r, http.StatusUnprocessableEntity, "flow_validation_failed",
			strings.Join(messages, "; "), false)
		return
	}
	revision, err := sources.PublishFlowRevision(id)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "flow_publish_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, revision)
}

// runGlobalFlow starts one flow from the project binding's pinned revision. It
// never falls back to an unbound "latest" revision.
func (s *Server) runGlobalFlow(w http.ResponseWriter, r *http.Request) {
	if s.FlowRuns == nil {
		writeError(w, r, http.StatusServiceUnavailable, "flow_runner_unavailable", "Flow runner is unavailable", true)
		return
	}
	var body struct {
		SessionID string         `json:"sessionId"`
		Version   int            `json:"version,omitempty"`
		Inputs    map[string]any `json:"inputs"`
		Vars      map[string]any `json:"vars"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.SessionID == "" {
		writeError(w, r, http.StatusBadRequest, "session_required", "sessionId is required", false)
		return
	}
	session, err := s.Sessions.FindByID(r.Context(), body.SessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", "session not found", false)
		return
	}
	run, err := s.FlowRuns.StartFlow(r.Context(), session.ProjectID, r.PathValue("flowID"), body.Version,
		body.SessionID, body.Inputs, body.Vars)
	if err != nil {
		code := "flow_run_failed"
		status := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "flow_binding_") {
			status = http.StatusConflict
			code = "flow_binding_failed"
		}
		writeError(w, r, status, code, err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, run)
}

func (s *Server) listGlobalFlowVersions(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	id := r.PathValue("flowID")
	revisions, err := sources.ListFlowRevisions(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "not a flow") {
			writeError(w, r, http.StatusNotFound, "flow_not_found", err.Error(), false)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "flow_versions_failed", err.Error(), true)
		return
	}
	writeData(w, http.StatusOK, revisions)
}
