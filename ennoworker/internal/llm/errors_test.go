package llm

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestClassifyProviderFailure(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		category  domain.ProviderFailureCategory
		retryable bool
	}{
		{"credential", fmtCredentialNotFound(), domain.ProviderFailureCredentialUnavailable, false},
		{"authentication", &ProviderError{StatusCode: 401, Code: "invalid_api_key"}, domain.ProviderFailureAuthentication, false},
		{"model", &ProviderError{StatusCode: 400, Code: "model_not_found"}, domain.ProviderFailureModelNotFound, false},
		{"model wrapped as retryable 5xx", &ProviderError{StatusCode: 503, Code: "model_not_found", Retryable: true}, domain.ProviderFailureModelNotFound, false},
		{"missing endpoint", &ProviderError{StatusCode: 404, Code: "http_error", Message: "page not found"}, domain.ProviderFailureEndpointUnreachable, false},
		{"rate limit", &ProviderError{StatusCode: 429, Code: "rate_limit", Retryable: true}, domain.ProviderFailureRateLimited, true},
		{"provider timeout", &ProviderError{StatusCode: 408, Code: "timeout"}, domain.ProviderFailureTimeout, true},
		{"deadline", context.DeadlineExceeded, domain.ProviderFailureTimeout, true},
		{"overflow", &ProviderError{StatusCode: 400, Code: "context_length_exceeded"}, domain.ProviderFailureContextOverflow, false},
		{"internal", &ProviderError{StatusCode: 503, Code: "busy", Retryable: true}, domain.ProviderFailureInternal, true},
		{"transport", &ProviderError{Code: "transport_error", Cause: &net.DNSError{Name: "provider.test", Err: "not found"}, Retryable: true}, domain.ProviderFailureEndpointUnreachable, true},
		{"transport timeout", &ProviderError{Code: "transport_error", Cause: &net.DNSError{Name: "provider.test", Err: "timeout", IsTimeout: true}, Retryable: true}, domain.ProviderFailureTimeout, true},
		{"protocol", &ProtocolError{Message: "invalid event"}, domain.ProviderFailureMalformedResponse, false},
		{"cancelled", ErrCancelled, domain.ProviderFailureCancelled, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := ClassifyProviderFailure(test.err)
			assert.Equal(t, test.category, failure.Category)
			assert.Equal(t, test.retryable, failure.Retryable)
			assert.NotEmpty(t, failure.Message)
		})
	}
}

func TestClassifyProviderFailureCarriesRequestIDWithoutRawMessage(t *testing.T) {
	failure := ClassifyProviderFailure(&ProviderError{StatusCode: 403, Code: "forbidden", Message: "secret provider detail", RequestID: "req-123"})
	assert.Equal(t, "req-123", failure.RequestID)
	assert.NotContains(t, failure.Message, "secret provider detail")
}

func fmtCredentialNotFound() error {
	return errors.Join(ErrCredentialNotFound, errors.New("environment variable PROVIDER_KEY"))
}
