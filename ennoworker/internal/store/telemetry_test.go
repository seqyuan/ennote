package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunTelemetryPrecedesTerminalEvent(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "telemetry-order")
	ctx := context.Background()

	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, repo.FinalizeSuccess(ctx, submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}},
	}}))

	events, err := (&store.EventRepo{DB: repo.DB}).After(ctx, submission.Run.ID, 0, 100)
	require.NoError(t, err)
	types := eventTypes(events)

	// run_telemetry must appear, and run_succeeded must be last.
	telemetryIdx := indexOf(types, "run_telemetry")
	succeededIdx := indexOf(types, "run_succeeded")
	require.GreaterOrEqual(t, telemetryIdx, 0, "run_telemetry must be emitted")
	require.GreaterOrEqual(t, succeededIdx, 0, "run_succeeded must be emitted")
	assert.Less(t, telemetryIdx, succeededIdx, "run_telemetry must precede run_succeeded")
	assert.Equal(t, len(types)-1, succeededIdx, "run_succeeded must be the terminal event")

	// Payload decodes as RunTelemetryPayload and does not degrade: a run with no
	// tool rows is the branch the julianday scan bug never exercised.
	for _, ev := range events {
		if ev.EventType == "run_telemetry" {
			var payload domain.RunTelemetryPayload
			require.NoError(t, json.Unmarshal(ev.Payload, &payload))
			assert.NotEmpty(t, string(ev.Payload))
			assert.False(t, payload.Partial, "a run without tool rows must not report a partial payload")
		}
	}
}

func TestRunTelemetryOnFailure(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "telemetry-fail")
	ctx := context.Background()

	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, repo.Fail(ctx, submission.Run.ID, "provider_unavailable", "boom"))

	events, err := (&store.EventRepo{DB: repo.DB}).After(ctx, submission.Run.ID, 0, 100)
	require.NoError(t, err)
	types := eventTypes(events)

	telemetryIdx := indexOf(types, "run_telemetry")
	failedIdx := indexOf(types, "run_failed")
	require.GreaterOrEqual(t, telemetryIdx, 0)
	assert.Less(t, telemetryIdx, failedIdx)
}

func TestRunTelemetryDurationMS(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "telemetry-duration")
	ctx := context.Background()

	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	// Pin the start: FinalizeSuccess stamps finished_at from its own clock and
	// hands the same value to the aggregation, so started_at is the only end a
	// test can choose. Relative to now, so the assertion cannot depend on the
	// host clock being later than a literal date.
	startedAt := time.Now().UTC().Add(-90 * time.Minute).Format(time.RFC3339Nano)
	_, err = repo.DB.Exec(`UPDATE agent_runs SET started_at = ? WHERE id = ?`, startedAt, submission.Run.ID)
	require.NoError(t, err)

	require.NoError(t, repo.FinalizeSuccess(ctx, submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}},
	}}))

	telemetry := readRunTelemetry(t, repo.DB, submission.Run.ID)
	require.False(t, telemetry.Partial, "a readable started_at must not degrade the payload")

	var startedText, finishedText string
	require.NoError(t, repo.DB.QueryRow(`SELECT started_at, finished_at FROM agent_runs WHERE id = ?`,
		submission.Run.ID).Scan(&startedText, &finishedText))
	started, err := time.Parse(time.RFC3339Nano, startedText)
	require.NoError(t, err)
	finished, err := time.Parse(time.RFC3339Nano, finishedText)
	require.NoError(t, err)

	// The field is the wall-clock span rounded to the nearest millisecond, so the
	// assertion is exact against the stored ends — "greater than zero" alone would
	// not catch a wrong pair of columns.
	assert.Greater(t, telemetry.DurationMS, int64(0), "a Run that started must report a duration")
	assert.Equal(t, finished.Sub(started).Round(time.Millisecond).Milliseconds(), telemetry.DurationMS)
}

func TestRunTelemetryDurationMSStaysZeroWithoutUsableStart(t *testing.T) {
	for name, startedAt := range map[string]string{
		"unparseable": "not-a-timestamp",
		"future":      "9999-01-01T00:00:00.000Z",
	} {
		t.Run(name, func(t *testing.T) {
			repo, submission := setupSubmittedRun(t, "telemetry-duration-"+name)
			ctx := context.Background()

			_, err := repo.Claim(ctx, submission.Run.ID)
			require.NoError(t, err)
			_, err = repo.DB.Exec(`UPDATE agent_runs SET started_at = ? WHERE id = ?`, startedAt, submission.Run.ID)
			require.NoError(t, err)

			require.NoError(t, repo.Succeed(ctx, submission.Run.ID))

			telemetry := readRunTelemetry(t, repo.DB, submission.Run.ID)
			assert.False(t, telemetry.Partial, "an unusable start is not an aggregation failure")
			assert.Equal(t, int64(0), telemetry.DurationMS)
		})
	}
}

