package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validHeroDefinition is a reusable fixed-model HeroDefinition for tests that
// exercise file-hero resolution and delegation validation (V2).
func validHeroDefinition(modelID string) domain.HeroDefinition {
	return domain.HeroDefinition{
		SchemaVersion: 1,
		HeroPrompt:    "Review the supplied evidence independently.",
		ModelBinding: domain.HeroModelBinding{
			Mode: domain.HeroModelFixed, ModelProfileID: modelID, ThinkingEffort: domain.ThinkingMedium,
			FallbackModelProfileIDs: []string{}, OverridableFields: []string{},
		},
		Skills:            domain.HeroSkills{Entries: []domain.HeroSkillEntry{}},
		Authority:         domain.HeroAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss,
		AllowedTools:      []string{"read", "grep"},
		ContextPolicy: domain.HeroContextPolicy{
			DefaultMode: domain.HeroContextRoom, AllowedModes: []domain.HeroContextMode{domain.HeroContextRoom, domain.HeroContextReply, domain.HeroContextFresh},
			OwnExecutionContinuity: domain.HeroContinuityNone,
		},
		DelegationPolicy: domain.HeroDelegationPolicy{
			Admission: domain.DelegationApprovalRequired, AllowedCallerKinds: []string{"host"},
			AllowedStrategies: []string{"single"}, MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000},
		},
		OutputContract: "text-v1", MaxLoopIterations: 8,
	}
}

func TestDelegationHeroResolutionIsFileNativeAndDeterministic(t *testing.T) {
	ctx := context.Background()
	db, _, session := newSessionDB(t)
	sources, models, modelID, _ := setupFileHeroDelegation(t)

	delegations := &store.DelegationRepo{DB: db, HeroSources: sources, Models: models}
	// V2: file Heroes are global; no project override exists. Resolution is
	// deterministic: the latest published file revision wins, every time.
	document := &heroesource.Document{
		SchemaVersion: 1, Handle: "shared-reviewer", Name: "Global Reviewer",
		Description: "Reviews output.", Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextRoom,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextRoom}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single", "parallel"},
			MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Review the supplied evidence.",
	}
	require.NoError(t, createFileHero(t, sources, document))

	resolved, err := delegations.ResolveHeroForDelegation(ctx, session.ID, "shared-reviewer")
	require.NoError(t, err)
	assert.Equal(t, "shared-reviewer@v000001", resolved.VersionID)
	assert.True(t, resolved.DelegationEnabled)
	assert.Equal(t, domain.HeroScopeGlobal, resolved.Scope)
	assert.Equal(t, document.Prompt, resolved.Definition.HeroPrompt)
	// Deterministic: a second resolution returns the same immutable revision.
	again, err := delegations.ResolveHeroForDelegation(ctx, session.ID, "shared-reviewer")
	require.NoError(t, err)
	assert.Equal(t, resolved.VersionID, again.VersionID)

	// Unknown handles are never candidates.
	_, err = delegations.ResolveHeroForDelegation(ctx, session.ID, "isolated-reviewer")
	assert.ErrorIs(t, err, store.ErrDelegationHeroUnavailable)
	// Without a wired file source no hero resolves at all (legacy SQL removed).
	_, err = (&store.DelegationRepo{DB: db}).ResolveHeroForDelegation(ctx, session.ID, "shared-reviewer")
	assert.ErrorIs(t, err, store.ErrDelegationHeroUnavailable)
}

func TestDelegationCreationEnforcesAdmissionKillSwitchAndBudget(t *testing.T) {
	ctx := context.Background()
	db, _, session := newSessionDB(t)
	sources, models, modelID, _ := setupFileHeroDelegation(t)

	// approval-reviewer requires explicit approval.
	document := &heroesource.Document{
		SchemaVersion: 1, Handle: "approval-reviewer", Name: "Approval",
		Description: "Reviewer", Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextRoom,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextRoom}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationApprovalRequired,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Review the authorization boundary.",
	}
	require.NoError(t, createFileHero(t, sources, document))
	resolved, err := (&store.DelegationRepo{DB: db, HeroSources: sources, Models: models}).
		ResolveHeroForDelegation(ctx, session.ID, "approval-reviewer")
	require.NoError(t, err)
	definitionJSON, err := json.Marshal(resolved.Definition)
	require.NoError(t, err)
	meta, err := store.NewDelegationHeroMeta(resolved.VersionID, definitionJSON)
	require.NoError(t, err)
	meta.Handle = "approval-reviewer"
	meta.DisplayName = "Approval"

	runs := &store.RunRepo{DB: db}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID,
		ClientRequestID: "hero-policy", Text: "delegate"})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	delegations := &store.DelegationRepo{DB: db,
		Policies: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}
	item := store.CreateDelegationItemInput{Name: "review", HeroVersionID: resolved.VersionID,
		AssignmentJSON: []byte(`{"task":"review"}`), OutputContract: "text-v1",
		Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
			MaxOutputTokens: 4000, MaxCostMicros: 100000, MaxWallTimeMS: 120000},
		HeroMeta: meta}

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
	// A denied Hero (admission=denied) is rejected even when approved.
	deniedDoc := *document
	deniedDoc.Handle = "denied-reviewer"
	deniedDoc.Delegation.Admission = domain.DelegationDenied
	require.NoError(t, createFileHero(t, sources, &deniedDoc))
	deniedResolved, err := (&store.DelegationRepo{DB: db, HeroSources: sources, Models: models}).
		ResolveHeroForDelegation(ctx, session.ID, "denied-reviewer")
	require.NoError(t, err)
	deniedJSON, err := json.Marshal(deniedResolved.Definition)
	require.NoError(t, err)
	deniedMeta, err := store.NewDelegationHeroMeta(deniedResolved.VersionID, deniedJSON)
	require.NoError(t, err)
	deniedMeta.Handle = "denied-reviewer"
	deniedMeta.DisplayName = "Denied"
	deniedItem := item
	deniedItem.HeroVersionID = deniedResolved.VersionID
	deniedItem.HeroMeta = deniedMeta
	_, err = delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{ParentRunID: submission.Run.ID,
		ParentToolCallID: "disabled", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{deniedItem}, AdmissionApproved: true})
	assert.ErrorIs(t, err, store.ErrDelegationNotAuthorized)
}
