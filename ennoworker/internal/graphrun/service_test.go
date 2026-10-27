package graphrun_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceFreezesFileNativeGraphRun(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-graph-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "graph-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)

	sources := &globalsource.Store{HomeDir: home}
	roleDoc := &rolesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer", Description: "Reviews output",
		Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: rolesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Use the published prompt.",
	}
	_, _, err = sources.CreateRole(roleDoc)
	require.NoError(t, err)
	roleRevision, err := sources.PublishRoleRevision(roleDoc.Handle)
	require.NoError(t, err)

	_, digest, err := sources.CreateGraph("pipeline", "Pipeline")
	require.NoError(t, err)
	_, _, err = sources.UpdateGraph("pipeline", digest, func(document *graphsource.Document) error {
		document.Tasks = map[string]graphsource.Task{
			"produce": {Name: "Produce", Model: model.ID, Goal: "Produce an artifact listing for {inputs.target}", Budget: &graphsource.TaskBudget{Tokens: 1000}},
			"review":  {Name: "Review", Role: "global/reviewer", Goal: "Review the produced artifact and submit a verdict"},
		}
		document.Graph = map[string][]string{
			"produce": {},
			"review":  {"produce"},
		}
		return nil
	})
	require.NoError(t, err)
	graphRevision, err := sources.PublishGraphRevision("pipeline")
	require.NoError(t, err)

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)

	var startedDB *sql.DB
	var startedRunID string
	service := &graphrun.Service{
		Sources: sources, Models: &store.ModelRepo{Files: models}, Sessions: sessions,
		OnRunStarted: func(ctx context.Context, db *sql.DB, sessionPath, runID string) error {
			startedDB = db
			startedRunID = runID
			return nil
		},
	}
	run, err := service.Start(ctx, project.ID, "pipeline", 0, session.ID,
		map[string]any{"target": "src/main.go"}, nil)
	require.NoError(t, err)
	assert.Equal(t, graphrun.FlowVersionID("pipeline", graphRevision.ID()), run.FlowVersionID)
	assert.Equal(t, domain.FlowStatePending, run.State)
	require.NotNil(t, startedDB)
	assert.Equal(t, run.RunID, startedRunID)

	// The frozen execution plan lives in the owning Session database.
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	var definitionJSON, configDigest string
	require.NoError(t, db.QueryRow(`SELECT definition_json, config_digest FROM run_agent_flow WHERE run_id=?`, run.RunID).
		Scan(&definitionJSON, &configDigest))
	assert.Equal(t, graphRevision.Digest, configDigest)
	var def domain.FlowDefinition
	require.NoError(t, json.Unmarshal([]byte(definitionJSON), &def))
	require.Contains(t, def.Tasks, "produce")
	require.Contains(t, def.Tasks, "review")

	// Every node froze its full Role definition (inline + global revision).
	var handle, roleVersionID, roleDefJSON, skillJSON string
	require.NoError(t, db.QueryRow(`SELECT handle, role_version_id, role_definition_json, skill_digests_json
		FROM run_agent_flow_nodes WHERE run_id=? AND task_index=0`, run.RunID).
		Scan(&handle, &roleVersionID, &roleDefJSON, &skillJSON))
	assert.Equal(t, "produce", handle)
	assert.Contains(t, roleVersionID, "inline:produce@")
	var inlineDef domain.RoleDefinition
	require.NoError(t, json.Unmarshal([]byte(roleDefJSON), &inlineDef))
	assert.Equal(t, model.ID, inlineDef.ModelBinding.ModelProfileID)
	assert.Equal(t, `{"schemaVersion":2,"skills":[]}`, skillJSON)

	require.NoError(t, db.QueryRow(`SELECT handle, role_version_id, role_definition_json FROM run_agent_flow_nodes
		WHERE run_id=? AND task_index=1`, run.RunID).
		Scan(&handle, &roleVersionID, &roleDefJSON))
	assert.Equal(t, "review", handle)
	assert.Equal(t, "reviewer@"+roleRevision.ID(), roleVersionID)
	var reviewDef domain.RoleDefinition
	require.NoError(t, json.Unmarshal([]byte(roleDefJSON), &reviewDef))
	assert.Equal(t, domain.RoleAuthorityReadOnly, reviewDef.Authority)

	// The orchestrator flow store resolves the definition from the frozen row.
	version, err := (&store.OrchestratorStore{Runs: &store.AgentFlowRunRepo{DB: db}}).
		GetVersion(ctx, run.FlowVersionID)
	require.NoError(t, err)
	assert.Equal(t, graphRevision.Digest, version.ConfigDigest)
	manifest, err := graphrun.ManifestDigest(version.ConfigDigest, run.InputsJSON)
	require.NoError(t, err)
	assert.Equal(t, run.ManifestDigest, manifest)
}

