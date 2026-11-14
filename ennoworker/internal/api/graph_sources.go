package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

type globalGraphSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Digest        string `json:"digest,omitempty"`
	Error         string `json:"error,omitempty"`
	LatestVersion int    `json:"latestVersion"`
}

// globalGraphDetail is the detail projection of one Graph object.
type globalGraphDetail struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	Path          string                 `json:"path"`
	Digest        string                 `json:"digest"`
	LatestVersion int                    `json:"latestVersion"`
	Definition    *domain.FlowDefinition `json:"definition,omitempty"`
}

// graphDefinitionDetailFor builds the detail response for a definition-shaped
// draft. The caller supplies the digest it just computed; latestVersion always
// comes from the published revisions so create, read and update agree.
func (s *Server) graphDefinitionDetailFor(sources *globalsource.Store, definition *domain.FlowDefinition, digest string) globalGraphDetail {
	detail := globalGraphDetail{
		ID: definition.ID, Name: definition.Name, Digest: digest, Definition: definition,
		Path: sources.GraphsDir() + "/" + definition.ID + "/graph.yaml",
	}
	if revisions, err := sources.ListGraphRevisions(definition.ID); err == nil && len(revisions) > 0 {
		detail.LatestVersion = revisions[len(revisions)-1].Version
	}
	return detail
}

func (s *Server) sourceStore(w http.ResponseWriter, r *http.Request) *globalsource.Store {
	if s.GlobalSources == nil {
		writeError(w, r, http.StatusServiceUnavailable, "global_sources_unavailable", "global source store is unavailable", true)
		return nil
	}
	return s.GlobalSources
}

func (s *Server) listGlobalGraphs(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	entries, err := sources.ListGraphs()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "graph_catalog_failed", err.Error(), true)
		return
	}
	globalsource.SortGraphEntries(entries)
	result := make([]globalGraphSummary, 0, len(entries))
	for _, entry := range entries {
		summary := globalGraphSummary{
			ID: entry.ID, Name: entry.Name, Path: entry.Path, Digest: entry.Digest, Error: entry.Error,
		}
		if revisions, revisionErr := sources.ListGraphRevisions(entry.ID); revisionErr == nil && len(revisions) > 0 {
			summary.LatestVersion = revisions[len(revisions)-1].Version
		}
		result = append(result, summary)
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) createGlobalGraph(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	definition, digest, err := sources.CreateGraphDefinition(body.ID, body.Name)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "graph_create_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, s.graphDefinitionDetailFor(sources, definition, digest))
}

// getGlobalGraph reads the definition-shaped graph.yaml the object holds.
func (s *Server) getGlobalGraph(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	id := r.PathValue("graphID")
	definition, digest, err := sources.ReadGraphDefinition(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "graph_not_found", "graph not found", false)
		return
	}
	writeData(w, http.StatusOK, s.graphDefinitionDetailFor(sources, definition, digest))
}

// publishGlobalGraph validates the draft with the flow validator and writes one
// immutable revision.
func (s *Server) publishGlobalGraph(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	id := r.PathValue("graphID")
	definition, _, err := sources.ReadGraphDefinition(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "graph_not_found", "graph not found", false)
		return
	}
	if strings.TrimSpace(definition.Name) == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "graph_name_required",
			"graph.yaml must declare a name before it can be published", false)
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
	revision, err := sources.PublishGraphRevision(id)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "graph_publish_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, revision)
}

