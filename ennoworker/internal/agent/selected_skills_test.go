package agent

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadedFixture(id, text, hash string) *skills.LoadedSkill {
	return &skills.LoadedSkill{Manifest: skills.Manifest{ID: id}, PromptText: text, ContentHash: hash}
}

// A Skill the user picked is injected with its own provenance, so the frozen
// composition record can tell a Hero's preload from a person's choice.
func TestSelectedSkillSegmentsNameTheUserAsTheSource(t *testing.T) {
	segments, err := SelectedSkillSegments(
		domain.FrozenSkillRefs{{ID: "cell-qc", Digest: "hash-1"}},
		[]*skills.LoadedSkill{loadedFixture("cell-qc", "how to QC", "hash-1")},
	)
	require.NoError(t, err)
	require.Len(t, segments, 1)
	assert.Equal(t, domain.PromptSectionSkillPreload, segments[0].Kind)
	assert.Equal(t, SelectedSkillSource, segments[0].Source)
	assert.Equal(t, "skill.preload.cell-qc", segments[0].ID)
	assert.Contains(t, segments[0].Text, "how to QC")
}

// A Skill edited after the turn was submitted must fail the Run rather than
// silently hand the model different instructions than the ones that were chosen.
func TestSelectedSkillSegmentsFailClosedOnDigestDrift(t *testing.T) {
	_, err := SelectedSkillSegments(
		domain.FrozenSkillRefs{{ID: "cell-qc", Digest: "hash-1"}},
		[]*skills.LoadedSkill{loadedFixture("cell-qc", "edited body", "hash-2")},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match its frozen content digest")
}

func TestSelectedSkillSegmentsSkipAnEmptySelection(t *testing.T) {
	segments, err := SelectedSkillSegments(nil, nil)
	require.NoError(t, err)
	assert.Empty(t, segments)
}
