package graphrun_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type graphRunFixture struct {
	home      string
	sources   *globalsource.Store
	projects  *projectstore.Store
	sessions  *sessionstore.Manager
	models    *store.ModelRepo
	projectID string
	sessionID string
}

func newGraphRunFixture(t *testing.T) *graphRunFixture {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	modelFiles := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := modelFiles.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-flow-secret",
	})
	require.NoError(t, err)
	_, err = modelFiles.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "flow-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Flow Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Flow Session"})
	require.NoError(t, err)
	return &graphRunFixture{
		home: home, sources: &globalsource.Store{HomeDir: home}, projects: projects,
		sessions: sessions, models: &store.ModelRepo{Files: modelFiles},
		projectID: project.ID, sessionID: session.ID,
	}
}

func (f *graphRunFixture) localHero(t *testing.T, handle string, authority heroesource.Authority, tools []string, prompt string) *heroesource.Document {
	t.Helper()
	return &heroesource.Document{
		SchemaVersion: 1, Handle: handle, Name: handle,
		Model:  heroesource.ModelBinding{Ref: "provider/flow-model", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: authority,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: tools,
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 8, MaxConcurrentInstances: 8,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 16,
				MaxTotalTokens: 40000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: prompt,
	}
}

func (f *graphRunFixture) graph(id string, fanOut bool) *domain.FlowDefinition {
	scan := domain.FlowTask{Type: domain.FlowTaskHero, Hero: "local/reviewer",
		Goal: "Scan the workspace", Depends: []string{"gate"}, Budget: &domain.FlowTaskBudget{Tokens: 1000}}
	if fanOut {
		scan.FanOut = &domain.FlowFanOut{Min: 2, Max: 2}
	}
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: id, Budget: domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"review": {Type: domain.FlowTaskHero, Hero: "local/reviewer", Goal: "Review the result",
				Budget: &domain.FlowTaskBudget{Tokens: 1000}},
			"gate": {Type: domain.FlowTaskCheck, Command: "test -f ready", Depends: []string{"review"}},
			"scan": scan,
			"done": {Type: domain.FlowTaskHero, Depends: []string{"scan"},
				Terminal: &domain.FlowTerminal{Status: "success"}},
		},
	}
}

func (f *graphRunFixture) publish(t *testing.T, id string, fanOut bool, heroPrompt string) *globalsource.Revision {
	t.Helper()
	_, err := f.sources.CreateGraphFromDefinition(f.graph(id, fanOut))
	require.NoError(t, err)
	_, err = f.sources.CreateGraphHero(id, f.localHero(t, "reviewer", heroesource.AuthorityReadOnly, []string{"read", "grep"}, heroPrompt))
	require.NoError(t, err)
	revision, err := f.sources.PublishGraphRevision(id)
	require.NoError(t, err)
	return revision
}

func (f *graphRunFixture) bind(t *testing.T, id, revision string, enabled bool) {
	t.Helper()
	f.bindKind(t, projectstore.SourceKindGraph, id, revision, enabled)
}

func (f *graphRunFixture) bindKind(t *testing.T, kind string, id, revision string, enabled bool) {
	t.Helper()
	_, err := f.projects.UpdateGraphBindings(context.Background(), f.projectID, func(bindings *[]projectstore.GraphBinding) error {
		*bindings = append(*bindings, projectstore.GraphBinding{
			SourceKind: kind, GraphID: id, Revision: revision, Enabled: enabled,
		})
		return nil
	})
	require.NoError(t, err)
}

func TestServiceFreezesFullFlowWithCheckFanOutTerminal(t *testing.T) {
	fixture := newGraphRunFixture(t)
	revision := fixture.publish(t, "qc-flow", true, "Use the published prompt.")
	fixture.bind(t, "qc-flow", revision.ID(), true)
	service := &graphrun.Service{Sources: fixture.sources, Models: fixture.models, Sessions: fixture.sessions, Projects: fixture.projects}
	run, err := service.Start(context.Background(), fixture.projectID, "qc-flow", 0, fixture.sessionID, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, graphrun.FlowVersionID("qc-flow", revision.ID()), run.FlowVersionID)

	db, err := fixture.sessions.OpenSession(context.Background(), fixture.sessionID)
	require.NoError(t, err)
	var definitionJSON, configDigest string
	require.NoError(t, db.QueryRow(`SELECT definition_json, config_digest FROM run_agent_flow WHERE run_id=?`, run.RunID).
		Scan(&definitionJSON, &configDigest))
	assert.Equal(t, revision.Digest, configDigest)
	var definition domain.FlowDefinition
	require.NoError(t, json.Unmarshal([]byte(definitionJSON), &definition))
	assert.Equal(t, domain.FlowTaskCheck, definition.Tasks["gate"].Type)
	require.NotNil(t, definition.Tasks["scan"].FanOut)
	require.NotNil(t, definition.Tasks["done"].Terminal)

	nodes, err := (&store.AgentFlowRunRepo{DB: db}).ListNodes(context.Background(), run.RunID)
	require.NoError(t, err)
	require.Len(t, nodes, 4)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodes {
		byHandle[node.Handle] = node
	}
	require.Contains(t, byHandle, "gate")
	assert.Empty(t, byHandle["gate"].HeroDefinitionJSON)
	assert.Empty(t, byHandle["gate"].HeroVersionID)
	assert.Empty(t, byHandle["done"].HeroDefinitionJSON)
	assert.True(t, byHandle["done"].ReadOnly)
	assert.True(t, byHandle["scan"].ReadOnly)
	require.NotEmpty(t, byHandle["review"].HeroDefinitionJSON)
	var reviewHero domain.HeroDefinition
	require.NoError(t, json.Unmarshal(byHandle["review"].HeroDefinitionJSON, &reviewHero))
	assert.Equal(t, "Use the published prompt.", reviewHero.HeroPrompt)
	assert.True(t, strings.HasPrefix(byHandle["review"].HeroVersionID, "graph:qc-flow/reviewer@sha256:"), byHandle["review"].HeroVersionID)
}

