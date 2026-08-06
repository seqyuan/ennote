package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupFlowFixture publishes one role (auto_within_budget) in a fresh project
// and returns the repos, project, model, and role version id.
func setupFlowFixture(t *testing.T) (*sql.DB, *store.ProjectRepo, *store.RoleRepo, *store.AgentFlowProfileRepo,
	*store.AgentFlowBindingRepo, *store.AgentFlowRunRepo, string, string, string) {
	t.Helper()
	db := store.SetupDB(t)
	ctx := context.Background()
	projects := &store.ProjectRepo{DB: db}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Flows", HostPath: t.TempDir()})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test", CredentialRef: "env:FLOW_TEST_KEY",
	})
	require.NoError(t, err)
	model, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "flow-model", ContextWindow: 32000, MaxOutputTokens: 2048,
		SupportsToolUse: true, SupportsThinking: true,
		ThinkingDialect:            domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts:   []domain.ThinkingEffort{domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium},
	})
	require.NoError(t, err)
	roles := &store.RoleRepo{DB: db, KnownTools: map[string]bool{"read": true, "grep": true}}
	roleDef := validRoleDefinition(model.ID)
	roleDef.DelegationPolicy.Admission = domain.DelegationAutoWithinBudget
	roleDef.DelegationPolicy.AllowedCallerKinds = []string{"host"}
	roleDef.DelegationPolicy.BudgetCeiling.MaxTotalTokens = 200000
	roleDef.DelegationPolicy.BudgetCeiling.MaxWallTimeMS = 1_800_000
	role, err := roles.Create(ctx, store.CreateRoleInput{
		Handle: "flow-worker", Name: "Flow Worker", Description: "Flow task worker",
		Positioning: "Executes one task in a flow.", Icon: "bot", Color: "neutral",
		Scope: domain.RoleScopeProject, ProjectID: &project.ID, Definition: roleDef,
	})
	require.NoError(t, err)
	version, err := roles.Publish(ctx, role.ID, 0)
	require.NoError(t, err)
	return db, projects, roles,
		&store.AgentFlowProfileRepo{DB: db}, &store.AgentFlowBindingRepo{DB: db},
		&store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}},
		project.ID, model.ID, version.ID
}

func flowYAML(id string, extra string) string {
	return `schemaVersion: 1
id: ` + id + `
description: test flow
inputs:
  target: {type: path, required: true}
outputs:
  report: {type: string}
budget:
  max_total_tokens: 200000
tasks:
  producer:
    role: flow-worker@1
    skills: [go-dev]
    goal: "Implement {inputs.target}"
    budget: {tokens: 50000}
  reviewer:
    role: flow-worker@1
    goal: "Review {task.producer.output.changed_files}"
    depends: [producer]
  accept:
    terminal: {status: success, output: report}
    output: report
    depends: [reviewer]
` + extra
}

func TestAgentFlowProfilePublishImmutableVersions(t *testing.T) {
	_, _, _, profiles, _, _, _, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Go Change Review", Slug: "go-change-review", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, profile.LatestVersion)

	def, err := agentflow.ParseDefinition([]byte(flowYAML("go-change-review", "")))
	require.NoError(t, err)
	version1, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)
	assert.Equal(t, 1, version1.Version)
	assert.NotEmpty(t, version1.ConfigDigest)

	// Identical definition reuses the existing immutable version (digest hit).
	same, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.Error(t, err)
	assert.Nil(t, same)

	// Changed definition -> new immutable version.
	changed, err := agentflow.ParseDefinition([]byte(flowYAML("go-change-review", "\n  fixer:\n    role: flow-worker@1\n    depends: [reviewer]\n")))
	require.NoError(t, err)
	version2, err := profiles.CreateVersion(ctx, profile.ID, changed)
	require.NoError(t, err)
	assert.Equal(t, 2, version2.Version)
	assert.NotEqual(t, version1.ConfigDigest, version2.ConfigDigest)

	versions, err := profiles.ListVersions(ctx, profile.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)

	// config_digest is stable and reproducible from the same YAML.
	reparsed, err := agentflow.ParseDefinition([]byte(flowYAML("go-change-review", "\n  fixer:\n    role: flow-worker@1\n    depends: [reviewer]\n")))
	require.NoError(t, err)
	digest, err := agentflow.ConfigDigest(reparsed)
	require.NoError(t, err)
	assert.Equal(t, version2.ConfigDigest, digest)

	found, err := profiles.FindVersionByDigest(ctx, profile.ID, version1.ConfigDigest)
	require.NoError(t, err)
	assert.Equal(t, version1.ID, found.ID)
}

