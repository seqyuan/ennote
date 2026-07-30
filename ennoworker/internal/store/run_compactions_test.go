package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCompactionLifecycleAndDigestReuse(t *testing.T) {
	db := SetupDB(t)
	seedCompactionSession(t, db)
	ctx := context.Background()
	submission, err := (&RunRepo{DB: db}).SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: "session", ClientRequestID: "mid-run", Text: "continue"})
	require.NoError(t, err)

	repo := &RunCompactionRepo{DB: db}
	input := RunCompactionCreate{RunID: submission.Run.ID, Reason: domain.CompactionReasonThreshold,
		Iteration: 3, RequestGeneration: 1,
		Policy:                domain.PolicySnapshot{ID: "policy", Version: 2},
		EffectiveConfig:       json.RawMessage(`{"frozen":true}`),
		SourceDigest:          "source-digest",
		SummaryContractDigest: "contract-digest",
		CoveredGenerated:      4,
		TokensBefore:          12000}
	planned, reused, err := repo.CreateOrReuse(ctx, input)
	require.NoError(t, err)
	assert.False(t, reused)
	assert.Equal(t, domain.CompactionPlanned, planned.Status)
	require.NoError(t, repo.Start(ctx, submission.Run.ID, planned.ID))

	callID := uuid.NewString()
	calls := &CallRepo{DB: db}
	require.NoError(t, calls.ModelStarted(ctx, domain.ModelCallStart{ID: callID, RunID: submission.Run.ID,
		Iteration: 3, Attempt: 1, RequestGeneration: 1, Purpose: domain.ModelCallContextCompaction,
		RequestedConfig: json.RawMessage(`{}`), EffectiveConfig: json.RawMessage(`{}`)}))
	require.NoError(t, calls.ModelCompleted(ctx, domain.ModelCallFinish{ID: callID, RunID: submission.Run.ID,
		Iteration: 3, Attempt: 1, RequestGeneration: 1, Purpose: domain.ModelCallContextCompaction,
		ActualModel: "model", StopReason: domain.StopReasonStop,
		Usage: domain.Usage{InputTokens: 100, OutputTokens: 30}}))
	summary := "## Goal\nContinue."
	require.NoError(t, repo.Complete(ctx, RunCompactionCompletion{ID: planned.ID,
		RunID: submission.Run.ID, ModelCallID: callID, Summary: summary, SummaryDigest: "summary-digest",
		EstimatedTokensAfter: 5000, ReclaimedTokens: 7000}))

	completed, err := repo.Get(ctx, planned.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.CompactionCompleted, completed.Status)
	assert.Equal(t, summary, completed.Summary)
	assert.Equal(t, 4, completed.CoveredGenerated)
	require.NotNil(t, completed.ModelCallID)
	assert.Equal(t, callID, *completed.ModelCallID)

	reusedValue, reused, err := repo.CreateOrReuse(ctx, input)
	require.NoError(t, err)
	assert.True(t, reused)
	assert.Equal(t, planned.ID, reusedValue.ID)
}

func TestRunTerminalizationClosesActiveRunCompaction(t *testing.T) {
	db := SetupDB(t)
	seedCompactionSession(t, db)
	ctx := context.Background()
	runs := &RunRepo{DB: db}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: "session", ClientRequestID: "mid-run-cancel", Text: "continue"})
	require.NoError(t, err)
	repo := &RunCompactionRepo{DB: db}
	planned, _, err := repo.CreateOrReuse(ctx, RunCompactionCreate{RunID: submission.Run.ID,
		Reason: domain.CompactionReasonOverflow, Iteration: 2, RequestGeneration: 1,
		SourceDigest: "source-cancel", SummaryContractDigest: "contract-cancel"})
	require.NoError(t, err)
	require.NoError(t, repo.Start(ctx, submission.Run.ID, planned.ID))
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.Cancel(ctx, submission.Run.ID))

	closed, err := repo.Get(ctx, planned.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.CompactionCancelled, closed.Status)
	require.NotNil(t, closed.ErrorCode)
	assert.Equal(t, string(domain.ErrorCompactionCancelled), *closed.ErrorCode)
}
