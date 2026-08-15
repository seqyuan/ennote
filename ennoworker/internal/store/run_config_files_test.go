package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileRunConfigFreezesWithoutCredential(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "deepseek", Name: "DeepSeek", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-file-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "deepseek", ModelName: "deepseek-chat", ContextWindow: 131072,
		MaxOutputTokens: 8192, SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	providers := &store.ProviderRepo{Files: models}
	modelRepo := &store.ModelRepo{Files: models}
	policies := &fileconfig.PolicyStore{Path: filepath.Join(home, "config", "policies.json")}

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session", DefaultModelProfileID: &model.ID})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db, Providers: providers, Models: modelRepo, Policies: policies}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "request-1", Text: "hello"})
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	assert.Equal(t, "sk-file-secret", resolved.Provider.APIKey)
	assert.Equal(t, model.ID, resolved.Model.ID)

	var effective string
	require.NoError(t, db.QueryRow(`SELECT effective_config_json FROM agent_runs WHERE id=?`, claimed.ID).Scan(&effective))
	assert.NotContains(t, effective, "sk-file-secret")
	assert.NotContains(t, effective, `"apiKey"`)
	assert.Contains(t, effective, `"credentialRef":"deepseek"`)

	require.NoError(t, models.Credentials.Put("deepseek", "sk-rotated"))
	reloaded, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	assert.Equal(t, "sk-rotated", reloaded.Provider.APIKey)
	assert.NotContains(t, string(reloaded.Effective.InitialRuntime.APIKey), "sk-file-secret")
}

func TestFileRoleRevisionInvocationFreezesPublishedDefinition(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-role-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "role-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	sources := &globalsource.Store{HomeDir: home}
	document := &rolesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer", Description: "Reviews output",
		Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextRoom,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Use the published prompt.",
	}
	_, _, err = sources.CreateRole(document)
	require.NoError(t, err)
	revision, err := sources.PublishRoleRevision(document.Handle)
	require.NoError(t, err)

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessionManager := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessionManager.Close()) })
	session, err := sessionManager.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)
	db, err := sessionManager.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db, Providers: &store.ProviderRepo{Files: models}, Models: &store.ModelRepo{Files: models},
		Policies: &fileconfig.PolicyStore{Path: filepath.Join(home, "config", "policies.json")}, RoleSources: sources}
	submission, err := runs.SubmitInvocation(ctx, domain.SubmitInvocationInput{
		SessionID: session.ID, ClientRequestID: "role-request", Text: "review",
		Target: domain.RoleInvocationTarget{Kind: domain.InvocationTargetRole, ObjectID: document.Handle,
			VersionID: revision.ID(), ContextMode: domain.InvocationContextRoom},
	})
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, claimed)
	require.NoError(t, err)
	require.NotNil(t, resolved.Effective.Role)
	assert.Equal(t, revision.ID(), resolved.Effective.Role.VersionID)
	assert.Equal(t, "Use the published prompt.", resolved.SystemPrompt.AgentPrompt)
	assert.Equal(t, model.ID, resolved.Model.ID)
	assert.NotContains(t, string(claimed.EffectiveConfig), "sk-role-secret")
	require.NoError(t, runs.FinalizeSuccess(ctx, claimed.ID, domain.RunOutput{Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "reviewed"}},
	}}}))
	finished, err := runs.Get(ctx, claimed.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, finished.Status)
}