func TestServiceFreezesGraphLocalRole(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-local-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "local-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	sources := &globalsource.Store{HomeDir: home}
	_, _, err = sources.CreateGraph("pipeline", "Pipeline")
	require.NoError(t, err)
	localRole := &rolesource.Document{
		SchemaVersion: 1, Handle: "local_reviewer", Name: "Local Reviewer",
		Positioning: "Review", Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: rolesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 4, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Local prompt.",
	}
	if err := os.MkdirAll(filepath.Join(home, "agents", "graphs", "pipeline", "roles", "local_reviewer"), 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := rolesource.Encode(localRole)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, "agents", "graphs", "pipeline", "roles", "local_reviewer", "role.md"), encoded, 0o600))

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)

	// Add a task referencing the graph-local role, then publish a revision.
	current, _, err := sources.ReadGraph("pipeline")
	require.NoError(t, err)
	currentDigest, err := graphsource.SourceDigest(current)
	require.NoError(t, err)
	_, _, err = sources.UpdateGraph("pipeline", currentDigest, func(document *graphsource.Document) error {
		document.Tasks = map[string]graphsource.Task{
			"local_check": {Name: "Local Check", Role: "local/local_reviewer", Goal: "Verify locally"},
		}
		document.Graph = map[string][]string{"local_check": {}}
		return nil
	})
	require.NoError(t, err)
	revision, err := sources.PublishGraphRevision("pipeline")
	require.NoError(t, err)

	service := &graphrun.Service{Sources: sources, Models: &store.ModelRepo{Files: models}, Sessions: sessions}
	run, err := service.Start(ctx, project.ID, "pipeline", 0, session.ID, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, graphrun.FlowVersionID("pipeline", revision.ID()), run.FlowVersionID)

	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	var handle, roleVersionID, roleDefJSON string
	require.NoError(t, db.QueryRow(`SELECT handle, role_version_id, role_definition_json FROM run_agent_flow_nodes
		WHERE run_id=? AND task_index=0`, run.RunID).Scan(&handle, &roleVersionID, &roleDefJSON))
	assert.Equal(t, "local_check", handle)
	assert.Contains(t, roleVersionID, "graph:pipeline/local_reviewer@sha256:")
	var def domain.RoleDefinition
	require.NoError(t, json.Unmarshal([]byte(roleDefJSON), &def))
	assert.Equal(t, "Local prompt.", def.RolePrompt)
	assert.Equal(t, model.ID, def.ModelBinding.ModelProfileID)
}

// skillGraphFixture publishes a one-task graph whose inline task requests one
// global Skill, so the freeze path under test resolves a real catalog digest.
// (Graph documents reject duplicate Skill references at publish time, so the
// duplicate-dedupe invariant is exercised by the store/delegation tests.)
type skillGraphFixture struct {
	service   *graphrun.Service
	sessions  *sessionstore.Manager
	projectID string
	sessionID string
	digest    string
}

