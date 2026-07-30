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

func setupApprovalRun(t *testing.T) (*store.RunRepo, *store.ApprovalRepo, *domain.TurnSubmission) {
	t.Helper()
	db := store.SetupDB(t)
	runs := &store.RunRepo{DB: db}
	sessionID := createRunTestSession(t, runs)
	submission, err := runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "approval-turn", Text: "change the file",
	})
	require.NoError(t, err)
	_, err = runs.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	return runs, &store.ApprovalRepo{DB: db}, submission
}

func approvalItems() []domain.ApprovalItem {
	return []domain.ApprovalItem{{CallIndex: 1, ToolCallID: "call-write", ToolName: "write",
		RiskClass: domain.RiskLocalWrite, ArgumentsPreview: "path: notes.txt"}}
}

func TestApprovalSuspendIsAtomicAndSessionRestorable(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 2, "digest",
		json.RawMessage(`{"version":1}`), approvalItems())
	require.NoError(t, err)
	assert.Equal(t, domain.ApprovalPending, request.Status)

	run, err := runs.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunWaitingForApproval, run.Status)
	pending, err := approvals.FindPendingBySession(context.Background(), submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, request.ID, pending.ID)
	assert.Equal(t, "path: notes.txt", pending.Items[0].ArgumentsPreview)

	var eventType string
	require.NoError(t, runs.DB.QueryRow(`SELECT event_type FROM run_events WHERE run_id=? ORDER BY seq DESC LIMIT 1`, submission.Run.ID).Scan(&eventType))
	assert.Equal(t, "approval_requested", eventType)
}

func TestApprovalDecisionIsIdempotentAndOppositeDecisionConflicts(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":1}`), approvalItems())
	require.NoError(t, err)

	resolved, err := approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "decision-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ApprovalApproved, resolved.Status)
	resolved, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "decision-2")
	require.NoError(t, err)
	assert.Equal(t, "decision-1", resolved.DecisionClientRequestID)
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionRejected, "decision-3")
	assert.ErrorIs(t, err, store.ErrApprovalConflict)

	run, err := runs.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, run.Status)
}

func TestApprovalBeginResumeClaimsCheckpointOnce(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 3, "digest",
		json.RawMessage(`{"version":1,"messages":[]}`), approvalItems())
	require.NoError(t, err)
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionRejected, "decision")
	require.NoError(t, err)
	_, err = runs.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)

	resume, err := approvals.BeginResume(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	require.NotNil(t, resume)
	assert.Equal(t, domain.DecisionRejected, resume.Decision)
	assert.Equal(t, domain.CheckpointExecuting, resume.Checkpoint.Status)
	assert.JSONEq(t, `{"version":1,"messages":[]}`, string(resume.Checkpoint.State))
	second, err := approvals.BeginResume(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Nil(t, second)
	require.NoError(t, approvals.CompleteExecuting(context.Background(), submission.Run.ID))
}

func TestWaitingApprovalBlocksTurnAndCompactionButAllowsSteerAndCancel(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	_, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":1}`), approvalItems())
	require.NoError(t, err)
	_, err = runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: submission.Run.SessionID, ClientRequestID: "second", Text: "second",
	})
	assert.ErrorIs(t, err, store.ErrSessionRunActive)

	queue := &store.QueueRepo{DB: runs.DB}
	_, err = queue.Enqueue(context.Background(), submission.Run.ID, "steer", domain.QueuedInputSteer, "do less")
	require.NoError(t, err)
	require.NoError(t, runs.Cancel(context.Background(), submission.Run.ID))
	pending, err := approvals.FindPendingByRun(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Nil(t, pending)
	var approvalStatus, checkpointStatus string
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM tool_approval_requests WHERE run_id=?`, submission.Run.ID).Scan(&approvalStatus))
	require.NoError(t, runs.DB.QueryRow(`SELECT status FROM run_execution_checkpoints WHERE run_id=?`, submission.Run.ID).Scan(&checkpointStatus))
	assert.Equal(t, "cancelled", approvalStatus)
	assert.Equal(t, "cancelled", checkpointStatus)
}
