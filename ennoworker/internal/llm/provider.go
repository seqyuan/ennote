package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var (
	ErrIncompleteStream = errors.New("model stream ended without a terminal marker")
	ErrCancelled        = errors.New("model request cancelled")
)

type ProtocolError struct {
	Message string
	Cause   error
}

func (e *ProtocolError) Error() string {
	if e.Cause == nil {
		return "model protocol error: " + e.Message
	}
	return fmt.Sprintf("model protocol error: %s: %v", e.Message, e.Cause)
}

func (e *ProtocolError) Unwrap() error { return e.Cause }

type Provider interface {
	Stream(context.Context, domain.CompletionRequest, StreamSink) (domain.Completion, error)
	Capabilities() ModelCapabilities
}

type StreamSink interface {
	TextDelta(string) error
	ThinkingDelta(string) error
	ToolCallDelta(ToolCallDelta) error
	Usage(domain.Usage) error
}

type ToolCallDelta struct {
	Index             int
	ID                string
	Name              string
	ArgumentsFragment string
}

type ModelCapabilities struct {
	Streaming bool
	ToolUse   bool
	Thinking  bool
	Vision    bool
}

type ProviderError struct {
	StatusCode int
	Code       string
	Message    string
	Retryable  bool
	RequestID  string
	Cause      error
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider error (%s, status %d): %s", e.Code, e.StatusCode, e.Message)
}

func (e *ProviderError) Unwrap() error { return e.Cause }

func ClassifyProviderFailure(err error) domain.ProviderFailure {
	failure := domain.ProviderFailure{Category: domain.ProviderFailureUnknown, Message: "The provider request failed."}
	if err == nil {
		return failure
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrCancelled) {
		failure.Category = domain.ProviderFailureCancelled
		failure.Message = "The provider request was cancelled."
		return failure
	}
	if errors.Is(err, context.DeadlineExceeded) {
		failure.Category = domain.ProviderFailureTimeout
		failure.Message = "The provider did not respond before the timeout."
		failure.Retryable = true
		return failure
	}
	if errors.Is(err, ErrCredentialNotFound) {
		failure.Category = domain.ProviderFailureCredentialUnavailable
		failure.Message = "The configured credential could not be resolved."
		return failure
	}
	if IsContextOverflow(err) {
		failure.Category = domain.ProviderFailureContextOverflow
		failure.Message = "The request exceeds the model context window."
		return failure
	}

	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		failure.Retryable = providerErr.Retryable
		failure.RequestID = providerErr.RequestID
		value := strings.ToLower(providerErr.Code + " " + providerErr.Message)
		var networkErr net.Error
		switch {
		case providerErr.StatusCode == http.StatusUnauthorized || providerErr.StatusCode == http.StatusForbidden:
			failure.Category = domain.ProviderFailureAuthentication
			failure.Message = "The provider rejected the configured credential."
		case containsAny(value, "model_not_found", "unknown_model", "unknown model", "model does not exist", "model not found"):
			failure.Category = domain.ProviderFailureModelNotFound
			failure.Message = "The configured model was not found by the provider."
			failure.Retryable = false
		case providerErr.StatusCode == http.StatusNotFound:
			failure.Category = domain.ProviderFailureEndpointUnreachable
			failure.Message = "The endpoint does not expose a compatible chat-completions route."
		case providerErr.StatusCode == http.StatusTooManyRequests:
			failure.Category = domain.ProviderFailureRateLimited
			failure.Message = "The provider rate limit was reached."
		case providerErr.StatusCode == http.StatusRequestTimeout || containsAny(value, "timeout", "timed out") ||
			errors.As(providerErr.Cause, &networkErr) && networkErr.Timeout():
			failure.Category = domain.ProviderFailureTimeout
			failure.Message = "The provider did not respond before the timeout."
			failure.Retryable = true
		case providerErr.StatusCode >= 500:
			failure.Category = domain.ProviderFailureInternal
			failure.Message = "The provider is temporarily unavailable."
		case providerErr.StatusCode >= 400:
			failure.Category = domain.ProviderFailureRequestRejected
			failure.Message = "The provider rejected the request."
		default:
			if providerErr.StatusCode == 0 || errors.As(providerErr.Cause, &networkErr) {
				failure.Category = domain.ProviderFailureEndpointUnreachable
				failure.Message = "The provider endpoint could not be reached."
			}
		}
		return failure
	}

	var protocolErr *ProtocolError
	if errors.As(err, &protocolErr) || errors.Is(err, ErrIncompleteStream) {
		failure.Category = domain.ProviderFailureMalformedResponse
		failure.Message = "The provider returned an invalid or incomplete response."
		return failure
	}
	return failure
}

func containsAny(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func IsContextOverflow(err error) bool {
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		return false
	}
	value := strings.ToLower(providerErr.Code + " " + providerErr.Message)
	for _, marker := range []string{"context_length_exceeded", "context window", "maximum context", "too many tokens", "prompt is too long", "request too large"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return providerErr.StatusCode == 413
}

func IsRetryable(err error) bool {
	if errors.Is(err, ErrIncompleteStream) {
		return true
	}
	var providerErr *ProviderError
	return errors.As(err, &providerErr) && providerErr.Retryable
}

type NopSink struct{}

func (NopSink) TextDelta(string) error            { return nil }
func (NopSink) ThinkingDelta(string) error        { return nil }
func (NopSink) ToolCallDelta(ToolCallDelta) error { return nil }
func (NopSink) Usage(domain.Usage) error          { return nil }
