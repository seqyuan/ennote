//go:build integration

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveWorkspaceExplorerChildCompletesContractAndFoldsResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	stack := newLiveStack(t, "explorer-live")
	db := stack.DB
	session := stack.Session

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub, Providers: stack.Providers,
		Models: stack.ModelRepo, Policies: stack.Policies}
	messageRepo := &store.MessageRepo{DB: db}

	// ——— Parent Run: create, claim, freeze config (so child can inherit model) ———
	parentSubmission, err := runRepo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "explorer-parent",
		Text: "Parent inquiry", RequestedConfig: json.RawMessage(`{"maxIterations":1}`),
	})
	require.NoError(t, err)
	parentRun, err := runRepo.Claim(ctx, parentSubmission.Run.ID)
	require.NoError(t, err)
	parentResolved, err := runRepo.ResolveAndFreezeConfig(ctx, parentRun)
	require.NoError(t, err)
	require.NotEmpty(t, parentResolved.Effective.ModelProfileID, "parent must have a frozen model for child inherit")

	// ——— Delegation: group + item (Workspace Explorer builtin) ———
	delegations := &store.DelegationRepo{DB: db}
	group, err := delegations.CreateGroup(ctx, store.CreateDelegationGroupInput{
		ParentRunID: parentRun.ID, ParentToolCallID: "explore", Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{{
			Name: "explore-workspace", RoleVersionID: "builtin-workspace-explorer-v3",
			AssignmentJSON: json.RawMessage(`{"objective":"List the files in /workspace and note their names in one sentence."}`),
			OutputContract: "text-v1",
			Budget: domain.BudgetCeilingJSON{MaxModelCalls: 6, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 2048, MaxWallTimeMS: 120000},
			RoleMeta: liveExplorerRoleMeta(stack.ModelID),
		}},
	})
	require.NoError(t, err)
	items, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)

	// Create child run — this sets parent waiting_children, group waiting_children.
	child, err := delegations.CreateChildRun(ctx, store.CreateChildRunInput{
		ParentRunID: parentRun.ID, ItemID: items[0].ID, SessionID: session.ID,
	})
	require.NoError(t, err)
	require.Equal(t, domain.RunKindDelegatedAgent, child.RunKind)

	// ——— Executor: run the child through the real Provider ———
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	callRepo := &store.CallRepo{DB: db, Publisher: hub}
	trustStore, err := workspace.NewTrustStore(t.TempDir())
	require.NoError(t, err)
	emptySkills := t.TempDir()

	executor := &agentExecutor{
		db: db, writer: writer, homeDir: t.TempDir(), runs: runRepo, calls: callRepo,
		sessionDB: &store.SessionRepo{DB: db}, msgRepo: messageRepo, projects: &store.ProjectRepo{Files: stack.Projects},
		skillRepo: &store.SkillSnapshotRepo{DB: db}, skillsDir: emptySkills,
		builtinDir: emptySkills, sandbox: "none",
		hub:               hub,
		approvals:         &store.ApprovalRepo{DB: db},
		standingApprovals: &store.StandingApprovalRepo{DB: db},
		trustStore:        trustStore,
	}

	// Claim the child so it's running and executeDelegatedChild runs.
	_, err = runRepo.Claim(ctx, child.ID)
	require.NoError(t, err)
	output, execErr := executor.executeDelegatedChild(ctx, &domain.AgentRun{
		ID: child.ID, SessionID: child.SessionID, RunKind: child.RunKind,
		ParentRunID: child.ParentRunID, CommitFormatVersion: child.CommitFormatVersion,
		PublishMode: child.PublishMode, SpeakerSnapshot: child.SpeakerSnapshot,
	})
	require.NoError(t, execErr, "child execution must succeed on the real Provider")
	require.NotNil(t, output.Terminal, "child must call submit_result")
	t.Logf("child submit_result status=%s summary=%s", output.Terminal.Status, output.Terminal.Summary)
	assert.Equal(t, domain.SubmitCompleted, output.Terminal.Status)
	assert.NotEmpty(t, output.Terminal.Summary)

	// ——— Finalize: private transcript, item fold, group settle, parent wake ———
	require.NoError(t, runRepo.FinalizeChildSuccess(ctx, child.ID, output))
	childStored, err := runRepo.Get(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, childStored.Status)

	// Child must have no canonical messages (private_only).
	var canonical int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM messages WHERE run_id=?`, child.ID).Scan(&canonical))
	assert.Zero(t, canonical, "children must not publish to the canonical conversation")

	// Private transcript for the child must exist.
	var shadowRows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, child.ID).Scan(&shadowRows))
	assert.Positive(t, shadowRows, "child must have a private execution transcript")

	// Budget must have been consumed (at least one model call).
	var consumedCalls int
	require.NoError(t, db.QueryRow(`SELECT consumed_model_calls FROM run_budgets WHERE run_id=?`, child.ID).Scan(&consumedCalls))
	assert.Positive(t, consumedCalls, "child budget must reflect model calls consumed")

	// Parent is woken to queued after the child settles; re-claim and succeed.
	parentAfter, err := runRepo.Get(ctx, parentRun.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, parentAfter.Status, "parent must be queued after child settles")
	_, err = runRepo.Claim(ctx, parentRun.ID)
	require.NoError(t, err)
	require.NoError(t, runRepo.FinalizeSuccess(ctx, parentRun.ID, domain.RunOutput{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Child reported: " + output.Terminal.Summary}}},
	}}))

	// Item folded with submit_result content.
	finalItems, err := delegations.ListItems(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationItemTerminal, finalItems[0].Status)
	assert.Contains(t, string(finalItems[0].ResultJSON), output.Terminal.Summary)

	// Group settled.
	settledGroup, err := delegations.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DelegationGroupSettled, settledGroup.Status)

	// Real Provider execution must have recorded usage.
	var usageCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE run_id=?`, child.ID).Scan(&usageCount))
	assert.Positive(t, usageCount, "real Provider usage must be recorded for the child")
}
