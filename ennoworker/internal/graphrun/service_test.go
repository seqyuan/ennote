package graphrun_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
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
	heroDoc := &heroesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer", Description: "Reviews output",
		Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Use the published prompt.",
	}
	_, _, err = sources.CreateHero(heroDoc)
	require.NoError(t, err)
	heroRevision, err := sources.PublishHeroRevision(heroDoc.Handle)
	require.NoError(t, err)

	_, _, err = sources.CreateGraphDefinition("pipeline", "Pipeline")
	require.NoError(t, err)
	_, err = sources.CreateGraphHero("pipeline", &heroesource.Document{
		SchemaVersion: 1, Handle: "producer", Name: "Producer",
		Model:  heroesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Produce the listing.",
	})
	require.NoError(t, err)
	definition := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "pipeline", Name: "Pipeline",
		Inputs: map[string]domain.FlowPort{"target": {Type: domain.PortTypeString, Required: true}},
		Budget: domain.FlowBudget{MaxTotalTokens: 40000},
		Tasks: map[string]domain.FlowTask{
			"produce": {Type: domain.FlowTaskHero, Hero: "local/producer", Goal: "Produce an artifact listing for {inputs.target}", Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"review":  {Type: domain.FlowTaskHero, Hero: "global/reviewer", Goal: "Review the produced artifact and submit a verdict", Depends: []string{"produce"}},
		},
	}
	encoded, err := agentflow.DefinitionToYAML(definition)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sources.GraphsDir(), "pipeline", "graph.yaml"), encoded, 0o600))
	graphRevision, err := sources.PublishGraphRevision("pipeline")
	require.NoError(t, err)

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	_, err = projects.UpdateGraphBindings(ctx, project.ID, func(bindings *[]projectstore.GraphBinding) error {
		*bindings = append(*bindings, projectstore.GraphBinding{SourceKind: projectstore.SourceKindGraph, GraphID: "pipeline", Revision: graphRevision.ID(), Enabled: true})
		return nil
	})
	require.NoError(t, err)

	var startedDB *sql.DB
	var startedRunID string
	service := &graphrun.Service{
		Sources: sources, Models: &store.ModelRepo{Files: models}, Sessions: sessions, Projects: projects,
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

	// Every node froze its full Hero definition (inline + global revision).
	var handle, heroVersionID, heroDefJSON, skillJSON string
	require.NoError(t, db.QueryRow(`SELECT handle, hero_version_id, hero_definition_json, skill_digests_json
		FROM run_agent_flow_nodes WHERE run_id=? AND task_index=0`, run.RunID).
		Scan(&handle, &heroVersionID, &heroDefJSON, &skillJSON))
	assert.Equal(t, "produce", handle)
	assert.Contains(t, heroVersionID, "graph:pipeline/producer@sha256:")
	var inlineDef domain.HeroDefinition
	require.NoError(t, json.Unmarshal([]byte(heroDefJSON), &inlineDef))
	assert.Equal(t, model.ID, inlineDef.ModelBinding.ModelProfileID)
	assert.Equal(t, `{"schemaVersion":2,"skills":[]}`, skillJSON)

	require.NoError(t, db.QueryRow(`SELECT handle, hero_version_id, hero_definition_json FROM run_agent_flow_nodes
		WHERE run_id=? AND task_index=1`, run.RunID).
		Scan(&handle, &heroVersionID, &heroDefJSON))
	assert.Equal(t, "review", handle)
	assert.Equal(t, "reviewer@"+heroRevision.ID(), heroVersionID)
	var reviewDef domain.HeroDefinition
	require.NoError(t, json.Unmarshal([]byte(heroDefJSON), &reviewDef))
	assert.Equal(t, domain.HeroAuthorityReadOnly, reviewDef.Authority)

	// The orchestrator flow store resolves the definition from the frozen row.
	version, err := (&store.OrchestratorStore{Runs: &store.AgentFlowRunRepo{DB: db}}).
		GetVersion(ctx, run.FlowVersionID)
	require.NoError(t, err)
	assert.Equal(t, graphRevision.Digest, version.ConfigDigest)
	manifest, err := graphrun.ManifestDigest(version.ConfigDigest, run.InputsJSON)
	require.NoError(t, err)
	assert.Equal(t, run.ManifestDigest, manifest)
}

