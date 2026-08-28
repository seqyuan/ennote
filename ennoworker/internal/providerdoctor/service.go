package providerdoctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/ssrf"
)

var (
	ErrProviderNotFound = errors.New("provider profile not found")
	ErrModelNotFound    = errors.New("model profile not found")
	ErrModelMismatch    = errors.New("model profile belongs to another provider")
)

type ProviderStore interface {
	FindByID(context.Context, string) (*domain.ProviderProfile, error)
}

type ModelStore interface {
	FindByID(context.Context, string) (*domain.ModelProfile, error)
	FirstByProvider(context.Context, string) (*domain.ModelProfile, error)
}

type Service struct {
	Providers  ProviderStore
	Models     ModelStore
	HTTPClient *http.Client
	Timeout    time.Duration
	Now        func() time.Time
}

func (s *Service) Diagnose(ctx context.Context, providerID, modelProfileID string) (domain.ProviderDiagnostic, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	started := now()
	diagnostic := domain.ProviderDiagnostic{
		ProviderID: providerID,
		Status:     "failed",
		Stages:     []domain.ProviderDiagnosticStage{},
		TestedAt:   started.UTC(),
	}
	finish := func() domain.ProviderDiagnostic {
		diagnostic.LatencyMS = max(now().Sub(started).Milliseconds(), 0)
		return diagnostic
	}

	stageStarted := now()
	provider, err := s.Providers.FindByID(ctx, providerID)
	if err != nil {
		return diagnostic, err
	}
	if provider == nil || provider.Status != "active" {
		return diagnostic, ErrProviderNotFound
	}
	parsedURL, parseErr := url.Parse(provider.BaseURL)
	if provider.ProviderType != domain.ProviderOpenAICompatible || parseErr != nil ||
		ssrf.ValidateProviderURL(provider.BaseURL) != nil ||
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		failure := domain.ProviderFailure{Category: domain.ProviderFailureConfigurationInvalid, Message: "The provider URL or type is not supported by this worker."}
		diagnostic.Failure = &failure
		diagnostic.Stages = append(diagnostic.Stages, failedStage("configuration", failure.Message, now().Sub(stageStarted)))
		return finish(), nil
	}
	diagnostic.Stages = append(diagnostic.Stages, passedStage("configuration", "Provider configuration is valid.", now().Sub(stageStarted)))

	stageStarted = now()
	if strings.TrimSpace(provider.APIKey) == "" {
		failure := domain.ProviderFailure{Category: domain.ProviderFailureCredentialUnavailable, Message: "No API key is configured for this provider."}
		diagnostic.Failure = &failure
		diagnostic.Stages = append(diagnostic.Stages, failedStage("credentials", failure.Message, now().Sub(stageStarted)))
		return finish(), nil
	}
	diagnostic.Stages = append(diagnostic.Stages, passedStage("credentials", "API key configured.", now().Sub(stageStarted)))

	stageStarted = now()
	var model *domain.ModelProfile
	if modelProfileID != "" {
		model, err = s.Models.FindByID(ctx, modelProfileID)
		if err != nil {
			return diagnostic, err
		}
		if model == nil {
			return diagnostic, ErrModelNotFound
		}
		if model.ProviderID != provider.ID {
			return diagnostic, ErrModelMismatch
		}
	} else {
		model, err = s.Models.FirstByProvider(ctx, provider.ID)
		if err != nil {
			return diagnostic, err
		}
		if model == nil {
			failure := domain.ProviderFailure{Category: domain.ProviderFailureConfigurationInvalid, Message: "Add an active model profile before testing this provider."}
			diagnostic.Failure = &failure
			diagnostic.Stages = append(diagnostic.Stages, failedStage("model", failure.Message, now().Sub(stageStarted)))
			return finish(), nil
		}
	}
	diagnostic.ModelProfileID = model.ID
	diagnostic.ModelName = model.ModelName
	diagnostic.Stages = append(diagnostic.Stages, passedStage("model", "Model profile is available.", now().Sub(stageStarted)))

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpClient := s.HTTPClient
	if httpClient == nil {
		pinned, clientErr := ssrf.ClientForURL(probeCtx, provider.BaseURL, timeout)
		if clientErr != nil {
			failure := domain.ProviderFailure{Category: domain.ProviderFailureConfigurationInvalid, Message: "The provider URL is not reachable from this worker."}
			diagnostic.Failure = &failure
			diagnostic.Stages = append(diagnostic.Stages, failedStage("generation", failure.Message, 0))
			return finish(), nil
		}
		httpClient = pinned
	}
	api := provider.API
	if api == "" {
		api = domain.APIOpenAICompletions
	}
	providerClient, err := llm.NewProviderForAPI(api, llm.ProviderConfig{
		BaseURL: provider.BaseURL, APIKey: llm.NewSecret(provider.APIKey), Model: model.ModelName,
		MaxTokens: min(max(model.MaxOutputTokens, 1), 8), HTTPClient: httpClient,
	})
	if err != nil {
		failure := domain.ProviderFailure{Category: domain.ProviderFailureConfigurationInvalid, Message: "The provider runtime configuration is invalid."}
		diagnostic.Failure = &failure
		diagnostic.Stages = append(diagnostic.Stages, failedStage("generation", failure.Message, 0))
		return finish(), nil
	}
	stageStarted = now()
	_, err = providerClient.Stream(probeCtx, domain.CompletionRequest{
		Messages:  []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Reply with OK."}}}},
		MaxTokens: min(max(model.MaxOutputTokens, 1), 8),
	}, llm.NopSink{})
	if err != nil {
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
		failure := llm.ClassifyProviderFailure(err)
		diagnostic.Failure = &failure
		diagnostic.Stages = append(diagnostic.Stages, failedStage("generation", failure.Message, now().Sub(stageStarted)))
		return finish(), nil
	}
	diagnostic.Stages = append(diagnostic.Stages, passedStage("generation", "Minimal generation completed successfully.", now().Sub(stageStarted)))
	diagnostic.Status = "ready"
	return finish(), nil
}

