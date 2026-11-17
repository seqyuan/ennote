package api

import (
	"errors"
	"net/http"
	"os"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
)

// graphLocalHeroDetail is the draft projection of a Graph-private Hero: the same
// shape the global Hero detail uses, so the editor renders one Hero form for both.
type graphLocalHeroDetail struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Path     string                `json:"path"`
	Digest   string                `json:"digest"`
	Document *heroesource.Document `json:"document"`
}

// getGraphLocalHero reads the Graph-private Hero draft a Task's local/<id>
// reference resolves to. Until this endpoint existed the editor could write that
// reference and never create or inspect the document behind it.
func (s *Server) getGraphLocalHero(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	graphID, heroID := r.PathValue("graphID"), r.PathValue("heroID")
	document, digest, err := sources.ReadGraphHero(graphID, heroID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "hero_not_found", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, graphLocalHeroDetail{
		ID: heroID, Name: document.Name, Path: sources.GraphHeroPath(graphID, heroID),
		Digest: digest, Document: document,
	})
}

// updateGraphLocalHero applies one whole-document CAS update to that draft.
//
// It is deliberately not a publish path: a Graph Run freezes the Heroes it uses
// from the published revision, so editing the draft changes the next publish, not
// a Run that already exists.
func (s *Server) updateGraphLocalHero(w http.ResponseWriter, r *http.Request) {
	sources := s.sourceStore(w, r)
	if sources == nil {
		return
	}
	var body struct {
		ExpectedDigest string                `json:"expectedDigest"`
		Document       *heroesource.Document `json:"document"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Document == nil {
		writeError(w, r, http.StatusBadRequest, "hero_document_required", "Hero document is required", false)
		return
	}
	if err := heroesource.ValidateForWrite(body.Document); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "hero_validation_failed", err.Error(), false)
		return
	}
	if s.Models != nil {
		refs := append([]string{body.Document.Model.Ref}, body.Document.Model.Fallbacks...)
		for _, ref := range refs {
			if _, err := s.Models.ResolvePortableRef(r.Context(), ref); err != nil {
				writeError(w, r, http.StatusUnprocessableEntity, "hero_validation_failed", err.Error(), false)
				return
			}
		}
	}
	graphID, heroID := r.PathValue("graphID"), r.PathValue("heroID")
	document, digest, err := sources.UpdateGraphHero(graphID, heroID, body.ExpectedDigest, func(current *heroesource.Document) error {
		*current = *body.Document
		return nil
	})
	if err != nil {
		writeSourceError(w, r, err, "local Hero %q is not a draft of this Graph")
		return
	}
	writeData(w, http.StatusOK, graphLocalHeroDetail{
		ID: heroID, Name: document.Name, Path: sources.GraphHeroPath(graphID, heroID),
		Digest: digest, Document: document,
	})
}

// writeSourceError maps the two errors every source-backed write can produce:
// somebody else changed the file first, or the object does not exist. Everything
// else is a validation failure the caller has to fix.
func writeSourceError(w http.ResponseWriter, r *http.Request, err error, notFoundMessage string) {
	if err == globalsource.ErrConflict {
		writeError(w, r, http.StatusConflict, "source_digest_conflict",
			"the file changed since it was loaded; reload before saving", false)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "hero_not_found", notFoundMessage, false)
		return
	}
	writeError(w, r, http.StatusUnprocessableEntity, "hero_validation_failed", err.Error(), false)
}
