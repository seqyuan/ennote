package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueueInputsAreIdempotentOrderedAndKindSeparated(t *testing.T) {
	db := store.SetupDB(t)
	runs := &store.RunRepo{DB: db}
	queue := &store.QueueRepo{DB: db}
	sessionID := createRunTestSession(t, runs)
	submission, err := runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "turn", Text: "start",
	})
	require.NoError(t, err)

	first, err := queue.Enqueue(context.Background(), submission.Run.ID, "q1", domain.QueuedInputSteer, "first steer")
	require.NoError(t, err)
	duplicate, err := queue.Enqueue(context.Background(), submission.Run.ID, "q1", domain.QueuedInputSteer, "ignored duplicate")
	require.NoError(t, err)
	assert.Equal(t, first.ID, duplicate.ID)
	_, err = queue.Enqueue(context.Background(), submission.Run.ID, "q2", domain.QueuedInputSteer, "second steer")
	require.NoError(t, err)
	_, err = queue.Enqueue(context.Background(), submission.Run.ID, "q3", domain.QueuedInputFollowUp, "follow later")
	require.NoError(t, err)

	steering, err := queue.Drain(context.Background(), submission.Run.ID, domain.QueuedInputSteer, domain.QueueOneAtATime)
	require.NoError(t, err)
	require.Len(t, steering, 1)
	assert.Equal(t, "first steer", steering[0].Text)
	assert.Equal(t, "injected", steering[0].Status)

	steering, err = queue.Drain(context.Background(), submission.Run.ID, domain.QueuedInputSteer, domain.QueueAll)
	require.NoError(t, err)
	require.Len(t, steering, 1)
	assert.Equal(t, "second steer", steering[0].Text)

	followUps, err := queue.Drain(context.Background(), submission.Run.ID, domain.QueuedInputFollowUp, domain.QueueOneAtATime)
	require.NoError(t, err)
	require.Len(t, followUps, 1)
	assert.Equal(t, "follow later", followUps[0].Text)

	var queuedEvents, injectedEvents int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'input_queued'`, submission.Run.ID).Scan(&queuedEvents))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'input_injected'`, submission.Run.ID).Scan(&injectedEvents))
	assert.Equal(t, 3, queuedEvents)
	assert.Equal(t, 3, injectedEvents)
}

func TestTerminalRunCancelsPendingInputs(t *testing.T) {
	db := store.SetupDB(t)
	runs := &store.RunRepo{DB: db}
	queue := &store.QueueRepo{DB: db}
	sessionID := createRunTestSession(t, runs)
	submission, err := runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "turn", Text: "start",
	})
	require.NoError(t, err)
	_, err = queue.Enqueue(context.Background(), submission.Run.ID, "q1", domain.QueuedInputFollowUp, "pending")
	require.NoError(t, err)
	require.NoError(t, runs.Cancel(context.Background(), submission.Run.ID))

	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM run_input_queue WHERE run_id = ?`, submission.Run.ID).Scan(&status))
	assert.Equal(t, "cancelled", status)
	_, err = queue.Enqueue(context.Background(), submission.Run.ID, "q2", domain.QueuedInputSteer, "too late")
	assert.True(t, errors.Is(err, store.ErrRunNotActive), err)
}

func TestQueueRejectsInvalidModeAndKind(t *testing.T) {
	db := store.SetupDB(t)
	queue := &store.QueueRepo{DB: db}
	_, err := queue.Enqueue(context.Background(), "missing", "q", "invalid", "text")
	assert.ErrorContains(t, err, "unsupported")
	_, err = queue.Drain(context.Background(), "missing", domain.QueuedInputSteer, "invalid")
	assert.ErrorContains(t, err, "unsupported")
}
