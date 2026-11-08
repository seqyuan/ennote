package store

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The user's selection is appended after the Hero's own preload bindings and is
// de-duplicated by id: a Hero preload already pinned by a published revision
// wins, because the selected copy would only restate the same digest.
func TestFreezeSelectedSkillsMergesAfterHeroPreload(t *testing.T) {
	repo := &RunRepo{SkillDigests: map[string]string{"cell-qc": "d-cell", "report": "d-report"}}
	hero := &domain.FrozenHeroExecution{
		FrozenSkills: domain.FrozenSkillRefs{{ID: "report", Digest: "d-report"}},
	}

	refs, err := repo.freezeSelectedSkills(hero, []string{"cell-qc", "report", "cell-qc"})
	require.NoError(t, err)
	require.Len(t, refs, 1, "report is already carried by the Hero; cell-qc appears once")
	assert.Equal(t, "cell-qc", refs[0].ID)
	assert.Equal(t, "d-cell", refs[0].Digest)
}

func TestFreezeSelectedSkillsRefusesACatalogMiss(t *testing.T) {
	repo := &RunRepo{SkillDigests: map[string]string{"cell-qc": "d-cell"}}
	_, err := repo.freezeSelectedSkills(nil, []string{"cell-qc", "ghost"})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorSkillNotFound, domain.ErrorCodeOf(err))
}

func TestFreezeSelectedSkillsIsEmptyWithoutASelection(t *testing.T) {
	repo := &RunRepo{SkillDigests: map[string]string{"cell-qc": "d-cell"}}
	refs, err := repo.freezeSelectedSkills(nil, nil)
	require.NoError(t, err)
	assert.Empty(t, refs)
}
