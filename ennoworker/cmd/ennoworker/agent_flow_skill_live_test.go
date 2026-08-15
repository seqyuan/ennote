//go:build integration

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveAgentFlowTaskCarriesSkill qualifies the Agent Flow task-Skill path
// against a real Provider: an inline model-backed Task declares a global Skill;
// the Skill is frozen onto the child delegation item, loaded at execution, and
// its prompt marker reaches the submitted result.
//
// It requires ENNOTE_LIVE_BASE_URL / ENNOTE_LIVE_API_KEY / ENNOTE_LIVE_MODEL.
func TestLiveAgentFlowTaskCarriesSkill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	stack := newLiveStack(t, "flow-skill-live")
	db := stack.DB
	project := stack.Project
	session := stack.Session
	// ——— global Skill directory (SKILL.md-only pi-ecosystem skill) ———
	skillsDir := filepath.Join(stack.Home, "skills")
	skillDir := filepath.Join(skillsDir, "live-marker")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: live-marker
description: Emits a fixed marker token so the run can prove the Skill was loaded.
---

Your submit_result summary MUST begin with the exact token LIVE_SKILL_MARKER followed by your answer.`), 0o600))
	globalSkills := map[string]bool{}
	for _, skill := range skills.Discover(skillsDir) {
		globalSkills[skill.Manifest.ID] = true
	}
	require.True(t, globalSkills["live-marker"], "live-marker skill must be discovered")

	// ——— file-native global Role (reader) carrying a preload Skill ———
	role := &rolesource.Document{
		SchemaVersion: 1, Handle: "skill-reader", Name: "Skill Reader",
		Description: "Read-only inspector with a preloaded marker Skill.", Positioning: "Independent",
		Icon: "bot", Color: "neutral",
		Model:             rolesource.ModelBinding{Ref: stack.ModelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills:            []rolesource.SkillBinding{{ID: "live-marker", Mode: domain.RoleSkillPreload}},
		Authority:         domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "ls", "grep", "find"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single", "parallel"},
			MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 20, MaxToolCalls: 40,
				MaxTotalTokens: 100000, MaxOutputTokens: 4096, MaxCostUSDMicros: 0, MaxWallTimeMS: 180000}},
		OutputContract: "text-v1", MaxLoopIterations: 8,
		Prompt: "You are a read-only workspace inspector. Use read, ls, grep, and find to answer the task. Be concise. End by calling submit_result with a structured result.",
	}
	_, _, err := stack.Sources.CreateRole(role)
	require.NoError(t, err)
	_, err = stack.Sources.PublishRoleRevision(role.Handle)
	require.NoError(t, err)

	// ——— file-native Graph: one role-backed Task (Skill preloaded via Role) ———
	_, digest, err := stack.Sources.CreateGraph("skill-flow", "Skill Flow")
	require.NoError(t, err)
	_, _, err = stack.Sources.UpdateGraph("skill-flow", digest, func(d *graphsource.Document) error {
		d.Tasks = map[string]graphsource.Task{
			"work": {Name: "work", Role: "global/skill-reader",
				Goal: "State the LIVE_SKILL_MARKER token value and call submit_result."},
		}
		d.Graph = map[string][]string{"work": {}}
		return nil
	})
	require.NoError(t, err)
	_, err = stack.Sources.PublishGraphRevision("skill-flow")
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
		return &agentExecutor{
			db: db, writer: writer, homeDir: t.TempDir(), runs: &store.RunRepo{DB: db, Publisher: hub,
				Providers: stack.Providers, Models: stack.ModelRepo, Policies: stack.Policies, RoleSources: stack.Sources},
			calls: callRepo, sessionDB: &store.SessionRepo{DB: db}, msgRepo: &store.MessageRepo{DB: db},
			projects:  &store.ProjectRepo{Files: stack.Projects},
			skillRepo: &store.SkillSnapshotRepo{DB: db}, skillsDir: skillsDir,
			builtinDir: "", sandbox: "none",
			hub:               hub,
			approvals:         &store.ApprovalRepo{DB: db},
			standingApprovals: &store.StandingApprovalRepo{DB: db},
			trustStore:        trustStore,
		}, nil
	}
	coordinator := runs.NewCoordinator(routedRuns, executorRouter, 2)
	executorRouter.onChild = func(_ context.Context, ids []string) {
		for _, id := range ids {
			_ = coordinator.Enqueue(context.Background(), id)
		}
	}

	// ——— graph runner ———
	service := &graphrun.Service{
		Sources: stack.Sources, Models: stack.ModelRepo, Sessions: stack.Sessions,
		GlobalSkills: globalSkills,
		OnRunStarted: func(_ context.Context, db *sql.DB, sessionPath, runID string) error {
			startFlowOrchestrator(db, runID, hub, coordinator, stack.Sources, stack.ModelRepo, stack.Policies)
			return nil
		},
	}
	run, err := service.Start(ctx, project.ID, "skill-flow", 1, session.ID, nil, nil)
	require.NoError(t, err)

	flowRuns := &store.AgentFlowRunRepo{DB: db}
	// Diagnostic polling so a stuck flow leaves an actionable trace.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				cur, err := flowRuns.GetRun(context.Background(), run.RunID)
				if err == nil {
					nodes, _ := flowRuns.ListNodes(context.Background(), run.RunID)
					states := []string{}
					for _, n := range nodes {
						childState, code := "", ""
						if n.ChildRunID != "" {
							_ = db.QueryRow(`SELECT status, COALESCE(error_code,'') FROM agent_runs WHERE id=?`, n.ChildRunID).Scan(&childState, &code)
						}
						states = append(states, n.Handle+"="+string(n.TerminalState)+"/child="+childState+"/"+code)
					}
					t.Logf("diag flow state=%s reason=%s nodes=[%s]", cur.State, cur.TerminalReason, strings.Join(states, ","))
				}
			}
		}
	}()
	final := waitLiveFlowTerminal(t, flowRuns, run.RunID, 150*time.Second)
	if final.State != domain.FlowStateCompleted {
		nodes, _ := flowRuns.ListNodes(context.Background(), run.RunID)
		for _, n := range nodes {
			var code, msg sql.NullString
			if n.ChildRunID != "" {
				_ = db.QueryRow(`SELECT error_code, error_message FROM agent_runs WHERE id=?`, n.ChildRunID).Scan(&code, &msg)
			}
			t.Logf("skill-flow node %s state=%s child=%s err=%s/%s", n.Handle, n.TerminalState, n.ChildRunID, code.String, msg.String)
		}
	}
	require.Equal(t, domain.FlowStateCompleted, final.State, "flow must complete: %s", final.TerminalReason)

	// ——— the Skill was frozen onto the child delegation item ———
	var roleMetaJSON string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(i.role_meta_json, '{}') FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id
		WHERE a.child_run_id IN (SELECT child_run_id FROM run_agent_flow_nodes WHERE run_id=?) LIMIT 1`, run.RunID).Scan(&roleMetaJSON))
	assert.Contains(t, roleMetaJSON, "live-marker", "frozen Role meta must carry the preloaded Skill")

	// ——— the Skill's prompt marker reached the submitted result ———
	var resultJSON string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(i.result_json, '') FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id
		WHERE a.child_run_id IN (SELECT child_run_id FROM run_agent_flow_nodes WHERE run_id=?) LIMIT 1`, run.RunID).Scan(&resultJSON))
	assert.Contains(t, resultJSON, "LIVE_SKILL_MARKER",
		"the Skill prompt must be injected so the child emits the marker")

	// ——— real Provider usage recorded ———
	var usageCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM usage_records`).Scan(&usageCount))
	assert.Positive(t, usageCount)
}
