package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
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

type globalGraphDetail struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Path          string                `json:"path"`
	Digest        string                `json:"digest"`
	LatestVersion int                   `json:"latestVersion"`
	Document      *graphsource.Document `json:"document"`
}

// errGraphValidationUnavailable reports that the publish could not be validated
// because a dependency the Worker needs is missing, which is a 503 and not the
// author's 422.
var errGraphValidationUnavailable = errors.New("graph validation is unavailable")

// graphHasSkillRefs reports whether any Task declares a Skill, which is what makes
// the Skill catalog a required dependency of the publish.
func graphHasSkillRefs(document *graphsource.Document) bool {
	for _, task := range document.Tasks {
		if len(task.Skills) > 0 {
			return true
		}
	}
	return false
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
	if err := s.validateGraphPublish(r, document); err != nil {
		if errors.Is(err, errGraphValidationUnavailable) {
			writeError(w, r, http.StatusServiceUnavailable, "graph_validation_unavailable", err.Error(), true)
			return
		}
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

// validateGraphPublish is the Graph authoring format's share of Flow publish
// validation. A Graph is a draft (it runs straight from its revision, with no
// project binding and a synthetic budget), so it cannot adopt the Flow checks
// that need a frozen role, a declared budget or an enabled binding. Two checks
// do carry over, and they are not the same kind of check:
//
//   - An unresolved Skill reference already fails the Run when it freezes
//     (graphrun.resolveTaskRole), so rejecting it at publish only moves an
//     existing failure earlier. It cannot reject a Graph that would have run.
//   - A writes scope is scheduling-only: the planner reads it as a literal
//     prefix and nothing enforces it at Run time, so a scope the parser cannot
//     read is not a Run-time failure today. Rejecting it at publish is a
//     deliberate tightening — it catches a typo at the moment the author can fix
//     it, at the cost of refusing a Graph that would technically have run.
func (s *Server) validateGraphPublish(r *http.Request, document *graphsource.Document) error {
	// Resolving Skills needs the live catalog; Flow publish answers 503 for the same
	// missing dependency (flow_validation_unavailable) instead of reporting it as a
	// node-level unknown Skill.
	if s.SkillCatalog == nil && graphHasSkillRefs(document) {
		return fmt.Errorf("%w: the Skill catalog is missing, so Skill references cannot be resolved",
			errGraphValidationUnavailable)
	}
	// Skill resolution goes through the same resolver Flow publish uses, so
	// local/<id> and global/<id> mean one thing across both formats.
	resolver := store.NewFlowValidator(store.FlowPublishOptions{
		FlowID: document.ID, Sources: s.GlobalSources, Models: s.Models, SkillCatalog: s.SkillCatalog,
	}).Resolver

	for taskID, task := range document.Tasks {
		for _, scope := range task.Writes {
			if err := agentflow.ValidateWritesScope(scope); err != nil {
				return fmt.Errorf("Task %q writes scope %q: %w", taskID, scope, err)
			}
		}
		for _, skill := range task.Skills {
			if !resolver.KnownSkill(r.Context(), skill) {
				return fmt.Errorf("Task %q references unknown skill %q", taskID, skill)
			}
		}
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