func TestServiceFreezesGraphLocalHero(t *testing.T) {
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
	_, _, err = sources.CreateGraphDefinition("pipeline", "Pipeline")
	require.NoError(t, err)
	localHero := &heroesource.Document{
		SchemaVersion: 1, Handle: "local_reviewer", Name: "Local Reviewer",
		Positioning: "Review", Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 4, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Local prompt.",
	}
	if err := os.MkdirAll(filepath.Join(home, "agents", "graphs", "pipeline", "heroes", "local_reviewer"), 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := heroesource.Encode(localHero)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(home, "agents", "graphs", "pipeline", "heroes", "local_reviewer", "hero.md"), encoded, 0o600))

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)

	definition := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "pipeline", Name: "Pipeline",
		Budget: domain.FlowBudget{MaxTotalTokens: 20000},
		Tasks:  map[string]domain.FlowTask{"local_check": {Type: domain.FlowTaskHero, Hero: "local/local_reviewer", Goal: "Verify locally"}},
	}
	definitionYAML, err := agentflow.DefinitionToYAML(definition)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sources.GraphsDir(), "pipeline", "graph.yaml"), definitionYAML, 0o600))
	revision, err := sources.PublishGraphRevision("pipeline")
	require.NoError(t, err)
	_, err = projects.UpdateGraphBindings(ctx, project.ID, func(bindings *[]projectstore.GraphBinding) error {
		*bindings = append(*bindings, projectstore.GraphBinding{SourceKind: projectstore.SourceKindGraph, GraphID: "pipeline", Revision: revision.ID(), Enabled: true})
		return nil
	})
	require.NoError(t, err)

	service := &graphrun.Service{Sources: sources, Models: &store.ModelRepo{Files: models}, Sessions: sessions, Projects: projects}
	run, err := service.Start(ctx, project.ID, "pipeline", 0, session.ID, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, graphrun.FlowVersionID("pipeline", revision.ID()), run.FlowVersionID)

	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	var handle, heroVersionID, heroDefJSON string
	require.NoError(t, db.QueryRow(`SELECT handle, hero_version_id, hero_definition_json FROM run_agent_flow_nodes
		WHERE run_id=? AND task_index=0`, run.RunID).Scan(&handle, &heroVersionID, &heroDefJSON))
	assert.Equal(t, "local_check", handle)
	assert.Contains(t, heroVersionID, "graph:pipeline/local_reviewer@sha256:")
	var def domain.HeroDefinition
	require.NoError(t, json.Unmarshal([]byte(heroDefJSON), &def))
	assert.Equal(t, "Local prompt.", def.HeroPrompt)
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
	_, _, err = sources.CreateGraphDefinition("pinned", "Pinned")
	require.NoError(t, err)
	_, err = sources.CreateGraphHero("pinned", &heroesource.Document{
		SchemaVersion: 1, Handle: "builder", Name: "Builder",
		Model:  heroesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Build it.",
	})
	require.NoError(t, err)
	definition := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "pinned", Name: "Pinned",
		Budget: domain.FlowBudget{MaxTotalTokens: 20000},
		Tasks:  map[string]domain.FlowTask{"build": {Type: domain.FlowTaskHero, Hero: "local/builder", Goal: "Build it", Skills: []string{"global/go-dev"}}},
	}
	encoded, err := agentflow.DefinitionToYAML(definition)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sources.GraphsDir(), "pinned", "graph.yaml"), encoded, 0o600))
	revision, err := sources.PublishGraphRevision("pinned")
	require.NoError(t, err)

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	_, err = projects.UpdateGraphBindings(ctx, project.ID, func(bindings *[]projectstore.GraphBinding) error {
		*bindings = append(*bindings, projectstore.GraphBinding{SourceKind: projectstore.SourceKindGraph, GraphID: "pinned", Revision: revision.ID(), Enabled: true})
		return nil
	})
	require.NoError(t, err)

	return &skillGraphFixture{
		service: &graphrun.Service{Sources: sources, Models: &store.ModelRepo{Files: models},
			Sessions: sessions, Projects: projects, GlobalSkills: globalSkills},
		sessions: sessions, projectID: project.ID, sessionID: session.ID,
	}
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

// localGraphRunFixture wires the file stores, one Session, and a ready Service
// for the graph-local Hero/Skill freeze tests.
type localGraphRunFixture struct {
	ctx       context.Context
	home      string
	sources   *globalsource.Store
	files     *fileconfig.ModelStore
	sessions  *sessionstore.Manager
	projects  *projectstore.Store
	projectID string
	sessionID string
	modelID   string
}