func TestRunTelemetryToolTimingHasNoAttemptsKey(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "telemetry-no-attempts")
	ctx := context.Background()

	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	calls := &store.CallRepo{DB: repo.DB}
	call := domain.ToolCall{ID: "tool-read", Name: "read", Arguments: json.RawMessage(`{}`)}
	require.NoError(t, calls.ToolStarted(ctx, domain.ToolCallStart{ID: "record-tool", RunID: submission.Run.ID,
		Iteration: 1, CallIndex: 0, Call: call}))
	result := domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "ok"}
	require.NoError(t, calls.ToolCompleted(ctx, domain.ToolCallFinish{ID: "record-tool", RunID: submission.Run.ID,
		Iteration: 1, CallIndex: 0, Call: call, RawResult: result, Result: result}))

	require.NoError(t, repo.Succeed(ctx, submission.Run.ID))

	events, err := (&store.EventRepo{DB: repo.DB}).After(ctx, submission.Run.ID, 0, 100)
	require.NoError(t, err)
	var raw []byte
	for _, ev := range events {
		if ev.EventType == "run_telemetry" {
			raw = ev.Payload
			break
		}
	}
	require.NotNil(t, raw, "run_telemetry must be emitted")

	// ToolTiming.Attempts was never written, so it shipped a constant 0 to every
	// consumer. The wire shape is now exactly count/errorCount/totalMs.
	assert.NotContains(t, string(raw), "attempts")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	timings, ok := payload["toolTimings"].(map[string]any)
	require.True(t, ok, "toolTimings must be an object")
	entry, ok := timings["read"].(map[string]any)
	require.True(t, ok, "toolTimings must include the tool name")
	assert.Len(t, entry, 3)
	for _, key := range []string{"count", "errorCount", "totalMs"} {
		assert.Contains(t, entry, key)
	}
}

// readRunTelemetry returns the decoded run_telemetry payload of a finished Run.
func readRunTelemetry(t *testing.T, db *sql.DB, runID string) domain.RunTelemetryPayload {
	t.Helper()
	events, err := (&store.EventRepo{DB: db}).After(context.Background(), runID, 0, 100)
	require.NoError(t, err)
	for _, ev := range events {
		if ev.EventType != "run_telemetry" {
			continue
		}
		var telemetry domain.RunTelemetryPayload
		require.NoError(t, json.Unmarshal(ev.Payload, &telemetry))
		return telemetry
	}
	require.FailNow(t, "run_telemetry must be emitted", runID)
	return domain.RunTelemetryPayload{}
}

func TestRunTelemetryAggregatesToolTimings(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "telemetry-tool-timings")
	ctx := context.Background()

	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	calls := &store.CallRepo{DB: repo.DB}
	call := domain.ToolCall{ID: "tool-read", Name: "read", Arguments: json.RawMessage(`{}`)}
	for index, recordID := range []string{"record-tool-ok", "record-tool-failed"} {
		require.NoError(t, calls.ToolStarted(ctx, domain.ToolCallStart{ID: recordID, RunID: submission.Run.ID,
			Iteration: 1, CallIndex: index, Call: call}))
	}

	completed := domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "ok"}
	require.NoError(t, calls.ToolCompleted(ctx, domain.ToolCallFinish{ID: "record-tool-ok", RunID: submission.Run.ID,
		Iteration: 1, CallIndex: 0, Call: call, RawResult: completed, Result: completed}))
	failed := domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "boom", IsError: true}
	require.NoError(t, calls.ToolFailed(ctx, domain.ToolCallFinish{ID: "record-tool-failed", RunID: submission.Run.ID,
		Iteration: 1, CallIndex: 1, Call: call, RawResult: failed, Result: failed}))

	// Wall-clock timestamps land wherever the test runs; pin them 501ms apart so
	// julianday produces the fractional millisecond count the aggregation reads.
	_, err = repo.DB.Exec(`UPDATE tool_calls SET started_at = '2026-01-01T00:00:00.000Z',
		finished_at = '2026-01-01T00:00:00.501Z' WHERE run_id = ?`, submission.Run.ID)
	require.NoError(t, err)

	require.NoError(t, repo.FinalizeSuccess(ctx, submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}},
	}}))

	events, err := (&store.EventRepo{DB: repo.DB}).After(ctx, submission.Run.ID, 0, 100)
	require.NoError(t, err)

	var telemetry domain.RunTelemetryPayload
	found := false
	for _, ev := range events {
		if ev.EventType == "run_telemetry" {
			require.NoError(t, json.Unmarshal(ev.Payload, &telemetry))
			found = true
			break
		}
	}
	require.True(t, found, "run_telemetry must be emitted")

	assert.False(t, telemetry.Partial, "tool timings must aggregate instead of degrading the payload")
	timing, ok := telemetry.ToolTimings["read"]
	require.True(t, ok, "tool timings must include the tool name")
	assert.Equal(t, 2, timing.Count)
	assert.Equal(t, 1, timing.ErrorCount)
	// Two rows of a 501 ms span: 500.98 ms each in SQLite's julian-day arithmetic,
	// rounded per aggregate to 1002. InDelta keeps the assertion on the magnitude
	// instead of "greater than zero", which a dropped *86400000 would also pass.
	assert.InDelta(t, 1002, timing.TotalMS, 3)
}
