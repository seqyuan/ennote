package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupFailedRun(t *testing.T, requestID string) (*store.RunRepo, *domain.TurnSubmission) {
	t.Helper()
	db := store.SetupDB(t)
	_, err := db.Exec(`UPDATE settings SET value='1' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db}
	sessionID := createRunTestSession(t, runs)
	submission, err := runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: requestID, Text: "recover this",
		RequestedConfig: json.RawMessage(`{"maxIterations":4}`),
	})
	require.NoError(t, err)
	_, err = runs.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	return runs, submission
}

func TestRunRecoveryRetriesFailedAttemptIdempotentlyWithoutDuplicatingUserMessage(t *testing.T) {
	runs, submission := setupFailedRun(t, "failed-turn")
	ctx := context.Background()
	require.NoError(t, runs.Fail(ctx, submission.Run.ID, "provider_unavailable", "temporary"))

	recovery, err := runs.FindRecoveryBySession(ctx, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, recovery)
	assert.True(t, recovery.Retryable)
	assert.Empty(t, recovery.BlockedReason)
	assert.Equal(t, submission.Run.ID, recovery.Run.ID)

	first, err := runs.Retry(ctx, submission.Run.ID, "retry-request")
	require.NoError(t, err)
	assert.False(t, first.Existing)
	assert.Equal(t, 2, first.Run.Attempt)
	assert.Equal(t, submission.Run.ID, first.Run.RetryOfRunID)
	assert.Equal(t, domain.CommitFormatLegacyV1, first.Run.CommitFormatVersion)
	assert.Equal(t, domain.PublishPublicFinal, first.Run.PublishMode)
	assert.NotEmpty(t, first.Run.RootRunID)
	assert.JSONEq(t, `{"maxIterations":4}`, string(first.Run.RequestedConfig))
	assert.JSONEq(t, `{}`, string(first.Run.EffectiveConfig))

	second, err := runs.Retry(ctx, submission.Run.ID, "retry-request")
	require.NoError(t, err)
	assert.True(t, second.Existing)
	assert.Equal(t, first.Run.ID, second.Run.ID)

	var messages, attempts int
	require.NoError(t, runs.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=?`, submission.Run.SessionID).Scan(&messages))
	require.NoError(t, runs.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE turn_id=?`, submission.TurnID).Scan(&attempts))
	assert.Equal(t, 1, messages)
	assert.Equal(t, 2, attempts)
}

func TestRunRecoveryAllowsOnlyExecutedReadOnlyTools(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		risk      domain.RiskClass
		retryable bool
	}{
		{name: "read only", risk: domain.RiskReadOnly, retryable: true},
		{name: "local write", risk: domain.RiskLocalWrite, retryable: false},
		{name: "shell", risk: domain.RiskShell, retryable: false},
		{name: "unknown", risk: "", retryable: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runs, submission := setupFailedRun(t, "tool-turn")
			ctx := context.Background()
			calls := &store.CallRepo{DB: runs.DB}
			call := domain.ToolCall{ID: uuid.NewString(), Name: "tool", Arguments: json.RawMessage(`{}`)}
			recordID := uuid.NewString()
			require.NoError(t, calls.ToolStarted(ctx, domain.ToolCallStart{ID: recordID, RunID: submission.Run.ID,
				Iteration: 1, CallIndex: 0, Call: call, Policy: domain.ToolPolicyMetadata{RiskClass: testCase.risk}}))
			require.NoError(t, calls.ToolCompleted(ctx, domain.ToolCallFinish{ID: recordID, RunID: submission.Run.ID,
				Iteration: 1, CallIndex: 0, Call: call, Result: domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "done"},
				Policy: domain.ToolPolicyMetadata{RiskClass: testCase.risk}}))
			require.NoError(t, runs.Fail(ctx, submission.Run.ID, "provider_unavailable", "after tool"))

			recovery, err := runs.FindRecoveryBySession(ctx, submission.Run.SessionID)
			require.NoError(t, err)
			require.NotNil(t, recovery)
			assert.Equal(t, testCase.retryable, recovery.Retryable)
			if testCase.retryable {
				_, err = runs.Retry(ctx, submission.Run.ID, "retry")
				require.NoError(t, err)
			} else {
				assert.Equal(t, domain.RetryBlockedSideEffect, recovery.BlockedReason)
				_, err = runs.Retry(ctx, submission.Run.ID, "retry")
				assert.ErrorIs(t, err, store.ErrRunRetryUnsafe)
			}
		})
	}
}

func TestRunRecoveryRejectsOlderAttemptAndInactiveLeaf(t *testing.T) {
	runs, submission := setupFailedRun(t, "stale-turn")
	ctx := context.Background()
	require.NoError(t, runs.Interrupt(ctx, submission.Run.ID, "restart"))
	retry, err := runs.Retry(ctx, submission.Run.ID, "retry-one")
	require.NoError(t, err)
	_, err = runs.Retry(ctx, submission.Run.ID, "retry-two")
	assert.ErrorIs(t, err, store.ErrRunRetryStale)

	_, err = runs.Claim(ctx, retry.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.Fail(ctx, retry.Run.ID, "provider_unavailable", "again"))
	_, err = runs.DB.Exec(`UPDATE sessions SET active_leaf_message_id=NULL WHERE id=?`, submission.Run.SessionID)
	require.NoError(t, err)
	_, err = runs.Retry(ctx, retry.Run.ID, "retry-three")
	assert.ErrorIs(t, err, store.ErrRunRetryStale)
}
