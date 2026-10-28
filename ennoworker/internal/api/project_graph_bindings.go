package api

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
)

type graphBindingMutation struct {
	SourceKind      string `json:"sourceKind,omitempty"`
	Revision        string `json:"revision,omitempty"`
	Enabled         bool   `json:"enabled"`
	ClientRequestID string `json:"clientRequestId,omitempty"`
}

func (s *Server) graphBindingStore(w http.ResponseWriter, r *http.Request) *projectstore.Store {
	if s.Projects == nil || s.Projects.Files == nil {
		writeError(w, r, http.StatusServiceUnavailable, "project_store_unavailable", "project store is unavailable", true)
		return nil
	}
	return s.Projects.Files
}

func (s *Server) listProjectGraphBindings(w http.ResponseWriter, r *http.Request) {
	store := s.graphBindingStore(w, r)
	if store == nil {
		return
	}
	manifest, err := store.ReadManifest(r.PathValue("projectID"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, projectstore.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "project_not_found", "project not found", false)
			return
		}
		writeError(w, r, http.StatusBadRequest, "project_invalid", err.Error(), false)
		return
	}
	bindings := manifest.GraphBindings
	if bindings == nil {
		bindings = []projectstore.GraphBinding{}
	}
	writeData(w, http.StatusOK, bindings)
}

// putProjectGraphBinding enables or disables one published source revision for
// a Project. Enabling without an explicit revision pins the latest published
// revision; it never leaves a "latest" wildcard that could drift. Publishing
// and enabling remain separate operations.
func (s *Server) putProjectGraphBinding(w http.ResponseWriter, r *http.Request) {
	store := s.graphBindingStore(w, r)
	if store == nil {
		return
	}
	if s.GlobalSources == nil {
		writeError(w, r, http.StatusServiceUnavailable, "source_store_unavailable", "source store is unavailable", true)
		return
	}
	projectID := r.PathValue("projectID")
	graphID := r.PathValue("graphID")
	manifest, err := store.ReadManifest(projectID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, projectstore.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "project_not_found", "project not found", false)
			return
		}
		writeError(w, r, http.StatusBadRequest, "project_invalid", err.Error(), false)
		return
	}
	var body graphBindingMutation
	if !decodeJSON(w, r, &body) {
		return
	}
	kind := strings.TrimSpace(body.SourceKind)
	if kind == "" {
		kind = inferSourceKind(s.GlobalSources, graphID)
	}
	if kind != globalsource.SourceKindGraph && kind != globalsource.SourceKindFlow {
		writeError(w, r, http.StatusBadRequest, "source_kind_invalid", "sourceKind must be graph or flow", false)
		return
	}
	actualKind, err := s.GlobalSources.SourceKind(graphID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, r, http.StatusNotFound, "source_not_found", "source not found", false)
			return
		}
		writeError(w, r, http.StatusBadRequest, "source_invalid", err.Error(), false)
		return
	}
	if actualKind != kind {
		writeError(w, r, http.StatusConflict, "source_kind_mismatch",
			"binding sourceKind does not match the source object", false)
		return
	}
	revisions, err := listSourceRevisions(s.GlobalSources, kind, graphID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "source_versions_failed", err.Error(), true)
		return
	}
	revision := strings.TrimSpace(body.Revision)
	if revision != "" {
		found := false
		for _, candidate := range revisions {
			if candidate.ID() == revision || candidate.Digest == revision {
				revision = candidate.ID()
				found = true
				break
			}
		}
		if !found {
			writeError(w, r, http.StatusUnprocessableEntity, "source_revision_not_found",
				"revision "+revision+" is not published for this source", false)
			return
		}
	}
	if body.Enabled && revision == "" {
		if len(revisions) == 0 {
			writeError(w, r, http.StatusUnprocessableEntity, "source_revision_not_found",
				"source has no published revision; publish it before enabling", false)
			return
		}
		revision = revisions[len(revisions)-1].ID()
	}
	if !body.Enabled && revision == "" {
		// Disabling keeps the existing pinned revision when present.
		for _, binding := range manifest.GraphBindings {
			bindingKind := binding.SourceKind
			if bindingKind == "" {
				bindingKind = globalsource.SourceKindGraph
			}
			if bindingKind == kind && binding.GraphID == graphID {
				revision = binding.Revision
				break
			}
		}
	}
	updated, err := store.UpdateGraphBindings(r.Context(), projectID, func(bindings *[]projectstore.GraphBinding) error {
		for index := range *bindings {
			binding := (*bindings)[index]
			bindingKind := binding.SourceKind
			if bindingKind == "" {
				bindingKind = globalsource.SourceKindGraph
			}
			if bindingKind == kind && binding.GraphID == graphID {
				(*bindings)[index] = projectstore.GraphBinding{
					SourceKind: kind, GraphID: graphID, Revision: revision, Enabled: body.Enabled,
				}
				return nil
			}
		}
		*bindings = append(*bindings, projectstore.GraphBinding{
			SourceKind: kind, GraphID: graphID, Revision: revision, Enabled: body.Enabled,
		})
		return nil
	})
	if err != nil {
		if errors.Is(err, projectstore.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "project_not_found", "project not found", false)
			return
		}
		writeError(w, r, http.StatusUnprocessableEntity, "graph_binding_invalid", err.Error(), false)
		return
	}
	for _, binding := range updated.GraphBindings {
		bindingKind := binding.SourceKind
		if bindingKind == "" {
			bindingKind = globalsource.SourceKindGraph
		}
		if bindingKind == kind && binding.GraphID == graphID {
			binding.SourceKind = bindingKind
			writeData(w, http.StatusOK, binding)
			return
		}
	}
	writeError(w, r, http.StatusInternalServerError, "graph_binding_missing", "binding was not persisted", true)
}

func (s *Server) deleteProjectGraphBinding(w http.ResponseWriter, r *http.Request) {
	store := s.graphBindingStore(w, r)
	if store == nil {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("sourceKind"))
	if kind == "" && s.GlobalSources != nil {
		kind = inferSourceKind(s.GlobalSources, r.PathValue("graphID"))
	}
	if kind == "" {
		kind = globalsource.SourceKindGraph
	}
	graphID := r.PathValue("graphID")
	_, err := store.UpdateGraphBindings(r.Context(), r.PathValue("projectID"), func(bindings *[]projectstore.GraphBinding) error {
		filtered := (*bindings)[:0]
		for _, binding := range *bindings {
			bindingKind := binding.SourceKind
			if bindingKind == "" {
				bindingKind = globalsource.SourceKindGraph
			}
			if bindingKind == kind && binding.GraphID == graphID {
				continue
			}
			filtered = append(filtered, binding)
		}
		*bindings = filtered
		return nil
	})
	if err != nil {
		if errors.Is(err, projectstore.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "project_not_found", "project not found", false)
			return
		}
		writeError(w, r, http.StatusUnprocessableEntity, "graph_binding_invalid", err.Error(), false)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func inferSourceKind(sources *globalsource.Store, id string) string {
	if sources == nil {
		return ""
	}
	kind, err := sources.SourceKind(id)
	if err != nil {
		return ""
	}
	return kind
}

func listSourceRevisions(sources *globalsource.Store, kind, id string) ([]globalsource.Revision, error) {
	if kind == globalsource.SourceKindFlow {
		return sources.ListFlowRevisions(id)
	}
	return sources.ListGraphRevisions(id)
}
