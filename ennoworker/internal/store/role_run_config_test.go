package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleRunFreezesPublishedExecutionDefinitionAndPrompt(t *testing.T) {
	runs, _, session, role, version, sources := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-config", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	// A later publish (identity + prompt change) cannot alter the frozen
	// speaker or the published definition selected by the Turn: the run stays
	// pinned to the originally selected revision.
	_, _, err = sources.UpdateRole(role.ID, version.ConfigDigest, func(document *rolesource.Document) error {
		document.Name = "Renamed Live Role"
		document.Prompt = "This draft must not be executed."
		return nil
	})
	require.NoError(t, err)
	_, err = sources.PublishRoleRevision(role.ID)
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
	assert.Equal(t, "builtin-tool-discuss-v3", resolved.Effective.ToolPolicy.ID)
	assert.Equal(t, version.Definition.RolePrompt, resolved.SystemPrompt.AgentPrompt)
	assert.Equal(t, role.ID, resolved.SystemPrompt.AgentProfileID)
	assert.NotContains(t, string(claimed.EffectiveConfig), version.Definition.RolePrompt)

	stored, err := runs.Get(ctx, claimed.ID)
	require.NoError(t, err)
	assert.Equal(t, resolved.SystemPrompt.Digest, stored.SystemPrompt.Digest)
	assert.NotContains(t, string(stored.EffectiveConfig), version.Definition.RolePrompt)
}

func TestRoleRunRejectsRuntimeModelAndPermissionOverrides(t *testing.T) {
	runs, roles, session, role, version, _ := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	input := roleInvocation(session.ID, "role-override", role, version)
	input.RequestedConfig = json.RawMessage(`{"modelProfileId":"other","toolPolicyProfileId":"builtin-tool-auto-v1"}`)
	// The override is rejected before a Run is created; the ResolveAndFreezeConfig
	// guard remains as defense-in-depth for crafted rows.
	_, err := runs.SubmitInvocation(ctx, input)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorInvocationTargetInvalid, domain.ErrorCodeOf(err))
	var runCount int
	require.NoError(t, roles.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE session_id=?`, session.ID).Scan(&runCount))
	assert.Zero(t, runCount)
}
