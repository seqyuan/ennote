package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupFlowServer builds a Server with Agent Flows wired to real stores and a
// stub StartRun that freeze+creates the run without dispatching children.
func setupFlowServer(t *testing.T) (*Server, http.Handler, *store.ProjectRepo) {
	t.Helper()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	hub := events.NewHub()
	projects := &store.ProjectRepo{DB: db}
	profiles := &store.AgentFlowProfileRepo{DB: db}
	bindings := &store.AgentFlowBindingRepo{DB: db}
	runs := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}
	checks := &store.CheckTaskRunner{DB: db}
	server := &Server{
		DB: db, Token: "test-token", Sandbox: "none",
		Projects: projects, Providers: &store.ProviderRepo{DB: db},
		Models: &store.ModelRepo{DB: db}, Roles: &store.RoleRepo{DB: db, KnownTools: map[string]bool{
			"read": true, "ls": true, "grep": true, "find": true, "bash": true, "write": true,
		}}, Policies: &store.PolicyRepo{DB: db},
		Artifacts: &artifacts.Service{DB: db, Root: t.TempDir()}, Sessions: &store.SessionRepo{DB: db},
		Branches: &store.BranchRepo{DB: db}, Messages: &store.MessageRepo{DB: db},
		Compactions: &store.CompactionRepo{DB: db}, Approvals: &store.ApprovalRepo{DB: db},
		Delegations: &store.DelegationRepo{DB: db}, Runs: &store.RunRepo{DB: db},
		Queue: &store.QueueRepo{DB: db}, Events: &store.EventRepo{DB: db}, Hub: hub,
		AgentFlows: &AgentFlowServer{
			Profiles: profiles, Bindings: bindings, Runs: runs,
			Projects: projects, Sessions: &store.SessionRepo{DB: db},
			Checks:    checks,
			Discovery: &store.AgentFlowDiscovery{Profiles: profiles},
			Skills:    map[string]bool{"go-dev": true},
		},
	}
	return server, server.Handler(), projects
}

// publishFixtureRole publishes a global flow-worker role and returns its
// handle@version reference.
func publishFixtureRole(t *testing.T, server *Server) string {
	t.Helper()
	ctx := context.Background()
	provider, err := (&store.ProviderRepo{DB: server.DB}).Create(ctx, store.CreateProviderInput{
		Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test", CredentialRef: "env:FLOW_KEY",
	})
	require.NoError(t, err)
	model, err := (&store.ModelRepo{DB: server.DB}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "flow-model", ContextWindow: 32000, MaxOutputTokens: 2048,
		SupportsToolUse: true, SupportsThinking: true,
		ThinkingDialect:          domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault},
	})
	require.NoError(t, err)
	roleDef := domain.RoleDefinition{
		SchemaVersion: 1, RolePrompt: "Execute one flow task.",
		ModelBinding: domain.RoleModelBinding{Mode: domain.RoleModelFixed, ModelProfileID: model.ID,
			ThinkingEffort: domain.ThinkingDefault, FallbackModelProfileIDs: []string{}, OverridableFields: []string{}},
		Skills:    domain.RoleSkills{Entries: []domain.RoleSkillEntry{}},
		Authority: domain.RoleAuthorityReadOnly, PermissionCeiling: domain.PermissionDiscuss,
		AllowedTools: []string{"read", "grep"},
		ContextPolicy: domain.RoleContextPolicy{
			DefaultMode: domain.RoleContextTask, AllowedModes: []domain.RoleContextMode{domain.RoleContextTask},
			OwnExecutionContinuity: domain.RoleContinuityNone,
		},
		DelegationPolicy: domain.RoleDelegationPolicy{
			Admission: domain.DelegationAutoWithinBudget, AllowedCallerKinds: []string{"host"},
			AllowedStrategies: []string{"single"}, MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
			BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 8, MaxToolCalls: 16,
				MaxTotalTokens: 200000, MaxOutputTokens: 8000, MaxCostUSDMicros: 1000000, MaxWallTimeMS: 1800000},
		},
		OutputContract: "text-v1", MaxLoopIterations: 8,
	}
	roles := &store.RoleRepo{DB: server.DB, KnownTools: map[string]bool{"read": true, "grep": true}}
	role, err := roles.Create(ctx, store.CreateRoleInput{
		Handle: "flow-worker", Name: "Flow Worker", Description: "",
		Positioning: "", Icon: "bot", Color: "neutral",
		Scope: domain.RoleScopeGlobal, Definition: roleDef,
	})
	require.NoError(t, err)
	_, err = roles.Publish(ctx, role.ID, 0)
	require.NoError(t, err)
	return "flow-worker@1"
}

