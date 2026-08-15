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

func TestRoleApprovalCarriesFrozenAttributionAndPermissionCeiling(t *testing.T) {
	runs, roles, session, role, version, _ := setupPublishedRoleInvocation(t)
	ctx := context.Background()
	submission, err := runs.SubmitInvocation(ctx, roleInvocation(session.ID, "role-approval", role, version))
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)

	approvals := &store.ApprovalRepo{DB: roles}
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