func newLocalGraphRunFixture(t *testing.T) *localGraphRunFixture {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	files := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := files.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-local-secret",
	})
	require.NoError(t, err)
	model, err := files.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "local-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)

	sources := &globalsource.Store{HomeDir: home}
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)

	return &localGraphRunFixture{
		ctx: ctx, home: home, sources: sources, files: files, sessions: sessions, projects: projects,
		projectID: project.ID, sessionID: session.ID, modelID: model.ID,
	}
}

func (f *localGraphRunFixture) service() *graphrun.Service {
	return &graphrun.Service{Sources: f.sources, Models: &store.ModelRepo{Files: f.files}, Sessions: f.sessions, Projects: f.projects}
}

// writeDefinition replaces the draft graph.yaml with one definition whose tasks
// are the ones under test. The budget covers any of them.
func (f *localGraphRunFixture) writeDefinition(t *testing.T, id string, tasks map[string]domain.FlowTask) {
	t.Helper()
	definition := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: id, Name: id,
		Budget: domain.FlowBudget{MaxTotalTokens: 80000}, Tasks: tasks,
	}
	encoded, err := agentflow.DefinitionToYAML(definition)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.sources.GraphsDir(), id, "graph.yaml"), encoded, 0o600))
}

// publishBound publishes the draft and pins the Project binding to it.
func (f *localGraphRunFixture) publishBound(t *testing.T, id string) *globalsource.Revision {
	t.Helper()
	revision, err := f.sources.PublishGraphRevision(id)
	require.NoError(t, err)
	_, err = f.projects.UpdateGraphBindings(f.ctx, f.projectID, func(bindings *[]projectstore.GraphBinding) error {
		*bindings = append(*bindings, projectstore.GraphBinding{
			SourceKind: projectstore.SourceKindGraph, GraphID: id, Revision: revision.ID(), Enabled: true,
		})
		return nil
	})
	require.NoError(t, err)
	return revision
}

func (f *localGraphRunFixture) localHero(handle, prompt string) *heroesource.Document {
	return &heroesource.Document{
		SchemaVersion: 1, Handle: handle, Name: handle,
		Positioning: "Review", Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: f.modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 4, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: prompt,
	}
}

// overwriteLocalHero edits the mutable draft of a graph-local Hero in place.
func (f *localGraphRunFixture) overwriteLocalHero(t *testing.T, graphID, handle, prompt string) {
	t.Helper()
	doc, _, err := f.sources.ReadGraphHero(graphID, handle)
	require.NoError(t, err)
	doc.Prompt = prompt
	encoded, err := heroesource.Encode(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.home, "agents", "graphs", graphID, "heroes", handle, "hero.md"), encoded, 0o600))
}

