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

// admissionCase is one Hero-admission violation. It mutates the Hero source
// document, which both surfaces resolve: the publish validator through the
// file Hero catalog, the freeze path through the published revision.
type admissionCase struct {
	name string
	// code is the diagnostic publish must report and the code the Graph Run
	// must fail with. Both surfaces run the same gate.
	code string
	// mutate edits the saveable Hero document.
	mutate func(*heroesource.Document)
	// taskBudgetTokens is the Graph task budget.tokens, 0 for none.
	taskBudgetTokens int64
	// fanOutMin is the Graph task's fan_out.min, 0 for no fan-out.
	fanOutMin int
	// wantField is the publish diagnostic field; empty means tasks.check.hero.
	wantField string
}

// TestPublishAndRunStartAgreeOnHeroAdmission pins the shared admission gate: a
// Hero the publish validator rejects must fail graphrun.Service.Start with the
// same code, instead of publishing and then failing after the user presses Run.
func TestPublishAndRunStartAgreeOnHeroAdmission(t *testing.T) {
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
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	service := &graphrun.Service{Sources: sources, Models: &store.ModelRepo{Files: models}, Sessions: sessions, Projects: projects}
	discovery := &store.HeroDiscovery{Models: &store.ModelRepo{Files: models}}

	cases := []admissionCase{
		{
			name: "admission",
			code: "hero_admission_incompatible",
			mutate: func(document *heroesource.Document) {
				document.Delegation.Admission = domain.DelegationApprovalRequired
			},
		},
		{
			name: "context",
			code: "hero_context_incompatible",
			mutate: func(document *heroesource.Document) {
				document.Context.AllowedModes = []domain.HeroContextMode{domain.HeroContextRoom}
			},
		},
		{
			name: "caller_kind",
			code: "hero_caller_kind_incompatible",
			mutate: func(document *heroesource.Document) {
				document.Delegation.AllowedCallerKinds = []string{"parent_hero"}
			},
		},
		{
			name: "strategy",
			code: "hero_strategy_incompatible",
			mutate: func(document *heroesource.Document) {
				document.Delegation.AllowedStrategies = []string{"parallel"}
			},
		},
		{
			name: "ceiling_minimum",
			code: "hero_ceiling_invalid",
			mutate: func(document *heroesource.Document) {
				// Saveable (the Hero editor allows maxToolCalls >= 0) and only
				// rejected when a child Run submits its ceiling.
				document.Delegation.BudgetCeiling.MaxToolCalls = 0
			},
		},
		{
			name:             "budget_over_ceiling",
			code:             "hero_budget_exceeds_ceiling",
			taskBudgetTokens: 25000,
			wantField:        "tasks.check.budget.tokens",
		},
		{
			name: "limit_minimum",
			code: "hero_limit_invalid",
			mutate: func(document *heroesource.Document) {
				// A pack-installed Graph-local Hero bypasses the editor minimum.
				document.Delegation.MaxInvocationsPerParentRun = 0
			},
		},
		{
			// One fan-out task consumes fan_out.min invocations, so the flow
			// total already exceeds the limit for this Hero.
			name:      "invocation_total",
			code:      "hero_invocation_limit_exceeded",
			fanOutMin: 2,
			mutate: func(document *heroesource.Document) {
				document.Delegation.MaxInvocationsPerParentRun = 1
				document.Delegation.MaxConcurrentInstances = 8
			},
		},
		{
			name:      "fan_out_concurrency",
			code:      "hero_concurrency_limit_exceeded",
			fanOutMin: 2,
			mutate: func(document *heroesource.Document) {
				document.Delegation.MaxConcurrentInstances = 1
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			graphID := "pipeline-" + tc.name
			handle := "checker-" + tc.name
			heroDocument := admissionHeroDocument(model.ID)
			heroDocument.Handle = handle
			heroDocument.Name = "Checker " + tc.name
			if tc.mutate != nil {
				tc.mutate(heroDocument)
			}
			_, _, err := sources.CreateHero(heroDocument)
			require.NoError(t, err)
			heroRevision, err := sources.PublishHeroRevision(handle)
			require.NoError(t, err)

			_, _, err = sources.CreateGraphDefinition(graphID, "Pipeline "+tc.name)
			require.NoError(t, err)
			task := domain.FlowTask{Type: domain.FlowTaskHero, Hero: "global/" + handle + "@" + heroRevision.ID(), Goal: "Check the artifact"}
			if tc.taskBudgetTokens > 0 {
				task.Budget = &domain.FlowTaskBudget{Tokens: tc.taskBudgetTokens}
			}
			if tc.fanOutMin > 0 {
				task.FanOut = &domain.FlowFanOut{Min: tc.fanOutMin, Max: tc.fanOutMin}
			}
			definition := &domain.FlowDefinition{
				SchemaVersion: domain.FlowSchemaVersion, ID: graphID, Name: "Pipeline " + tc.name,
				Budget: domain.FlowBudget{MaxTotalTokens: 100000},
				Tasks:  map[string]domain.FlowTask{"check": task},
			}
			definitionYAML, err := agentflow.DefinitionToYAML(definition)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(sources.GraphsDir(), graphID, "graph.yaml"), definitionYAML, 0o600))
			revision, err := sources.PublishGraphRevision(graphID)
			require.NoError(t, err)

			// Publish surface: the same Hero definition the freeze resolves.
			heroDefinition, diagnostics := discovery.ResolveDocument(ctx, heroDocument)
			require.NotNil(t, heroDefinition, "%v", diagnostics)
			validator := &agentflow.Validator{Resolver: fixedHeroResolver{definition: *heroDefinition}}
			result := validator.Validate(ctx, definition)
			require.False(t, result.Valid, "publish accepted a Hero the Run start rejects")
			wantField := tc.wantField
			if wantField == "" {
				wantField = "tasks.check.hero"
			}
			var published *agentflow.ValidationDiagnostic
			for index, diagnostic := range result.Diagnostics {
				if diagnostic.Code == tc.code {
					published = &result.Diagnostics[index]
					break
				}
			}
			require.NotNil(t, published, "publish diagnostics: %v", result.Diagnostics)
			assert.Equal(t, wantField, published.Field)
			// The HTTP layer flattens diagnostics and drops Field, so the
			// message itself has to name the Hero the user has to open.
			assert.Contains(t, published.Message, "global/"+handle)

			// Run start: the freeze must report the same rule for the same Hero.
			_, err = projects.UpdateGraphBindings(ctx, project.ID, func(bindings *[]projectstore.GraphBinding) error {
				*bindings = append(*bindings, projectstore.GraphBinding{
					SourceKind: projectstore.SourceKindGraph, GraphID: graphID, Revision: revision.ID(), Enabled: true,
				})
				return nil
			})
			require.NoError(t, err)
			_, err = service.Start(ctx, project.ID, graphID, 0, session.ID, nil, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.code)
			assert.Contains(t, err.Error(), `Task "check"`)
		})
	}
}

