package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/systemprompt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePreloadSkill seeds one loadable Skill bundle so Discover finds it.
func writePreloadSkill(t *testing.T, skillsDir, id, body string) {
	t.Helper()
	dir := filepath.Join(skillsDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"id":"`+id+`","version":"1","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600))
}

func TestRolePreloadSegmentsNameTheFrozenRoleVersion(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "review-guard", "GUARD_BODY")

	executor := &agentExecutor{skillsDir: skillsDir}
	role := &domain.FrozenRoleExecution{
		Handle: "security-reviewer", Version: 4,
		Skills: domain.RoleSkills{Entries: []domain.RoleSkillEntry{
			{SkillID: "review-guard", Mode: domain.RoleSkillPreload},
		}},
	}

	segments := executor.rolePreloadSegments(role)
	require.Len(t, segments, 1)
	assert.Equal(t, "skill.preload.review-guard", segments[0].ID)
	assert.Equal(t, domain.PromptSectionSkillPreload, segments[0].Kind)
	assert.Equal(t, "review-guard", segments[0].SkillID)
	// The inlined body is attributed to the exact frozen Role version that
	// carried the preload binding, so a reader can trace it back.
	assert.Equal(t, "role:security-reviewer@4", segments[0].Source)
	assert.Contains(t, segments[0].Text, "GUARD_BODY")
}

func TestRolePreloadSegmentsSkipAvailableAndMissingSkills(t *testing.T) {
	executor := &agentExecutor{}
	assert.Nil(t, executor.rolePreloadSegments(nil))

	available := &domain.FrozenRoleExecution{Skills: domain.RoleSkills{
		Entries: []domain.RoleSkillEntry{{SkillID: "x", Mode: domain.RoleSkillAvailable}}}}
	assert.Empty(t, executor.rolePreloadSegments(available))

	// A Skill that was frozen at publish time but has since left the on-disk
	// catalog is skipped defensively rather than aborting the Run.
	frozen := &domain.FrozenRoleExecution{Handle: "r", Version: 1, Skills: domain.RoleSkills{
		Entries: []domain.RoleSkillEntry{{SkillID: "gone", Mode: domain.RoleSkillPreload}}}}
	assert.Empty(t, executor.rolePreloadSegments(frozen))
}

func TestTaskPreloadSegmentsAttributeToTheTask(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "task-skill", "TASK_BODY")

	executor := &agentExecutor{skillsDir: skillsDir}
	segments, err := executor.taskPreloadSegments(nil, domain.FrozenSkillRefs{{ID: "task-skill"}}, "task:child-run-1")
	require.NoError(t, err)
	require.Len(t, segments, 1)
	assert.Equal(t, "task:child-run-1", segments[0].Source)
	assert.Equal(t, "task-skill", segments[0].SkillID)
	assert.Contains(t, segments[0].Text, "TASK_BODY")

	// An explicit task Skill must still exist in the current catalog: a removed
	// or invalid frozen binding fails loudly instead of silently weakening the
	// task.
	_, err = executor.taskPreloadSegments(nil, domain.FrozenSkillRefs{{ID: "missing"}}, "task:x")
	require.ErrorContains(t, err, "missing")
}

// A task Skill frozen with a content digest is pinned: the on-disk Skill must
// still hash to exactly that digest, or the Run fails closed.
func TestTaskPreloadSegmentsVerifyFrozenContentDigest(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "task-skill", "FROZEN_BODY")
	frozen, err := skills.Load(filepath.Join(skillsDir, "task-skill"))
	require.NoError(t, err)
	require.NotEmpty(t, frozen.ContentHash)

	executor := &agentExecutor{skillsDir: skillsDir}
	pinned := domain.FrozenSkillRefs{{ID: "task-skill", Digest: frozen.ContentHash}}
	segments, err := executor.taskPreloadSegments(nil, pinned, "task:run-1")
	require.NoError(t, err, "unchanged content still runs")
	require.Len(t, segments, 1)
	assert.Contains(t, segments[0].Text, "FROZEN_BODY")

	// Editing the Skill after the freeze must not silently change behaviour.
	writePreloadSkill(t, skillsDir, "task-skill", "EDITED_BODY")
	_, err = executor.taskPreloadSegments(nil, pinned, "task:run-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task-skill")
	assert.Contains(t, err.Error(), "frozen content digest")
	assert.Contains(t, err.Error(), "task:run-1")
	assert.Contains(t, err.Error(), frozen.ContentHash)
}

// Legacy (pre-pinning) references carry no digest and keep their old behaviour:
// the current on-disk Skill body is used. Documented compatibility behaviour.
func TestTaskPreloadSegmentsUseCurrentContentForLegacyUnpinnedRefs(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "task-skill", "CURRENT_BODY")

	executor := &agentExecutor{skillsDir: skillsDir}
	segments, err := executor.taskPreloadSegments(nil, domain.FrozenSkillRefs{{ID: "task-skill"}}, "task:legacy")
	require.NoError(t, err)
	require.Len(t, segments, 1)
	assert.Contains(t, segments[0].Text, "CURRENT_BODY")
}

// The string API and the segment API must agree byte for byte: freezing the
// composition must never change the prompt the model receives.
func TestPreloadSegmentsComposeToTheStringAPI(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "role-skill", "ROLE_BODY")
	writePreloadSkill(t, skillsDir, "task-skill", "TASK_BODY")

	executor := &agentExecutor{skillsDir: skillsDir}
	role := &domain.FrozenRoleExecution{Handle: "analyst", Version: 2, Skills: domain.RoleSkills{
		Entries: []domain.RoleSkillEntry{{
			SkillID: "role-skill", Mode: domain.RoleSkillPreload,
		}},
	}}

	roleText, roleSections := systemprompt.Compose(executor.rolePreloadSegments(role))
	assert.Equal(t, executor.rolePreloadPrompt(role), roleText)
	require.Len(t, roleSections, 1)
	assert.Contains(t, roleText, "ROLE_BODY")

	taskSegments, err := executor.taskPreloadSegments(role, domain.FrozenSkillRefs{
		{ID: "role-skill"}, {ID: "task-skill"}, {ID: "task-skill"},
	}, "task:r")
	require.NoError(t, err)
	taskText, taskSections := systemprompt.Compose(taskSegments)
	assert.Equal(t, mustTaskPreloadPrompt(t, executor, role, domain.FrozenSkillRefs{
		{ID: "role-skill"}, {ID: "task-skill"}, {ID: "task-skill"},
	}), taskText)
	require.Len(t, taskSections, 1, "Role preloads are de-duplicated out of the task overlay")
	assert.Equal(t, "task-skill", taskSections[0].SkillID)
}

func mustTaskPreloadPrompt(t *testing.T, executor *agentExecutor, role *domain.FrozenRoleExecution, refs domain.FrozenSkillRefs) string {
	t.Helper()
	text, err := executor.taskPreloadPrompt(role, refs)
	require.NoError(t, err)
	return text
}