func TestServiceFlowFreezeUsesPublishedHeroBundle(t *testing.T) {
	fixture := newGraphRunFixture(t)
	revision := fixture.publish(t, "immutable-flow", false, "published prompt")
	fixture.bind(t, "immutable-flow", revision.ID(), true)

	// Editing the draft Hero after publish must not change this revision.
	encoded, err := heroesource.Encode(fixture.localHero(t, "reviewer", heroesource.AuthorityReadOnly, []string{"read"}, "edited prompt"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.sources.GraphsDir(), "immutable-flow", "heroes", "reviewer", "hero.md"), encoded, 0o600))

	service := &graphrun.Service{Sources: fixture.sources, Models: fixture.models, Sessions: fixture.sessions, Projects: fixture.projects}
	run, err := service.Start(context.Background(), fixture.projectID, "immutable-flow", 0, fixture.sessionID, nil, nil)
	require.NoError(t, err)
	db, err := fixture.sessions.OpenSession(context.Background(), fixture.sessionID)
	require.NoError(t, err)
	nodes, err := (&store.AgentFlowRunRepo{DB: db}).ListNodes(context.Background(), run.RunID)
	require.NoError(t, err)
	for _, node := range nodes {
		if node.Handle != "review" {
			continue
		}
		var definition domain.HeroDefinition
		require.NoError(t, json.Unmarshal(node.HeroDefinitionJSON, &definition))
		assert.Equal(t, "published prompt", definition.HeroPrompt)
	}
}

func TestServiceStartRequiresEnabledPinnedBinding(t *testing.T) {
	fixture := newGraphRunFixture(t)
	revision := fixture.publish(t, "bound-flow", false, "prompt")
	service := &graphrun.Service{Sources: fixture.sources, Models: fixture.models, Sessions: fixture.sessions, Projects: fixture.projects}

	_, err := service.Start(context.Background(), fixture.projectID, "bound-flow", 0, fixture.sessionID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_binding_required")

	fixture.bind(t, "bound-flow", revision.ID(), false)
	_, err = service.Start(context.Background(), fixture.projectID, "bound-flow", 0, fixture.sessionID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_binding_disabled")

	// Flip the same binding to enabled and request a different revision.
	_, err = fixture.projects.UpdateGraphBindings(context.Background(), fixture.projectID, func(bindings *[]projectstore.GraphBinding) error {
		(*bindings)[0].Enabled = true
		return nil
	})
	require.NoError(t, err)
	_, err = service.Start(context.Background(), fixture.projectID, "bound-flow", 2, fixture.sessionID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_binding_revision_mismatch")
}

func TestServiceRejectsFanOutOnWriterHero(t *testing.T) {
	fixture := newGraphRunFixture(t)
	_, err := fixture.sources.CreateGraphFromDefinition(fixture.graph("writer-fan", true))
	require.NoError(t, err)
	_, err = fixture.sources.CreateGraphHero("writer-fan", fixture.localHero(t, "reviewer", heroesource.AuthorityWriteWorkspace, []string{"read", "write"}, "writer"))
	require.NoError(t, err)
	revision, err := fixture.sources.PublishGraphRevision("writer-fan")
	require.NoError(t, err)
	fixture.bind(t, "writer-fan", revision.ID(), true)
	service := &graphrun.Service{Sources: fixture.sources, Models: fixture.models, Sessions: fixture.sessions, Projects: fixture.projects}
	_, err = service.Start(context.Background(), fixture.projectID, "writer-fan", 0, fixture.sessionID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graph_fan_out_not_read_only")
}
