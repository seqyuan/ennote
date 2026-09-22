package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertDelegatedChildRun seeds a running delegated_agent Run under a parent.
// Children share the parent's Session database, so a child's approval is visible
// through the same session-scoped queries as a public Run's.
func insertDelegatedChildRun(t *testing.T, ctx context.Context, runs *store.RunRepo, sessionID, parentRunID string) string {
	t.Helper()
	childRunID := uuid.NewString()
	_, err := runs.DB.ExecContext(ctx, `INSERT INTO agent_runs
		(id,session_id,run_kind,status,parent_run_id,root_run_id,execution_depth,publish_mode,created_at)
		VALUES (?,?,'delegated_agent','running',?,?,1,'private_to_parent',?)`,
		childRunID, sessionID, parentRunID, parentRunID, "2026-09-23T00:00:00Z")
	require.NoError(t, err)
	return childRunID
}

// A delegated child that suspends for approval must be resumable through exactly
// the same checkpoint mechanism as a public Run. If it is not, the only way to
// continue it is a fresh start, which replays work the user already saw and runs
// a batch nobody approved.
func TestDelegatedChildApprovalCheckpointRoundTrips(t *testing.T) {
	runs, _, submission := setupApprovalRun(t)
	ctx := context.Background()
	approvals := &store.ApprovalRepo{DB: runs.DB}
	childRunID := insertDelegatedChildRun(t, ctx, runs, submission.Run.SessionID, submission.Run.ID)

	state, err := json.Marshal(map[string]any{
		"version": agent.ResumeStateVersion, "iteration": 2,
		"approvalDigestVersion": agent.ApprovalDigestV2,
		"systemPrompt":          "frozen child prompt",
	})
	require.NoError(t, err)
	request, err := approvals.Suspend(ctx, childRunID, agent.ResumeStateVersion, 2, "child-digest",
		state, approvalItems(), nil)
	require.NoError(t, err)

	child, err := runs.Get(ctx, childRunID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunWaitingForApproval, child.Status)

	// The child's approval is reachable from its Session, like any other.
	pending, err := approvals.FindPendingBySession(ctx, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, request.ID, pending.ID)

	_, err = approvals.Decide(ctx, request.ID, domain.DecisionApproved, "child-decision", nil)
	require.NoError(t, err)
	requeued, err := runs.Get(ctx, childRunID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, requeued.Status)

	_, err = runs.Claim(ctx, childRunID)
	require.NoError(t, err)

	resume, err := approvals.BeginResume(ctx, childRunID)
	require.NoError(t, err)
	require.NotNil(t, resume, "a delegated child must be able to claim its checkpoint")
	assert.Equal(t, domain.DecisionApproved, resume.Decision)
	assert.Equal(t, "child-digest", resume.Approval.BatchDigest)
	assert.Equal(t, string(state), string(resume.Checkpoint.State))

	// Claiming is one-shot: a second claim without a new suspension finds nothing.
	second, err := approvals.BeginResume(ctx, childRunID)
	require.NoError(t, err)
	assert.Nil(t, second)

	// The resumed child must retire the checkpoint, or it stays claimed forever.
	require.NoError(t, approvals.CompleteExecuting(ctx, childRunID))
	after, err := approvals.BeginResume(ctx, childRunID)
	require.NoError(t, err)
	assert.Nil(t, after)
}

// A child Run's private transcript is written by the same finalizer as a public
// Run's, so the resume state it carries must survive the freeze/read path.
func TestDelegatedChildRunStatusIsIndependentOfItsParent(t *testing.T) {
	runs, _, submission := setupApprovalRun(t)
	ctx := context.Background()
	approvals := &store.ApprovalRepo{DB: runs.DB}
	childRunID := insertDelegatedChildRun(t, ctx, runs, submission.Run.SessionID, submission.Run.ID)

	state, err := json.Marshal(map[string]any{"version": agent.ResumeStateVersion, "iteration": 1,
		"approvalDigestVersion": agent.ApprovalDigestV2})
	require.NoError(t, err)
	_, err = approvals.Suspend(ctx, childRunID, agent.ResumeStateVersion, 1, "digest", state, approvalItems(), nil)
	require.NoError(t, err)

	// Suspending the child must not touch the parent.
	parent, err := runs.Get(ctx, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunRunning, parent.Status)
}
