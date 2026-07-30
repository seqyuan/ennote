package domain

import "time"

type ProviderType string

const (
	ProviderOpenAICompatible ProviderType = "openai-compatible"
)

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
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	ProviderType  ProviderType `json:"providerType"`
	BaseURL       string       `json:"baseUrl"`
	CredentialRef string       `json:"credentialRef"`
	Proxy         string       `json:"proxy,omitempty"`
	Status        string       `json:"status"`
	CreatedAt     time.Time    `json:"createdAt"`
	UpdatedAt     time.Time    `json:"updatedAt"`
}

type ModelProfile struct {
	ID               string    `json:"id"`
	ProviderID       string    `json:"providerId"`
	ModelName        string    `json:"modelName"`
	DisplayName      string    `json:"displayName"`
	ContextWindow    int       `json:"contextWindow"`
	MaxOutputTokens  int       `json:"maxOutputTokens"`
	SupportsVision   bool      `json:"supportsVision"`
	SupportsToolUse  bool      `json:"supportsToolUse"`
	SupportsThinking bool      `json:"supportsThinking"`
	IsDefault        bool      `json:"isDefault"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}
