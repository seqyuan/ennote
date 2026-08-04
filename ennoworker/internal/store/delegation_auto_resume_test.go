package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settleAutoResumeBackground creates a background group with auto-resume
// enabled, settles its child, and returns repos plus the session id.
func settleAutoResumeBackground(t *testing.T) (*store.DelegationRepo, *store.RunRepo, string, string) {
	t.Helper()
	delegations, runs, submission := setupRootBudgetParent(t, "auto-resume")
	ctx := context.Background()
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "bg-ar", Strategy: domain.DelegationStrategySingle,
		ExecutionMode: domain.DelegationExecutionBackground, AutoResume: true,
		Items: []store.CreateDelegationItemInput{explorerItem()},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = runs.Claim(ctx, children[0].ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeChildSuccess(ctx, children[0].ID, domain.RunOutput{
		Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}},
		Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "auto result"},
	}))
	// The parent Agent Loop finishes normally after accepting the handle; the
	// session then becomes idle and eligible for auto-resume delivery.
	_, err = delegations.DB.Exec(`UPDATE agent_runs SET status='succeeded' WHERE id=?`, submission.Run.ID)
	require.NoError(t, err)
	return delegations, runs, submission.Run.SessionID, group.ID
}

func TestTickSessionCreatesOneContinuationRun(t *testing.T) {
	delegations, _, sessionID, _ := settleAutoResumeBackground(t)
	ctx := context.Background()

	continuation, err := delegations.TickSession(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, continuation)
	assert.Equal(t, domain.RunKindAgent, continuation.RunKind)
	assert.Equal(t, 0, continuation.ExecutionDepth)
	assert.NotEmpty(t, continuation.TurnID)

	// The continuation is one Host public message run with a system input turn.
	var turnKind string
	require.NoError(t, delegations.DB.QueryRow(`SELECT input_kind FROM turns WHERE id=?`,
		continuation.TurnID).Scan(&turnKind))
	assert.Equal(t, "delegation_completion", turnKind)
	var speaker, visibility string
	require.NoError(t, delegations.DB.QueryRow(`SELECT speaker_kind,visibility FROM messages WHERE id=(SELECT user_message_id FROM turns WHERE id=?)`,
		continuation.TurnID).Scan(&speaker, &visibility))
	assert.Equal(t, "system", speaker)
	assert.Equal(t, "public", visibility)

	// Completion moved to resume_queued with the run attached.
	var deliveryStatus string
	var resumeRunID sqlRowString
	require.NoError(t, delegations.DB.QueryRow(`SELECT delivery_status,resume_run_id FROM delegation_completions`).
		Scan(&deliveryStatus, &resumeRunID.value))
	assert.Equal(t, "resume_queued", deliveryStatus)
	assert.Equal(t, continuation.ID, resumeRunID.value.String)

	// A second tick is a no-op: exactly one continuation per completion.
	again, err := delegations.TickSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Nil(t, again)
	var continuationRuns int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE source_completion_id IS NOT NULL`).Scan(&continuationRuns))
	assert.Equal(t, 1, continuationRuns)
}

func TestTickSessionSkipsWhenParentBusy(t *testing.T) {
	delegations, _, sessionID, _ := settleAutoResumeBackground(t)
	ctx := context.Background()

	// A queued top-level run makes the session busy; no continuation.
	_, err := (&store.RunRepo{DB: delegations.DB}).SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "busy-1", Text: "busy",
	})
	require.NoError(t, err)
	continuation, err := delegations.TickSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Nil(t, continuation)

	// The completion stays pending for later delivery.
	var deliveryStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT delivery_status FROM delegation_completions`).Scan(&deliveryStatus))
	assert.Equal(t, "pending", deliveryStatus)
}

func TestTickSessionSkipsWhenAutoResumeDisabled(t *testing.T) {
	delegations, _, submission, _ := settleBackgroundGroup(t)
	ctx := context.Background()
	sessionID := submission.Run.SessionID

	// settleBackgroundGroup used AutoResume=false; the handle must carry that.
	continuation, err := delegations.TickSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Nil(t, continuation)
	var deliveryStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT delivery_status FROM delegation_completions`).Scan(&deliveryStatus))
	assert.Equal(t, "pending", deliveryStatus)
}

func TestTickSessionSkipsWhenBranchChanged(t *testing.T) {
	delegations, _, sessionID, _ := settleAutoResumeBackground(t)
	ctx := context.Background()

	// Fork/switch the active branch; the source branch no longer matches.
	_, err := delegations.DB.Exec(`INSERT INTO session_branches (id,session_id,parent_branch_id,label,created_at,updated_at)
		SELECT 'b-fork',id,active_branch_id,'fork',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP FROM sessions WHERE id=?`,
		sessionID)
	require.NoError(t, err)
	_, err = delegations.DB.Exec(`UPDATE sessions SET active_branch_id='b-fork' WHERE id=?`, sessionID)
	require.NoError(t, err)

	continuation, err := delegations.TickSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Nil(t, continuation, "branch switch must suppress auto-resume")
	var deliveryStatus string
	require.NoError(t, delegations.DB.QueryRow(`SELECT delivery_status FROM delegation_completions`).Scan(&deliveryStatus))
	assert.Equal(t, "pending", deliveryStatus)
}

func TestTickSessionDuplicateTickAfterRunCreatedIsSafe(t *testing.T) {
	delegations, _, sessionID, _ := settleAutoResumeBackground(t)
	ctx := context.Background()

	first, err := delegations.TickSession(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, first)
	// Simulate restart: the run exists but was never enqueued; a new tick must
	// not create a second continuation.
	second, err := delegations.TickSession(ctx, sessionID)
	require.NoError(t, err)
	assert.Nil(t, second)
	var count int
	require.NoError(t, delegations.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE source_completion_id IS NOT NULL`).Scan(&count))
	assert.Equal(t, 1, count)
}

// sqlRowString is a tiny scan helper for nullable TEXT columns.
type sqlRowString struct {
	value sql.NullString
}
