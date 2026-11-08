package api

import (
	"errors"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
)

type globalHeroSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Digest string `json:"digest,omitempty"`
	Error  string `json:"error,omitempty"`
}

type globalHeroDetail struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Path     string               `json:"path"`
	Digest   string               `json:"digest"`
	Document *heroesource.Document `json:"document"`
}

func (s *Server) listGlobalHeroes(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	entries, err := sources.ListHeroes()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "hero_catalog_failed", err.Error(), true)
		return
	}
	globalsource.SortHeroEntries(entries)
	result := make([]globalHeroSummary, 0, len(entries))
	for _, entry := range entries {
		result = append(result, globalHeroSummary{ID: entry.ID, Name: entry.Name, Path: entry.Path, Digest: entry.Digest, Error: entry.Error})
	}
	writeData(w, http.StatusOK, result)
}

func (s *Server) createGlobalHero(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	var body struct {
		Document *heroesource.Document `json:"document"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Document == nil {
		writeError(w, r, http.StatusBadRequest, "hero_document_required", "Role document is required", false)
		return
	}
	document, digest, err := sources.CreateHero(body.Document)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "hero_create_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, globalHeroDetail{ID: document.Handle, Name: document.Name, Path: sources.HeroesDir() + "/" + document.Handle + "/hero.md", Digest: digest, Document: document})
}

func (s *Server) getGlobalHero(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	id := r.PathValue("heroID")
	document, digest, err := sources.ReadHero(id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "hero_not_found", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, globalHeroDetail{ID: id, Name: document.Name, Path: sources.HeroesDir() + "/" + id + "/hero.md", Digest: digest, Document: document})
}

func (s *Server) publishGlobalHero(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	document, _, err := sources.ReadHero(r.PathValue("heroID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "hero_not_found", err.Error(), false)
		return
	}
	if s.Models != nil {
		refs := append([]string{document.Model.Ref}, document.Model.Fallbacks...)
		for _, ref := range refs {
			if _, err := s.Models.ResolvePortableRef(r.Context(), ref); err != nil {
				writeError(w, r, http.StatusUnprocessableEntity, "hero_validation_failed", err.Error(), false)
				return
			}
		}
	}
	// V2: publishing a global Role is a pure file operation (immutable
	// revision). The legacy SQL Role repo was removed.
	revision, err := sources.PublishHeroRevision(document.Handle)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "hero_publish_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"heroId": document.Handle, "revision": revision, "version": revision.Version})
}

func (s *Server) listGlobalHeroRevisions(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	revisions, err := sources.ListHeroRevisions(r.PathValue("heroID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "hero_revisions_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, revisions)
}

// getResolvedGlobalHero dumps the final effective Role for a bare handle: the
// latest published revision plus its audit source (design 六 P1). Unlike the
// draft endpoint it never returns unpublished hero.md content.
func (s *Server) getResolvedGlobalHero(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	resolved, err := sources.ResolveHero(r.PathValue("heroID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "hero_not_found", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"heroId":   resolved.Document.Handle,
		"source":   resolved.Source,
		"revision": resolved.Revision,
		"document": resolved.Document,
	})
}

func (s *Server) updateGlobalHero(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	var body struct {
		ExpectedDigest string               `json:"expectedDigest"`
		Document       *heroesource.Document `json:"document"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Document == nil {
		writeError(w, r, http.StatusBadRequest, "hero_document_required", "Role document is required", false)
		return
	}
	document, digest, err := sources.UpdateHero(r.PathValue("heroID"), body.ExpectedDigest, func(current *heroesource.Document) error {
		*current = *body.Document
		return nil
	})
	if errors.Is(err, globalsource.ErrConflict) {
		writeError(w, r, http.StatusConflict, "source_digest_conflict", "hero.md changed since it was loaded; reload before saving", false)
		return
	}
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "hero_invalid", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, globalHeroDetail{ID: document.Handle, Name: document.Name, Path: sources.HeroesDir() + "/" + document.Handle + "/hero.md", Digest: digest, Document: document})
}
