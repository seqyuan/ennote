package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyRoleCannotPublishMutationTools(t *testing.T) {
	roles, projectID, modelID := setupRoleRepo(t)
	ctx := context.Background()
	definition := validRoleDefinition(modelID)
	definition.Authority = domain.RoleAuthorityReadOnly
	definition.AllowedTools = append(definition.AllowedTools, "write", "bash")
	role, err := roles.Create(ctx, store.CreateRoleInput{Handle: "unsafe-reader", Name: "Unsafe Reader",
		Scope: domain.RoleScopeProject, ProjectID: &projectID, Definition: definition})
	require.NoError(t, err)
	validation, err := roles.Validate(ctx, role.ID)
	require.NoError(t, err)
	assert.False(t, validation.Valid)
	var conflicts int
	for _, diagnostic := range validation.Diagnostics {
		if diagnostic.Code == "authority_tool_conflict" {
			conflicts++
		}
	}
	assert.Equal(t, 2, conflicts)
	_, err = roles.Publish(ctx, role.ID, role.DraftRevision)
	assert.ErrorIs(t, err, store.ErrRoleValidation)
}

func TestRoleApprovalCarriesFrozenAttributionAndPermissionCeiling(t *testing.T) {
	runs, roles, session, role, version := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	_, err := roles.DB.Exec(`UPDATE settings SET value='2' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-approval", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)

	approvals := &store.ApprovalRepo{DB: roles.DB}
	approval, err := approvals.Suspend(ctx, claimed.ID, 1, 1, "batch-digest", json.RawMessage(`{"version":1}`),
		[]domain.ApprovalItem{{CallIndex: 0, ToolCallID: "tool-1", ToolName: "read", RiskClass: domain.RiskReadOnly,
			ArgumentsPreview: `{"path":"README.md"}`}}, nil)
	require.NoError(t, err)
	require.NotNil(t, approval.Attribution)
	assert.Equal(t, domain.SpeakerRole, approval.Attribution.SpeakerKind)
	assert.Equal(t, role.ID, approval.Attribution.ObjectID)
	assert.Equal(t, version.ID, approval.Attribution.VersionID)
	assert.Equal(t, "security-reviewer", approval.Attribution.Handle)
	assert.Equal(t, domain.PermissionDiscuss, approval.Attribution.PermissionCeiling)
	assert.Equal(t, domain.RoleAuthorityReadOnly, approval.Attribution.Authority)

	restored, err := approvals.FindPendingBySession(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, restored.Attribution)
	assert.Equal(t, approval.Attribution, restored.Attribution)
}