// TestPublishRejectsHeroInvocationTotalAcrossTasks covers the flow-level half of
// the invocation limit, which the Run start does not model: it freezes one task
// at a time. Two tasks referencing one Hero whose limit is 1 publish today and
// then fail at the second task's child Run.
func TestPublishRejectsHeroInvocationTotalAcrossTasks(t *testing.T) {
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
	heroDocument := admissionHeroDocument(model.ID)
	heroDocument.Delegation.MaxInvocationsPerParentRun = 1
	_, _, err = sources.CreateHero(heroDocument)
	require.NoError(t, err)
	heroRevision, err := sources.PublishHeroRevision(heroDocument.Handle)
	require.NoError(t, err)

	heroDefinition, diagnostics := (&store.HeroDiscovery{Models: &store.ModelRepo{Files: models}}).ResolveDocument(ctx, heroDocument)
	require.NotNil(t, heroDefinition, "%v", diagnostics)
	validator := &agentflow.Validator{Resolver: fixedHeroResolver{definition: *heroDefinition}}

	heroRef := "global/" + heroDocument.Handle + "@" + heroRevision.ID()
	tasks := map[string]domain.FlowTask{
		"first":  {Type: domain.FlowTaskHero, Hero: heroRef, Goal: "First"},
		"second": {Type: domain.FlowTaskHero, Hero: heroRef, Goal: "Second", Depends: []string{"first"}},
	}
	result := validator.Validate(ctx, &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: "pipeline", Name: "Pipeline",
		Budget: domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks:  tasks,
	})
	require.False(t, result.Valid)
	var reported []agentflow.ValidationDiagnostic
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == "hero_invocation_limit_exceeded" {
			reported = append(reported, diagnostic)
		}
	}
	require.Len(t, reported, 2, "%v", result.Diagnostics)
	assert.Equal(t, "tasks.first.hero", reported[0].Field)
	assert.Equal(t, "tasks.second.hero", reported[1].Field)
	assert.Contains(t, reported[0].Message, "2 invocations")
	assert.Contains(t, reported[0].Message, "maxInvocationsPerParentRun is 1")
}

// admissionHeroDocument is a saveable, publishable Hero: only the Graph
// admission gate rejects the mutated variants.
func admissionHeroDocument(modelID string) *heroesource.Document {
	return &heroesource.Document{
		SchemaVersion: 1, Handle: "checker", Name: "Checker", Description: "Checks output",
		Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  heroesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, Authority: heroesource.AuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read"},
		Context: heroesource.ContextPolicy{DefaultMode: domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask}, OwnExecutionContinuity: domain.HeroContinuityNone},
		Delegation: heroesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: heroesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Check the artifact.",
	}
}
