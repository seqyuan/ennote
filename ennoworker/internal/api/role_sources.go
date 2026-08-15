package api

import (
	"errors"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
)

type globalRoleSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Digest string `json:"digest,omitempty"`
	Error  string `json:"error,omitempty"`
}

type globalRoleDetail struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Path     string               `json:"path"`
	Digest   string               `json:"digest"`
	Document *rolesource.Document `json:"document"`
}

func (s *Server) listGlobalRoles(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	entries, err := sources.ListRoles()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "role_catalog_failed", err.Error(), true)
		return
	}
	globalsource.SortRoleEntries(entries)
	result := make([]globalRoleSummary, 0, len(entries))
	for _, entry := range entries {
		result = append(result, globalRoleSummary{ID: entry.ID, Name: entry.Name, Path: entry.Path, Digest: entry.Digest, Error: entry.Error})
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) createGlobalRole(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	var body struct {
		Document *rolesource.Document `json:"document"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Document == nil {
		writeError(w, r, http.StatusBadRequest, "role_document_required", "Role document is required", false)
		return
	}
	document, digest, err := sources.CreateRole(body.Document)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "role_create_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, globalRoleDetail{ID: document.Handle, Name: document.Name, Path: sources.RolesDir() + "/" + document.Handle + "/role.md", Digest: digest, Document: document})
}

func (s *Server) getGlobalRole(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	id := r.PathValue("roleID")
	document, digest, err := sources.ReadRole(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "role_not_found", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, globalRoleDetail{ID: id, Name: document.Name, Path: sources.RolesDir() + "/" + id + "/role.md", Digest: digest, Document: document})
}

func (s *Server) publishGlobalRole(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	document, _, err := sources.ReadRole(r.PathValue("roleID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "role_not_found", err.Error(), false)
		return
	}
	if s.Models != nil {
		refs := append([]string{document.Model.Ref}, document.Model.Fallbacks...)
		for _, ref := range refs {
			if _, err := s.Models.ResolvePortableRef(r.Context(), ref); err != nil {
				writeError(w, r, http.StatusUnprocessableEntity, "role_validation_failed", err.Error(), false)
				return
			}
		}
	}
	// V2: publishing a global Role is a pure file operation (immutable
	// revision). The legacy SQL Role repo was removed.
	revision, err := sources.PublishRoleRevision(document.Handle)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "role_publish_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"roleId": document.Handle, "revision": revision, "version": revision.Version})
}

func (s *Server) listGlobalRoleRevisions(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	revisions, err := sources.ListRoleRevisions(r.PathValue("roleID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "role_revisions_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, revisions)
}

// getResolvedGlobalRole dumps the final effective Role for a bare handle: the
// latest published revision plus its audit source (design 六 P1). Unlike the
// draft endpoint it never returns unpublished role.md content.
func (s *Server) getResolvedGlobalRole(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	resolved, err := sources.ResolveRole(r.PathValue("roleID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "role_not_found", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"roleId":   resolved.Document.Handle,
		"source":   resolved.Source,
		"revision": resolved.Revision,
		"document": resolved.Document,
	})
}

func (s *Server) updateGlobalRole(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	var body struct {
		ExpectedDigest string               `json:"expectedDigest"`
		Document       *rolesource.Document `json:"document"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Document == nil {
		writeError(w, r, http.StatusBadRequest, "role_document_required", "Role document is required", false)
		return
	}
	document, digest, err := sources.UpdateRole(r.PathValue("roleID"), body.ExpectedDigest, func(current *rolesource.Document) error {
		*current = *body.Document
		return nil
	})
	if errors.Is(err, globalsource.ErrConflict) {
		writeError(w, r, http.StatusConflict, "source_digest_conflict", "role.md changed since it was loaded; reload before saving", false)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "role_invalid", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, globalRoleDetail{ID: document.Handle, Name: document.Name, Path: sources.RolesDir() + "/" + document.Handle + "/role.md", Digest: digest, Document: document})
}
