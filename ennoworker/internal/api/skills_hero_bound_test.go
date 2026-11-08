package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The composer's picker offers global Skills, so a Skill a Hero already loads
// has to be explainable rather than silently absent.
func TestSkillsListReportsWhichSkillAHeroLoads(t *testing.T) {
	home := t.TempDir()
	skillRoot := t.TempDir()
	for _, id := range []string{"cell-qc", "report-writer"} {
		require.NoError(t, os.MkdirAll(filepath.Join(skillRoot, id), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillRoot, id, "SKILL.md"),
			[]byte("---\nname: "+id+"\ndescription: fixture.\n---\n\nbody\n"), 0o644))
	}
	heroesDir := filepath.Join(home, "agents", "heroes", "reviewer")
	require.NoError(t, os.MkdirAll(heroesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(heroesDir, "hero.md"), []byte(`---
schemaVersion: 1
handle: reviewer
name: Reviewer
description: Reviews things
positioning: review
icon: shield
color: "#111111"
model:
  ref: provider/model
skills:
  - id: cell-qc
    mode: preload
permissionCeiling: discuss
---
Body.
`), 0o644))

	server := &Server{
		Token:         "test-token",
		Sandbox:       "none",
		Skills:        &skillsmgmt.Service{UserRoot: skillRoot, HomeDir: home},
		GlobalSources: &globalsource.Store{HomeDir: home},
	}
	handler := server.Handler()
	response := request(t, handler, http.MethodGet, "/v1/skills", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var result skillsmgmt.ListResult
	decodeData(t, response, &result)
	byID := map[string]skillsmgmt.AnnotatedSkill{}
	for _, skill := range result.Skills {
		byID[skill.SkillID] = skill
	}
	require.Contains(t, byID, "cell-qc")
	assert.True(t, byID["cell-qc"].HeroBound)
	assert.Equal(t, []string{"reviewer"}, byID["cell-qc"].BoundBy)
	require.Contains(t, byID, "report-writer")
	assert.False(t, byID["report-writer"].HeroBound)
	assert.Empty(t, byID["report-writer"].BoundBy)
}
