//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveTaskGraphQualifies the dynamic task graph vertical slice against a
// real Provider:
//
//	Host delegates a two-task chain (explore -> review) via delegate_tasks
//	with depends; only the entry task starts first
//	the entry task settles against the real Provider, then the dependent task
//	becomes ready and runs
//	live child_progress events arrive on the parent run's channel
//	the group settles and the parent folding sees both task results
//
// It requires ENNOTE_LIVE_BASE_URL / ENNOTE_LIVE_API_KEY / ENNOTE_LIVE_MODEL
// (same contract as the Item 6 live qualification).
func TestLiveTaskGraph(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	stack := newLiveStack(t, "task-graph-live")
	db := stack.DB
	session := stack.Session
	modelProfileID := stack.ModelID

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub, Providers: stack.Providers,
		Models: stack.ModelRepo, Policies: stack.Policies}
	messageRepo := &store.MessageRepo{DB: db}

	parentSubmission, err := runRepo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "tg-parent",
		Text: "Parent inquiry", RequestedConfig: json.RawMessage(`{"maxIterations":1}`),
	})
	require.NoError(t, err)
	parentRun, err := runRepo.Claim(ctx, parentSubmission.Run.ID)
	require.NoError(t, err)
	parentResolved, err := runRepo.ResolveAndFreezeConfig(ctx, parentRun)
	require.NoError(t, err)
	require.NotEmpty(t, parentResolved.Effective.ModelProfileID)

	delegations := &store.DelegationRepo{DB: db}
	// Simulate the parent's recorded delegate_tasks tool call so folding lands
	// on the expected result_preview when the group settles.
	_, err = db.Exec(`INSERT INTO tool_calls
		(id,run_id,seq,tool_call_id,tool_name,arguments_json,status,started_at)
		VALUES('tc-tg',?,1,'tg-chain','delegate_tasks','{}','completed',CURRENT_TIMESTAMP)`,
		parentRun.ID)
	require.NoError(t, err)
	explore := explorerLiveItem(t, modelProfileID)
	explore.Name = "explore"
	review := explorerLiveItem(t, modelProfileID)
	review.Name = "review"
	review.Depends = []string{"explore"}
	review.AssignmentJSON = json.RawMessage(`{"objective":"Review the workspace listing from the explore task and answer whether any data files are present. Say REVIEW_OK when done."}`)
	group, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: parentRun.ID, ParentToolCallID: "tg-chain", Strategy: domain.DelegationStrategyParallel,
		Items: []store.CreateDelegationItemInput{explore, review},
	}, session.ID)
	require.NoError(t, err)
	require.Len(t, children, 2)

	// Only the entry task is ready initially.
	ready, err := delegations.ReadyChildrenForEnqueue(ctx, []string{children[0].ID, children[1].ID})
	require.NoError(t, err)
	require.Equal(t, []string{children[0].ID}, ready, "dependent task must stay queued")

	// Live child_progress on the parent channel while the entry task runs.
	parentLive, parentLiveStop := hub.SubscribeLive(parentRun.ID, 128)
	defer parentLiveStop()

	executor := newV15Executor(t, db, hub, runRepo, &store.SessionRepo{DB: db}, messageRepo, &store.ProjectRepo{Files: stack.Projects})
	require.NoError(t, executeV15Child(ctx, t, executor, runRepo, children[0], "explore"))

	// Entry settled -> dependent task ready.
	ready, err = runRepo.ReadySuccessorRuns(ctx, children[0].ID)
	require.NoError(t, err)
	require.Equal(t, []string{children[1].ID}, ready, "review must start after explore settles")
	require.NoError(t, executeV15Child(ctx, t, executor, runRepo, children[1], "review"))

	// The group settles.
	var groupStatus string
	require.NoError(t, db.QueryRow(`SELECT status FROM delegation_groups WHERE id=?`, group.ID).Scan(&groupStatus))
	assert.Equal(t, "settled", groupStatus)

	// Parent folding sees both task results.
	var itemCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM delegation_items WHERE group_id=? AND status='succeeded'`, group.ID).Scan(&itemCount))
	assert.Equal(t, 2, itemCount, "both tasks must succeed")

	// At least one child_progress arrived on the parent live channel while
	// children executed (bounded, live-only). The channel is drained here; the
	// exact activity set depends on the Provider's tool usage.
	sawChildProgress := false
	drain := func() {
		for {
			select {
			case ev := <-parentLive:
				if ev.Type == domain.LiveChildProgress {
					sawChildProgress = true
				}
			default:
				return
			}
		}
	}
	drain()
	assert.True(t, sawChildProgress, "parent live channel must carry child_progress during child execution")

	// A fresh group-level assertion: the frozen parent tool call has folded
	// results injected (parent resume sees real output, not a placeholder).
	var folded sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT result_preview FROM tool_calls WHERE run_id=? AND tool_call_id=?`,
		parentRun.ID, "tg-chain").Scan(&folded))
	assert.True(t, folded.Valid && strings.Contains(folded.String, "settled"),
		"parent tool call must carry the folded task-graph result")
}