// rewriteRevisionJSON rewrites one revision's metadata in place so tests can
// simulate pre-freeze revisions or corrupt bundles.
func (f *localGraphRunFixture) rewriteRevisionJSON(t *testing.T, graphID, revisionID string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(f.home, "agents", "graphs", graphID, "revisions", revisionID, "revision.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(data, &metadata))
	mutate(metadata)
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
}

// frozenNodeHero reads the task_index 0 frozen Hero back out of the Session DB.
func (f *localGraphRunFixture) frozenNodeHero(t *testing.T, runID string) (string, domain.HeroDefinition) {
	t.Helper()
	db, err := f.sessions.OpenSession(f.ctx, f.sessionID)
	require.NoError(t, err)
	var versionID, definitionJSON string
	require.NoError(t, db.QueryRow(`SELECT hero_version_id, hero_definition_json FROM run_agent_flow_nodes
		WHERE run_id=? AND task_index=0`, runID).Scan(&versionID, &definitionJSON))
	var def domain.HeroDefinition
	require.NoError(t, json.Unmarshal([]byte(definitionJSON), &def))
	return versionID, def
}

func writeLocalSkill(t *testing.T, home, graphID, skillID, body string) string {
	t.Helper()
	dir := filepath.Join(home, "agents", "graphs", graphID, "skills", skillID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"id":"`+skillID+`","version":"0.1.0","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600))
	return dir
}

func TestServiceFreezesPublishedLocalHeroAgainstDraftEdit(t *testing.T) {
	f := newLocalGraphRunFixture(t)
	_, _, err := f.sources.CreateGraphDefinition("pipeline", "Pipeline")
	require.NoError(t, err)
	publishedDigest, err := f.sources.CreateGraphHero("pipeline", f.localHero("reviewer", "Published prompt."))
	require.NoError(t, err)
	f.writeDefinition(t, "pipeline", map[string]domain.FlowTask{
		"review": {Type: domain.FlowTaskHero, Hero: "local/reviewer", Goal: "Review"},
	})
	revision := f.publishBound(t, "pipeline")
	require.Equal(t, publishedDigest, revision.HeroDigests["reviewer"])

	// Edit the mutable draft after publishing; the frozen revision must win.
	f.overwriteLocalHero(t, "pipeline", "reviewer", "Edited draft prompt.")

	run, err := f.service().Start(f.ctx, f.projectID, "pipeline", 0, f.sessionID, nil, nil)
	require.NoError(t, err)

	versionID, def := f.frozenNodeHero(t, run.RunID)
	assert.Equal(t, "graph:pipeline/reviewer@"+publishedDigest, versionID)
	assert.Equal(t, "Published prompt.", def.HeroPrompt)
}

func TestServiceFreezesPublishedLocalSkillAgainstDraftEdit(t *testing.T) {
	f := newLocalGraphRunFixture(t)
	_, _, err := f.sources.CreateGraphDefinition("pipeline", "Pipeline")
	require.NoError(t, err)
	_, err = f.sources.CreateGraphHero("pipeline", f.localHero("builder", "Build it."))
	require.NoError(t, err)
	skillDir := writeLocalSkill(t, f.home, "pipeline", "tools", "Published tools.")
	f.writeDefinition(t, "pipeline", map[string]domain.FlowTask{
		"build": {Type: domain.FlowTaskHero, Hero: "local/builder", Goal: "Build", Skills: []string{"local/tools"}},
	})
	revision := f.publishBound(t, "pipeline")
	require.Contains(t, revision.SkillDigests, "tools")

	// Edit the mutable draft SKILL.md after publishing.
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("Edited draft skill."), 0o600))

	run, err := f.service().Start(f.ctx, f.projectID, "pipeline", 0, f.sessionID, nil, nil)
	require.NoError(t, err)

	_, def := f.frozenNodeHero(t, run.RunID)
	assert.Contains(t, def.HeroPrompt, "Published tools.")
	assert.NotContains(t, def.HeroPrompt, "Edited draft skill.")
}

func TestServiceLegacyRevisionStillReadsLocalDraft(t *testing.T) {
	f := newLocalGraphRunFixture(t)
	_, _, err := f.sources.CreateGraphDefinition("pipeline", "Pipeline")
	require.NoError(t, err)
	_, err = f.sources.CreateGraphHero("pipeline", f.localHero("reviewer", "Published prompt."))
	require.NoError(t, err)
	f.writeDefinition(t, "pipeline", map[string]domain.FlowTask{
		"review": {Type: domain.FlowTaskHero, Hero: "local/reviewer", Goal: "Review"},
	})
	revision := f.publishBound(t, "pipeline")
	require.Contains(t, revision.HeroDigests, "reviewer")

	// Rewrite the metadata into the pre-freeze shape: no Hero/Skill digests.
	f.rewriteRevisionJSON(t, "pipeline", revision.ID(), func(metadata map[string]any) {
		delete(metadata, "heroDigests")
		delete(metadata, "skillDigests")
	})

	f.overwriteLocalHero(t, "pipeline", "reviewer", "Edited draft prompt.")

	run, err := f.service().Start(f.ctx, f.projectID, "pipeline", 0, f.sessionID, nil, nil)
	require.NoError(t, err)

	versionID, def := f.frozenNodeHero(t, run.RunID)
	assert.Equal(t, "Edited draft prompt.", def.HeroPrompt)
	assert.True(t, strings.HasPrefix(versionID, "graph:pipeline/reviewer@"), versionID)
}

