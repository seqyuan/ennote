package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCompactionTriggerPredicates pins the pure threshold predicate contract
// (design 二 P1): the trigger owns token-state decisions and no side effects.
func TestCompactionTriggerPredicates(t *testing.T) {
	trigger := CompactionTrigger{TriggerLimit: 100, MainUsable: 500}

	assert.True(t, trigger.BelowTrigger(99))
	assert.False(t, trigger.BelowTrigger(100))

	assert.True(t, trigger.ProjectionSufficient(99))
	assert.False(t, trigger.ProjectionSufficient(100))

	assert.True(t, trigger.ShouldSummarize(100, 100))
	assert.False(t, trigger.ShouldSummarize(99, 100))
	assert.False(t, trigger.ShouldSummarize(100, 99))

	assert.True(t, trigger.NoMeaningfulWork(0))
	assert.True(t, trigger.NoMeaningfulWork(500))
	assert.False(t, trigger.NoMeaningfulWork(501))
}
