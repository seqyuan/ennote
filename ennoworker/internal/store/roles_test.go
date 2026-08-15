package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validRoleDefinition is a reusable fixed-model RoleDefinition for tests that
// exercise file-role resolution and delegation validation (V2).
func validRoleDefinition(modelID string) domain.RoleDefinition {
	return domain.RoleDefinition{
		SchemaVersion: 1,
		RolePrompt:    "Review the supplied evidence independently.",
		ModelBinding: domain.RoleModelBinding{
			Mode: domain.RoleModelFixed, ModelProfileID: modelID, ThinkingEffort: domain.ThinkingMedium,
			FallbackModelProfileIDs: []string{}, OverridableFields: []string{},
		},
		Skills:            domain.RoleSkills{Entries: []domain.RoleSkillEntry{}},
		Authority:         domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss,
		AllowedTools:      []string{"read", "grep"},
		ContextPolicy: domain.RoleContextPolicy{
			DefaultMode: domain.RoleContextRoom, AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom, domain.RoleContextReply, domain.RoleContextFresh},
			OwnExecutionContinuity: domain.RoleContinuityNone,
		},
		DelegationPolicy: domain.RoleDelegationPolicy{
			Admission: domain.DelegationApprovalRequired, AllowedCallerKinds: []string{"host"},
			AllowedStrategies: []string{"single"}, MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000},
		},
		OutputContract: "text-v1", MaxLoopIterations: 8,
	}
}

func TestDelegationRoleResolutionIsFileNativeAndDeterministic(t *testing.T) {
	ctx := context.Background()
	db, _, session := newSessionDB(t)
	sources, models, modelID, _ := setupFileRoleDelegation(t)

	delegations := &store.DelegationRepo{DB: db, RoleSources: sources, Models: models}
	// V2: file Roles are global; no project override exists. Resolution is
	// deterministic: the latest published file revision wins, every time.
	document := &rolesource.Document{
		SchemaVersion: 1, Handle: "shared-reviewer", Name: "Global Reviewer",
		Description: "Reviews output.", Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextRoom,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single", "parallel"},
			MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Review the supplied evidence.",
	}
	require.NoError(t, createFileRole(t, sources, document))

	resolved, err := delegations.ResolveRoleForDelegation(ctx, session.ID, "shared-reviewer")
	require.NoError(t, err)
	assert.Equal(t, "shared-reviewer@v000001", resolved.VersionID)
	assert.True(t, resolved.DelegationEnabled)
	assert.Equal(t, domain.RoleScopeGlobal, resolved.Scope)
	assert.Equal(t, document.Prompt, resolved.Definition.RolePrompt)
	// Deterministic: a second resolution returns the same immutable revision.
	again, err := delegations.ResolveRoleForDelegation(ctx, session.ID, "shared-reviewer")
	require.NoError(t, err)
	assert.Equal(t, resolved.VersionID, again.VersionID)

	// Unknown handles are never candidates.
	_, err = delegations.ResolveRoleForDelegation(ctx, session.ID, "isolated-reviewer")
	assert.ErrorIs(t, err, store.ErrDelegationRoleUnavailable)
	// Without a wired file source no role resolves at all (legacy SQL removed).
	_, err = (&store.DelegationRepo{DB: db}).ResolveRoleForDelegation(ctx, session.ID, "shared-reviewer")
	assert.ErrorIs(t, err, store.ErrDelegationRoleUnavailable)
}

func TestDelegationCreationEnforcesAdmissionKillSwitchAndBudget(t *testing.T) {
	ctx := context.Background()
	db, _, session := newSessionDB(t)
	sources, models, modelID, _ := setupFileRoleDelegation(t)

	// approval-reviewer requires explicit approval.
	document := &rolesource.Document{
		SchemaVersion: 1, Handle: "approval-reviewer", Name: "Approval",
		Description: "Reviewer", Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextRoom,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationApprovalRequired,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Review the authorization boundary.",
	}
	require.NoError(t, createFileRole(t, sources, document))
	resolved, err := (&store.DelegationRepo{DB: db, RoleSources: sources, Models: models}).
		ResolveRoleForDelegation(ctx, session.ID, "approval-reviewer")
	require.NoError(t, err)
	definitionJSON, err := json.Marshal(resolved.Definition)
	require.NoError(t, err)
	meta, err := store.NewDelegationRoleMeta(resolved.VersionID, definitionJSON)
	require.NoError(t, err)
	meta.Handle = "approval-reviewer"
	meta.DisplayName = "Approval"

	runs := &store.RunRepo{DB: db}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID,
		ClientRequestID: "role-policy", Text: "delegate"})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	delegations := &store.DelegationRepo{DB: db,
		Policies: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}
	item := store.CreateDelegationItemInput{Name: "review", RoleVersionID: resolved.VersionID,
		AssignmentJSON: []byte(`{"task":"review"}`), OutputContract: "text-v1",
		Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
			MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		RoleMeta: meta}

	// Not approved → rejected.
	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{ParentRunID: submission.Run.ID,
		ParentToolCallID: "not-approved", Strategy: domain.DelegationStrategySingle, Items: []store.CreateDelegationItemInput{item}})
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
	// Budget beyond the frozen ceiling → rejected.
	tooLarge := item
	tooLarge.Budget.MaxModelCalls = 5
	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{ParentRunID: submission.Run.ID,
		ParentToolCallID: "over-budget", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{tooLarge}, AdmissionApproved: true})
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
	// A denied Role (admission=denied) is rejected even when approved.
	deniedDoc := *document
	deniedDoc.Handle = "denied-reviewer"
	deniedDoc.Delegation.Admission = domain.DelegationDenied
	require.NoError(t, createFileRole(t, sources, &deniedDoc))
	deniedResolved, err := (&store.DelegationRepo{DB: db, RoleSources: sources, Models: models}).
		ResolveRoleForDelegation(ctx, session.ID, "denied-reviewer")
	require.NoError(t, err)
	deniedJSON, err := json.Marshal(deniedResolved.Definition)
	require.NoError(t, err)
	deniedMeta, err := store.NewDelegationRoleMeta(deniedResolved.VersionID, deniedJSON)
	require.NoError(t, err)
	deniedMeta.Handle = "denied-reviewer"
	deniedMeta.DisplayName = "Denied"
	deniedItem := item
	deniedItem.RoleVersionID = deniedResolved.VersionID
	deniedItem.RoleMeta = deniedMeta
	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{ParentRunID: submission.Run.ID,
		ParentToolCallID: "disabled", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{deniedItem}, AdmissionApproved: true})
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
}
