package sessionstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertModelCallRow(t *testing.T, db *sql.DB, id, runID string, seq, iteration, attempt int,
	startedAt, finishedAt string, firstTokenAt *string, input, output, cacheRead, cacheWrite int64) {
	t.Helper()
	var first any
	if firstTokenAt != nil {
		first = *firstTokenAt
	}
	_, err := db.Exec(`INSERT INTO model_calls
		(id, run_id, seq, purpose, status, iteration, attempt, started_at, finished_at,
		 first_token_at, uncached_input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
		VALUES (?,?,?,'agent_turn','completed',?,?,?,?,?,?,?,?,?)`,
		id, runID, seq, iteration, attempt, startedAt, finishedAt, first, input, output, cacheRead, cacheWrite)
	require.NoError(t, err)
}

func TestReadSessionStatsFoldsStepsTimingAndTokens(t *testing.T) {
	db := setupTailDB(t)
	ctx := context.Background()
	insertTailSession(t, db, "session")
	insertTailRun(t, db, "run", "session", "running")

	first1 := "2026-08-10T00:00:00.5Z"
	first2 := "2026-08-10T00:00:02.2Z"
	first2b := "2026-08-10T00:00:03.6Z"
	// input = uncached; cacheRead = cache hit; cacheWrite = cache write.
	insertModelCallRow(t, db, "c1", "run", 1, 1, 1,
		"2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", &first1, 100, 30, 10, 2)
	insertModelCallRow(t, db, "c2", "run", 2, 2, 1,
		"2026-08-10T00:00:02Z", "2026-08-10T00:00:03Z", &first2, 200, 20, 5, 1)
	insertModelCallRow(t, db, "c3", "run", 3, 2, 2,
		"2026-08-10T00:00:03.5Z", "2026-08-10T00:00:04Z", &first2b, 250, 40, 15, 3)

	_, err := db.Exec(`INSERT INTO tool_calls(id, run_id, seq, tool_call_id, tool_name, status, started_at, finished_at)
		VALUES ('t1','run',1,'tcid','read','completed','2026-08-10T00:00:10Z','2026-08-10T00:00:11Z')`)
	require.NoError(t, err)

	stats, err := readSessionStats(ctx, db)
	require.NoError(t, err)
	require.NotNil(t, stats)

	assert.Equal(t, 1, stats.Turns)
	assert.Equal(t, 2, stats.Steps)
	assert.EqualValues(t, 1500, stats.LLMMs) // 1000 (step1) + 500 (step2 attempt2)
	assert.EqualValues(t, 1000, stats.ToolMs)
	assert.EqualValues(t, 600, stats.TTFTMs) // 500 + 100
	assert.Equal(t, 2, stats.TTFTSteps)
	assert.EqualValues(t, 900, stats.DecodeMs)            // 500 + 400
	assert.EqualValues(t, 70, stats.DecodeTokens)         // 30 + 40 (last attempt wins)
	assert.EqualValues(t, 350, stats.UncachedInputTokens) // 100 + 250
	assert.EqualValues(t, 70, stats.OutputTokens)
	assert.EqualValues(t, 25, stats.CacheReadTokens) // 10 + 15
	assert.EqualValues(t, 5, stats.CacheWriteTokens) // 2 + 3
}

func TestReadSessionStatsEmptySession(t *testing.T) {
	db := setupTailDB(t)
	insertTailSession(t, db, "session")
	insertTailRun(t, db, "run", "session", "running")

	stats, err := readSessionStats(context.Background(), db)
	require.NoError(t, err)
	require.NotNil(t, stats)
	assert.Equal(t, 0, stats.Steps)
	assert.EqualValues(t, 0, stats.LLMMs)
}
