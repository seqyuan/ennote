package sessionstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadTurnMetricsPerRunFooter(t *testing.T) {
	db := setupTailDB(t)
	ctx := context.Background()
	insertTailSession(t, db, "session")
	insertTailRun(t, db, "run", "session", "succeeded")
	_, err := db.Exec(`UPDATE agent_runs SET finished_at='2026-08-10T00:00:04Z' WHERE id='run'`)
	require.NoError(t, err)

	first1 := "2026-08-10T00:00:00.5Z"
	first2 := "2026-08-10T00:00:02.2Z"
	first2b := "2026-08-10T00:00:03.6Z"
	// Step 1: ttft 500ms, decode 500ms, output 30.
	insertModelCallRow(t, db, "c1", "run", 1, 1, 1,
		"2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", &first1, 100, 30, 10, 0)
	// Step 2 attempt 1 (superseded by attempt 2).
	insertModelCallRow(t, db, "c2", "run", 2, 2, 1,
		"2026-08-10T00:00:02Z", "2026-08-10T00:00:03Z", &first2, 200, 20, 5, 0)
	// Step 2 attempt 2 (wins): ttft 100ms, decode 400ms, output 40.
	insertModelCallRow(t, db, "c3", "run", 3, 2, 2,
		"2026-08-10T00:00:03.5Z", "2026-08-10T00:00:04Z", &first2b, 250, 40, 15, 0)

	metrics, err := readTurnMetrics(ctx, db)
	require.NoError(t, err)
	require.Len(t, metrics, 1)

	m := metrics[0]
	assert.Equal(t, "run", m.RunID)
	assert.EqualValues(t, 4000, m.RunMs) // created_at 00:00:00 → finished 00:00:04
	assert.EqualValues(t, 500, m.TTFTMs) // first step's TTFT
	// decodeMs = 500 + 400 = 900; decodeTokens = 30 + 40 = 70; tps = 70 / 0.9.
	assert.InDelta(t, 70.0/0.9, m.TokensPerSecond, 0.001)
}

func TestReadTurnMetricsSkipsFailedRuns(t *testing.T) {
	db := setupTailDB(t)
	insertTailSession(t, db, "session")
	insertTailRun(t, db, "failed", "session", "failed")
	insertTailRun(t, db, "ok", "session", "succeeded")
	_, err := db.Exec(`UPDATE agent_runs SET finished_at='2026-08-10T00:00:01Z' WHERE id IN ('failed','ok')`)
	require.NoError(t, err)

	metrics, err := readTurnMetrics(context.Background(), db)
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.Equal(t, "ok", metrics[0].RunID)
}
