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

func TestCatalogSuppliesModelDefaults(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	_, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "deepseek", Name: "DeepSeek", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com",
	})
	require.NoError(t, err)
	// Only the model id is supplied; every capability field falls back to the
	// built-in catalog for deepseek-chat.
	model, err := store.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "deepseek", ModelName: "deepseek-chat",
	})
	require.NoError(t, err)
	assert.Equal(t, 131072, model.ContextWindow)
	assert.Equal(t, 8192, model.MaxOutputTokens)
	assert.Equal(t, int64(140), model.InputCostUSDMicrosPerMillion)
	assert.Equal(t, int64(280), model.OutputCostUSDMicrosPerMillion)
	assert.True(t, model.SupportsToolUse)
	assert.False(t, model.SupportsThinking)
	assert.False(t, model.SupportsVision)
}

func TestCatalogMissRequiresFullDeclaration(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	_, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "ollama", Name: "Ollama", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "http://127.0.0.1:11434/v1",
	})
	require.NoError(t, err)
	_, err = store.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "ollama", ModelName: "llama3",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contextWindow and maxTokens are required")
}

func TestHandwrittenCatalogFieldOverride(t *testing.T) {
	store := newModelStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(store.Models), 0o700))
	content := `{
  "schemaVersion": 1,
  "providers": {
    "openai": {
      "name": "OpenAI",
      "type": "openai-compatible",
      "api": "openai-completions",
      "baseUrl": "https://api.openai.com/v1",
      "credential": "openai",
      "models": [{"id": "gpt-4o", "vision": false}]
    }
  }
}`
	require.NoError(t, os.WriteFile(store.Models, []byte(content), 0o600))

	models, err := store.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	// The explicit false wins over the catalog's vision:true default.
	assert.False(t, models[0].SupportsVision)
	// Omitted fields fall back to the catalog.
	assert.Equal(t, 128000, models[0].ContextWindow)
	assert.Equal(t, 16384, models[0].MaxOutputTokens)
	assert.True(t, models[0].SupportsToolUse)
}

func TestModelStoreDegradesToSnapshotOnInvalidFile(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	_, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "deepseek", Name: "DeepSeek", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com",
	})
	require.NoError(t, err)
	_, err = store.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "deepseek", ModelName: "deepseek-chat",
	})
	require.NoError(t, err)

	// Corrupt the file after a valid snapshot exists.
	require.NoError(t, os.WriteFile(store.Models, []byte(`{invalid`), 0o600))

	// Reads degrade to the last valid snapshot rather than failing.
	models, err := store.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "deepseek/deepseek-chat", models[0].ID)

	// Writes fail closed on the invalid file and do not overwrite it.
	_, err = store.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "deepseek", ModelName: "deepseek-reasoner",
	})
	require.Error(t, err)
	raw, readErr := os.ReadFile(store.Models)
	require.NoError(t, readErr)
	assert.Equal(t, `{invalid`, string(raw))
}

func TestModelStoreFailsReadWithoutSnapshot(t *testing.T) {
	store := newModelStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(store.Models), 0o700))
	require.NoError(t, os.WriteFile(store.Models, []byte(`{invalid`), 0o600))
	_, err := store.ListModels(context.Background())
	require.Error(t, err)
}

func TestModelStoreRecoversAfterFileRepair(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	_, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "deepseek", Name: "DeepSeek", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com",
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.Models, []byte(`{invalid`), 0o600))
	models, err := store.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, models, 0) // no model was created; degraded snapshot has no models

	// Repair the file; the next read picks up the repaired content.
	require.NoError(t, os.WriteFile(store.Models, []byte(`{"schemaVersion":1,"providers":{"deepseek":{"name":"DeepSeek","type":"openai-compatible","api":"openai-completions","baseUrl":"https://api.deepseek.com","credential":"deepseek","models":[{"id":"deepseek-chat"}]}}}`), 0o600))
	models, err = store.ListModels(ctx)
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "deepseek/deepseek-chat", models[0].ID)
}

func TestCreateAnthropicProviderSetsAPI(t *testing.T) {
	store := newModelStore(t)
	ctx := context.Background()
	provider, err := store.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "anthropic", Name: "Anthropic", ProviderType: domain.ProviderAnthropic,
		BaseURL: "https://api.anthropic.com",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.ProviderAnthropic, provider.ProviderType)
	assert.Equal(t, domain.APIAnthropicMessages, provider.API)
}

func TestHandwrittenAnthropicProviderResolves(t *testing.T) {
	store := newModelStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(store.Models), 0o700))
	content := `{
  "schemaVersion": 1,
  "providers": {
    "anthropic": {
      "name": "Anthropic", "type": "anthropic", "api": "anthropic-messages",
      "baseUrl": "https://api.anthropic.com", "credential": "anthropic",
      "models": [{"id": "claude-sonnet-4-5"}]
    }
  }
}`
	require.NoError(t, os.WriteFile(store.Models, []byte(content), 0o600))

	providers, err := store.ListProviders(context.Background())
	require.NoError(t, err)
	require.Len(t, providers, 1)
	assert.Equal(t, domain.ProviderAnthropic, providers[0].ProviderType)
	assert.Equal(t, domain.APIAnthropicMessages, providers[0].API)

	models, err := store.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", models[0].ID)
}

func TestRejectsUnsupportedProviderType(t *testing.T) {
	store := newModelStore(t)
	_, err := store.CreateProvider(context.Background(), fileconfig.CreateProviderInput{
		Key: "bedrock", Name: "Bedrock", ProviderType: domain.ProviderType("bedrock"),
		BaseURL: "https://bedrock.aws.amazon.com",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported provider type")
}

func TestRejectsMismatchedTypeAndAPI(t *testing.T) {
	store := newModelStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(store.Models), 0o700))
	content := `{
  "schemaVersion": 1,
  "providers": {
    "mismatch": {
      "name": "Mismatch", "type": "openai-compatible", "api": "anthropic-messages",
      "baseUrl": "https://example.com", "credential": "mismatch", "models": []
    }
  }
}`
	require.NoError(t, os.WriteFile(store.Models, []byte(content), 0o600))
	_, err := store.ListProviders(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incompatible with type")
}

