package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRolePreloadPromptInjectsFrozenSkillPrompts(t *testing.T) {
	skillsDir := t.TempDir()
	skillDir := filepath.Join(skillsDir, "review-guard")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.json"),
		[]byte(`{"id":"review-guard","version":"1","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("REVIEW_GUARD_PRELOAD: only report evidence-backed conclusions."), 0o600))

	executor := &agentExecutor{skillsDir: skillsDir}
	role := &domain.FrozenRoleExecution{
		Skills: domain.RoleSkills{Entries: []domain.RoleSkillEntry{
			{SkillID: "review-guard", Mode: domain.RoleSkillPreload},
			{SkillID: "missing-skill", Mode: domain.RoleSkillPreload},
		}},
	}
	fragment := executor.rolePreloadPrompt(role)
	assert.Contains(t, fragment, "<preloaded_skill id=\"review-guard\">")
	assert.Contains(t, fragment, "REVIEW_GUARD_PRELOAD")
	assert.NotContains(t, fragment, "missing-skill")
}

func TestRolePreloadPromptSkipsAvailableAndNoSkillRoles(t *testing.T) {
	executor := &agentExecutor{}
	assert.Empty(t, executor.rolePreloadPrompt(nil))
	available := &domain.FrozenRoleExecution{Skills: domain.RoleSkills{
		Entries: []domain.RoleSkillEntry{{SkillID: "x", Mode: domain.RoleSkillAvailable}}}}
	assert.Empty(t, executor.rolePreloadPrompt(available))
}

func TestRoleCatalogAcceptsSkillsWhenKnownCatalogIsWired(t *testing.T) {
	// Regression: production wires KnownSkills from skills.Discover; without it
	// every Skill reference fails with skill_catalog_unavailable.
	skillsDir := t.TempDir()
	skillDir := filepath.Join(skillsDir, "catalog-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.json"),
		[]byte(`{"id":"catalog-skill","version":"1","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("catalog skill body"), 0o600))

	known := make(map[string]bool)
	for _, skill := range skills.Discover(skillsDir, "") {
		known[skill.Manifest.ID] = true
	}
	assert.True(t, known["catalog-skill"])
}
