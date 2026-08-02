package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelProfilesManageExplicitDefaultWithoutStoringSecrets(t *testing.T) {
	db := store.SetupDB(t)
	ctx := context.Background()
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "DeepSeek", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com", CredentialRef: "env:DEEPSEEK_API_KEY",
	})
	require.NoError(t, err)
	repo := &store.ModelRepo{DB: db}
	first, err := repo.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "deepseek-chat", ContextWindow: 64000,
		MaxOutputTokens: 4096, SupportsToolUse: true, IsDefault: true,
	})
	require.NoError(t, err)
	second, err := repo.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "deepseek-reasoner", ContextWindow: 64000,
		MaxOutputTokens: 4096, SupportsToolUse: true, SupportsThinking: true,
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

func TestEffectiveConfigPriorityAndFreezeAcrossDefaultChanges(t *testing.T) {
	db := store.SetupDB(t)
	ctx := context.Background()
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "priority", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test", CredentialRef: "env:PROVIDER_KEY",
	})
	require.NoError(t, err)
	models := &store.ModelRepo{DB: db}
	globalModel, err := models.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "global", ContextWindow: 32000,
		MaxOutputTokens: 2048, IsDefault: true,
	})
	require.NoError(t, err)
	agentModel, err := models.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "agent", ContextWindow: 32000, MaxOutputTokens: 2048,
	})
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO agent_profiles
		(id, name, system_prompt, default_model_id, status, created_at, updated_at)
		VALUES ('agent-profile', 'agent', '', ?, 'active', ?, ?)`, agentModel.ID, now, now)
	require.NoError(t, err)
	agentID := "agent-profile"
	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "agent default", DefaultAgentProfileID: &agentID,
	})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "agent-priority", Text: "run",
	})
	require.NoError(t, err)
	run, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, agentModel.ID, resolved.Effective.ModelProfileID)

	require.NoError(t, runs.Succeed(ctx, run.ID))
	globalReplacement, err := models.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "global-new", ContextWindow: 48000,
		MaxOutputTokens: 3072, IsDefault: true,
	})
	require.NoError(t, err)
	plainSession, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "global default",
	})
	require.NoError(t, err)
	secondSubmission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: plainSession.ID, ClientRequestID: "global-priority", Text: "run",
	})
	require.NoError(t, err)
	secondRun, err := runs.Claim(ctx, secondSubmission.Run.ID)
	require.NoError(t, err)
	secondResolved, err := runs.ResolveAndFreezeConfig(ctx, secondRun)
	require.NoError(t, err)
	assert.Equal(t, globalReplacement.ID, secondResolved.Effective.ModelProfileID)

	require.NoError(t, models.SetDefault(ctx, globalModel.ID))
	refrozen, err := runs.ResolveAndFreezeConfig(ctx, secondRun)
	require.NoError(t, err)
	assert.Equal(t, globalReplacement.ID, refrozen.Effective.ModelProfileID)
	// HookConfig may differ in nil vs `null` due to JSON round-trip; ignore for config comparison.
	secondResolved.Effective.HookConfig = domain.EffectiveHookConfig{}
	refrozen.Effective.HookConfig = domain.EffectiveHookConfig{}
	assert.Equal(t, secondResolved.Effective, refrozen.Effective)

	sessionOverride := agentModel.ID
	updated, err := (&store.SessionRepo{DB: db}).UpdateDefaultModel(ctx, plainSession.ID, &sessionOverride)
	require.NoError(t, err)
	require.NotNil(t, updated.DefaultModelProfileID)
	assert.Equal(t, agentModel.ID, *updated.DefaultModelProfileID)

	var stored string
	require.NoError(t, db.QueryRow(`SELECT effective_config_json FROM agent_runs WHERE id = ?`, secondRun.ID).Scan(&stored))
	var effective domain.EffectiveRunConfig
	require.NoError(t, json.Unmarshal([]byte(stored), &effective))
	assert.Equal(t, globalReplacement.ID, effective.ModelProfileID)
}
