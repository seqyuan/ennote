package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// validateSessionSkills checks the Skills a client attached to one turn against
// the live catalog.
//
// Two rules, both about honesty rather than permission:
//
//   - An id the catalog does not know is refused. A turn that claims to load a
//     Skill it cannot name would freeze a prompt whose provenance is a guess.
//   - A Skill marked `disable-model-invocation` is accepted. That marker exists
//     precisely to withhold the Skill from the model's own reach; a user picking
//     it in the composer is the explicit invocation it reserves.
//
// An empty list is not an error: most turns carry no Skills.
func (s *Server) validateSessionSkills(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if s.Skills == nil {
		return domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("skills are not configured on this Worker"))
	}
	result, err := s.Skills.List("")
	if err != nil {
		return fmt.Errorf("load skill catalog: %w", err)
	}
	known := make(map[string]bool, len(result.Skills))
	for _, skill := range result.Skills {
		known[skill.SkillID] = true
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !known[id] {
			return domain.NewCodedError(domain.ErrorSkillNotFound,
				fmt.Errorf("skill %q is not in the catalog", id))
		}
	}
	return nil
}