func TestAgentFlowBindingLifecycle(t *testing.T) {
	_, _, _, profiles, bindings, _, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Review", Slug: "review", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	def, err := agentflow.ParseDefinition([]byte(flowYAML("review", "")))
	require.NoError(t, err)
	version, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)

	// Ensure + enable.
	b, err := bindings.EnsureBindingExists(ctx, projectID, version.ID)
	require.NoError(t, err)
	assert.False(t, b.DesiredEnabled)
	updated, err := bindings.Update(ctx, b.ID, true)
	require.NoError(t, err)
	assert.True(t, updated.DesiredEnabled)
	assert.Equal(t, 2, updated.Revision)

	// Same pair returns the existing binding (no duplicate).
	same, err := bindings.EnsureBindingExists(ctx, projectID, version.ID)
	require.NoError(t, err)
	assert.Equal(t, b.ID, same.ID)

	listed, err := bindings.ListByProject(ctx, projectID)
	require.NoError(t, err)
	require.Len(t, listed, 1)

	// Another project cannot see this project's binding.
	otherProject, _, err := (&store.ProjectRepo{DB: profiles.DB}).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Other", HostPath: t.TempDir()})
	require.NoError(t, err)
	otherListed, err := bindings.ListByProject(ctx, otherProject.ID)
	require.NoError(t, err)
	assert.Empty(t, otherListed)

	require.NoError(t, bindings.Delete(ctx, b.ID))
	_, err = bindings.Get(ctx, b.ID)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestAgentFlowRunFreezeAndNodes(t *testing.T) {
	db, _, _, profiles, _, runs, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Review", Slug: "review", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	def, err := agentflow.ParseDefinition([]byte(flowYAML("review", "")))
	require.NoError(t, err)
	version, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)

	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: projectID, Title: "flow session",
	})
	require.NoError(t, err)

	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "src/main.go"}, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := runs.FreezeFlowDefinition(ctx, projectID, def, inputs)
	require.NoError(t, err, diagnostics)
	require.Len(t, freeze, 3) // producer, reviewer, accept

	flowRun, err := runs.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)
	assert.Equal(t, domain.FlowStatePending, flowRun.State)
	assert.NotEmpty(t, flowRun.ManifestDigest)

	// Anchor run exists, is a top-level host agent run owned by the orchestrator.
	var anchorKind, anchorStatus string
	var anchorDepth int
	require.NoError(t, db.QueryRow(`SELECT run_kind,status,execution_depth FROM agent_runs WHERE id=?`,
		flowRun.RunID).Scan(&anchorKind, &anchorStatus, &anchorDepth))
	assert.Equal(t, "agent", anchorKind)
	assert.Equal(t, "running", anchorStatus)
	assert.Equal(t, 0, anchorDepth)

	nodes, err := runs.ListNodes(ctx, flowRun.RunID)
	require.NoError(t, err)
	require.Len(t, nodes, 3)
	assert.Equal(t, "producer", nodes[0].Handle)
	assert.Equal(t, domain.FlowNodePending, nodes[0].TerminalState)
	assert.NotEmpty(t, nodes[0].RoleVersionID)
	assert.Equal(t, "skill-go-dev", nodes[0].SkillDigests[0])
	assert.NotEmpty(t, nodes[0].GoalDigest)

	// Task index order is topological: reviewer (depends producer) comes later.
	assert.Equal(t, 0, nodes[0].TaskIndex)
	assert.Equal(t, 1, nodes[1].TaskIndex)

	// Terminal (check-less) accept task has no role binding.
	assert.Empty(t, nodes[2].RoleVersionID)

	// CAS update: pending -> running with child run id.
	childID := "flow-child-1"
	updated, err := runs.UpdateNode(ctx, flowRun.RunID, store.NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: childID,
	})
	require.NoError(t, err)
	assert.Equal(t, domain.FlowNodeRunning, updated.TerminalState)
	assert.Equal(t, childID, updated.ChildRunID)

	// Conflicting CAS fails.
	_, err = runs.UpdateNode(ctx, flowRun.RunID, store.NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning,
	})
	assert.ErrorIs(t, err, store.ErrFlowNodeStateConflict)

	// Checkpoint: completed with output ref.
	_, err = runs.UpdateNode(ctx, flowRun.RunID, store.NodeUpdate{
		TaskIndex: 0, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeCompleted, OutputRef: json.RawMessage(`{"changedFiles":["a.go"]}`),
	})
	require.NoError(t, err)
	done, err := runs.GetNode(ctx, flowRun.RunID, 0)
	require.NoError(t, err)
	assert.Equal(t, domain.FlowNodeCompleted, done.TerminalState)
	assert.JSONEq(t, `{"changedFiles":["a.go"]}`, string(done.OutputRef))

	// Budget accumulation + terminal transition.
	total, err := runs.AddTokenUsage(ctx, flowRun.RunID, 1234)
	require.NoError(t, err)
	assert.Equal(t, int64(1234), total)
	finalRun, err := runs.UpdateFlowState(ctx, flowRun.RunID, domain.FlowStateCompleted, total, "")
	require.NoError(t, err)
	assert.Equal(t, domain.FlowStateCompleted, finalRun.State)
	assert.NotNil(t, finalRun.FinishedAt)
}

