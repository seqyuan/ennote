package sessionstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadContextUsageReturnsNilBeforeFirstReport(t *testing.T) {
	db := setupTailDB(t)
	ctx := context.Background()
	insertTailSession(t, db, "session")
	insertTailRun(t, db, "run", "session", "running")

	usage, err := readContextUsage(ctx, db, "session")
	require.NoError(t, err)
	assert.Nil(t, usage)
}

func TestReadContextUsageReturnsLatestReport(t *testing.T) {
	db := setupTailDB(t)
	ctx := context.Background()
	insertTailSession(t, db, "session")
	insertTailRun(t, db, "run", "session", "running")

	_, err := db.Exec(`INSERT INTO run_events(run_id,seq,event_type,payload_json,created_at) VALUES(?,?,?,?,?)`,
		"run", 1, "context_usage",
		`{"contextWindow":128000,"projectedTokens":1200,"systemTokens":400,"toolsTokens":300,"messageTokens":500}`,
		"2026-08-10T00:00:00Z")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO run_events(run_id,seq,event_type,payload_json,created_at) VALUES(?,?,?,?,?)`,
		"run", 2, "context_usage",
		`{"contextWindow":64000,"projectedTokens":900,"systemTokens":400,"toolsTokens":300,"messageTokens":200}`,
		"2026-08-10T00:00:01Z")
	require.NoError(t, err)

	usage, err := readContextUsage(ctx, db, "session")
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 64000, usage.ContextWindow)
	assert.Equal(t, 900, usage.ProjectedTokens)
	assert.Equal(t, 200, usage.MessageTokens)
}
