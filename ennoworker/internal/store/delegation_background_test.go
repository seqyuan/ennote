package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackgroundModeCreatesHandleAndKeepsParentRunning(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "background-mode")
	ctx := context.Background()

	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "bg-call", Strategy: domain.DelegationStrategyParallel,
		ExecutionMode: domain.DelegationExecutionBackground, AutoResume: true,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)

	// A durable handle exists with the frozen mode and auto-resume.
	handle, err := delegations.HandleForGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationExecutionBackground, handle.ExecutionMode)
	assert.True(t, handle.AutoResume)
	assert.Equal(t, "active", handle.Status)
	assert.NotEmpty(t, handle.SourceBranchID)

	// The parent stays running — no waiting_children wake protocol.
	parentAfter, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunRunning, parentAfter.Status)
}

func TestBlockingModeStillCreatesHandle(t *testing.T) {
	delegations, _, submission := setupRootBudgetParent(t, "blocking-handle")
	ctx := context.Background()

	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "blk-call", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	handle, err := delegations.HandleForGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationExecutionBlocking, handle.ExecutionMode)
	assert.False(t, handle.AutoResume)
}

func TestBackgroundMutationRoleDeniedAtomically(t *testing.T) {
	delegations, _, submission := setupRootBudgetParent(t, "background-mutation")
	ctx := context.Background()

	roleRepo := &store.RoleRepo{DB: delegations.DB, KnownTools: map[string]bool{"write": true}}
	var modelID string
	require.NoError(t, delegations.DB.QueryRow(`SELECT id FROM model_profiles WHERE status='active' LIMIT 1`).Scan(&modelID))
	identity, err := roleRepo.Create(ctx, store.CreateRoleInput{
		Handle: "mutator", Name: "Mutator", Scope: domain.RoleScopeGlobal,
		ProjectID: nil, Definition: domain.RoleDefinition{
			SchemaVersion: 1, RolePrompt: "mutate", Authority: domain.RoleAuthorityMutation,
			PermissionCeiling: domain.PermissionAsk, AllowedTools: []string{"write"},
			ModelBinding: domain.RoleModelBinding{Mode: domain.RoleModelFixed, ModelProfileID: modelID},
			ContextPolicy: domain.RoleContextPolicy{DefaultMode: domain.RoleContextTask,
				AllowedModes: []domain.RoleContextMode{domain.RoleContextTask},
				OwnExecutionContinuity: domain.RoleContinuityNone},
			DelegationPolicy: domain.RoleDelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
				AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
				MaxInvocationsPerParentRun: 4, MaxConcurrentInstances: 4,
				BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
					MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxWallTimeMS: 120000}},
			OutputContract: "text-v1", MaxLoopIterations: 8,
		},
	})
	require.NoError(t, err)
	_, err = roleRepo.Publish(ctx, identity.ID, identity.DraftRevision)
	require.NoError(t, err)

	// Resolve the published mutation Role version and attempt background mode.
	snapshot, err := delegations.ResolveRoleForDelegation(ctx, submission.Run.SessionID, "mutator")
	require.NoError(t, err)
	mutationItem := explorerItem()
	mutationItem.Name = "mutate"
	mutationItem.RoleVersionID = snapshot.VersionID
	group, items, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "bg-mut", Strategy: domain.DelegationStrategySingle,
		ExecutionMode: domain.DelegationExecutionBackground,
		Items:         []store.CreateDelegationItemInput{mutationItem},
	}, submission.Run.SessionID)
	require.Error(t, err)
	assert.Nil(t, group)
	assert.Nil(t, items)
	assert.Nil(t, children)

	// Nothing was created: no group, handle, child, attempt, or budget row.
	var rows int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_groups WHERE parent_tool_call_id='bg-mut'`).Scan(&rows))
	assert.Zero(t, rows)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_handles`).Scan(&rows))
	assert.Zero(t, rows)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE parent_run_id=?`, submission.Run.ID).Scan(&rows))
	assert.Zero(t, rows)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM run_budgets`).Scan(&rows))
	assert.Zero(t, rows)
}

func TestBackgroundChildTerminalDoesNotWakeParent(t *testing.T) {
	delegations, runs, submission := setupRootBudgetParent(t, "background-no-wake")
	ctx := context.Background()

	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "bg-call", Strategy: domain.DelegationStrategySingle,
		ExecutionMode: domain.DelegationExecutionBackground,
		Items:         []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)

	// Child succeeds while the parent is still running: the parent must not be
	// requeued by the background completion.
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "ok"},
	}))

	parentAfter, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunRunning, parentAfter.Status, "background completion must not requeue the parent")

	// Generation settles normally via its attempts.
	var generationStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT status FROM delegation_group_generations
		WHERE group_id=? AND generation=0`, group.ID).Scan(&generationStatus))
	assert.Equal(t, "settled", generationStatus)
}
