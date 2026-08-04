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

func TestSystemPromptAndThinkingSelectionFreezeTogether(t *testing.T) {
	db := store.SetupDB(t)
	ctx := context.Background()
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "freeze", HostPath: t.TempDir()})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "provider", ProviderType: domain.ProviderOpenAICompatible, BaseURL: "https://provider.test",
		CredentialRef: "env:PROVIDER_KEY",
	})
	require.NoError(t, err)
	model, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "reasoning-model", ContextWindow: 32000, MaxOutputTokens: 2048,
		SupportsThinking: true, ThinkingDialect: domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium},
		IsDefault:                true,
	})
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`INSERT INTO agent_profiles
		(id,name,system_prompt,default_model_id,status,created_at,updated_at)
		VALUES('agent','Reviewer','Review evidence precisely.',?,'active',?,?)`, model.ID, now, now)
	require.NoError(t, err)
	agentID := "agent"
	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "prompt", DefaultAgentProfileID: &agentID,
	})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "freeze", Text: "inspect",
		RequestedConfig: json.RawMessage(`{"thinkingEffort":"medium"}`),
	})
	require.NoError(t, err)
	run, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, "Review evidence precisely.", resolved.SystemPrompt.AgentPrompt)
	assert.NotEmpty(t, resolved.SystemPrompt.Digest)
	assert.Equal(t, domain.ThinkingMedium, resolved.Effective.ThinkingEffort)
	assert.Equal(t, domain.ThinkingDialectOpenAIReasoningEffort, resolved.Effective.InitialRuntime.ThinkingDialect)

	_, err = db.Exec(`UPDATE agent_profiles SET system_prompt='changed' WHERE id='agent'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE model_profiles SET thinking_dialect='none',supported_thinking_efforts_json='["default"]' WHERE id=?`, model.ID)
	require.NoError(t, err)
	refrozen, err := runs.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, resolved.SystemPrompt, refrozen.SystemPrompt)
	assert.Equal(t, domain.ThinkingMedium, refrozen.Effective.ThinkingEffort)
	assert.Equal(t, domain.ThinkingDialectOpenAIReasoningEffort, refrozen.Effective.InitialRuntime.ThinkingDialect)

	var snapshotJSON, digest string
	require.NoError(t, db.QueryRow(`SELECT system_prompt_snapshot_json,system_prompt_digest
		FROM agent_runs WHERE id=?`, run.ID).Scan(&snapshotJSON, &digest))
	var snapshot domain.SystemPromptSnapshot
	require.NoError(t, json.Unmarshal([]byte(snapshotJSON), &snapshot))
	assert.Equal(t, snapshot.Digest, digest)
}

func TestUnsupportedThinkingEffortFailsBeforeConfigFreeze(t *testing.T) {
	db := store.SetupDB(t)
	ctx := context.Background()
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "unsupported", HostPath: t.TempDir()})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "provider", ProviderType: domain.ProviderOpenAICompatible, BaseURL: "https://provider.test",
		CredentialRef: "env:PROVIDER_KEY",
	})
	require.NoError(t, err)
	model, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "plain", ContextWindow: 32000, MaxOutputTokens: 2048, IsDefault: true,
	})
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "unsupported", DefaultModelProfileID: &model.ID,
	})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID,
		ClientRequestID: "unsupported", Text: "inspect", RequestedConfig: json.RawMessage(`{"thinkingEffort":"high"}`)})
	require.NoError(t, err)
	run, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = runs.ResolveAndFreezeConfig(ctx, run)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderConfigurationInvalid, domain.ErrorCodeOf(err))
	var effective string
	require.NoError(t, db.QueryRow(`SELECT effective_config_json FROM agent_runs WHERE id=?`, run.ID).Scan(&effective))
	assert.JSONEq(t, `{}`, effective)
}

func TestAgentRunJSONRedactsFrozenCredentials(t *testing.T) {
	run := domain.AgentRun{ID: "run", SessionID: "session", CommitFormatVersion: domain.CommitFormatLegacyV1,
		ExecutionDepth: 0, PublishMode: domain.PublishPublicFinal, SpeakerSnapshot: json.RawMessage(`{}`),
		ContextSnapshot: json.RawMessage(`{}`), RequestedConfig: json.RawMessage(`{}`),
		EffectiveConfig: json.RawMessage(`{"initialRuntime":{"credentialRef":"env:SECRET","baseUrl":"https://example.test"}}`)}
	encoded, err := json.Marshal(run)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "env:SECRET")
	assert.Contains(t, string(encoded), "https://example.test")
}
