package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrentRetriesOneGenerationWins verifies that two concurrent retries
// of the same group with different client request ids produce exactly one next
// generation; the loser fails closed with a generation conflict and creates
// nothing.
func TestConcurrentRetriesOneGenerationWins(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	// Force SQLite to use the shared connection so both goroutines serialize
	// through the single-writer model exactly like production.
	const attempts = 2
	results := make([]struct {
		generation *domain.DelegationGeneration
		children   []*domain.AgentRun
		err        error
	}, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			generation, children, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
				ExpectedGeneration: 0, ItemIDs: []string{failedItemID},
				ClientRequestID: "concurrent-retry-" + string(rune('a'+index)),
			})
			results[index].generation = generation
			results[index].children = children
			results[index].err = err
		}(i)
	}
	wg.Wait()

	winners := 0
	losers := 0
	for _, result := range results {
		if result.err == nil {
			require.NotNil(t, result.generation)
			assert.Equal(t, 1, result.generation.Generation)
			require.Len(t, result.children, 1)
			winners++
		} else {
			assert.Equal(t, domain.ErrorDelegationGenerationConflict, domain.ErrorCodeOf(result.err))
			losers++
		}
	}
	assert.Equal(t, 1, winners, "exactly one concurrent retry wins")
	assert.Equal(t, 1, losers)

	// Exactly one generation 1 and one retry attempt exist.
	var generationRows, attemptRows int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_group_generations WHERE group_id=? AND generation=1`,
		group.ID).Scan(&generationRows))
	assert.Equal(t, 1, generationRows)
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attemptRows))
	assert.Equal(t, 1, attemptRows)
}

// TestConcurrentRetrySameRequestIsIdempotent verifies that concurrent retries
// sharing one client request id both observe the same generation.
func TestConcurrentRetrySameRequestIsIdempotent(t *testing.T) {
	delegations, _, _, group, failedItemID := settleMixedGroup(t)
	ctx := context.Background()

	const attempts = 3
	ids := make([]string, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			generation, _, _, err := delegations.RetryGeneration(ctx, group.ID, domain.RetryDelegationInput{
				ExpectedGeneration: 0, ItemIDs: []string{failedItemID}, ClientRequestID: "same-request-id",
			})
			if err == nil && generation != nil {
				ids[index] = generation.ID
			}
		}(i)
	}
	wg.Wait()

	nonEmpty := 0
	for _, id := range ids {
		if id != "" {
			nonEmpty++
			assert.Equal(t, ids[0], id)
		}
	}
	assert.Equal(t, attempts, nonEmpty, "all concurrent callers must observe the same generation")

	var attemptRows int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM delegation_item_attempts WHERE generation=1`).Scan(&attemptRows))
	assert.Equal(t, 1, attemptRows)
}
