package store_test

import (
	"context"
	"encoding/json"
	"testing"

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
