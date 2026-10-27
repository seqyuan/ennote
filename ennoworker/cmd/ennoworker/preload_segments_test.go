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

	segments, err := executor.rolePreloadSegments(role)
	require.NoError(t, err)
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
	segments, err := executor.rolePreloadSegments(nil)
	require.NoError(t, err)
	assert.Nil(t, segments)

	available := &domain.FrozenRoleExecution{Skills: domain.RoleSkills{
		Entries: []domain.RoleSkillEntry{{SkillID: "x", Mode: domain.RoleSkillAvailable}}}}
	segments, err = executor.rolePreloadSegments(available)
	require.NoError(t, err)
	assert.Empty(t, segments)

	// A legacy (unpinned) Skill that was frozen at publish time but has since
	// left the on-disk catalog is skipped defensively rather than aborting the
	// Run.
	frozen := &domain.FrozenRoleExecution{Handle: "r", Version: 1, Skills: domain.RoleSkills{
		Entries: []domain.RoleSkillEntry{{SkillID: "gone", Mode: domain.RoleSkillPreload}}}}
	segments, err = executor.rolePreloadSegments(frozen)
	require.NoError(t, err)
	assert.Empty(t, segments)
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

// A Role preload Skill frozen with a content digest is pinned: the on-disk
// Skill must still hash to exactly that digest, or prompt composition fails
// closed instead of silently changing a published Role's prompt.
func TestRolePreloadSegmentsVerifyFrozenContentDigest(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "role-skill", "FROZEN_ROLE_BODY")
	frozen, err := skills.Load(filepath.Join(skillsDir, "role-skill"))
	require.NoError(t, err)
	require.NotEmpty(t, frozen.ContentHash)

	executor := &agentExecutor{skillsDir: skillsDir}
	role := &domain.FrozenRoleExecution{
		Handle: "analyst", Version: 2,
		Skills: domain.RoleSkills{Entries: []domain.RoleSkillEntry{
			{SkillID: "role-skill", Mode: domain.RoleSkillPreload},
		}},
		FrozenSkills: domain.FrozenSkillRefs{{ID: "role-skill", Digest: frozen.ContentHash}},
	}
	segments, err := executor.rolePreloadSegments(role)
	require.NoError(t, err, "unchanged content still composes")
	require.Len(t, segments, 1)
	assert.Contains(t, segments[0].Text, "FROZEN_ROLE_BODY")

	// Editing the Skill after the Role was published must not silently change
	// what the frozen Role injects into the model prompt.
	writePreloadSkill(t, skillsDir, "role-skill", "EDITED_ROLE_BODY")
	_, err = executor.rolePreloadSegments(role)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "role-skill")
	assert.Contains(t, err.Error(), "analyst@2")
	assert.Contains(t, err.Error(), "frozen content digest")
	assert.Contains(t, err.Error(), frozen.ContentHash)
	assert.NotContains(t, err.Error(), "EDITED_ROLE_BODY")
}

// A pinned preload Skill that disappeared from the catalog fails closed too: a
// published Role cannot silently lose a prompt constraint.
func TestRolePreloadSegmentsFailClosedOnMissingPinnedSkill(t *testing.T) {
	executor := &agentExecutor{}
	role := &domain.FrozenRoleExecution{
		Handle: "analyst", Version: 3,
		Skills: domain.RoleSkills{Entries: []domain.RoleSkillEntry{
			{SkillID: "gone", Mode: domain.RoleSkillPreload},
		}},
		FrozenSkills: domain.FrozenSkillRefs{{ID: "gone", Digest: "sha256:deadbeef"}},
	}
	_, err := executor.rolePreloadSegments(role)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gone")
	assert.Contains(t, err.Error(), "analyst@3")
	assert.Contains(t, err.Error(), "sha256:deadbeef")
}

// Legacy Role snapshots carry no FrozenSkills. They keep the historical
// behavior: the current on-disk body is used, and a missing Skill is skipped.
func TestRolePreloadSegmentsUseCurrentContentForLegacyUnpinnedRole(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "role-skill", "CURRENT_ROLE_BODY")

	executor := &agentExecutor{skillsDir: skillsDir}
	role := &domain.FrozenRoleExecution{
		Handle: "analyst", Version: 1,
		Skills: domain.RoleSkills{Entries: []domain.RoleSkillEntry{
			{SkillID: "role-skill", Mode: domain.RoleSkillPreload},
			{SkillID: "gone", Mode: domain.RoleSkillPreload},
		}},
	}
	segments, err := executor.rolePreloadSegments(role)
	require.NoError(t, err)
	require.Len(t, segments, 1, "the missing legacy Skill is skipped defensively")
	assert.Contains(t, segments[0].Text, "CURRENT_ROLE_BODY")
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

	roleSegments, err := executor.rolePreloadSegments(role)
	require.NoError(t, err)
	roleText, roleSections := systemprompt.Compose(roleSegments)
	rolePrompt, err := executor.rolePreloadPrompt(role)
	require.NoError(t, err)
	assert.Equal(t, rolePrompt, roleText)
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
