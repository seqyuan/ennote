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
		json.RawMessage(`{"version":1}`), approvalItems(), nil)
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

func TestDelegationApprovalUsesAdmissionStatusAndQueuesAfterDecision(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	items := []domain.ApprovalItem{{CallIndex: 0, ToolCallID: "delegate-call", ToolName: "delegate_tasks",
		RiskClass: domain.RiskDelegation, ArgumentsPreview: "delegate to @workspace-explorer",
		Delegations: []domain.DelegationApprovalPreview{{Name: "inspect", RoleHandle: "workspace-explorer",
			AssignmentPreview: "Inspect the workspace", Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8}}}}}
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "delegation-digest",
		json.RawMessage(`{"version":1}`), items, nil)
	require.NoError(t, err)

	run, err := runs.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunWaitingDelegationAdmit, run.Status)
	pending, err := approvals.FindPendingBySession(context.Background(), submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.Len(t, pending.Items[0].Delegations, 1)
	assert.Equal(t, "workspace-explorer", pending.Items[0].Delegations[0].RoleHandle)

	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "admit", nil)
	require.NoError(t, err)
	run, err = runs.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, run.Status)
}

func TestApprovalDecisionIsIdempotentAndOppositeDecisionConflicts(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":1}`), approvalItems(), nil)
	require.NoError(t, err)

	resolved, err := approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "decision-1", nil)
	require.NoError(t, err)
	assert.Equal(t, domain.ApprovalApproved, resolved.Status)
	resolved, err = approvals.Decide(context.Background(), request.ID, domain.DecisionApproved, "decision-2", nil)
	require.NoError(t, err)
	assert.Equal(t, "decision-1", resolved.DecisionClientRequestID)
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionRejected, "decision-3", nil)
	assert.ErrorIs(t, err, store.ErrApprovalConflict)

	run, err := runs.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, run.Status)
}

func TestApprovalBeginResumeClaimsCheckpointOnce(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	request, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 3, "digest",
		json.RawMessage(`{"version":1,"messages":[]}`), approvalItems(), nil)
	require.NoError(t, err)
	_, err = approvals.Decide(context.Background(), request.ID, domain.DecisionRejected, "decision", nil)
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

func TestApprovalResumeFinalizationCommitsOneShadowTranscript(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	ctx := context.Background()
	request, err := approvals.Suspend(ctx, submission.Run.ID, 1, 2, "digest",
		json.RawMessage(`{"version":1,"messages":[]}`), approvalItems(), nil)
	require.NoError(t, err)
	var shadowCount int
	require.NoError(t, runs.DB.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, submission.Run.ID).Scan(&shadowCount))
	assert.Zero(t, shadowCount)

	_, err = approvals.Decide(ctx, request.ID, domain.DecisionApproved, "decision", nil)
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resume, err := approvals.BeginResume(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NotNil(t, resume)
	require.NoError(t, approvals.CompleteExecuting(ctx, submission.Run.ID))

	require.NoError(t, runs.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput()))
	require.NoError(t, runs.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput()),
		"terminal replay must be idempotent")
	require.NoError(t, runs.DB.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, submission.Run.ID).Scan(&shadowCount))
	assert.Equal(t, 3, shadowCount)
	var committedEvents int
	require.NoError(t, runs.DB.QueryRow(`SELECT COUNT(*) FROM run_events
		WHERE run_id=? AND event_type='run_transcript_committed'`, submission.Run.ID).Scan(&committedEvents))
	assert.Equal(t, 1, committedEvents)
}

func TestWaitingApprovalBlocksTurnAndCompactionButAllowsSteerAndCancel(t *testing.T) {
	runs, approvals, submission := setupApprovalRun(t)
	_, err := approvals.Suspend(context.Background(), submission.Run.ID, 1, 1, "digest",
		json.RawMessage(`{"version":1}`), approvalItems(), nil)
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
