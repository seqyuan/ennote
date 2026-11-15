package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acceptanceKindsHeroDocument is a saveable Hero document with an acceptance
// restriction on top of the minimal valid shape.
func acceptanceKindsHeroDocument(modelRef string, kinds []string) *heroesource.Document {
	return &heroesource.Document{
		SchemaVersion: 1, Handle: "kinds-hero", Name: "Kinds",
		Description: "States which acceptance leaves it produces.", Positioning: "Independent",
		Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: modelRef, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		AcceptanceKinds: kinds,
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 1, MaxToolCalls: 1,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Produce JSON.",
	}
}

// The Hero's acceptance restriction is frozen with its definition, and it
// survives the JSON round-trip the Graph freeze writes onto the node. A Hero
// without the field freezes without it, exactly as before.
func TestHeroDiscoveryFreezesAcceptanceKinds(t *testing.T) {
	stack := newFileConfigStack(t)
	discovery := &store.HeroDiscovery{Models: stack.ModelRepo}

	definition, diagnostics := discovery.ResolveDocument(context.Background(),
		acceptanceKindsHeroDocument(stack.DefaultRef, []string{"csv-columns", "json-valid"}))
	require.Empty(t, diagnostics)
	require.NotNil(t, definition)
	assert.Equal(t, []string{"csv-columns", "json-valid"}, definition.AcceptanceKinds)

	encoded, err := json.Marshal(definition)
	require.NoError(t, err)
	var decoded domain.HeroDefinition
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, definition.AcceptanceKinds, decoded.AcceptanceKinds)

	unrestricted, diagnostics := discovery.ResolveDocument(context.Background(),
		acceptanceKindsHeroDocument(stack.DefaultRef, nil))
	require.Empty(t, diagnostics)
	assert.Empty(t, unrestricted.AcceptanceKinds)
	encoded, err = json.Marshal(unrestricted)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "acceptanceKinds",
		"an unrestricted Hero must freeze exactly the JSON it froze before the field existed")
}