func TestAgentFlowRunFreezeRejectsMissingRequiredInput(t *testing.T) {
	_, _, _, profiles, _, runs, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Review", Slug: "review", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	def, err := agentflow.ParseDefinition([]byte(flowYAML("review", "")))
	require.NoError(t, err)
	_, err = profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)

	inputs, err := store.NormalizeFlowInputs(def, map[string]any{}, nil)
	require.NoError(t, err)
	_, diagnostics, err := runs.FreezeFlowDefinition(ctx, projectID, def, inputs)
	require.Error(t, err)
	require.Contains(t, diagnostics[0], `input "target" is required`)

	// Unknown input names are rejected.
	_, err = store.NormalizeFlowInputs(def, map[string]any{"nope": "x"}, nil)
	require.Error(t, err)
}

func TestAgentFlowCandidateDiscoveryNoExecution(t *testing.T) {
	_, projects, _, profiles, bindings, runs, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()

	// Write a project file whose goal text contains a would-be dangerous
	// command. Discovery must only parse it: no execution, no binding.
	root := t.TempDir()
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
	_, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Pwn", HostPath: root})
	require.NoError(t, err)

	discovery := &store.AgentFlowDiscovery{Profiles: profiles}
	candidates, err := discovery.DiscoverCandidates(ctx, root, projectID)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, "pwn-flow", candidates[0].Slug)
	assert.Equal(t, "should-not-run --pwn", candidates[0].Definition.Tasks["producer"].Goal)
	assert.False(t, candidates[0].AlreadyBound)
	assert.Empty(t, candidates[0].ParseError)
	// Nothing was materialized by discovery.
	assert.Empty(t, mustListProfiles(t, profiles, projectID))

	// Bind the candidate: materializes a project_file profile + immutable version.
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "pwn-flow", Slug: "pwn-flow", SourceKind: domain.FlowSourceProjectFile, ProjectScope: &projectID,
		SourceLocator: filepath.Join(dir, "pwn-flow.yaml"),
	})
	require.NoError(t, err)
	def, err := agentflow.ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	version, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)
	binding, err := bindings.EnsureBindingExists(ctx, projectID, version.ID)
	require.NoError(t, err)
	assert.False(t, binding.DesiredEnabled)

	// Re-discovery marks the candidate bound with no update.
	candidates, err = discovery.DiscoverCandidates(ctx, root, projectID)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.True(t, candidates[0].AlreadyBound)
	assert.Equal(t, version.ID, candidates[0].BoundVersionID)
	assert.False(t, candidates[0].UpdateAvailable)

	// Project file change -> update available (new digest, same bound version).
	changed := `schemaVersion: 1
id: pwn-flow
budget:
  max_total_tokens: 10000
tasks:
  producer:
    role: flow-worker@1
    goal: "should-not-run --pwn --new-flag"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pwn-flow.yaml"), []byte(changed), 0o600))
	candidates, err = discovery.DiscoverCandidates(ctx, root, projectID)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.True(t, candidates[0].AlreadyBound)
	assert.Equal(t, version.ID, candidates[0].BoundVersionID)
	assert.True(t, candidates[0].UpdateAvailable)

	// Invalid YAML surfaces per-file as a parse error, never a hard failure.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("schemaVersion: [oops"), 0o600))
	candidates, err = discovery.DiscoverCandidates(ctx, root, projectID)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	// The broken file failed to parse: no slug; the parse error is surfaced.
	broken := candidates[0]
	assert.Contains(t, broken.SourceLocator, "broken.yaml")
	assert.NotEmpty(t, broken.ParseError)
	assert.Empty(t, broken.Slug)
	_ = runs
}

func TestAgentFlowSameSlugTwoProjects(t *testing.T) {
	_, projects, _, profiles, _, _, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	otherProject, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Other", HostPath: t.TempDir()})
	require.NoError(t, err)

	for _, project := range []string{projectID, otherProject.ID} {
		profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
			Name: "Shared", Slug: "shared-flow", SourceKind: domain.FlowSourceProjectFile,
			ProjectScope: &project,
		})
		require.NoError(t, err)
		def, err := agentflow.ParseDefinition([]byte(flowYAML("shared-flow", "")))
		require.NoError(t, err)
		version, err := profiles.CreateVersion(ctx, profile.ID, def)
		require.NoError(t, err)
		assert.Equal(t, 1, version.Version)
	}
	// Two independent profiles exist with the same slug (per-project scope).
	first, err := profiles.FindProfileBySource(ctx, "shared-flow", domain.FlowSourceProjectFile, &projectID)
	require.NoError(t, err)
	second, err := profiles.FindProfileBySource(ctx, "shared-flow", domain.FlowSourceProjectFile, &otherProject.ID)
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
}

func TestAgentFlowRoleVersionResolutionAndAdmission(t *testing.T) {
	db, _, roles, profiles, _, runs, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	versionID, defJSON, err := runs.ResolveFlowRoleVersion(ctx, "flow-worker@1", projectID)
	require.NoError(t, err)
	require.NotEmpty(t, versionID)
	var roleDef domain.RoleDefinition
	require.NoError(t, json.Unmarshal(defJSON, &roleDef))
	assert.Equal(t, domain.DelegationAutoWithinBudget, roleDef.DelegationPolicy.Admission)

	// Wrong version fails.
	_, _, err = runs.ResolveFlowRoleVersion(ctx, "flow-worker@2", projectID)
	require.Error(t, err)
	// Unknown role fails.
	_, _, err = runs.ResolveFlowRoleVersion(ctx, "nope@1", projectID)
	require.Error(t, err)
	// Missing @version fails.
	_, _, err = runs.ResolveFlowRoleVersion(ctx, "flow-worker", projectID)
	require.Error(t, err)

	// A Role that denies delegation rejects the flow freeze.
	deniedDef := validRoleDefinition(mustGetModelID(t, db))
	deniedDef.DelegationPolicy.Admission = domain.DelegationDenied
	deniedRole, err := roles.Create(ctx, store.CreateRoleInput{
		Handle: "denied-worker", Name: "Denied", Description: "",
		Positioning: "", Icon: "bot", Color: "neutral",
		Scope: domain.RoleScopeProject, ProjectID: &projectID, Definition: deniedDef,
	})
	require.NoError(t, err)
	_, err = roles.Publish(ctx, deniedRole.ID, 0)
	require.NoError(t, err)
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Denied", Slug: "denied", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	yaml := `schemaVersion: 1
id: denied
budget:
  max_total_tokens: 10000
