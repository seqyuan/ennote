package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleRunFreezesPublishedExecutionDefinitionAndPrompt(t *testing.T) {
	runs, roles, session, role, version := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	_, err := roles.DB.Exec(`UPDATE settings SET value='2' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-config", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	// Live identity and draft changes after submission cannot alter the frozen
	// speaker or the published definition selected by the Turn.
	changed := validRoleDefinition(version.Definition.ModelBinding.ModelProfileID)
	changed.RolePrompt = "This draft must not be executed."
	encodedDraft, err := json.Marshal(changed)
	require.NoError(t, err)
	_, err = roles.DB.Exec(`UPDATE agent_profiles SET name='Renamed Live Role',handle='renamed-role',draft_json=?,draft_revision=draft_revision+1
		WHERE id=?`, string(encodedDraft), role.ID)
	require.NoError(t, err)

	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Equal(t, role.ID, resolved.Effective.Role.ObjectID)
	assert.Equal(t, version.ID, resolved.Effective.Role.VersionID)
	assert.Equal(t, version.ConfigDigest, resolved.Effective.Role.ConfigDigest)
	assert.Equal(t, "security-reviewer", resolved.Effective.Role.Handle)
	assert.Equal(t, "Security Reviewer", resolved.Effective.Role.DisplayName)
	assert.Equal(t, version.Definition.AllowedTools, resolved.Effective.Role.AllowedTools)
	assert.Equal(t, version.Definition.ModelBinding.ModelProfileID, resolved.Effective.ModelProfileID)
	assert.Equal(t, version.Definition.ModelBinding.ThinkingEffort, resolved.Effective.ThinkingEffort)
	assert.Equal(t, version.Definition.MaxLoopIterations, resolved.Effective.MaxIterations)
	assert.True(t, resolved.Effective.Routing.Pinned)
	assert.False(t, resolved.Effective.Routing.AllowAutoRoute)
	assert.Equal(t, "builtin-tool-discuss-v2", resolved.Effective.ToolPolicy.ID)
	assert.Equal(t, version.Definition.RolePrompt, resolved.SystemPrompt.AgentPrompt)
	assert.Equal(t, role.ID, resolved.SystemPrompt.AgentProfileID)
	assert.NotContains(t, string(claimed.EffectiveConfig), version.Definition.RolePrompt)

	stored, err := runs.Get(ctx, claimed.ID)
	require.NoError(t, err)
	assert.Equal(t, resolved.SystemPrompt.Digest, stored.SystemPrompt.Digest)
	assert.NotContains(t, string(stored.EffectiveConfig), version.Definition.RolePrompt)
}

func TestRoleRunRejectsRuntimeModelAndPermissionOverrides(t *testing.T) {
	runs, roles, session, role, version := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	_, err := roles.DB.Exec(`UPDATE settings SET value='2' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	input := roleInvocation(session.ID, "role-override", role, version)
	input.RequestedConfig = json.RawMessage(`{"modelProfileId":"other","toolPolicyProfileId":"builtin-tool-auto-v1"}`)
	// The override is rejected before a Run is created; the ResolveAndFreezeConfig
	// guard remains as defense-in-depth for crafted rows.
	_, err = runs.SubmitInvocation(ctx, input)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorInvocationTargetInvalid, domain.ErrorCodeOf(err))
	var runCount int
	require.NoError(t, roles.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE session_id=?`, session.ID).Scan(&runCount))
	assert.Zero(t, runCount)
}
