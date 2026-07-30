//go:build integration

package providerdoctor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func liveDoctorConfig(t *testing.T) (string, string, string) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	return baseURL, apiKey, model
}

func liveDoctor(baseURL, apiKey, model string) *Service {
	return &Service{
		Providers: providerStoreStub{profile: &domain.ProviderProfile{
			ID: "provider", ProviderType: domain.ProviderOpenAICompatible,
			BaseURL: baseURL, CredentialRef: "env:ENNOTE_DOCTOR_KEY", Status: "active",
		}},
		Models: modelStoreStub{model: &domain.ModelProfile{
			ID: "model", ProviderID: "provider", ModelName: model, MaxOutputTokens: 32,
		}},
		Credentials: llm.CredentialResolver{LookupEnv: func(name string) (string, bool) {
			return apiKey, name == "ENNOTE_DOCTOR_KEY"
		}},
		Timeout: 45 * time.Second,
	}
}

func TestLiveProviderDoctorExpectedRateLimit(t *testing.T) {
	expected := domain.ProviderFailureCategory(strings.TrimSpace(os.Getenv("ENNOTE_LIVE_EXPECTED_FAILURE")))
	if expected != domain.ProviderFailureRateLimited {
		t.Skip("ENNOTE_LIVE_EXPECTED_FAILURE=rate_limited is required")
	}
	baseURL, apiKey, model := liveDoctorConfig(t)
	service := liveDoctor(baseURL, apiKey, model)
	diagnostic, err := service.Diagnose(context.Background(), "provider", "")
	require.NoError(t, err)
	assert.Equal(t, "failed", diagnostic.Status)
	require.NotNil(t, diagnostic.Failure)
	assert.Equal(t, expected, diagnostic.Failure.Category)
	assert.True(t, diagnostic.Failure.Retryable)
	assert.Equal(t, "The provider rate limit was reached.", diagnostic.Failure.Message)
	assert.NotContains(t, diagnostic.Failure.Message, apiKey)
}

func TestLiveProviderDoctorClassifiesInvalidCredential(t *testing.T) {
	baseURL, _, model := liveDoctorConfig(t)
	service := liveDoctor(baseURL, "ennote-intentionally-invalid-credential", model)
	diagnostic, err := service.Diagnose(context.Background(), "provider", "")
	require.NoError(t, err)
	assert.Equal(t, "failed", diagnostic.Status)
	require.NotNil(t, diagnostic.Failure)
	assert.Equal(t, domain.ProviderFailureAuthentication, diagnostic.Failure.Category)
	assert.False(t, diagnostic.Failure.Retryable)
	assert.NotContains(t, diagnostic.Failure.Message, "ennote-intentionally-invalid-credential")
}

func TestLiveProviderDoctorClassifiesMissingModel(t *testing.T) {
	baseURL, apiKey, _ := liveDoctorConfig(t)
	service := liveDoctor(baseURL, apiKey, "ennote-intentionally-missing-model")
	diagnostic, err := service.Diagnose(context.Background(), "provider", "")
	require.NoError(t, err)
	assert.Equal(t, "failed", diagnostic.Status)
	require.NotNil(t, diagnostic.Failure)
	assert.Equal(t, domain.ProviderFailureModelNotFound, diagnostic.Failure.Category)
	assert.False(t, diagnostic.Failure.Retryable)
}
