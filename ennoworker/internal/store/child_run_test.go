package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateChildRunFrozenRoleAndConfigResolution(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "child-freeze")
	ctx := context.Background()
	provider, err := (&store.ProviderRepo{DB: repo.DB}).Create(ctx, store.CreateProviderInput{
		Name: "provider", ProviderType: domain.ProviderOpenAICompatible, BaseURL: "https://provider.test",
		CredentialRef: "env:PROVIDER_KEY",
	})
	require.NoError(t, err)
	_, err = (&store.ModelRepo{DB: repo.DB}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "child-model", ContextWindow: 32000, MaxOutputTokens: 2048,
		SupportsThinking: true, ThinkingDialect: domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: repo.DB}
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	parentResolved, err := runs.ResolveAndFreezeConfig(ctx, &domain.AgentRun{ID: submission.Run.ID, TurnID: submission.TurnID,
		SessionID: submission.Run.SessionID, RunKind: domain.RunKindAgent, CommitFormatVersion: domain.CommitFormatLegacyV1})
	require.NoError(t, err)
	assert.NotEmpty(t, parentResolved.Effective.ModelProfileID)

	delegations := &store.DelegationRepo{DB: repo.DB}
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "call-child", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)

	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: submission.Run.ID, ItemID: items[0].ID, SessionID: submission.Run.SessionID,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.RunKindDelegatedAgent, child.RunKind)
	assert.Equal(t, domain.CommitFormatSpeakerV2, child.CommitFormatVersion)
	assert.Equal(t, domain.PublishPrivateToParent, child.PublishMode)
	assert.Equal(t, submission.Run.ID, child.ParentRunID)
	assert.Equal(t, 1, child.ExecutionDepth)
	assert.Contains(t, string(child.SpeakerSnapshot), `"handle":"workspace-explorer"`)

	// Parent moved to waiting_children; group waiting_children.
	parent, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunWaitingChildren, parent.Status)
	storedGroup, err := delegations.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationGroupWaitingChildren, storedGroup.Status)

	// The child freezes its Role config from the delegation item (no Turn).
	claimed, err := runs.Claim(ctx, child.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Equal(t, "workspace-explorer", resolved.Effective.Role.Handle)
	assert.Equal(t, "builtin-workspace-explorer", resolved.Effective.Role.ObjectID)
	assert.Equal(t, "builtin-workspace-explorer-v2", resolved.Effective.Role.VersionID)
	assert.Equal(t, "sha256:24c22a66689d403447648700e5b26dea5c3361d251002a717f5999db3a8aeddf",
		resolved.Effective.Role.ConfigDigest)
	assert.Equal(t, []string{"read", "ls", "grep", "find", "git_readonly"}, resolved.Effective.Role.AllowedTools)
	// inherit binding resolved the parent's frozen model.
	assert.Equal(t, parentResolved.Effective.ModelProfileID, resolved.Effective.ModelProfileID)

	var canonicalCount int
	require.NoError(t, runs.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE run_id=?`, child.ID).Scan(&canonicalCount))
	assert.Zero(t, canonicalCount)
}
