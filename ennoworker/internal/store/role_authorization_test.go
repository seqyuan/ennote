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

// TestChildRunPermissionConvergesToParentPolicyMode pins the one-way
// convergence of delegated permissions: a Role can tighten but never loosen the
// permission mode its parent Run froze. A parent in Ask with a Role ceiling of
// Auto must freeze Ask for the child.
func TestChildRunPermissionConvergesToParentPolicyMode(t *testing.T) {
	fixture := newFileRunFixture(t, "child-permission-mode")
	ctx := context.Background()
	runs := fixture.Runs
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: fixture.SessionID, ClientRequestID: "child-permission-mode", Text: "run",
		RequestedConfig: json.RawMessage(`{"toolPolicyProfileId":"builtin-tool-ask-v1"}`),
	})
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	parentResolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.Equal(t, "builtin-tool-ask-v1", parentResolved.Effective.ToolPolicy.ID)

	delegations := fixture.Delegations()
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-child-permission", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{autoCeilingExplorerItem(t)},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: submission.Run.ID, ItemID: items[0].ID, SessionID: submission.Run.SessionID,
	})
	require.NoError(t, err)
	claimedChild, err := runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	childResolved, err := runs.ResolveAndFreezeConfig(ctx, claimedChild)
	require.NoError(t, err)

	require.NotNil(t, childResolved.Effective.Role)
	require.Equal(t, domain.PermissionAuto, childResolved.Effective.Role.PermissionCeiling)
	var childPolicy domain.ToolPolicyConfig
	require.NoError(t, json.Unmarshal(childResolved.Effective.ToolPolicy.Config, &childPolicy))
	assert.Equal(t, "builtin-tool-ask-v1", childResolved.Effective.ToolPolicy.ID)
	assert.Equal(t, string(domain.PermissionAsk), childPolicy.Mode)
}

// autoCeilingExplorerItem freezes the explorer Role with an auto permission
// ceiling, so the child's own ceiling is looser than its parent's policy mode.
func autoCeilingExplorerItem(t *testing.T) store.CreateDelegationItemInput {
	t.Helper()
	item := explorerItem()
	item.RoleMeta.Definition.PermissionCeiling = domain.PermissionAuto
	return item
}
