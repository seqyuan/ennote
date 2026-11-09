package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHeroAdmissionDenyMapsToDomainDenied pins the contract mismatch where the
// portable Hero document (and the Hero editor) spell a denied delegation
// admission "deny", while the frozen domain vocabulary spells it "denied". An
// unresolved "deny" was silently treated as not-denied by the admission gate.
func TestHeroAdmissionDenyMapsToDomainDenied(t *testing.T) {
	stack := newFileConfigStack(t)
	document := &heroesource.Document{
		SchemaVersion: 1, Handle: "deny-hero", Name: "Deny",
		Description: "Denies delegation.", Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: stack.DefaultRef, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextFresh,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextFresh}, OwnExecutionContinuity: domain.HeroContinuityNone},
		// The literal wire value from the document/editor, not the mapped constant,
		// so this test fails if the mapping is ever removed.
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAdmission("deny"),
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 1, MaxToolCalls: 1,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "You never delegate.",
	}
	definition, diagnostics := (&store.HeroDiscovery{Models: stack.ModelRepo}).ResolveDocument(context.Background(), document)
	require.Empty(t, diagnostics)
	require.NotNil(t, definition)
	assert.Equal(t, domain.DelegationDenied, definition.DelegationPolicy.Admission)
}
