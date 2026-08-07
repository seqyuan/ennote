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

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flowLiveRoleDefinition mirrors the store-test RoleDefinition shape so this
// live test can create a published Role without importing test helpers.
func flowLiveRoleDefinition(modelID string) domain.RoleDefinition {
	return domain.RoleDefinition{
		SchemaVersion: 1,
		RolePrompt:    "Review the supplied evidence independently.",
		ModelBinding: domain.RoleModelBinding{
			Mode: domain.RoleModelFixed, ModelProfileID: modelID, ThinkingEffort: domain.ThinkingDefault,
			FallbackModelProfileIDs: []string{}, OverridableFields: []string{},
		},
		Skills:            domain.RoleSkills{Entries: []domain.RoleSkillEntry{}},
		Authority:         domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss,
		AllowedTools:      []string{"read", "grep"},
		ContextPolicy: domain.RoleContextPolicy{
			DefaultMode: domain.RoleContextTask, AllowedModes: []domain.RoleContextMode{domain.RoleContextTask},
			OwnExecutionContinuity: domain.RoleContinuityNone,
		},
		DelegationPolicy: domain.RoleDelegationPolicy{
			Admission: domain.DelegationAutoWithinBudget, AllowedCallerKinds: []string{"host"},
			AllowedStrategies: []string{"single"}, MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
			BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 100000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000},
		},
		OutputContract: "text-v1", MaxLoopIterations: 8,
	}
}

