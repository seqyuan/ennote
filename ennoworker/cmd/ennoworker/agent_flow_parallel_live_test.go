//go:build integration

package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveAgentFlowParallelConvergence qualifies the Agent Flow ready-set
// parallel dispatch + convergence against a real Provider:
//
//	a0 (dispatcher, reader) runs first; a1/a2/a3 (three independent readers)
//	dispatch concurrently once a0 completes; the flow converges when all four
//	role tasks settle.
//
// It requires ENNOTE_LIVE_BASE_URL / ENNOTE_LIVE_API_KEY / ENNOTE_LIVE_MODEL
// (same contract as the other live qualifications).
func TestLiveAgentFlowParallelConvergence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	stack := newLiveStack(t, "flow-par-live")
	db := stack.DB
	project := stack.Project
	session := stack.Session

	// ——— file-native global Role (reader) ———
	role := &rolesource.Document{
		SchemaVersion: 1, Handle: "par-reader", Name: "Parallel Reader",
		Description: "Read-only workspace inspector.", Positioning: "Independent",
		Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: stack.ModelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "ls", "grep", "find"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single", "parallel"},
			MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 8, MaxToolCalls: 16,
				MaxTotalTokens: 40000, MaxOutputTokens: 4096, MaxCostUSDMicros: 0, MaxWallTimeMS: 180000}},
		OutputContract: "text-v1", MaxLoopIterations: 8,
		Prompt: "You are a read-only workspace inspector. Use read, ls, grep, and find to answer the task. Be concise. End by calling submit_result with a structured result.",
	}
	_, _, err := stack.Sources.CreateRole(role)
	require.NoError(t, err)
	_, err = stack.Sources.PublishRoleRevision(role.Handle)
	require.NoError(t, err)

	// ——— file-native Graph: a0 -> a1/a2/a3 (parallel readers) ———
	_, digest, err := stack.Sources.CreateGraph("par-readers", "Parallel Readers")
	require.NoError(t, err)
	_, _, err = stack.Sources.UpdateGraph("par-readers", digest, func(d *graphsource.Document) error {
		d.Tasks = map[string]graphsource.Task{
			"a0": {Name: "dispatcher", Role: "global/par-reader", Goal: "Inspect /workspace and report its top-level contents."},
			"a1": {Name: "explore-a1", Role: "global/par-reader", Goal: "Inspect /workspace and report its top-level contents."},
			"a2": {Name: "explore-a2", Role: "global/par-reader", Goal: "Inspect /workspace and report its top-level contents."},
			"a3": {Name: "explore-a3", Role: "global/par-reader", Goal: "Inspect /workspace and report its top-level contents."},
		}
		d.Graph = map[string][]string{
			"a0": {},
			"a1": {"a0"},
			"a2": {"a0"},
			"a3": {"a0"},
		}
		return nil
	})
	require.NoError(t, err)
	_, err = stack.Sources.PublishGraphRevision("par-readers")
	require.NoError(t, err)

	// ——— coordinator + executor router ———
	hub := events.NewHub()
	runTemplate := &store.RunRepo{Publisher: hub, Providers: stack.Providers, Models: stack.ModelRepo,
		Policies: stack.Policies, RoleSources: stack.Sources}
	routedRuns := &store.RoutedRunRepo{Sessions: stack.Sessions, Template: runTemplate}
	executorRouter := &sessionExecutorRouter{sessions: stack.Sessions}
	executorRouter.build = func(db *sql.DB, sessionPath string) (*agentExecutor, error) {
		writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
		callRepo := &store.CallRepo{DB: db, Publisher: hub}
		trustStore, err := workspace.NewTrustStore(t.TempDir())
		if err != nil {
			return nil, err
		}
		emptySkills := t.TempDir()
		return &agentExecutor{
			db: db, writer: writer, homeDir: t.TempDir(), runs: &store.RunRepo{DB: db, Publisher: hub,
				Providers: stack.Providers, Models: stack.ModelRepo, Policies: stack.Policies, RoleSources: stack.Sources},
			calls: callRepo, sessionDB: &store.SessionRepo{DB: db}, msgRepo: &store.MessageRepo{DB: db},
			projects:  &store.ProjectRepo{Files: stack.Projects},
			skillRepo: &store.SkillSnapshotRepo{DB: db}, skillsDir: emptySkills,
			builtinDir: emptySkills, sandbox: "none",
			hub:               hub,
			approvals:         &store.ApprovalRepo{DB: db},
			standingApprovals: &store.StandingApprovalRepo{DB: db},
			trustStore:        trustStore,
		}, nil
	}
	coordinator := runs.NewCoordinator(routedRuns, executorRouter, 4)
	executorRouter.onChild = func(_ context.Context, ids []string) {
		for _, id := range ids {
			_ = coordinator.Enqueue(context.Background(), id)
		}
	}

	// ——— graph runner ———
	service := &graphrun.Service{
		Sources: stack.Sources, Models: stack.ModelRepo, Sessions: stack.Sessions,
		OnRunStarted: func(_ context.Context, db *sql.DB, sessionPath, runID string) error {
			startFlowOrchestrator(db, runID, hub, coordinator, stack.Sources, stack.ModelRepo, stack.Policies)
			return nil
		},
	}
	run, err := service.Start(ctx, project.ID, "par-readers", 1, session.ID, nil, nil)
	require.NoError(t, err)
	t.Logf("flow run started: %s", run.RunID)

	// ——— wait for terminal ———
	flowRuns := &store.AgentFlowRunRepo{DB: db}
	final := waitLiveFlowTerminal(t, flowRuns, run.RunID, 180*time.Second)
	if !final.State.Terminal() || final.State != domain.FlowStateCompleted {
		nodes, _ := flowRuns.ListNodes(ctx, run.RunID)
		for _, n := range nodes {
			var code, msg sql.NullString
			if n.ChildRunID != "" {
				_ = db.QueryRow(`SELECT error_code, error_message FROM agent_runs WHERE id=?`, n.ChildRunID).Scan(&code, &msg)
			}
			t.Logf("node %s state=%s child=%s err=%s/%s", n.Handle, n.TerminalState, n.ChildRunID, code.String, msg.String)
		}
	}
	assert.Equal(t, domain.FlowStateCompleted, final.State, "flow must converge: %s", final.TerminalReason)

	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	completed := 0
	for _, n := range nodes {
		if n.TerminalState == domain.FlowNodeCompleted {
			completed++
		}
		assert.Equal(t, domain.FlowNodeCompleted, n.TerminalState, "node %s", n.Handle)
	}
	assert.Equal(t, 4, completed, "all four role tasks must complete")

	// ——— usage recorded for the real Provider ———
	var usageCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&usageCount))
	assert.Positive(t, usageCount, "real Provider usage must be recorded")
}

// waitLiveFlowTerminal polls the flow run until terminal or deadline.
func waitLiveFlowTerminal(t *testing.T, flowRuns *store.AgentFlowRunRepo, runID string, timeout time.Duration) *domain.RunAgentFlow {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current, err := flowRuns.GetRun(context.Background(), runID)
		require.NoError(t, err)
		if current.State.Terminal() {
			return current
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.Fail(t, "flow run did not terminalize")
	return nil
}
