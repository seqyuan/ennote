package modelcatalog

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLookupHit(t *testing.T) {
	defaults, ok := Lookup("deepseek", "deepseek-chat")
	require.True(t, ok)
	assert.Equal(t, 131072, defaults.ContextWindow)
	assert.Equal(t, 8192, defaults.MaxTokens)
	assert.False(t, defaults.Thinking)
}

func TestLookupMissProvider(t *testing.T) {
	_, ok := Lookup("ollama", "llama3")
	assert.False(t, ok)
}

func TestLookupMissModel(t *testing.T) {
	_, ok := Lookup("deepseek", "no-such-model")
	assert.False(t, ok)
}

func TestHasProvider(t *testing.T) {
	assert.True(t, HasProvider("deepseek"))
	assert.True(t, HasProvider("openai"))
	assert.True(t, HasProvider("anthropic"))
	assert.False(t, HasProvider("bedrock"))
}

func TestProviderDefaults(t *testing.T) {
	api, ok := ProviderDefaultAPI("anthropic")
	require.True(t, ok)
	assert.Equal(t, "anthropic-messages", api)

	baseURL, ok := ProviderDefaultBaseURL("openai")
	require.True(t, ok)
	assert.Equal(t, "https://api.openai.com/v1", baseURL)

	_, ok = ProviderDefaultAPI("ollama")
	assert.False(t, ok)
}

func TestParseCatalogRejectsUnknownField(t *testing.T) {
	input := `{"providers":{"deepseek":{"api":"openai-completions","baseUrl":"x","models":{"m":{"contextWindow":1,"maxTokens":1,"inputCostUsdMicrosPerMillion":0,"outputCostUsdMicrosPerMillion":0,"vision":false,"toolUse":true,"thinking":false,"thinkingDialect":"none","thinkingEfforts":["default"],"bogus":1}}}}}`
	_, err := parseCatalog([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestParseCatalogRejectsTrailingJSON(t *testing.T) {
	valid := `{"providers":{"deepseek":{"api":"openai-completions","models":{"m":{"contextWindow":1,"maxTokens":1,"vision":false,"toolUse":true,"thinking":false,"thinkingDialect":"none","thinkingEfforts":["default"]}}}}}`
	_, err := parseCatalog([]byte(valid + ` {}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one JSON value")
}

func TestEmbeddedCatalogValid(t *testing.T) {
	data, err := catalogFS.ReadFile("catalog.json")
	require.NoError(t, err)
	doc, err := parseCatalog(data)
	require.NoError(t, err)
	require.NotEmpty(t, doc.Providers)

	for providerKey, provider := range doc.Providers {
		assert.NotEmpty(t, provider.API, "provider %q api", providerKey)
		assert.NotEmpty(t, provider.Models, "provider %q models", providerKey)
		for modelID, defaults := range provider.Models {
			assert.NoError(t, validateModel(providerKey, modelID, defaults))
			// Every non-"none" dialect must be enabled and carry a default effort.
			if defaults.Thinking {
				assert.NotEqual(t, domain.ThinkingDialectNone, defaults.ThinkingDialect,
					"%s/%s thinking enabled with dialect none", providerKey, modelID)
			}
		}
	}
}

func TestAnthropicDialectNone(t *testing.T) {
	// Phase A ships the Anthropic catalog entries with thinking disabled
	// (dialect none) until Phase B adds the anthropic-messages adapter and its
	// thinking translation. This test pins that contract so a catalog edit
	// cannot silently flip a Claude model into an unimplemented dialect.
	for _, modelID := range []string{"claude-sonnet-4-5", "claude-haiku-4-5", "claude-3-7-sonnet"} {
		defaults, ok := Lookup("anthropic", modelID)
		require.True(t, ok)
		assert.False(t, defaults.Thinking, "anthropic %s must stay thinking=false in Phase A", modelID)
		assert.Equal(t, domain.ThinkingDialectNone, defaults.ThinkingDialect, "anthropic %s dialect", modelID)
	}
}

func TestModelIDsNoWhitespace(t *testing.T) {
	data, err := catalogFS.ReadFile("catalog.json")
	require.NoError(t, err)
	doc, err := parseCatalog(data)
	require.NoError(t, err)
	for providerKey, provider := range doc.Providers {
		for modelID := range provider.Models {
			assert.False(t, strings.ContainsAny(modelID, " \t\r\n"),
				"%s/%s has whitespace", providerKey, modelID)
		}
	}
}
