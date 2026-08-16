package domain

import "time"

type ProviderType string

type ThinkingEffort string

const (
	ThinkingDefault ThinkingEffort = "default"
	ThinkingLow     ThinkingEffort = "low"
	ThinkingMedium  ThinkingEffort = "medium"
	ThinkingHigh    ThinkingEffort = "high"
)

type ThinkingDialect string

const (
	ThinkingDialectNone                  ThinkingDialect = "none"
	ThinkingDialectOpenAIReasoningEffort ThinkingDialect = "openai_reasoning_effort"
)

type ReasoningConfig struct {
	Dialect ThinkingDialect
	Effort  ThinkingEffort
}

const (
	ProviderOpenAICompatible ProviderType = "openai-compatible"
	ProviderAnthropic        ProviderType = "anthropic"
)

// Wire protocol identifiers stored in ProviderConfig.API and frozen into the
// run's ModelRuntimeSnapshot.API.
const (
	APIOpenAICompletions = "openai-completions"
	APIAnthropicMessages = "anthropic-messages"
)

// SupportedAPIs lists the wire protocols a hand-declared provider may name.
func SupportedAPIs() []string {
	return []string{APIOpenAICompletions, APIAnthropicMessages}
}

// UnsupportedAPIs lists protocols Ennote deliberately refuses: they require
// provider-native authentication (SigV4, application-default credentials, or
// OAuth) that a hand-declared route cannot express, so offering them would
// hand back a provider that cannot authenticate.
func UnsupportedAPIs() []string {
	return []string{"bedrock", "vertex", "azure", "codex"}
}

type ProviderFailureCategory string

const (
	ProviderFailureConfigurationInvalid  ProviderFailureCategory = "configuration_invalid"
	ProviderFailureCredentialUnavailable ProviderFailureCategory = "credential_unavailable"
	ProviderFailureEndpointUnreachable   ProviderFailureCategory = "endpoint_unreachable"
	ProviderFailureAuthentication        ProviderFailureCategory = "authentication_failed"
	ProviderFailureModelNotFound         ProviderFailureCategory = "model_not_found"
	ProviderFailureRateLimited           ProviderFailureCategory = "rate_limited"
	ProviderFailureTimeout               ProviderFailureCategory = "request_timeout"
	ProviderFailureContextOverflow       ProviderFailureCategory = "context_overflow"
	ProviderFailureMalformedResponse     ProviderFailureCategory = "malformed_response"
	ProviderFailureInternal              ProviderFailureCategory = "provider_internal_error"
	ProviderFailureRequestRejected       ProviderFailureCategory = "request_rejected"
	ProviderFailureCancelled             ProviderFailureCategory = "cancelled"
	ProviderFailureUnknown               ProviderFailureCategory = "unknown"
)

type ProviderFailure struct {
	Category  ProviderFailureCategory `json:"category"`
	Message   string                  `json:"message"`
	Retryable bool                    `json:"retryable"`
	RequestID string                  `json:"requestId,omitempty"`
}

type ProviderDiagnosticStage struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	LatencyMS int64  `json:"latencyMs"`
}

type ProviderDiagnostic struct {
	ProviderID     string                    `json:"providerId"`
	ModelProfileID string                    `json:"modelProfileId,omitempty"`
	ModelName      string                    `json:"modelName,omitempty"`
	Status         string                    `json:"status"`
	Failure        *ProviderFailure          `json:"failure,omitempty"`
	Stages         []ProviderDiagnosticStage `json:"stages"`
	LatencyMS      int64                     `json:"latencyMs"`
	TestedAt       time.Time                 `json:"testedAt"`
}

type ProviderProfile struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	ProviderType         ProviderType `json:"providerType"`
	API                  string       `json:"api,omitempty"`
	BaseURL              string       `json:"baseUrl"`
	CredentialRef        string       `json:"-"`
	APIKey               string       `json:"apiKey,omitempty"`
	CredentialConfigured bool         `json:"credentialConfigured"`
	Proxy                string       `json:"proxy,omitempty"`
	Status               string       `json:"status"`
	CreatedAt            time.Time    `json:"createdAt"`
	UpdatedAt            time.Time    `json:"updatedAt"`
}

type ModelProfile struct {
	ID                            string           `json:"id"`
	ProviderID                    string           `json:"providerId"`
	ModelName                     string           `json:"modelName"`
	DisplayName                   string           `json:"displayName"`
	ContextWindow                 int              `json:"contextWindow"`
	MaxOutputTokens               int              `json:"maxOutputTokens"`
	InputCostUSDMicrosPerMillion  int64            `json:"inputCostUsdMicrosPerMillion"`
	OutputCostUSDMicrosPerMillion int64            `json:"outputCostUsdMicrosPerMillion"`
	SupportsVision                bool             `json:"supportsVision"`
	SupportsToolUse               bool             `json:"supportsToolUse"`
	SupportsThinking              bool             `json:"supportsThinking"`
	ThinkingDialect               ThinkingDialect  `json:"thinkingDialect"`
	SupportedThinkingEfforts      []ThinkingEffort `json:"supportedThinkingEfforts"`
	IsDefault                     bool             `json:"isDefault"`
	Status                        string           `json:"status"`
	CreatedAt                     time.Time        `json:"createdAt"`
	UpdatedAt                     time.Time        `json:"updatedAt"`
}
