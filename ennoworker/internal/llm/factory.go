package llm

import (
	"fmt"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ProviderConfig carries the common fields needed to construct any wire
// provider. The API field selects the protocol.
type ProviderConfig struct {
	BaseURL    string
	APIKey     Secret
	Model      string
	MaxTokens  int
	HTTPClient *http.Client
}

// NewProviderForAPI constructs a Provider for the given wire protocol.
// Unknown protocols return an explicit error rather than a fallback.
func NewProviderForAPI(api string, cfg ProviderConfig) (Provider, error) {
	switch api {
	case domain.APIOpenAICompletions:
		return NewOpenAIProvider(OpenAIConfig{
			BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model,
			MaxTokens: cfg.MaxTokens, HTTPClient: cfg.HTTPClient,
		})
	case domain.APIAnthropicMessages:
		return NewAnthropicProvider(AnthropicConfig{
			BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model,
			MaxTokens: cfg.MaxTokens, HTTPClient: cfg.HTTPClient,
		})
	default:
		return nil, fmt.Errorf("unsupported provider API %q", api)
	}
}
