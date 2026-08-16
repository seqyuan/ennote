package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompactionPolicyResolveForMergesMatchingModelOverride(t *testing.T) {
	trigger := 0.6
	tailRatio := 0.25
	tailMax := 60000
	summaryInput := 0.5
	config := CompactionPolicyConfig{
		TriggerRatio: 0.75, TailTokenRatio: 0.20, TailMinTokens: 8000, TailMaxTokens: 32000,
		SummaryInputRatio: 0.70, SummaryMaxOutputTokens: 4096,
		ModelPolicies: []CompactionModelPolicy{
			{ModelProfileID: "big-model", TriggerRatio: &trigger, TailTokenRatio: &tailRatio,
				TailMaxTokens: &tailMax, SummaryInputRatio: &summaryInput},
		},
	}

	matched := config.ResolveFor(ModelRuntimeSnapshot{ProviderProfileID: "p1", ModelProfileID: "big-model"})
	assert.Equal(t, 0.6, matched.TriggerRatio)
	assert.Equal(t, 0.25, matched.TailTokenRatio)
	assert.Equal(t, 60000, matched.TailMaxTokens)
	assert.Equal(t, 0.5, matched.SummaryInputRatio)
	// Unoverridden knobs stay at the top-level defaults.
	assert.Equal(t, 8000, matched.TailMinTokens)
	assert.Equal(t, 4096, matched.SummaryMaxOutputTokens)
	// The resolution is one-shot: the override table is dropped.
	assert.Empty(t, matched.ModelPolicies)

	unmatched := config.ResolveFor(ModelRuntimeSnapshot{ProviderProfileID: "p1", ModelProfileID: "small-model"})
	assert.Equal(t, 0.75, unmatched.TriggerRatio)
	assert.Equal(t, 0.20, unmatched.TailTokenRatio)
	assert.Equal(t, 32000, unmatched.TailMaxTokens)
	assert.Equal(t, 0.70, unmatched.SummaryInputRatio)
}

func TestCompactionPolicyResolveForProviderScoping(t *testing.T) {
	trigger := 0.5
	config := CompactionPolicyConfig{
		TriggerRatio: 0.75,
		ModelPolicies: []CompactionModelPolicy{
			{ProviderProfileID: "provider-a", ModelProfileID: "m", TriggerRatio: &trigger},
		},
	}

	// Same model on a different provider does not match the scoped override.
	other := config.ResolveFor(ModelRuntimeSnapshot{ProviderProfileID: "provider-b", ModelProfileID: "m"})
	assert.Equal(t, 0.75, other.TriggerRatio)

	same := config.ResolveFor(ModelRuntimeSnapshot{ProviderProfileID: "provider-a", ModelProfileID: "m"})
	assert.Equal(t, 0.5, same.TriggerRatio)
}
