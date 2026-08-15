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

func TestSystemPromptAndThinkingSelectionFreezeTogether(t *testing.T) {
	ctx := context.Background()
	stack := newFileConfigStack(t)
	db := store.SetupDB(t)
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "freeze", HostPath: t.TempDir()})
	require.NoError(t, err)
	session := sqlCreateSession(t, db, project.ID)
	runs := &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo, Policies: stack.Policies}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "freeze", Text: "inspect",
		RequestedConfig: json.RawMessage(`{"thinkingEffort":"medium"}`),
	})
	require.NoError(t, err)
	run, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)
	assert.Empty(t, resolved.SystemPrompt.AgentProfileID)
	assert.NotEmpty(t, resolved.SystemPrompt.Digest)
	assert.Equal(t, domain.ThinkingMedium, resolved.Effective.ThinkingEffort)
	assert.Equal(t, domain.ThinkingDialectOpenAIReasoningEffort, resolved.Effective.InitialRuntime.ThinkingDialect)

	// The frozen config is immutable: a second freeze returns the identical
	// snapshot even though the live catalog changed behind it.
	_, err = stack.Models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "other", Name: "Other", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://other.test/v1", APIKey: "sk-other",
	})
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
	ctx := context.Background()
	stack := newFileConfigStack(t)
	// A second model without thinking support; the request pins it and asks for
	// an unsupported effort.
	_, err := stack.Models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "plain", DisplayName: "Plain",
		ContextWindow: 32000, MaxOutputTokens: 2048, IsDefault: false,
	})
	require.NoError(t, err)
	db := store.SetupDB(t)
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "unsupported", HostPath: t.TempDir()})
	require.NoError(t, err)
	session := sqlCreateSession(t, db, project.ID)
	runs := &store.RunRepo{DB: db, Providers: stack.Providers, Models: stack.ModelRepo, Policies: stack.Policies}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID,
		ClientRequestID: "unsupported", Text: "inspect",
		RequestedConfig: json.RawMessage(`{"modelProfileId":"provider/plain","thinkingEffort":"high"}`)})
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