func newSkillGraphFixture(t *testing.T, globalSkills map[string]string) *skillGraphFixture {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-skill-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "skill-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)

	sources := &globalsource.Store{HomeDir: home}
	_, _, err = sources.CreateGraph("pinned", "Pinned")
	require.NoError(t, err)
	_, _, err = sources.UpdateGraph("pinned", mustGraphDigest(t, sources), func(document *graphsource.Document) error {
		document.Tasks = map[string]graphsource.Task{
			"build": {Name: "Build", Model: model.ID, Goal: "Build it",
				Skills: []string{"global/go-dev"}},
		}
		document.Graph = map[string][]string{"build": {}}
		return nil
	})
	require.NoError(t, err)
	_, err = sources.PublishGraphRevision("pinned")
	require.NoError(t, err)

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)

	return &skillGraphFixture{
		service: &graphrun.Service{Sources: sources, Models: &store.ModelRepo{Files: models},
			Sessions: sessions, GlobalSkills: globalSkills},
		sessions: sessions, projectID: project.ID, sessionID: session.ID,
	}
}

func mustGraphDigest(t *testing.T, sources *globalsource.Store) string {
	t.Helper()
	current, _, err := sources.ReadGraph("pinned")
	require.NoError(t, err)
	digest, err := graphsource.SourceDigest(current)
	require.NoError(t, err)
	return digest
}

