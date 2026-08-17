package sessionstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// Stats returns the aggregate session statistics the composer's StatsLine
// renders, computed from the durable model_calls and tool_calls tables. It
// returns a zero-valued struct (not nil) even before the first run so callers
// can render an empty line; a nil result only signals a missing Session.
func (m *Manager) Stats(ctx context.Context, sessionID string) (*domain.SessionStats, error) {
	db, err := m.OpenSession(ctx, sessionID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return readSessionStats(ctx, db)
}

type modelCallRow struct {
	runID               string
	iteration           int
	attempt             int
	startedAt           string
	finishedAt          string
	firstTokenAt        sql.NullString
	uncachedInputTokens int64
	outputTokens        int64
	cachedTokens        int64
	cacheWriteTokens    int64
}

// readSessionStats folds completed agent-turn model calls into per-step figures
// (the last attempt per (run, iteration) wins, so retries do not double count)
// and sums tool wall time over finished tool calls.
func readSessionStats(ctx context.Context, db *sql.DB) (*domain.SessionStats, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT run_id, iteration, attempt, started_at, finished_at, first_token_at,
		       uncached_input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		FROM model_calls
		WHERE status = 'completed' AND purpose = 'agent_turn'
		ORDER BY run_id, iteration, attempt`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := &domain.SessionStats{}
	turns := map[string]struct{}{}
	type stepKey struct {
		run       string
		iteration int
	}
	best := map[stepKey]*modelCallRow{}
	for rows.Next() {
		var row modelCallRow
		if err := rows.Scan(&row.runID, &row.iteration, &row.attempt, &row.startedAt, &row.finishedAt,
			&row.firstTokenAt, &row.uncachedInputTokens, &row.outputTokens, &row.cachedTokens, &row.cacheWriteTokens); err != nil {
			return nil, err
		}
		key := stepKey{row.runID, row.iteration}
		if previous, ok := best[key]; !ok || row.attempt > previous.attempt {
			best[key] = &row
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for key, row := range best {
		turns[key.run] = struct{}{}
		stats.Steps++
		started, startErr := time.Parse(time.RFC3339Nano, row.startedAt)
		finished, finishErr := time.Parse(time.RFC3339Nano, row.finishedAt)
		if startErr != nil || finishErr != nil {
			continue
		}
		stats.LLMMs += durationMs(started, finished)
		stats.UncachedInputTokens += row.uncachedInputTokens
		stats.CacheReadTokens += row.cachedTokens
		stats.CacheWriteTokens += row.cacheWriteTokens
		stats.OutputTokens += row.outputTokens
		if row.firstTokenAt.Valid {
			first, firstErr := time.Parse(time.RFC3339Nano, row.firstTokenAt.String)
			if firstErr == nil {
				stats.TTFTMs += durationMs(started, first)
				stats.TTFTSteps++
				stats.DecodeMs += durationMs(first, finished)
				stats.DecodeTokens += row.outputTokens
			}
		}
	}
	stats.Turns = len(turns)

	toolRows, err := db.QueryContext(ctx, `SELECT started_at, finished_at FROM tool_calls WHERE finished_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer toolRows.Close()
	for toolRows.Next() {
		var startedAt, finishedAt string
		if err := toolRows.Scan(&startedAt, &finishedAt); err != nil {
			return nil, err
		}
		started, startErr := time.Parse(time.RFC3339Nano, startedAt)
		finished, finishErr := time.Parse(time.RFC3339Nano, finishedAt)
		if startErr == nil && finishErr == nil {
			stats.ToolMs += durationMs(started, finished)
		}
	}
	if err := toolRows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

func durationMs(start, end time.Time) int64 {
	d := end.Sub(start)
	if d < 0 {
		return 0
	}
	return d.Milliseconds()
}