func (s *Server) listGlobalGraphVersions(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	revisions, err := sources.ListGraphRevisions(r.PathValue("graphID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "graph_not_found", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, revisions)
}

func (s *Server) runGlobalGraph(w http.ResponseWriter, r *http.Request) {
	if s.GraphRuns == nil {
		writeError(w, r, http.StatusServiceUnavailable, "graph_runner_unavailable", "Graph runner is unavailable", true)
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
	run, err := s.GraphRuns.Start(r.Context(), session.ProjectID, r.PathValue("graphID"), body.Version,
		body.SessionID, body.Inputs, body.Vars)
	if err != nil {
		message := err.Error()
		// Surface the starter's binding errors with their own codes instead of
		// flattening every failure into graph_run_failed.
		switch {
		case strings.Contains(message, "graph_binding_required"):
			writeError(w, r, http.StatusUnprocessableEntity, "graph_binding_required", message, false)
		case strings.Contains(message, "graph_binding_revision_mismatch"):
			writeError(w, r, http.StatusUnprocessableEntity, "graph_binding_revision_mismatch", message, false)
		case strings.Contains(message, "graph_binding_disabled"):
			writeError(w, r, http.StatusUnprocessableEntity, "graph_binding_disabled", message, false)
		case strings.Contains(message, "graph_binding_revision_missing"):
			writeError(w, r, http.StatusUnprocessableEntity, "graph_binding_revision_missing", message, false)
		default:
			writeError(w, r, http.StatusUnprocessableEntity, "graph_run_failed", message, false)
		}
		return
	}
	writeData(w, http.StatusCreated, run)
}

func (s *Server) updateGlobalGraph(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	var body struct {
		ExpectedDigest string  `json:"expectedDigest"`
		Name           *string `json:"name"`
		Description    *string `json:"description"`
		Task           *struct {
			ID    string           `json:"id"`
			Value *domain.FlowTask `json:"value"`
		} `json:"task"`
		Dependencies *struct {
			TaskID  string   `json:"taskId"`
			Depends []string `json:"depends"`
		} `json:"dependencies"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == nil && body.Description == nil && body.Task == nil && body.Dependencies == nil {
		writeError(w, r, http.StatusBadRequest, "graph_mutation_required", "at least one semantic Graph change is required", false)
		return
	}
	definition, digest, err := sources.UpdateGraphDefinition(r.PathValue("graphID"), body.ExpectedDigest, func(definition *domain.FlowDefinition) error {
		if body.Name != nil {
			definition.Name = strings.TrimSpace(*body.Name)
		}
		if body.Description != nil {
			definition.Description = strings.TrimSpace(*body.Description)
		}
		if body.Task != nil {
			if body.Task.Value == nil {
				delete(definition.Tasks, body.Task.ID)
				for id, task := range definition.Tasks {
					task.Depends = pruneTaskDependency(task.Depends, body.Task.ID)
					definition.Tasks[id] = task
				}
			} else {
				definition.Tasks[body.Task.ID] = *body.Task.Value
			}
		}
		if body.Dependencies != nil {
			if _, ok := definition.Tasks[body.Dependencies.TaskID]; !ok {
				return fmt.Errorf("Task %q does not exist", body.Dependencies.TaskID)
			}
			task := definition.Tasks[body.Dependencies.TaskID]
			task.Depends = append([]string{}, body.Dependencies.Depends...)
			definition.Tasks[body.Dependencies.TaskID] = task
		}
		return nil
	})
	if errors.Is(err, globalsource.ErrConflict) {
		writeError(w, r, http.StatusConflict, "source_digest_conflict", "graph.yaml changed since it was loaded; reload before saving", false)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "graph_invalid", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, s.graphDefinitionDetailFor(sources, definition, digest))
}

// pruneTaskDependency removes one task id from a dependency list, preserving a
// non-nil empty slice so a task whose dependencies emptied still encodes as [].
func pruneTaskDependency(depends []string, taskID string) []string {
	filtered := depends[:0]
	for _, dependency := range depends {
		if dependency != taskID {
			filtered = append(filtered, dependency)
		}
	}
	return filtered
}

// GraphRunStarter starts one file-native Graph Run. graphrun.Service implements
// it; tests stub it to assert routing.
type GraphRunStarter interface {
	Start(ctx context.Context, projectID, graphID string, version int, sessionID string,
		inputs, vars map[string]any) (*domain.RunAgentFlow, error)
}