func TestServiceFreezesGlobalTaskSkillContentDigest(t *testing.T) {
	ctx := context.Background()
	skillsDir := t.TempDir()
	skillDir := filepath.Join(skillsDir, "go-dev")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.json"),
		[]byte(`{"id":"go-dev","version":"1","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("GO_DEV_BODY"), 0o600))
	loaded, err := skills.Load(skillDir)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.ContentHash)

	fixture := newSkillGraphFixture(t, map[string]string{"go-dev": loaded.ContentHash})
	run, err := fixture.service.Start(ctx, fixture.projectID, "pinned", 0, fixture.sessionID, nil, nil)
	require.NoError(t, err)

	db, err := fixture.sessions.OpenSession(ctx, fixture.sessionID)
	require.NoError(t, err)
	var skillJSON string
	require.NoError(t, db.QueryRow(`SELECT skill_digests_json FROM run_agent_flow_nodes
		WHERE run_id=? AND task_index=0`, run.RunID).Scan(&skillJSON))
	refs, err := domain.DecodeFrozenSkills([]byte(skillJSON))
	require.NoError(t, err)
	// The loader's own content hash, not a second invented digest.
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "go-dev", Digest: loaded.ContentHash}}, refs)

	node, err := (&store.AgentFlowRunRepo{DB: db}).GetNode(ctx, run.RunID, 0)
	require.NoError(t, err)
	assert.Equal(t, refs, node.FrozenSkills)
	assert.Equal(t, []string{"go-dev"}, node.SkillDigests, "the public ID list stays stable")
}

func TestServiceRejectsUnresolvedGlobalTaskSkill(t *testing.T) {
	fixture := newSkillGraphFixture(t, map[string]string{})
	_, err := fixture.service.Start(context.Background(), fixture.projectID, "pinned", 0, fixture.sessionID, nil, nil)
	require.ErrorContains(t, err, `global Skill "go-dev" is not available`)
}

func TestServiceRejectsGlobalTaskSkillWithoutDigest(t *testing.T) {
	fixture := newSkillGraphFixture(t, map[string]string{"go-dev": ""})
	_, err := fixture.service.Start(context.Background(), fixture.projectID, "pinned", 0, fixture.sessionID, nil, nil)
	require.ErrorContains(t, err, "refusing to freeze an unpinned Skill")
}

// writeCatalogSkill materializes one Skill directory under skillsDir and
// returns the loader content digest the catalog must report for it.
func writeCatalogSkill(t *testing.T, skillsDir, id, body string) string {
	t.Helper()
	dir := filepath.Join(skillsDir, id)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"id":"`+id+`","version":"1","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600))
	loaded, err := skills.Load(dir)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.ContentHash)
	return loaded.ContentHash
}

// newSession creates another Session in the fixture project (a Session admits
// one active flow Run at a time, so successive freezes need their own Session).
func (f *skillGraphFixture) newSession(t *testing.T, title string) string {
	t.Helper()
	session, err := f.sessions.Create(context.Background(),
		domain.CreateSessionInput{ProjectID: f.projectID, Title: title})
	require.NoError(t, err)
	return session.ID
}

// frozenTaskSkillRefs reads back the Skill refs frozen for task_index 0.
func frozenTaskSkillRefs(t *testing.T, fixture *skillGraphFixture, sessionID, runID string) domain.FrozenSkillRefs {
	t.Helper()
	db, err := fixture.sessions.OpenSession(context.Background(), sessionID)
	require.NoError(t, err)
	var skillJSON string
	require.NoError(t, db.QueryRow(`SELECT skill_digests_json FROM run_agent_flow_nodes
		WHERE run_id=? AND task_index=0`, runID).Scan(&skillJSON))
	refs, err := domain.DecodeFrozenSkills([]byte(skillJSON))
	require.NoError(t, err)
	return refs
}

// A live SkillCatalog resolver must be read at freeze time, not at Service
// construction time: a Skill created after the Service exists is usable, and a
// later edit is picked up by the next Run without restarting the Worker.
func TestServiceResolvesGlobalTaskSkillFromLiveCatalog(t *testing.T) {
	ctx := context.Background()
	skillsDir := t.TempDir()
	fixture := newSkillGraphFixture(t, nil)
	fixture.service.SkillCatalog = func() map[string]string {
		return skills.DigestCatalog(skillsDir)
	}

	// Nothing on disk yet: the live catalog is empty, so the freeze fails
	// loudly instead of freezing an unpinned Skill.
	_, err := fixture.service.Start(ctx, fixture.projectID, "pinned", 0, fixture.sessionID, nil, nil)
	require.ErrorContains(t, err, `global Skill "go-dev" is not available`)

	// Create the Skill *after* the Service already exists.
	firstSession := fixture.newSession(t, "before-edit")
	firstDigest := writeCatalogSkill(t, skillsDir, "go-dev", "GO_DEV_BODY_V1")
	firstRun, err := fixture.service.Start(ctx, fixture.projectID, "pinned", 0, firstSession, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "go-dev", Digest: firstDigest}},
		frozenTaskSkillRefs(t, fixture, firstSession, firstRun.RunID))

	// Edit the Skill and start another Run: the new freeze must see the new
	// content digest, not the one observed by the first Run.
	secondSession := fixture.newSession(t, "after-edit")
	secondDigest := writeCatalogSkill(t, skillsDir, "go-dev", "GO_DEV_BODY_V2")
	require.NotEqual(t, firstDigest, secondDigest)
	secondRun, err := fixture.service.Start(ctx, fixture.projectID, "pinned", 0, secondSession, nil, nil)
	require.NoError(t, err)
	refs := frozenTaskSkillRefs(t, fixture, secondSession, secondRun.RunID)
	assert.Equal(t, domain.FrozenSkillRefs{{ID: "go-dev", Digest: secondDigest}}, refs)
	assert.NotEqual(t, firstDigest, refs[0].Digest)

	// The first Run keeps its pinned digest; freezing is per Run.
	assert.Equal(t, firstDigest, frozenTaskSkillRefs(t, fixture, firstSession, firstRun.RunID)[0].Digest)
}

// A live catalog that no longer knows a Skill (deleted on disk) must fail the
// next freeze loudly, exactly like the static map did.
func TestServiceLiveCatalogRejectsSkillDeletedFromDisk(t *testing.T) {
	ctx := context.Background()
	skillsDir := t.TempDir()
	writeCatalogSkill(t, skillsDir, "go-dev", "GO_DEV_BODY")
	fixture := newSkillGraphFixture(t, nil)
	fixture.service.SkillCatalog = func() map[string]string {
		return skills.DigestCatalog(skillsDir)
	}
	_, err := fixture.service.Start(ctx, fixture.projectID, "pinned", 0, fixture.sessionID, nil, nil)
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(filepath.Join(skillsDir, "go-dev")))
	_, err = fixture.service.Start(ctx, fixture.projectID, "pinned", 0, fixture.sessionID, nil, nil)
	require.ErrorContains(t, err, `global Skill "go-dev" is not available`)
}