const apiFlowYAML = `schemaVersion: 1
id: go-review
inputs:
  target: {type: path, required: true}
outputs:
  report: {type: string}
budget:
  max_total_tokens: 120000
tasks:
  producer:
    role: %s
    skills: [go-dev]
    goal: "Implement {inputs.target}"
    budget: {tokens: 50000}
  reviewer:
    role: %s
    goal: "Review {task.producer.output.changedFiles}"
    depends: [producer]
  accept:
    terminal: {status: success, output: report}
    output: report
    depends: [reviewer]
`

func TestAgentFlowAPIProfileDraftPublish(t *testing.T) {
	server, handler, _ := setupFlowServer(t)
	roleRef := publishFixtureRole(t, server)

	rec := request(t, handler, http.MethodPost, "/v1/agent-flows",
		map[string]any{"name": "Go Review", "slug": "go-review"}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var profile domain.AgentFlowProfile
	decodeData(t, rec, &profile)

	// Save a draft.
	yaml := apiFlowYAML
	_ = roleRef
	yaml = "schemaVersion: 1\nid: go-review\nbudget:\n  max_total_tokens: 120000\ntasks:\n  producer:\n    role: flow-worker@1\n    goal: \"do it\"\n"
	rec = request(t, handler, http.MethodPatch, "/v1/agent-flows/"+profile.ID+"/draft",
		map[string]any{"yaml": yaml, "expectedRevision": 0}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var updated domain.AgentFlowProfile
	decodeData(t, rec, &updated)
	assert.Equal(t, 1, updated.DraftRevision)

	// Validate passes.
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/"+profile.ID+"/validate", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var validation agentflow.ValidationResult
	decodeData(t, rec, &validation)
	assert.True(t, validation.Valid, "%v", validation.Diagnostics)

	// Publish -> immutable version 1.
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/"+profile.ID+"/publish",
		map[string]any{"expectedRevision": 1}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version domain.AgentFlowVersion
	decodeData(t, rec, &version)
	assert.Equal(t, 1, version.Version)
	assert.NotEmpty(t, version.ConfigDigest)

	// Draft change + publish -> version 2, digest differs.
	yaml2 := "schemaVersion: 1\nid: go-review\nbudget:\n  max_total_tokens: 120000\ntasks:\n  producer:\n    role: flow-worker@1\n    goal: \"do it better\"\n"
	rec = request(t, handler, http.MethodPatch, "/v1/agent-flows/"+profile.ID+"/draft",
		map[string]any{"yaml": yaml2, "expectedRevision": 1}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/"+profile.ID+"/publish",
		map[string]any{"expectedRevision": 2}, true)
	require.Equal(t, http.StatusCreated, rec.Code)
	var version2 domain.AgentFlowVersion
	decodeData(t, rec, &version2)
	assert.Equal(t, 2, version2.Version)
	assert.NotEqual(t, version.ConfigDigest, version2.ConfigDigest)

	// Invalid draft fails loudly with a diagnostic list.
	badYAML := "schemaVersion: 1\nid: go-review\nbudget:\n  max_total_tokens: 1000\ntasks:\n  a:\n    role: flow-worker@1\n    goal: \"x\"\n  b:\n    role: flow-worker@1\n    goal: \"y\"\n"
	rec = request(t, handler, http.MethodPatch, "/v1/agent-flows/"+profile.ID+"/draft",
		map[string]any{"yaml": badYAML, "expectedRevision": 2}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/"+profile.ID+"/validate", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	decodeData(t, rec, &validation)
	assert.False(t, validation.Valid)
	codes := map[string]bool{}
	for _, d := range validation.Diagnostics {
		codes[d.Code] = true
	}
	assert.True(t, codes["entry_task_count"])
}

func TestAgentFlowAPIBindingRunCancelFlow(t *testing.T) {
	server, handler, projects := setupFlowServer(t)
	roleRef := publishFixtureRole(t, server)
	ctx := context.Background()

	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Flow", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: server.DB}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "flow session",
	})
	require.NoError(t, err)

	// Publish the flow.
	rec := request(t, handler, http.MethodPost, "/v1/agent-flows",
		map[string]any{"name": "Go Review", "slug": "go-review"}, true)
	require.Equal(t, http.StatusCreated, rec.Code)
	var profile domain.AgentFlowProfile
	decodeData(t, rec, &profile)
	yaml := "schemaVersion: 1\nid: go-review\ninputs:\n  target: {type: path, required: true}\noutputs:\n  report: {type: string}\nbudget:\n  max_total_tokens: 120000\ntasks:\n  producer:\n    role: " + roleRef + "\n    skills: [go-dev]\n    goal: \"Implement {inputs.target}\"\n    budget: {tokens: 50000}\n  reviewer:\n    role: " + roleRef + "\n    goal: \"Review {task.producer.output.changedFiles}\"\n    depends: [producer]\n  accept:\n    terminal: {status: success, output: report}\n    output: report\n    depends: [reviewer]\n"
	rec = request(t, handler, http.MethodPatch, "/v1/agent-flows/"+profile.ID+"/draft",
		map[string]any{"yaml": yaml, "expectedRevision": 0}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/"+profile.ID+"/publish",
		map[string]any{"expectedRevision": 1}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version domain.AgentFlowVersion
	decodeData(t, rec, &version)

	// Bind + enable.
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/bindings",
		map[string]any{"flowVersionId": version.ID, "desiredEnabled": true}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var binding domain.ProjectAgentFlowBinding
	decodeData(t, rec, &binding)
	assert.True(t, binding.DesiredEnabled)

	// Run (stub StartRun freeze+create only).
	server.AgentFlows.StartRun = func(ctx context.Context, projectID, flowVersionID, sessionID string,
		inputs, vars map[string]any) (*domain.RunAgentFlow, error) {
		v, err := server.AgentFlows.Profiles.GetVersion(ctx, flowVersionID)
		if err != nil {
			return nil, err
		}
		var def domain.FlowDefinition
		require.NoError(t, json.Unmarshal(v.DefinitionJSON, &def))
		inputsJSON, err := store.NormalizeFlowInputs(&def, inputs, vars)
		if err != nil {
			return nil, err
		}
		freeze, _, err := server.AgentFlows.Runs.FreezeFlowDefinition(ctx, projectID, &def, inputsJSON)
		if err != nil {
			return nil, err
		}
		return server.AgentFlows.Runs.CreateFlowRun(ctx, store.CreateFlowRunInput{
			SessionID: sessionID, ProjectID: projectID, FlowVersionID: flowVersionID, InputsJSON: inputsJSON,
		}, freeze)
	}
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/bindings/"+binding.ID+"/run",
		map[string]any{"sessionId": session.ID, "inputs": map[string]any{"target": "src/main.go"}, "clientRequestId": "run-1"}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var flowRun domain.RunAgentFlow
	decodeData(t, rec, &flowRun)
	assert.Equal(t, domain.FlowStatePending, flowRun.State)

	// Session in another project cannot be used.
	otherProject, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Other", HostPath: t.TempDir()})
	require.NoError(t, err)
	otherSession, err := (&store.SessionRepo{DB: server.DB}).Create(ctx, domain.CreateSessionInput{
		ProjectID: otherProject.ID, Title: "other",
	})
	require.NoError(t, err)
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/bindings/"+binding.ID+"/run",
		map[string]any{"sessionId": otherSession.ID}, true)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// Run detail + runs list.
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/agent-flows/runs/"+flowRun.RunID, nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/agent-flows/runs", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var runs []domain.RunAgentFlow
	decodeData(t, rec, &runs)
	require.Len(t, runs, 1)
	assert.Equal(t, flowRun.RunID, runs[0].RunID)

	// Cancel is idempotent and terminal runs respond OK.
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/runs/"+flowRun.RunID+"/cancel", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// Terminalize as cancelled (the real orchestrator does this after the
	// child hard-cancel), then resume -> checkpoint continuation.
	_, err = server.AgentFlows.Runs.UpdateFlowState(ctx, flowRun.RunID, domain.FlowStateCancelled, 0, "cancelled by user")
	require.NoError(t, err)
	server.AgentFlows.StartRecovered = func(ctx context.Context, runID string) error { return nil }
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/runs/"+flowRun.RunID+"/resume", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resumed domain.RunAgentFlow
	decodeData(t, rec, &resumed)
	assert.Equal(t, domain.FlowStatePending, resumed.State)

	// Cross-project run access denied.
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+otherProject.ID+"/agent-flows/runs/"+flowRun.RunID, nil, true)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestAgentFlowAPICandidateDiscoveryNoExecution(t *testing.T) {
	_, handler, projects := setupFlowServer(t)
	ctx := context.Background()
	root := t.TempDir()
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Pwn", HostPath: root})
	require.NoError(t, err)
	dir := filepath.Join(root, ".ennote", "agent-flows")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	yaml := `schemaVersion: 1
id: pwn-flow
budget:
  max_total_tokens: 10000
tasks:
  producer:
    role: flow-worker@1
    goal: "should-not-run --pwn"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pwn-flow.yaml"), []byte(yaml), 0o600))

	// Discovery parses only; nothing executes or binds.
	rec := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/agent-flows/candidates", nil, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var candidates []store.AgentFlowCandidate
	decodeData(t, rec, &candidates)
	require.Len(t, candidates, 1)
	assert.Equal(t, "pwn-flow", candidates[0].Slug)
	assert.Equal(t, "should-not-run --pwn", candidates[0].Definition.Tasks["producer"].Goal)
	assert.False(t, candidates[0].AlreadyBound)

	// Binding the candidate validates it first: the referenced role does not
	// exist, so the bind is rejected (fail-loud, nothing materialized).
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/bindings/from-candidate",
		map[string]any{"slug": "pwn-flow"}, true)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "agent_flow_validation_failed")
}

func TestAgentFlowAPICheckApprovals(t *testing.T) {
	server, handler, projects := setupFlowServer(t)
	ctx := context.Background()
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Check", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: server.DB}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "check session",
	})
	require.NoError(t, err)

	// Create a flow run row directly (no role needed for check tasks).
	profile, err := server.AgentFlows.Profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Gate", Slug: "gate", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	def, err := agentflow.ParseDefinition([]byte(`schemaVersion: 1
id: gate
budget:
  max_total_tokens: 10000
tasks:
  gate:
    type: check
    command: "go test ./..."
`))
	require.NoError(t, err)
	version, err := server.AgentFlows.Profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)
	inputsJSON, err := store.NormalizeFlowInputs(def, nil, nil)
	require.NoError(t, err)
	freeze, _, err := server.AgentFlows.Runs.FreezeFlowDefinition(ctx, project.ID, def, inputsJSON)
	require.NoError(t, err)
	run, err := server.AgentFlows.Runs.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: project.ID, FlowVersionID: version.ID, InputsJSON: inputsJSON,
	}, freeze)
	require.NoError(t, err)

	// Create a pending approval.
	err = server.AgentFlows.Checks.CreateCheckApproval(ctx, run.RunID, 0, "go test ./...")
	require.NoError(t, err)
	rec := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/agent-flows/check-approvals", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	var approvals []store.CheckApprovalRow
	decodeData(t, rec, &approvals)
	require.Len(t, approvals, 1)
	assert.Equal(t, run.RunID, approvals[0].RunID)

	// Reject it.
	rec = request(t, handler, http.MethodPost,
		"/v1/projects/"+project.ID+"/agent-flows/check-approvals/"+run.RunID+"/0/decide",
		map[string]any{"approved": false, "clientRequestId": "decide-1"}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	status, err := server.AgentFlows.Checks.CheckApprovalStatus(ctx, run.RunID, 0)
	require.NoError(t, err)
	assert.Equal(t, agentflow.CheckApprovalRejected, status)

	// Pending list is now empty.
	rec = request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/agent-flows/check-approvals", nil, true)
	require.Equal(t, http.StatusOK, rec.Code)
	decodeData(t, rec, &approvals)
	assert.Empty(t, approvals)
}

func TestAgentFlowAPIAuthRequired(t *testing.T) {
	_, handler, _ := setupFlowServer(t)
	rec := request(t, handler, http.MethodGet, "/v1/agent-flows", nil, false)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// Matrix 2E: /invoke_agent_flow resolves name[@version] in enabled bindings,
// fails closed for unbound/disabled/wrong-version flows, and never addresses
// a Room speaker.
func TestAgentFlowAPIInvokeByName(t *testing.T) {
	server, handler, projects := setupFlowServer(t)
	roleRef := publishFixtureRole(t, server)
	ctx := context.Background()
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Invoke", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: server.DB}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "invoke session",
	})
	require.NoError(t, err)

	// Publish + bind + enable the flow.
	yaml := "schemaVersion: 1\nid: invoke-flow\ninputs:\n  target: {type: path, required: true}\noutputs:\n  report: {type: string}\nbudget:\n  max_total_tokens: 120000\ntasks:\n  producer:\n    role: " + roleRef + "\n    goal: \"Implement {inputs.target}\"\n    budget: {tokens: 50000}\n  accept:\n    terminal: {status: success, output: report}\n    output: report\n    depends: [producer]\n"
	rec := request(t, handler, http.MethodPost, "/v1/agent-flows",
		map[string]any{"name": "Invoke Flow", "slug": "invoke-flow"}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var profile domain.AgentFlowProfile
	decodeData(t, rec, &profile)
	rec = request(t, handler, http.MethodPatch, "/v1/agent-flows/"+profile.ID+"/draft",
		map[string]any{"yaml": yaml, "expectedRevision": 0}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	rec = request(t, handler, http.MethodPost, "/v1/agent-flows/"+profile.ID+"/publish",
		map[string]any{"expectedRevision": 1}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var version domain.AgentFlowVersion
	decodeData(t, rec, &version)
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/bindings",
		map[string]any{"flowVersionId": version.ID, "desiredEnabled": true}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	server.AgentFlows.StartRun = func(ctx context.Context, projectID, flowVersionID, sessionID string,
		inputs, vars map[string]any) (*domain.RunAgentFlow, error) {
		v, err := server.AgentFlows.Profiles.GetVersion(ctx, flowVersionID)
		if err != nil {
			return nil, err
		}
		var def domain.FlowDefinition
		require.NoError(t, json.Unmarshal(v.DefinitionJSON, &def))
		inputsJSON, err := store.NormalizeFlowInputs(&def, inputs, vars)
		if err != nil {
			return nil, err
		}
		freeze, _, err := server.AgentFlows.Runs.FreezeFlowDefinition(ctx, projectID, &def, inputsJSON)
		if err != nil {
			return nil, err
		}
		return server.AgentFlows.Runs.CreateFlowRun(ctx, store.CreateFlowRunInput{
			SessionID: sessionID, ProjectID: projectID, FlowVersionID: flowVersionID, InputsJSON: inputsJSON,
		}, freeze)
	}

	// Invoke by name resolves the enabled binding.
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/invoke",
		map[string]any{"sessionId": session.ID, "name": "invoke-flow", "inputs": map[string]any{"target": "src/a.go"}}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var flowRun domain.RunAgentFlow
	decodeData(t, rec, &flowRun)
	assert.Equal(t, version.ID, flowRun.FlowVersionID)

	// Invoke by name@version resolves only that version (a fresh session: one
	// flow anchor per session is active at a time).
	session2, err := (&store.SessionRepo{DB: server.DB}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "invoke session 2",
	})
	require.NoError(t, err)
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/invoke",
		map[string]any{"sessionId": session2.ID, "name": "invoke-flow", "version": 1,
			"inputs": map[string]any{"target": "src/b.go"}}, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	decodeData(t, rec, &flowRun)
	assert.Equal(t, version.ID, flowRun.FlowVersionID)

	// Fail-closed: unbound flow.
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/invoke",
		map[string]any{"sessionId": session2.ID, "name": "ghost-flow"}, true)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "agent_flow_invoke_not_found")

	// Fail-closed: wrong version (only v1 exists).
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/invoke",
		map[string]any{"sessionId": session2.ID, "name": "invoke-flow", "version": 99}, true)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	// Fail-closed: disabled binding is not invocable.
	_, err = server.AgentFlows.Bindings.Update(ctx, bindingIDFor(t, server, project.ID, version.ID), false)
	require.NoError(t, err)
	rec = request(t, handler, http.MethodPost, "/v1/projects/"+project.ID+"/agent-flows/invoke",
		map[string]any{"sessionId": session2.ID, "name": "invoke-flow"}, true)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func bindingIDFor(t *testing.T, server *Server, projectID, versionID string) string {
	t.Helper()
	bindings, err := server.AgentFlows.Bindings.ListByProject(context.Background(), projectID)
	require.NoError(t, err)
	for _, binding := range bindings {
		if binding.FlowVersionID == versionID {
			return binding.ID
		}
	}
	t.Fatal("binding not found")
	return ""
}
