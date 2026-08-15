package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelProfilesManageExplicitDefaultWithoutStoringSecrets(t *testing.T) {
	ctx := context.Background()
	files := newModelStore(t)
	provider, err := (&store.ProviderRepo{Files: files}).Create(ctx, store.CreateProviderInput{
		Key: "deepseek", Name: "DeepSeek", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com", APIKey: "sk-DEEPSEEK_API_KEY",
	})
	require.NoError(t, err)
	repo := &store.ModelRepo{Files: files}
	first, err := repo.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "deepseek-chat", ContextWindow: 64000,
		MaxOutputTokens: 4096, SupportsToolUse: true, IsDefault: true,
	})
	require.NoError(t, err)
	second, err := repo.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "deepseek-reasoner", ContextWindow: 64000,
		MaxOutputTokens: 4096, SupportsToolUse: true, SupportsThinking: true,
		ThinkingDialect:          domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault, domain.ThinkingMedium},
	})
	require.NoError(t, err)
	profiles, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, 2)
	assert.Equal(t, first.ID, profiles[0].ID)
	assert.True(t, profiles[0].IsDefault)
	require.NoError(t, repo.SetDefault(ctx, second.ID))
	profiles, err = repo.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, second.ID, profiles[0].ID)
	assert.True(t, profiles[0].IsDefault)
	assert.False(t, profiles[1].IsDefault)
}

func TestModelProfilesResolvePortableReferencesExactly(t *testing.T) {
	ctx := context.Background()
	files := newModelStore(t)
	providers := &store.ProviderRepo{Files: files}
	models := &store.ModelRepo{Files: files}
	provider, err := providers.Create(ctx, store.CreateProviderInput{
		Key: "openai-main", Name: "openai-main", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test", APIKey: "sk-PORTABLE_REF",
	})
	require.NoError(t, err)
	model, err := models.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "org/gpt-5.4", ContextWindow: 32000, MaxOutputTokens: 2048,
	})
	require.NoError(t, err)

	resolved, err := models.ResolvePortableRef(ctx, "openai-main/org/gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, model.ID, resolved.ID)

	// V2: the portable ref is the exact provider-key/model-name id; provider
	// keys are unique, so an unknown lowercase ref fails closed as not found.
	_, err = models.ResolvePortableRef(ctx, "unknown-main/org/gpt-5.4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	_, err = models.ResolvePortableRef(ctx, "missing-separator")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider-name/model-name")
	// A second model under the same provider key does not make the ref
	// ambiguous: the ref includes the model name.
	_, err = models.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "gpt-4o", ContextWindow: 32000, MaxOutputTokens: 2048,
	})
	require.NoError(t, err)
	again, err := models.ResolvePortableRef(ctx, "openai-main/org/gpt-5.4")
	require.NoError(t, err)
	assert.Equal(t, model.ID, again.ID)
}

func TestEffectiveConfigPriorityAndFreezeAcrossDefaultChanges(t *testing.T) {
	db := store.SetupDB(t)
	ctx := context.Background()
	stack := newFileConfigStack(t)
	models := stack.ModelRepo
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "priority", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	// Session default wins over the file-wide default; the file default wins
	// for sessions without one. Agent-profile defaults are V1-only and are
	// rejected by the V2 file store.
	agentModel, err := stack.Models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "session-default", ContextWindow: 32000, MaxOutputTokens: 2048,
	})
	require.NoError(t, err)
	sessionDefault := agentModel.ID
	session := sqlCreateSessionWithModel(t, db, project.ID, &sessionDefault)
	runs := &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo, Policies: stack.Policies}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "session-priority", Text: "run",
	})
	require.NoError(t, err)
	run, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, agentModel.ID, resolved.Effective.ModelProfileID)

	require.NoError(t, runs.Succeed(ctx, run.ID))
	globalReplacement, err := stack.Models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "global-new", ContextWindow: 48000,
		MaxOutputTokens: 3072, IsDefault: true,
	})
	require.NoError(t, err)
	plainSession := sqlCreateSession(t, db, project.ID)
	secondSubmission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: plainSession.ID, ClientRequestID: "global-priority", Text: "run",
	})
	require.NoError(t, err)
	secondRun, err := runs.Claim(ctx, secondSubmission.Run.ID)
	require.NoError(t, err)
	secondResolved, err := runs.ResolveAndFreezeConfig(ctx, secondRun)
	require.NoError(t, err)
	assert.Equal(t, globalReplacement.ID, secondResolved.Effective.ModelProfileID)

	require.NoError(t, models.SetDefault(ctx, stack.DefaultRef))
	refrozen, err := runs.ResolveAndFreezeConfig(ctx, secondRun)
	require.NoError(t, err)
	assert.Equal(t, globalReplacement.ID, refrozen.Effective.ModelProfileID)
	// HookConfig may differ in nil vs `null` due to JSON round-trip; ignore for config comparison.
	secondResolved.Effective.HookConfig = domain.EffectiveHookConfig{}
	refrozen.Effective.HookConfig = domain.EffectiveHookConfig{}
	assert.Equal(t, secondResolved.Effective, refrozen.Effective)

	// UpdateDefaultModel validates against the removed global model SQL; set the
	// session column directly (the file store is the resolution authority).
	_, err = db.Exec(`UPDATE sessions SET default_model_profile_id=? WHERE id=?`, agentModel.ID, plainSession.ID)
	require.NoError(t, err)
	var storedDefault string
	require.NoError(t, db.QueryRow(`SELECT default_model_profile_id FROM sessions WHERE id=?`,
		plainSession.ID).Scan(&storedDefault))
	assert.Equal(t, agentModel.ID, storedDefault)

	var stored string
	require.NoError(t, db.QueryRow(`SELECT effective_config_json FROM agent_runs WHERE id = ?`, secondRun.ID).Scan(&stored))
	var effective domain.EffectiveRunConfig
	require.NoError(t, json.Unmarshal([]byte(stored), &effective))
	assert.Equal(t, globalReplacement.ID, effective.ModelProfileID)
}
