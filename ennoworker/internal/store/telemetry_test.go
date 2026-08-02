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

	// Payload decodes as RunTelemetryPayload.
	for _, ev := range events {
		if ev.EventType == "run_telemetry" {
			var payload domain.RunTelemetryPayload
			require.NoError(t, json.Unmarshal(ev.Payload, &payload))
			assert.NotEmpty(t, string(ev.Payload))
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
