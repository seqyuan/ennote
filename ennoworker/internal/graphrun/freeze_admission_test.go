package graphrun_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
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

// fixedHeroResolver resolves every Hero reference to one definition so the
// publish validator can be run against the same Hero the freeze path resolves.
type fixedHeroResolver struct {
	definition domain.HeroDefinition
}

func (r fixedHeroResolver) ResolveHero(context.Context, string) (*agentflow.HeroInfo, error) {
	return &agentflow.HeroInfo{Definition: r.definition}, nil
}

func (r fixedHeroResolver) KnownSkill(context.Context, string) bool   { return true }
func (r fixedHeroResolver) ToolReadOnly(context.Context, string) bool { return true }

// TestPublishAndFreezeAgreeOnHeroAdmission pins the shared gate: a Hero whose
// delegation does not allow the host caller publishes through Validate with
// hero_caller_kind_incompatible and fails Run start with the same code, instead
// of publishing and then failing after the user presses Run.
func TestPublishAndFreezeAgreeOnHeroAdmission(t *testing.T) {
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

	// Saveable and publishable: only the Graph admission gate rejects it.
	sources := &globalsource.Store{HomeDir: home}
	heroDoc := &heroesource.Document{
		SchemaVersion: 1, Handle: "checker", Name: "Checker", Description: "Checks output",
		Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"parent_hero"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Check the artifact.",
	}
	_, _, err = sources.CreateHero(heroDoc)
	require.NoError(t, err)
	heroRevision, err := sources.PublishHeroRevision(heroDoc.Handle)
	require.NoError(t, err)

	_, _, err = sources.CreateGraphDefinition("pipeline", "Pipeline")
	require.NoError(t, err)
	definition := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "pipeline", Name: "Pipeline",
		Budget: domain.FlowBudget{MaxTotalTokens: 20000},
		Tasks: map[string]domain.FlowTask{
			"check": {Type: domain.FlowTaskHero, Hero: "global/checker@" + heroRevision.ID(), Goal: "Check the artifact"},
		},
	}
	definitionYAML, err := agentflow.DefinitionToYAML(definition)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sources.GraphsDir(), "pipeline", "graph.yaml"), definitionYAML, 0o600))
	revision, err := sources.PublishGraphRevision("pipeline")
	require.NoError(t, err)

	// Publish surface: the same Hero definition the freeze resolves.
	heroDefinition, diagnostics := (&store.HeroDiscovery{Models: &store.ModelRepo{Files: models}}).ResolveDocument(ctx, heroDoc)
	require.NotNil(t, heroDefinition, "%v", diagnostics)
	validator := &agentflow.Validator{Resolver: fixedHeroResolver{definition: *heroDefinition}}
	result := validator.Validate(ctx, definition)
	require.False(t, result.Valid)
	require.Len(t, result.Diagnostics, 1, "%v", result.Diagnostics)
	assert.Equal(t, "hero_caller_kind_incompatible", result.Diagnostics[0].Code)
	assert.Equal(t, "tasks.check.hero", result.Diagnostics[0].Field)
	assert.Contains(t, result.Diagnostics[0].Message, "host")
	assert.Contains(t, result.Diagnostics[0].Message, "parent_hero")

	// Run start: the freeze must report the same rule for the same Hero.
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	_, err = projects.UpdateGraphBindings(ctx, project.ID, func(bindings *[]projectstore.GraphBinding) error {
		*bindings = append(*bindings, projectstore.GraphBinding{SourceKind: projectstore.SourceKindGraph, GraphID: "pipeline", Revision: revision.ID(), Enabled: true})
		return nil
	})
	require.NoError(t, err)

	service := &graphrun.Service{Sources: sources, Models: &store.ModelRepo{Files: models}, Sessions: sessions, Projects: projects}
	_, err = service.Start(ctx, project.ID, "pipeline", 0, session.ID, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hero_caller_kind_incompatible")
	assert.Contains(t, err.Error(), `Task "check"`)
	assert.Contains(t, err.Error(), "Hero delegation.allowedCallerKinds")
	assert.Contains(t, err.Error(), "parent_hero")
}
