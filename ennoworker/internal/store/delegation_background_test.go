package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
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

	// V2: the mutation Role lives as a file revision and is resolved through
	// the file-native DelegationRepo before the item freezes RoleMeta.
	sources, models, modelID, home := setupFileRoleDelegation(t)
	document := &rolesource.Document{
		SchemaVersion: 1, Handle: "mutator", Name: "Mutator", Description: "mutates",
		Positioning: "Mutates files.", Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: domain.RoleAuthorityMutation,
		PermissionCeiling: domain.PermissionAsk, AllowedTools: []string{"write"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 4, MaxConcurrentInstances: 4,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Mutate the file.",
	}
	require.NoError(t, createFileRole(t, sources, document))

	resolver := &store.DelegationRepo{DB: delegations.DB, RoleSources: sources, Models: models,
		Policies: delegations.Policies}
	// Resolve the published mutation Role version and attempt background mode.
	snapshot, err := resolver.ResolveRoleForDelegation(ctx, submission.Run.SessionID, "mutator")
	require.NoError(t, err)
	mutationItem := explorerItem()
	mutationItem.Name = "mutate"
	mutationItem.RoleVersionID = snapshot.VersionID
	definitionJSON, err := json.Marshal(snapshot.Definition)
	require.NoError(t, err)
	meta, err := store.NewDelegationRoleMeta(snapshot.VersionID, definitionJSON)
	require.NoError(t, err)
	meta.Handle = "mutator"
	meta.DisplayName = "Mutator"
	mutationItem.RoleMeta = meta
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
	_ = home
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
