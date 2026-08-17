package sessionstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// TurnMetrics returns one per-run footer reading for every successfully
// completed agent turn, ordered by run creation. It powers the message
// chrome's "Ran for / TTFT / tok/s" hover readings.
func (m *Manager) TurnMetrics(ctx context.Context, sessionID string) ([]domain.TurnMetric, error) {
	db, err := m.OpenSession(ctx, sessionID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return readTurnMetrics(ctx, db)
}

type turnCallRow struct {
	iteration    int
	attempt      int
	startedAt    string
	finishedAt   string
	firstTokenAt sql.NullString
	outputTokens int64
}

func readTurnMetrics(ctx context.Context, db *sql.DB) ([]domain.TurnMetric, error) {
	runRows, err := db.QueryContext(ctx, `
		SELECT id, created_at, finished_at
		FROM agent_runs
		WHERE run_kind = 'agent' AND status = 'succeeded' AND finished_at IS NOT NULL
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer runRows.Close()

	type runRow struct {
		id         string
		createdAt  string
		finishedAt string
	}
	var runs []runRow
	for runRows.Next() {
		var row runRow
		if err := runRows.Scan(&row.id, &row.createdAt, &row.finishedAt); err != nil {
			return nil, err
		}
		runs = append(runs, row)
	}
	if err := runRows.Err(); err != nil {
		return nil, err
	}

	metrics := make([]domain.TurnMetric, 0, len(runs))
	for _, run := range runs {
		runMs := durationMsBetween(run.createdAt, run.finishedAt)
		ttftMs, decodeMs, decodeTokens, err := foldRunCalls(ctx, db, run.id)
		if err != nil {
			return nil, err
		}
		metric := domain.TurnMetric{RunID: run.id, RunMs: runMs}
		if ttftMs > 0 {
			metric.TTFTMs = ttftMs
		}
		if decodeMs > 0 {
			metric.TokensPerSecond = float64(decodeTokens) / (float64(decodeMs) / 1000)
		}
		metrics = append(metrics, metric)
	}
	return metrics, nil
}

// foldRunCalls folds one run's completed agent-turn model calls into the
// turn-footer readings: the first step's TTFT and the summed decode wall time
// and output tokens. The last attempt per iteration wins, so retries do not
// double count.
func foldRunCalls(ctx context.Context, db *sql.DB, runID string) (ttftMs, decodeMs, decodeTokens int64, err error) {
	rows, err := db.QueryContext(ctx, `
		SELECT iteration, attempt, started_at, finished_at, first_token_at, output_tokens
		FROM model_calls
		WHERE run_id = ? AND status = 'completed' AND purpose = 'agent_turn'
		ORDER BY iteration, attempt`, runID)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()

	best := map[int]*turnCallRow{}
	for rows.Next() {
		var row turnCallRow
		if err := rows.Scan(&row.iteration, &row.attempt, &row.startedAt, &row.finishedAt,
			&row.firstTokenAt, &row.outputTokens); err != nil {
			return 0, 0, 0, err
		}
		if previous, ok := best[row.iteration]; !ok || row.attempt > previous.attempt {
			best[row.iteration] = &row
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}

	iterations := make([]int, 0, len(best))
	for iteration := range best {
		iterations = append(iterations, iteration)
	}
	sort.Ints(iterations)

	firstTTFTSet := false
	for _, iteration := range iterations {
		row := best[iteration]
		if !row.firstTokenAt.Valid {
			continue
		}
		started, startErr := time.Parse(time.RFC3339Nano, row.startedAt)
		first, firstErr := time.Parse(time.RFC3339Nano, row.firstTokenAt.String)
		finished, finishErr := time.Parse(time.RFC3339Nano, row.finishedAt)
		if startErr != nil || firstErr != nil || finishErr != nil {
			continue
		}
		if !firstTTFTSet {
			ttftMs = durationMs(started, first)
			firstTTFTSet = true
		}
		decodeMs += durationMs(first, finished)
		decodeTokens += row.outputTokens
	}
	return ttftMs, decodeMs, decodeTokens, nil
}

func durationMsBetween(start, end string) int64 {
	started, startErr := time.Parse(time.RFC3339Nano, start)
	finished, finishErr := time.Parse(time.RFC3339Nano, end)
	if startErr != nil || finishErr != nil {
		return 0
	}
	return durationMs(started, finished)
}