// TestLiveFlowScopedRole runs a real graph whose task references a
// flow-scoped Role by bare handle, with a same-handle global Role present as
// a trap. It proves that:
//
//   - freeze resolves the bare handle to the owning flow's Role version
//     (flow > shared catalog precedence), and
//   - the frozen Role actually executes against the real Provider and the
//     flow run completes.
//
// Requires ENNOTE_LIVE_BASE_URL / ENNOTE_LIVE_API_KEY / ENNOTE_LIVE_MODEL.
func TestLiveFlowScopedRole(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	t.Setenv("ENNOTE_LIVE_API_KEY", apiKey)
	t.Setenv("ENNOTE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 480*time.Second)
	defer cancel()

	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))

	workspaceDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "README.md"), []byte("# Flow role live\n"), 0o644))

	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "flow-role-live", HostPath: workspaceDir,
	})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "fr-provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: baseURL, CredentialRef: "env:ENNOTE_LIVE_API_KEY",
	})
	require.NoError(t, err)
	modelProfile, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: model, DisplayName: model,
		ContextWindow: 64000, MaxOutputTokens: 512,
		InputCostUSDMicrosPerMillion: 270, OutputCostUSDMicrosPerMillion: 1100,
		SupportsToolUse: true, SupportsThinking: true, IsDefault: true,
	})
	require.NoError(t, err)

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub}
	executor := newV15Executor(t, db, hub, runRepo, &store.SessionRepo{DB: db}, &store.MessageRepo{DB: db})

	profiles := &store.AgentFlowProfileRepo{DB: db}
	bindings := &store.AgentFlowBindingRepo{DB: db}
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{}}
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	checkRunner := &store.CheckTaskRunner{
		DB: db, MaxOutputBytes: 32 * 1024, DefaultTimeoutSeconds: 60,
		ManagerBuilder: func(ctx context.Context, sessionID string) (*workspace.Manager, error) {
			session, err := (&store.SessionRepo{DB: db}).FindByID(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			wSpace, err := (&store.ProjectRepo{DB: db}).FindWorkspaceByProjectID(ctx, session.ProjectID)
			if err != nil {
				return nil, err
			}
			canonicalRoot, err := workspace.CanonicalWorkspaceRoot(wSpace.HostPath)
			if err != nil {
				return nil, err
			}
			ioDir := filepath.Join(t.TempDir(), "io")
			if err := os.MkdirAll(ioDir, 0o700); err != nil {
				return nil, err
			}
			return workspace.NewManager(canonicalRoot, ioDir, "", workspace.SandboxNone)
		},
	}
	orchestrator := func() *agentflow.Orchestrator {
		return &agentflow.Orchestrator{
			Store:    &store.OrchestratorStore{Runs: flowRuns, Profiles: profiles},
			Children: &store.OrchestratorChildren{DB: db, Delegations: &store.DelegationRepo{DB: db}},
			Events:   &store.FlowEventSink{Writer: writer},
			Checker:  checkRunner,
			Enqueue: func(ctx context.Context, runID string) error {
				return executeLiveFlowChild(ctx, t, executor, runRepo, runID)
			},
			PollInterval: 150 * time.Millisecond,
		}
	}

	// The graph profile that OWNS the flow-scoped role.
	flowProfile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Flow Role Graph", Slug: "flow-role-graph", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)

	flowValidator := store.NewFlowValidator(store.FlowPublishOptions{
		DB: db, FlowID: flowProfile.ID, Skills: map[string]bool{}, CheckAllowlist: []string{"sh", "bash"},
	})

	// A flow-scoped Role under the owning graph.
	roles := &store.RoleRepo{DB: db, KnownTools: map[string]bool{
		"read": true, "grep": true, "ls": true, "glob": true, "bash": true,
	}}
	flowRoleDef := flowLiveRoleDefinition(modelProfile.ID)
	flowRoleDef.DelegationPolicy.Admission = domain.DelegationAutoWithinBudget
	flowRoleDef.DelegationPolicy.AllowedCallerKinds = []string{"host"}
	flowRoleDef.DelegationPolicy.MaxInvocationsPerParentRun = 16
	flowRoleDef.DelegationPolicy.MaxConcurrentInstances = 16
	flowRoleDef.DelegationPolicy.BudgetCeiling.MaxTotalTokens = 100000
	// Make the flow-local role unmistakable in the transcript: it must
	// describe itself as the FLOW-LOCAL role.
	flowRoleDef.RolePrompt = "You are the FLOW-LOCAL copy of this role. When asked what role you are, reply exactly: ROLE-IS-FLOW-LOCAL."
	flowRole, err := roles.Create(ctx, store.CreateRoleInput{
		Handle: "flow-worker", Name: "Flow Worker", Description: "",
		Positioning: "", Icon: "bot", Color: "neutral",
		Scope: domain.RoleScopeFlow, FlowID: &flowProfile.ID, Definition: flowRoleDef,
	})
	require.NoError(t, err)
	flowRoleVersion, err := roles.Publish(ctx, flowRole.ID, 0)
	require.NoError(t, err)

	// A same-handle GLOBAL role as a trap: freeze must prefer the flow-local
	// copy for this graph.
	sharedDef := flowRoleDef
	sharedDef.RolePrompt = "You are the SHARED copy of this role. Reply exactly: ROLE-IS-SHARED."
	sharedRole, err := roles.Create(ctx, store.CreateRoleInput{
		Handle: "flow-worker", Name: "Flow Worker (shared)", Description: "",
		Positioning: "", Icon: "bot", Color: "neutral",
		Scope: domain.RoleScopeGlobal, Definition: sharedDef,
	})
	require.NoError(t, err)
	_, err = roles.Publish(ctx, sharedRole.ID, 0)
	require.NoError(t, err)

	// The graph references the role by bare handle.
	yaml := `schemaVersion: 1
id: flow-role-live
outputs:
  verdict: {type: string}
budget:
  max_total_tokens: 90000
tasks:
  selfie:
    role: flow-worker
    goal: "Identify yourself (read no files). If your instructions tell you that you are the FLOW-LOCAL copy, submit verdict=FLOW-LOCAL; otherwise submit verdict=SHARED."
    budget: {tokens: 15000}
  done:
    terminal: {status: success, output: verdict}
    output: verdict
    depends: [selfie]
`
	def, err := agentflow.ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	_, err = profiles.UpdateDraft(ctx, flowProfile.ID, def, yaml, 0)
	require.NoError(t, err)
	version, err := profiles.Publish(ctx, flowProfile.ID, 1, flowValidator)
	require.NoError(t, err)
	binding, err := bindings.EnsureBindingExists(ctx, project.ID, version.ID)
	require.NoError(t, err)
	_, err = bindings.Update(ctx, binding.ID, true)
	require.NoError(t, err)

	// Freeze: the bare handle must resolve to the flow-local Role version,
	// not the global trap.
	inputsJSON, err := store.NormalizeFlowInputs(def, nil, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := flowRuns.FreezeFlowDefinition(ctx, project.ID, flowProfile.ID, def, inputsJSON)
	require.NoError(t, err, "flow-local role must freeze: %v", diagnostics)
	require.NotEmpty(t, freeze)
	var selfieFreeze store.FlowNodeFreeze
	found := false
	for _, node := range freeze {
		if node.Handle == "selfie" {
			selfieFreeze = node
			found = true
			break
		}
	}
	require.True(t, found, "selfie task must freeze")
	assert.Equal(t, flowRoleVersion.ID, selfieFreeze.RoleVersionID,
		"bare handle must resolve to the flow-local Role version, not the global trap")

	// Run against the real Provider and let the orchestrator execute the task.
	sessionRepo := &store.SessionRepo{DB: db}
	session, err := sessionRepo.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "flow role live"})
	require.NoError(t, err)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: project.ID, FlowVersionID: version.ID, InputsJSON: inputsJSON,
	}, freeze)
	require.NoError(t, err)
	orchestrator().Start(ctx, run.RunID)

	deadline := time.Now().Add(240 * time.Second)
	for time.Now().Before(deadline) {
		current, err := flowRuns.GetRun(ctx, run.RunID)
		require.NoError(t, err)
		if current.State == domain.FlowStateCompleted {
			break
		}
		if current.State == domain.FlowStateFailed {
			rows, _ := db.QueryContext(ctx, `SELECT handle, terminal_state, error_code, output_ref, child_run_id FROM run_agent_flow_nodes WHERE run_id=?`, run.RunID)
			for rows.Next() {
				var handle, ts, ec, cr sql.NullString
				var or sql.NullString
				_ = rows.Scan(&handle, &ts, &ec, &or, &cr)
				t.Logf("node handle=%s state=%s err=%s child=%s output=%s", handle.String, ts.String, ec.String, cr.String, or.String)
			}
			rows.Close()
			if cr, _ := db.QueryContext(ctx, `SELECT id,status,COALESCE(error_message,'') FROM agent_runs WHERE id IN (SELECT child_run_id FROM run_agent_flow_nodes WHERE run_id=?)`, run.RunID); cr != nil {
				for cr.Next() {
					var id, st, fr sql.NullString
					_ = cr.Scan(&id, &st, &fr)
					t.Logf("child %s status=%s reason=%s", id.String, st.String, fr.String)
				}
				cr.Close()
			}
			t.Fatalf("flow run failed: %s", current.TerminalReason)
		}
		time.Sleep(300 * time.Millisecond)
	}
	final, err := flowRuns.GetRun(ctx, run.RunID)
	require.NoError(t, err)
	assert.Equal(t, domain.FlowStateCompleted, final.State, "flow-local role run must complete: %s", final.TerminalReason)

	// The terminal output must carry the flow-local role's identity (the
	// frozen flow-local RolePrompt steered the model): the selfie task's
	// checkpoint output_ref holds the folded child result payload.
	var outputRef sql.NullString
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT output_ref FROM run_agent_flow_nodes WHERE run_id=? AND handle='selfie'`,
		run.RunID).Scan(&outputRef))
	require.True(t, outputRef.Valid && outputRef.String != "", "selfie checkpoint must carry output")
	assert.Contains(t, strings.ToUpper(outputRef.String), "FLOW-LOCAL",
		"the flow-local Role prompt must reach the model; got %q", outputRef.String)
}