tasks:
  producer:
    role: denied-worker@1
    goal: "do it"
`
	def, err := agentflow.ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	version, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)
	inputs, err := store.NormalizeFlowInputs(def, nil, nil)
	require.NoError(t, err)
	_, diagnostics, err := runs.FreezeFlowDefinition(ctx, projectID, def, inputs)
	require.Error(t, err)
	require.Contains(t, diagnostics[0], "denies Host delegation")
	_ = version
}

func TestAgentFlowFreezeRejectsUnknownSkill(t *testing.T) {
	_, _, _, profiles, _, runs, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Skill", Slug: "skill", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	yaml := `schemaVersion: 1
id: skill
budget:
  max_total_tokens: 10000
tasks:
  producer:
    role: flow-worker@1
    skills: [no-such-skill]
    goal: "do it"
`
	def, err := agentflow.ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	version, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)
	inputs, err := store.NormalizeFlowInputs(def, nil, nil)
	require.NoError(t, err)
	_, diagnostics, err := runs.FreezeFlowDefinition(ctx, projectID, def, inputs)
	require.Error(t, err)
	require.Contains(t, diagnostics[0], `skill "no-such-skill" is not in the catalog`)
	_ = version
}

func mustListProfiles(t *testing.T, profiles *store.AgentFlowProfileRepo, projectID string) []*domain.AgentFlowProfile {
	t.Helper()
	all, err := profiles.ListProfiles(context.Background())
	require.NoError(t, err)
	var visible []*domain.AgentFlowProfile
	for _, p := range all {
		if p.SourceKind == domain.FlowSourceManaged ||
			(p.ProjectScope != nil && *p.ProjectScope == projectID) {
			visible = append(visible, p)
		}
	}
	return visible
}

func mustGetModelID(t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	require.NoError(t, db.QueryRow(`SELECT id FROM model_profiles LIMIT 1`).Scan(&id))
	return id
}

func TestAgentFlowDraftPublishFlow(t *testing.T) {
	db, _, _, profiles, _, _, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Review", Slug: "review", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)

	// The fixture's flow-worker role is project-scoped, so a managed (global)
	// flow cannot reference it: publish validation fails loudly.
	yaml := flowYAML("review", "")
	def, err := agentflow.ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	updated, err := profiles.UpdateDraft(ctx, profile.ID, def, yaml, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, updated.DraftRevision)
	// Stale revision conflicts.
	_, err = profiles.UpdateDraft(ctx, profile.ID, def, yaml, 0)
	assert.ErrorIs(t, err, store.ErrFlowDraftConflict)

	validator := store.NewFlowValidator(store.FlowPublishOptions{
		DB: db, Skills: map[string]bool{"go-dev": true},
		CheckAllowlist: []string{"go", "python3"},
	})
	result, err := profiles.ValidateDraft(ctx, profile.ID, validator)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.True(t, hasCodeIn(result.Diagnostics, "role_not_found"))

	// Publish also refuses.
	_, err = profiles.Publish(ctx, profile.ID, 1, validator)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrFlowValidation)

	// Publish a GLOBAL role reference so the draft becomes valid.
	globalYAML := `schemaVersion: 1
