package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createRunTestSession(t *testing.T, dbRepo *store.RunRepo) string {
	t.Helper()
	// V2: each caller creates its own Session row + Main branch directly on
	// the opened per-Session database.
	return sqlCreateSession(t, dbRepo.DB, "00000000-0000-4000-8000-00000000000f").ID
}

func TestSubmitTurnIsIdempotentAndTransactional(t *testing.T) {
	db, _, _ := newSessionDB(t)
	repo := &store.RunRepo{DB: db}
	sessionID := createRunTestSession(t, repo)
	input := domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "request-1", Text: "Analyze this",
		RequestedConfig: json.RawMessage(`{"model":"test"}`),
	}

	first, err := repo.SubmitTurn(context.Background(), input)
	require.NoError(t, err)
	assert.False(t, first.Existing)
	assert.Equal(t, domain.RunQueued, first.Run.Status)

	second, err := repo.SubmitTurn(context.Background(), input)
	require.NoError(t, err)
	assert.True(t, second.Existing)
	assert.Equal(t, first.TurnID, second.TurnID)
	assert.Equal(t, first.UserMessageID, second.UserMessageID)
	assert.Equal(t, first.Run.ID, second.Run.ID)

	var messages, turns, runs, events int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&messages))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM turns WHERE session_id = ?`, sessionID).Scan(&turns))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE session_id = ?`, sessionID).Scan(&runs))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id = ?`, first.Run.ID).Scan(&events))
	assert.Equal(t, 1, messages)
	assert.Equal(t, 1, turns)
	assert.Equal(t, 1, runs)
	assert.Equal(t, 1, events)
	var sessionLeaf, branchLeaf string
	require.NoError(t, db.QueryRow(`SELECT active_leaf_message_id FROM sessions WHERE id=?`, sessionID).Scan(&sessionLeaf))
	require.NoError(t, db.QueryRow(`SELECT b.leaf_message_id FROM sessions s
		JOIN session_branches b ON b.id=s.active_branch_id WHERE s.id=?`, sessionID).Scan(&branchLeaf))
	assert.Equal(t, first.UserMessageID, sessionLeaf)
	assert.Equal(t, sessionLeaf, branchLeaf)
}

func TestSubmitTurnRejectsSecondActiveRunWithoutWritingMessage(t *testing.T) {
	db, _, _ := newSessionDB(t)
	repo := &store.RunRepo{DB: db}
	sessionID := createRunTestSession(t, repo)
	_, err := repo.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "request-1", Text: "first",
	})
	require.NoError(t, err)
	_, err = repo.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "request-2", Text: "second",
	})
	assert.ErrorIs(t, err, store.ErrSessionRunActive)

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestRunTransitionsAppendEventsAndTerminalIsImmutable(t *testing.T) {
	db, _, _ := newSessionDB(t)
	repo := &store.RunRepo{DB: db}
	events := &store.EventRepo{DB: db}
	sessionID := createRunTestSession(t, repo)
	submission, err := repo.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "request-1", Text: "work",
	})
	require.NoError(t, err)

	run, err := repo.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunRunning, run.Status)
	require.NoError(t, repo.Succeed(context.Background(), run.ID))

	stored, err := repo.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, stored.Status)
	assert.NotNil(t, stored.FinishedAt)

	replayed, err := events.After(context.Background(), run.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, replayed, 4)
	assert.Equal(t, []string{"run_queued", "run_started", "run_telemetry", "run_succeeded"}, []string{
		replayed[0].EventType, replayed[1].EventType, replayed[2].EventType, replayed[3].EventType,
	})
	assert.ErrorIs(t, repo.Fail(context.Background(), run.ID, "late", "late"), store.ErrInvalidRunState)
}

func TestRecoverActiveRequeuesQueuedAndInterruptsOnlyRunning(t *testing.T) {
	db, _, _ := newSessionDB(t)
	repo := &store.RunRepo{DB: db}
	queuedSession := createRunTestSession(t, repo)
	queuedSubmission, err := repo.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: queuedSession, ClientRequestID: "queued", Text: "work",
	})
	require.NoError(t, err)
	runningSession := createRunTestSession(t, repo)
	runningSubmission, err := repo.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: runningSession, ClientRequestID: "running", Text: "work",
	})
	require.NoError(t, err)
	_, err = repo.Claim(context.Background(), runningSubmission.Run.ID)
	require.NoError(t, err)

	recovered, err := repo.RecoverActive(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{queuedSubmission.Run.ID}, recovered)
	queuedRun, err := repo.Get(context.Background(), queuedSubmission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, queuedRun.Status)
	runningRun, err := repo.Get(context.Background(), runningSubmission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunInterrupted, runningRun.Status)
	recovery, err := repo.FindRecoveryBySession(context.Background(), runningSession)
	require.NoError(t, err)
	require.NotNil(t, recovery)
	assert.True(t, recovery.Retryable)
	assert.Equal(t, runningSubmission.Run.ID, recovery.Run.ID)
}

func TestRunStateMachine(t *testing.T) {
	assert.True(t, domain.CanTransitionRun(domain.RunQueued, domain.RunRunning))
	assert.True(t, domain.CanTransitionRun(domain.RunRunning, domain.RunWaitingForApproval))
	assert.True(t, domain.CanTransitionRun(domain.RunWaitingForApproval, domain.RunQueued))
	assert.True(t, domain.CanTransitionRun(domain.RunWaitingForApproval, domain.RunCancelled))
	assert.False(t, domain.CanTransitionRun(domain.RunWaitingForApproval, domain.RunSucceeded))
	assert.True(t, domain.CanTransitionRun(domain.RunRunning, domain.RunCancelled))
	assert.False(t, domain.CanTransitionRun(domain.RunSucceeded, domain.RunRunning))
	assert.True(t, domain.RunFailed.Terminal())
	assert.False(t, domain.RunRunning.Terminal())
	assert.True(t, errors.Is(store.ErrSessionRunActive, store.ErrSessionRunActive))
}