func TestServiceFrozenRevisionMissingLocalHeroFailsClosed(t *testing.T) {
	f := newLocalGraphRunFixture(t)
	_, _, err := f.sources.CreateGraphDefinition("pipeline", "Pipeline")
	require.NoError(t, err)
	_, err = f.sources.CreateGraphHero("pipeline", f.localHero("reviewer", "Published prompt."))
	require.NoError(t, err)
	_, err = f.sources.CreateGraphHero("pipeline", f.localHero("builder", "Build it."))
	require.NoError(t, err)
	writeLocalSkill(t, f.home, "pipeline", "tools", "Published tools.")
	f.writeDefinition(t, "pipeline", map[string]domain.FlowTask{
		"review": {Type: domain.FlowTaskHero, Hero: "local/reviewer", Goal: "Review"},
		"build":  {Type: domain.FlowTaskHero, Hero: "local/builder", Goal: "Build", Skills: []string{"local/tools"}},
	})
	revision := f.publishBound(t, "pipeline")
	require.Contains(t, revision.HeroDigests, "reviewer")
	require.Contains(t, revision.SkillDigests, "tools")

	// Drop only the reviewer digest: the Skill keeps the revision frozen, but the
	// referenced local Hero is absent from the bundle and must fail closed.
	f.rewriteRevisionJSON(t, "pipeline", revision.ID(), func(metadata map[string]any) {
		heroes, _ := metadata["heroDigests"].(map[string]any)
		delete(heroes, "reviewer")
	})

	_, err = f.service().Start(f.ctx, f.projectID, "pipeline", 0, f.sessionID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `revision v000001 local Hero "reviewer" missing from frozen bundle`)
}

func TestStartFreezesDefinition(t *testing.T) {
	fixture := newGraphRunFixture(t)
	definition := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "qc", Name: "QC",
		Budget: domain.FlowBudget{MaxTotalTokens: 1000},
		Tasks: map[string]domain.FlowTask{
			"review": {Type: domain.FlowTaskHero, Hero: "local/reviewer", Goal: "Review"},
			"gate":   {Type: domain.FlowTaskCheck, Command: "go test ./...", Depends: []string{"review"}},
		},
	}
	_, _, err := fixture.sources.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)
	_, err = fixture.sources.CreateGraphHero("qc", fixture.localHero(t, "reviewer", heroesource.AuthorityReadOnly, []string{"read"}, "prompt"))
	require.NoError(t, err)
	encoded, err := agentflow.DefinitionToYAML(definition)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.sources.GraphsDir(), "qc", "graph.yaml"), encoded, 0o600))
	revision, err := fixture.sources.PublishGraphRevision("qc")
	require.NoError(t, err)
	fixture.bindKind(t, projectstore.SourceKindGraph, "qc", revision.ID(), true)

	service := &graphrun.Service{Sources: fixture.sources, Models: fixture.models, Sessions: fixture.sessions, Projects: fixture.projects}
	run, err := service.Start(context.Background(), fixture.projectID, "qc", 1, fixture.sessionID, nil, nil)
	require.NoError(t, err)

	db, err := fixture.sessions.OpenSession(context.Background(), fixture.sessionID)
	require.NoError(t, err)
	var definitionJSON string
	require.NoError(t, db.QueryRow(`SELECT definition_json FROM run_agent_flow WHERE run_id=?`, run.RunID).Scan(&definitionJSON))
	var frozen domain.FlowDefinition
	require.NoError(t, json.Unmarshal([]byte(definitionJSON), &frozen))
	assert.Equal(t, int64(1000), frozen.Budget.MaxTotalTokens)

	nodes, err := (&store.AgentFlowRunRepo{DB: db}).ListNodes(context.Background(), run.RunID)
	require.NoError(t, err)
	handles := map[string]bool{}
	for _, node := range nodes {
		handles[node.Handle] = true
	}
	assert.True(t, handles["gate"], "the check task must freeze a node")
}

func TestStartRequiresBinding(t *testing.T) {
	fixture := newGraphRunFixture(t)
	_, _, err := fixture.sources.CreateGraphDefinition("qc", "QC")
	require.NoError(t, err)
	_, err = fixture.sources.PublishGraphRevision("qc")
	require.NoError(t, err)
	service := &graphrun.Service{Sources: fixture.sources, Models: fixture.models, Sessions: fixture.sessions, Projects: fixture.projects}

	_, err = service.Start(context.Background(), fixture.projectID, "qc", 0, fixture.sessionID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_binding_required")

	fixture.bindKind(t, projectstore.SourceKindGraph, "qc", "v000001", true)
	_, err = service.Start(context.Background(), fixture.projectID, "qc", 2, fixture.sessionID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_binding_revision_mismatch")
}
