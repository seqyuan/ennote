//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
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
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	t.Setenv("ENNOTE_LIVE_API_KEY", apiKey)
	t.Setenv("ENNOTE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))

	workspaceDir := t.TempDir()
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "task-graph-live", HostPath: workspaceDir,
	})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "tg-provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: baseURL, CredentialRef: "env:ENNOTE_LIVE_API_KEY",
	})
	require.NoError(t, err)
	modelProfile, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: model, DisplayName: model,
		ContextWindow: 64000, MaxOutputTokens: 512,
		SupportsToolUse: true, SupportsThinking: true, IsDefault: true,
	})
	require.NoError(t, err)
	modelProfileID := modelProfile.ID
	sessionRepo := &store.SessionRepo{DB: db}
	session, err := sessionRepo.Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "task graph live", DefaultModelProfileID: &modelProfileID,
	})
	require.NoError(t, err)

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub}
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
	explore := explorerLiveItem()
	explore.Name = "explore"
	review := explorerLiveItem()
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

	executor := newV15Executor(t, db, hub, runRepo, sessionRepo, messageRepo)
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
