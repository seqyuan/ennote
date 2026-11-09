package api

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// maxSelectedSkills bounds one turn's selection. Each selected Skill injects a
// whole body into the system prompt, so an unbounded list is an unbounded prompt.
const maxSelectedSkills = 64

// resolveSessionSkills pins the Skills a client attached to one turn.
//
// The digest is captured here, at submit time, rather than at freeze time. That
// is the difference between "the body you picked" and "whatever is on disk when
// the run finally starts": an edit in between must fail the Run, not silently
// replace the instructions the user chose.
//
// An id the digest catalog does not know is refused. The catalog is the same
// source the config freeze pins from, so validation and execution cannot
// disagree about which Skills exist.
func (s *Server) resolveSessionSkills(_ context.Context, ids []string) (domain.FrozenSkillRefs, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var catalog map[string]string
	if s.SkillCatalog != nil {
		catalog = s.SkillCatalog()
	}
	if catalog == nil {
		// Without the digest catalogue there is nothing to pin, so every selection
		// would freeze unpinned. That is an install-wide misconfiguration, and it
		// must not be reported as "this Skill does not exist".
		return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("the Skill catalogue is unavailable on this Worker, so a turn cannot pin Skill content"))
	}
	refs := make(domain.FrozenSkillRefs, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if len(refs) == maxSelectedSkills {
			return nil, domain.NewCodedError(domain.ErrorTooManySkills,
				fmt.Errorf("a turn may select at most %d skills", maxSelectedSkills))
		}
		seen[id] = true
		digest := strings.TrimSpace(catalog[id])
		if digest == "" {
			return nil, domain.NewCodedError(domain.ErrorSkillNotFound,
				fmt.Errorf("skill %q is not in the catalog", id))
		}
		refs = append(refs, domain.FrozenSkillRef{ID: id, Digest: digest})
	}
	return refs, nil
}

// skillHeroBindings indexes which Heroes load each Skill, so the skill list can
// say "this one already arrives with a Hero" instead of leaving a user hunting
// for a Skill the picker deliberately does not offer.
//
// It is built from the hero sources once per request: an N+1 over Hero documents
// on a list endpoint is not affordable, and the picker needs the answer for every
// row. Only Worker-global Heroes are indexed today; project- and flow-scoped
// Heroes have no Skill bindings the picker could surface anyway.
func (s *Server) skillHeroBindings() map[string][]string {
	bindings := map[string][]string{}
	if s.GlobalSources == nil {
		return bindings
	}
	entries, err := s.GlobalSources.ListHeroes()
	if err != nil {
		return bindings
	}
	for _, entry := range entries {
		if entry.Error != "" || entry.Document == nil {
			continue
		}
		handle := entry.Document.Handle
		if handle == "" {
			handle = entry.ID
		}
		for _, binding := range entry.Document.Skills {
			id := strings.TrimSpace(binding.ID)
			if id == "" {
				continue
			}
			bindings[id] = append(bindings[id], handle)
		}
	}
	for id := range bindings {
		sort.Strings(bindings[id])
	}
	return bindings
}
