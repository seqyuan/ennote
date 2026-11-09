package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeroPreloadPromptInjectsFrozenSkillPrompts(t *testing.T) {
	skillsDir := t.TempDir()
	skillDir := filepath.Join(skillsDir, "review-guard")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.json"),
		[]byte(`{"id":"review-guard","version":"1","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("REVIEW_GUARD_PRELOAD: only report evidence-backed conclusions."), 0o600))

	executor := &agentExecutor{skillsDir: skillsDir}
	hero := &domain.FrozenHeroExecution{
		Skills: domain.HeroSkills{Entries: []domain.HeroSkillEntry{
			{SkillID: "review-guard", Mode: domain.HeroSkillPreload},
			{SkillID: "missing-skill", Mode: domain.HeroSkillPreload},
		}},
	}
	fragment, err := executor.heroPreloadPrompt(hero)
	require.NoError(t, err)
	assert.Contains(t, fragment, "<preloaded_skill id=\"review-guard\">")
	assert.Contains(t, fragment, "REVIEW_GUARD_PRELOAD")
	assert.NotContains(t, fragment, "missing-skill")
}

func TestHeroPreloadPromptSkipsAvailableAndNoSkillHeroes(t *testing.T) {
	executor := &agentExecutor{}
	prompt, err := executor.heroPreloadPrompt(nil)
	require.NoError(t, err)
	assert.Empty(t, prompt)
	available := &domain.FrozenHeroExecution{Skills: domain.HeroSkills{
		Entries: []domain.HeroSkillEntry{{SkillID: "x", Mode: domain.HeroSkillAvailable}}}}
	prompt, err = executor.heroPreloadPrompt(available)
	require.NoError(t, err)
	assert.Empty(t, prompt)
}

func TestTaskPreloadPromptAddsExplicitSkillsWithoutDuplicatingHeroPreloads(t *testing.T) {
	skillsDir := t.TempDir()
	for id, body := range map[string]string{
		"hero-skill": "ROLE_SKILL_BODY",
		"task-skill": "TASK_SKILL_BODY",
	} {
		dir := filepath.Join(skillsDir, id)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
			[]byte(`{"id":"`+id+`","version":"1","prompt":"SKILL.md"}`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600))
	}
	executor := &agentExecutor{skillsDir: skillsDir}
	hero := &domain.FrozenHeroExecution{Skills: domain.HeroSkills{Entries: []domain.HeroSkillEntry{
		{SkillID: "hero-skill", Mode: domain.HeroSkillPreload},
	}}}

	fragment, err := executor.taskPreloadPrompt(hero, domain.FrozenSkillRefs{
		{ID: "hero-skill"}, {ID: "task-skill"}, {ID: "task-skill"},
	})
	require.NoError(t, err)
	assert.NotContains(t, fragment, "ROLE_SKILL_BODY", "Hero preloads are rendered by heroPreloadPrompt")
	assert.Equal(t, 1, strings.Count(fragment, "TASK_SKILL_BODY"))

	_, err = executor.taskPreloadPrompt(hero, domain.FrozenSkillRefs{{ID: "missing-task-skill"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-task-skill")
}

func TestHeroCatalogAcceptsSkillsWhenKnownCatalogIsWired(t *testing.T) {
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
