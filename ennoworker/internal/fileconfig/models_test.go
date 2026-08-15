package fileconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelStorePersistsPortableCatalogWithoutCredentials(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	provider, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Name: "DeepSeek Main", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-secret-value",
	})
	require.NoError(t, err)
	assert.Equal(t, "deepseek-main", provider.ID)
	assert.Empty(t, provider.APIKey)

	model, err := store.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "deepseek-main", ModelName: "deepseek-chat", DisplayName: "DeepSeek Chat",
		ContextWindow: 131072, MaxOutputTokens: 8192, SupportsToolUse: true,
		ThinkingDialect:          domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "deepseek-main/deepseek-chat", model.ID)
	assert.True(t, model.IsDefault)

	modelsJSON, err := os.ReadFile(store.Models)
	require.NoError(t, err)
	assert.NotContains(t, string(modelsJSON), "sk-secret-value")
	assert.Contains(t, string(modelsJSON), `"credential": "deepseek-main"`)

	resolvedProvider, err := store.FindProvider(ctx, "deepseek-main")
	require.NoError(t, err)
	assert.Equal(t, "sk-secret-value", resolvedProvider.APIKey)
	resolvedModel, err := store.ResolvePortableRef(ctx, "deepseek-main/deepseek-chat")
	require.NoError(t, err)
	assert.Equal(t, model.ID, resolvedModel.ID)
}

func TestModelStoreRejectsUnknownFieldsAndInvalidCatalog(t *testing.T) {
	store := newModelStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(store.Models), 0o700))
	require.NoError(t, os.WriteFile(store.Models, []byte(`{
  "schemaVersion": 1,
  "providers": {},
  "unknown": true
}`), 0o600))

	_, err := store.ListProviders(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestModelStoreRejectsDuplicateAndInvalidModels(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	_, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "openai-main", Name: "OpenAI", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.openai.com/v1",
	})
	require.NoError(t, err)
	input := fileconfig.CreateModelInput{
		ProviderID: "openai-main", ModelName: "gpt-5", ContextWindow: 128000,
		MaxOutputTokens: 16000, SupportsToolUse: true,
		ThinkingDialect:          domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault},
	}
	_, err = store.CreateModel(ctx, input)
	require.NoError(t, err)
	_, err = store.CreateModel(ctx, input)
	assert.ErrorContains(t, err, "already exists")

	input.ModelName = "bad model"
	_, err = store.CreateModel(ctx, input)
	assert.ErrorContains(t, err, "contain no whitespace")
	input.ModelName = "too-many-tokens"
	input.MaxOutputTokens = input.ContextWindow + 1
	_, err = store.CreateModel(ctx, input)
	assert.ErrorContains(t, err, "cannot exceed")
}

func TestDeletingProviderClearsDefaultAndCredential(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	_, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "local-main", Name: "Local Main", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "http://127.0.0.1:11434/v1", APIKey: "local-key",
	})
	require.NoError(t, err)
	_, err = store.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "local-main", ModelName: "qwen", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)

	require.NoError(t, store.DeleteProvider(ctx, "local-main"))
	settings, err := store.Settings.Read()
	require.NoError(t, err)
	assert.Empty(t, settings.DefaultModel)
	_, err = store.Credentials.Resolve("local-main")
	assert.True(t, fileconfig.IsCredentialUnavailable(err))
}

func TestModelCatalogSupportsProviderQualifiedModelIDs(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	_, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "openrouter", Name: "OpenRouter", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://openrouter.ai/api/v1",
	})
	require.NoError(t, err)
	model, err := store.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "openrouter", ModelName: "anthropic/claude-sonnet", ContextWindow: 200000,
		MaxOutputTokens: 16000, SupportsToolUse: true,
		ThinkingDialect:          domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault},
	})
	require.NoError(t, err)
	assert.Equal(t, "openrouter/anthropic/claude-sonnet", model.ID)
	provider, modelID, err := fileconfig.SplitModelRef(model.ID)
	require.NoError(t, err)
	assert.Equal(t, "openrouter", provider)
	assert.Equal(t, "anthropic/claude-sonnet", modelID)
}

func newModelStore(t *testing.T) *fileconfig.ModelStore {
	t.Helper()
	directory := t.TempDir()
	store := fileconfig.NewModelStore(
		filepath.Join(directory, "config", "models.json"),
		filepath.Join(directory, "config", "provider-auth.json"),
		filepath.Join(directory, "config", "settings.json"),
	)
	store.Now = func() time.Time { return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC) }
	return store
}
