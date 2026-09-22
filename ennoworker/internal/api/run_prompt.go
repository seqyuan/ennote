package api

import (
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// getRunPrompt returns the frozen composition of a Run's system prompt: the
// ordered identity of every contribution plus the exact composed text.
//
// This is a read-only audit projection of what the Worker already froze before
// the first Provider request. It deliberately lives on its own endpoint rather
// than on the Run object: the composed prompt is tens of kilobytes, and every
// run listing would otherwise carry it.
func (s *Server) getRunPrompt(w http.ResponseWriter, r *http.Request) {
	composition, err := (&store.RunRepo{DB: s.DB}).LoadRunPromptComposition(r.Context(), r.PathValue("runID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"runId":              composition.RunID,
		"version":            composition.Version,
		"agentProfileId":     composition.AgentProfileID,
		"platformVersion":    composition.PlatformVersion,
		"digest":             composition.Digest,
		"sections":           nonNilPromptSections(composition.Sections),
		"sectionsDigest":     composition.SectionsDigest,
		"composedDigest":     composition.ComposedDigest,
		"prompt":             composition.Prompt,
		"skillCatalogState":  string(composition.SkillCatalogState),
		"skillCatalogDigest": composition.SkillCatalogDigest,
		// A Run that predates composition freezing must read as "not recorded"
		// rather than as an empty prompt.
		"recorded": composition.ComposedDigest != "" || composition.SectionsDigest != "",
	})
}

// nonNilPromptSections keeps the wire shape an array. The contract declares
// additionalProperties: false with sections required, and a JSON null would
// force every carrier to special-case it.
func nonNilPromptSections(sections []domain.PromptSection) []domain.PromptSection {
	if sections == nil {
		return []domain.PromptSection{}
	}
	return sections
}
