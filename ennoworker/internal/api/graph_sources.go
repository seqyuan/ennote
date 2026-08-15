package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
)

type globalGraphSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Digest        string `json:"digest,omitempty"`
	Error         string `json:"error,omitempty"`
	LatestVersion int    `json:"latestVersion"`
}

type globalGraphDetail struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Path          string                `json:"path"`
	Digest        string                `json:"digest"`
	LatestVersion int                   `json:"latestVersion"`
	Document      *graphsource.Document `json:"document"`
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
		summary := globalGraphSummary{ID: entry.ID, Name: entry.Name, Path: entry.Path, Digest: entry.Digest, Error: entry.Error}
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
	document, digest, err := sources.CreateGraph(body.ID, body.Name)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "graph_create_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, globalGraphDetail{
		ID: document.ID, Name: document.Name, Path: sources.GraphsDir() + "/" + document.ID + "/graph.yaml", Digest: digest, Document: document,
	})
}

func (s *Server) getGlobalGraph(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	id := r.PathValue("graphID")
	document, digest, err := sources.ReadGraph(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "graph_not_found", err.Error(), false)
		return
	}
	latestVersion := 0
	if revisions, revisionErr := sources.ListGraphRevisions(id); revisionErr == nil && len(revisions) > 0 {
		latestVersion = revisions[len(revisions)-1].Version
	}
	writeData(w, http.StatusOK, globalGraphDetail{
		ID: id, Name: document.Name, Path: sources.GraphsDir() + "/" + id + "/graph.yaml",
		Digest: digest, LatestVersion: latestVersion, Document: document,
	})
}

func (s *Server) publishGlobalGraph(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	document, _, err := sources.ReadGraph(r.PathValue("graphID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "graph_not_found", err.Error(), false)
		return
	}
	if len(document.Tasks) == 0 {
		writeError(w, r, http.StatusUnprocessableEntity, "graph_publish_failed", "Graph requires at least one Task before publication", false)
		return
	}
	if err := s.validateGraphFileResources(r, document); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "graph_publish_failed", err.Error(), false)
		return
	}
	revision, err := sources.PublishGraphRevision(document.ID)
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
		writeError(w, r, http.StatusUnprocessableEntity, "graph_run_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, run)
}

func (s *Server) validateGraphFileResources(r *http.Request, document *graphsource.Document) error {
	for taskID, task := range document.Tasks {
		if task.Model != "" && s.Models != nil {
			if _, err := s.Models.ResolvePortableRef(r.Context(), task.Model); err != nil {
				return fmt.Errorf("Task %q: %w", taskID, err)
			}
		}
		if task.Role == "" {
			continue
		}
		scope, id, _ := strings.Cut(task.Role, "/")
		var refs []string
		switch scope {
		case "global":
			role, _, err := s.GlobalSources.ReadRole(id)
			if err != nil {
				return fmt.Errorf("Task %q global Role %q: %w", taskID, id, err)
			}
			refs = append([]string{role.Model.Ref}, role.Model.Fallbacks...)
		case "local":
			role, _, err := s.GlobalSources.ReadGraphRole(document.ID, id)
			if err != nil {
				return fmt.Errorf("Task %q local Role %q: %w", taskID, id, err)
			}
			refs = append([]string{role.Model.Ref}, role.Model.Fallbacks...)
		}
		if s.Models != nil {
			for _, ref := range refs {
				if _, err := s.Models.ResolvePortableRef(r.Context(), ref); err != nil {
					return fmt.Errorf("Task %q Role model: %w", taskID, err)
				}
			}
		}
	}
	return nil
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
			ID    string            `json:"id"`
			Value *graphsource.Task `json:"value"`
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
	document, digest, err := sources.UpdateGraph(r.PathValue("graphID"), body.ExpectedDigest, func(document *graphsource.Document) error {
		if body.Name != nil {
			document.Name = strings.TrimSpace(*body.Name)
		}
		if body.Description != nil {
			document.Description = strings.TrimSpace(*body.Description)
		}
		if body.Task != nil {
			if body.Task.Value == nil {
				delete(document.Tasks, body.Task.ID)
				delete(document.Graph, body.Task.ID)
			} else {
				document.Tasks[body.Task.ID] = *body.Task.Value
				if _, exists := document.Graph[body.Task.ID]; !exists {
					document.Graph[body.Task.ID] = []string{}
				}
			}
		}
		if body.Dependencies != nil {
			document.Graph[body.Dependencies.TaskID] = append([]string(nil), body.Dependencies.Depends...)
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
	writeData(w, http.StatusOK, globalGraphDetail{
		ID: document.ID, Name: document.Name, Path: sources.GraphsDir() + "/" + document.ID + "/graph.yaml", Digest: digest, Document: document,
	})
}

// GraphRunStarter starts one file-native Graph Run. graphrun.Service implements
// it; tests stub it to assert routing.
type GraphRunStarter interface {
	Start(ctx context.Context, projectID, graphID string, version int, sessionID string,
		inputs, vars map[string]any) (*domain.RunAgentFlow, error)
}
