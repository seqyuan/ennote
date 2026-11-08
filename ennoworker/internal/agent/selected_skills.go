package agent

import (
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/systemprompt"
)

// SelectedSkillSource names the prompt segment provenance of a Skill the user
// attached to one turn. It is deliberately distinct from the Hero's
// `hero:<handle>@<version>` source so the composition record answers "who put
// this body in front of the model": a Role revision, or the person typing.
const SelectedSkillSource = "user-skill-select"

// SelectedSkillSegments turns the turn's frozen Skill selection into inlined
// prompt segments.
//
// Every ref is digest-pinned, so a Skill edited after the user picked it fails
// the Run instead of silently changing what the model reads. That is the same
// fail-closed rule as a Hero preload; the only difference is provenance.
func SelectedSkillSegments(refs domain.FrozenSkillRefs, catalog []*skills.LoadedSkill) ([]systemprompt.Segment, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	loaded := make(map[string]*skills.LoadedSkill, len(catalog))
	for _, skill := range catalog {
		loaded[skill.Manifest.ID] = skill
	}
	segments := make([]systemprompt.Segment, 0, len(refs))
	for _, ref := range refs {
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			continue
		}
		skill, ok := loaded[id]
		if !ok {
			return nil, fmt.Errorf("selected skill %q is no longer in the catalog: restore it or send the turn again "+
				"without it", id)
		}
		if ref.Pinned() && ref.Digest != skill.ContentHash {
			return nil, fmt.Errorf("selected skill %q does not match its frozen content digest (frozen %s, on-disk %s): "+
				"the Skill changed after this turn was submitted, so the Run refuses to execute unpinned content",
				id, ref.Digest, skill.ContentHash)
		}
		segments = append(segments, systemprompt.SkillSegment(id, skill.PromptText, SelectedSkillSource))
	}
	return segments, nil
}
