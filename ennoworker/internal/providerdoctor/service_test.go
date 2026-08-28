package providerdoctor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type providerStoreStub struct{ profile *domain.ProviderProfile }

func (s providerStoreStub) FindByID(context.Context, string) (*domain.ProviderProfile, error) {
	return s.profile, nil
}

type modelStoreStub struct {
	model *domain.ModelProfile
	byID  *domain.ModelProfile
}

func (s modelStoreStub) FindByID(context.Context, string) (*domain.ModelProfile, error) {
	return s.byID, nil
}
func (s modelStoreStub) FirstByProvider(context.Context, string) (*domain.ModelProfile, error) {
	return s.model, nil
}

func TestDoctorCompletesMinimalGeneration(t *testing.T) {
	var receivedAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthorization = r.Header.Get("Authorization")
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"probe\",\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"probe\",\"model\":\"test-model\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	service := testService(server.URL + "/v1")
	diagnostic, err := service.Diagnose(context.Background(), "provider", "")
	require.NoError(t, err)
	assert.Equal(t, "ready", diagnostic.Status)
	assert.Equal(t, "model", diagnostic.ModelProfileID)
	assert.Nil(t, diagnostic.Failure)
	require.Len(t, diagnostic.Stages, 4)
	assert.Equal(t, "generation", diagnostic.Stages[3].Name)
	assert.Equal(t, "passed", diagnostic.Stages[3].Status)
	assert.Equal(t, "Bearer sk-TEST_KEY", receivedAuthorization)
}

func TestDoctorClassifiesProviderFailureSafely(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "req-doctor")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"credential secret was rejected","code":"invalid_api_key"}}`)
	}))
	defer server.Close()

	service := testService(server.URL)
	diagnostic, err := service.Diagnose(context.Background(), "provider", "")
	require.NoError(t, err)
	assert.Equal(t, "failed", diagnostic.Status)
	require.NotNil(t, diagnostic.Failure)
	assert.Equal(t, domain.ProviderFailureAuthentication, diagnostic.Failure.Category)
	assert.Equal(t, "req-doctor", diagnostic.Failure.RequestID)
	assert.NotContains(t, diagnostic.Failure.Message, "secret")
}

func TestDoctorReportsCredentialAndModelConfigurationFailures(t *testing.T) {
	service := testService("https://provider.test")
	service.Providers = providerStoreStub{profile: &domain.ProviderProfile{ID: "provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test", APIKey: "", Status: "active"}}
	diagnostic, err := service.Diagnose(context.Background(), "provider", "")
	require.NoError(t, err)
	require.NotNil(t, diagnostic.Failure)
	assert.Equal(t, domain.ProviderFailureCredentialUnavailable, diagnostic.Failure.Category)
	require.Len(t, diagnostic.Stages, 2)

	service = testService("https://provider.test")
	service.Models = modelStoreStub{byID: &domain.ModelProfile{ID: "other", ProviderID: "other-provider"}}
	_, err = service.Diagnose(context.Background(), "provider", "other")
	assert.ErrorIs(t, err, ErrModelMismatch)
}

func TestDoctorBoundsProbeTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()
	service := testService(server.URL)
	service.Timeout = 20 * time.Millisecond
	diagnostic, err := service.Diagnose(context.Background(), "provider", "")
	require.NoError(t, err)
	require.NotNil(t, diagnostic.Failure)
	assert.Equal(t, domain.ProviderFailureTimeout, diagnostic.Failure.Category)
	assert.True(t, diagnostic.Failure.Retryable)
}

func testService(baseURL string) *Service {
	return &Service{
		Providers: providerStoreStub{profile: &domain.ProviderProfile{ID: "provider", ProviderType: domain.ProviderOpenAICompatible,
			BaseURL: baseURL, APIKey: "sk-TEST_KEY", Status: "active"}},
		Models:  modelStoreStub{model: &domain.ModelProfile{ID: "model", ProviderID: "provider", ModelName: "test-model", MaxOutputTokens: 100}},
		Timeout: time.Second,
	}
}

func TestDiscoverModelsFetchesCatalog(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		assert.Equal(t, "/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o","object":"model"},{"id":"gpt-4o-mini","object":"model"}]}`)
	}))
	defer server.Close()

	service := testService(server.URL)
	models, err := service.DiscoverModels(context.Background(), DiscoverInput{BaseURL: server.URL, APIKey: "sk-discover"})
	require.NoError(t, err)
	require.Len(t, models, 2)
	assert.Equal(t, "gpt-4o", models[0].ModelName)
	assert.Equal(t, "gpt-4o-mini", models[1].ModelName)
	assert.Equal(t, "Bearer sk-discover", receivedAuth)
}

func TestDiscoverModelsRejectsBadBaseURLAndEmptyCatalog(t *testing.T) {
	service := testService("https://example.test")
	_, err := service.DiscoverModels(context.Background(), DiscoverInput{BaseURL: "not-a-url"})
	assert.ErrorContains(t, err, "absolute HTTP URL")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()
	_, err = service.DiscoverModels(context.Background(), DiscoverInput{BaseURL: server.URL})
	assert.ErrorContains(t, err, "empty model catalog")
}

func TestDiscoverModelsRejectsPrivateAndMetadataURLs(t *testing.T) {
	service := testService("https://example.test")
	_, err := service.DiscoverModels(context.Background(), DiscoverInput{BaseURL: "http://169.254.169.254/"})
	assert.Error(t, err)
	_, err = service.DiscoverModels(context.Background(), DiscoverInput{BaseURL: "https://192.168.1.10/v1"})
	assert.Error(t, err)
}

func TestDoctorRejectsPrivateProviderURL(t *testing.T) {
	service := testService("http://169.254.169.254/")
	diagnostic, err := service.Diagnose(context.Background(), "provider", "")
	require.NoError(t, err)
	assert.Equal(t, "failed", diagnostic.Status)
	require.NotNil(t, diagnostic.Failure)
	assert.Equal(t, domain.ProviderFailureConfigurationInvalid, diagnostic.Failure.Category)
}