func passedStage(name, message string, elapsed time.Duration) domain.ProviderDiagnosticStage {
	return domain.ProviderDiagnosticStage{Name: name, Status: "passed", Message: message, LatencyMS: max(elapsed.Milliseconds(), 0)}
}

func failedStage(name, message string, elapsed time.Duration) domain.ProviderDiagnosticStage {
	return domain.ProviderDiagnosticStage{Name: name, Status: "failed", Message: message, LatencyMS: max(elapsed.Milliseconds(), 0)}
}

// DiscoveredModel is a single entry from an OpenAI-compatible /models catalog.
type DiscoveredModel struct {
	ModelName        string `json:"modelName"`
	DisplayName      string `json:"displayName,omitempty"`
	ContextWindow    int    `json:"contextWindow,omitempty"`
	MaxOutputTokens  int    `json:"maxOutputTokens,omitempty"`
	SupportsVision   bool   `json:"supportsVision,omitempty"`
	SupportsThinking bool   `json:"supportsThinking,omitempty"`
}

// DiscoverInput carries the endpoint and optional key for a catalog fetch.
type DiscoverInput struct {
	BaseURL string
	APIKey  string
}

// DiscoverModels fetches the model catalog from an OpenAI-compatible provider
// (GET {baseURL}/models). The API key is injected per request and never
// returned to callers.
func (s *Service) DiscoverModels(ctx context.Context, input DiscoverInput) ([]DiscoveredModel, error) {
	base := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if err := ssrf.ValidateProviderURL(base); err != nil {
		return nil, err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build model catalog request: %w", err)
	}
	if key := strings.TrimSpace(input.APIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	client := s.HTTPClient
	if client == nil {
		pinned, clientErr := ssrf.ClientForURL(fetchCtx, base, timeout)
		if clientErr != nil {
			return nil, clientErr
		}
		client = pinned
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch model catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("model catalog responded HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode model catalog: %w", err)
	}
	models := make([]DiscoveredModel, 0, len(envelope.Data))
	for _, entry := range envelope.Data {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		models = append(models, DiscoveredModel{ModelName: id})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("provider returned an empty model catalog")
	}
	return models, nil
}
