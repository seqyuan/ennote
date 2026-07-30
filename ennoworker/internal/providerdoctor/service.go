package providerdoctor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
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
	Providers   ProviderStore
	Models      ModelStore
	Credentials llm.CredentialResolver
	HTTPClient  *http.Client
	Timeout     time.Duration
	Now         func() time.Time
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
		(parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		failure := domain.ProviderFailure{Category: domain.ProviderFailureConfigurationInvalid, Message: "The provider URL or type is not supported by this worker."}
		diagnostic.Failure = &failure
		diagnostic.Stages = append(diagnostic.Stages, failedStage("configuration", failure.Message, now().Sub(stageStarted)))
		return finish(), nil
	}
	diagnostic.Stages = append(diagnostic.Stages, passedStage("configuration", "Provider configuration is valid.", now().Sub(stageStarted)))

	stageStarted = now()
	secret, err := s.Credentials.Resolve(provider.CredentialRef)
	if err != nil {
		failure := llm.ClassifyProviderFailure(err)
		if failure.Category == domain.ProviderFailureUnknown {
			failure.Category = domain.ProviderFailureCredentialUnavailable
			failure.Message = "The configured credential could not be resolved."
		}
		diagnostic.Failure = &failure
		diagnostic.Stages = append(diagnostic.Stages, failedStage("credentials", failure.Message, now().Sub(stageStarted)))
		return finish(), nil
	}
	diagnostic.Stages = append(diagnostic.Stages, passedStage("credentials", "Credential reference resolved successfully.", now().Sub(stageStarted)))

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
	providerClient, err := llm.NewOpenAIProvider(llm.OpenAIConfig{
		BaseURL: provider.BaseURL, APIKey: secret, Model: model.ModelName,
		MaxTokens: min(max(model.MaxOutputTokens, 1), 8), HTTPClient: s.HTTPClient,
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
