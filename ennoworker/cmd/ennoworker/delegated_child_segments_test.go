package main

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/systemprompt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childRole() *domain.FrozenRoleExecution {
	return &domain.FrozenRoleExecution{
		ObjectID: "role-1", VersionID: "version-1", Version: 3,
		Handle: "skill-reader", DisplayName: "Skill Reader",
		ConfigDigest: "sha256:deadbeef", Authority: domain.RoleAuthorityReadOnly,
		Skills: domain.RoleSkills{Entries: []domain.RoleSkillEntry{
			{SkillID: "role-skill", Mode: domain.RoleSkillPreload},
		}},
	}
}

func TestDelegatedChildSegmentsInlineRolePreloadSkillBodies(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "role-skill", "ROLE_PRELOAD_BODY")

	executor := &agentExecutor{skillsDir: skillsDir}
	segments, err := executor.delegatedChildSegments(childRole(), "Review the workspace.", nil, "task:child-1")
	require.NoError(t, err)

	text, sections := systemprompt.Compose(segments)
	assert.Contains(t, text, "ROLE_PRELOAD_BODY",
		"a delegated child executes the frozen Role, so it must receive that Role's preload Skills")

	kinds := make([]domain.PromptSectionKind, 0, len(sections))
	for _, section := range sections {
		kinds = append(kinds, section.Kind)
	}
	assert.Equal(t, []domain.PromptSectionKind{
		domain.PromptSectionBase, domain.PromptSectionRole, domain.PromptSectionBase, domain.PromptSectionSkillPreload,
	}, kinds, "envelope, definition, envelope close, then the inlined Skill body")
	assert.Equal(t, "role:skill-reader@3", sections[3].Source)
	assert.Equal(t, "role-skill", sections[3].SkillID)
}

func TestDelegatedChildSegmentsInlineFrozenTaskSkills(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "role-skill", "ROLE_PRELOAD_BODY")
	writePreloadSkill(t, skillsDir, "task-skill", "TASK_PRELOAD_BODY")

	executor := &agentExecutor{skillsDir: skillsDir}
	segments, err := executor.delegatedChildSegments(childRole(), "Review.", []string{"role-skill", "task-skill"}, "task:child-9")
	require.NoError(t, err)

	text, sections := systemprompt.Compose(segments)
	assert.Contains(t, text, "ROLE_PRELOAD_BODY")
	assert.Contains(t, text, "TASK_PRELOAD_BODY")
	assert.Equal(t, 1, strings.Count(text, "ROLE_PRELOAD_BODY"),
		"a task Skill that the Role already preloads must not be inlined twice")

	require.Len(t, sections, 5)
	assert.Equal(t, "task:child-9", sections[4].Source, "the task Skill names the child Run it was frozen for")
	assert.Equal(t, "task-skill", sections[4].SkillID)
}

// A delegated child runs in task_only context. Project instruction files carry
// workspace-wide operating rules that the Role's own context policy never asked
// for, so they must never be folded into a child's prompt.
func TestDelegatedChildSegmentsContainNoProjectContext(t *testing.T) {
	executor := &agentExecutor{}
	segments, err := executor.delegatedChildSegments(childRole(), "Review.", nil, "task:child-2")
	require.NoError(t, err)

	for _, segment := range segments {
		assert.NotEqual(t, domain.PromptSectionContext, segment.Kind)
		assert.NotEqual(t, domain.PromptSectionSkillsCatalog, segment.Kind,
			"a child has no /skills mount, so it must not be told about a catalog it cannot read")
	}
}

func TestDelegatedChildSegmentsFailLoudlyOnUnavailableTaskSkill(t *testing.T) {
	executor := &agentExecutor{}
	_, err := executor.delegatedChildSegments(childRole(), "Review.", []string{"gone"}, "task:child-3")
	require.ErrorContains(t, err, "gone")
}

func TestDelegatedChildSegmentsEqualTheComposedPrompt(t *testing.T) {
	skillsDir := t.TempDir()
	writePreloadSkill(t, skillsDir, "role-skill", "ROLE_PRELOAD_BODY")

	executor := &agentExecutor{skillsDir: skillsDir}
	role := childRole()
	segments, err := executor.delegatedChildSegments(role, "Review.", nil, "task:child-4")
	require.NoError(t, err)

	text, _ := systemprompt.Compose(segments)
	assert.Equal(t, agent.RoleSystemPrompt(*role, "Review.")+executor.rolePreloadPrompt(role), text,
		"the child prompt is the Role prompt followed by its preload bodies")
}