id: review
budget:
  max_total_tokens: 10000
tasks:
  producer:
    role: flow-worker@1
    goal: "do it"
`
	_ = globalYAML
	// Create a global role with the same handle.
	roleDef := validRoleDefinition(mustGetModelID(t, db))
	roleDef.DelegationPolicy.Admission = domain.DelegationAutoWithinBudget
	roleDef.DelegationPolicy.AllowedCallerKinds = []string{"host"}
	roleDef.DelegationPolicy.BudgetCeiling.MaxTotalTokens = 100000
	globalRole, err := (&store.RoleRepo{DB: db, KnownTools: map[string]bool{"read": true, "grep": true}}).Create(ctx, store.CreateRoleInput{
		Handle: "flow-worker", Name: "Flow Worker Global", Description: "",
		Positioning: "", Icon: "bot", Color: "neutral",
		Scope: domain.RoleScopeGlobal, Definition: roleDef,
	})
	require.NoError(t, err)
	_, err = (&store.RoleRepo{DB: db, KnownTools: map[string]bool{"read": true, "grep": true}}).Publish(ctx, globalRole.ID, 0)
	require.NoError(t, err)

	validYAML := flowYAML("review", "")
	validDef, err := agentflow.ParseDefinition([]byte(validYAML))
	require.NoError(t, err)
	_, err = profiles.UpdateDraft(ctx, profile.ID, validDef, validYAML, 1)
	require.NoError(t, err)
	result, err = profiles.ValidateDraft(ctx, profile.ID, validator)
	require.NoError(t, err)
	assert.True(t, result.Valid, "%v", result.Diagnostics)

	version, err := profiles.Publish(ctx, profile.ID, 2, validator)
	require.NoError(t, err)
	assert.Equal(t, 1, version.Version)
	assert.Equal(t, result.ConfigDigest, version.ConfigDigest)
	_ = projectID
}

func hasCodeIn(diags []agentflow.ValidationDiagnostic, code string) bool {
	for _, d := range diags {
		if d.Code == code {
			return true
		}
	}
	return false
}
