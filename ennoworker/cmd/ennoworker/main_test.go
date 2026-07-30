package main

import (
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRuntimeProviderClassifiesPreflightFailures(t *testing.T) {
	executor := &agentExecutor{}
	_, err := executor.resolveRuntimeProvider(domain.ModelRuntimeSnapshot{
		ProviderProfileID: "provider", ModelProfileID: "model", APIModel: "test-model",
		BaseURL: "https://provider.test", CredentialRef: "env:MISSING_PROVIDER_KEY",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderCredentialUnavailable, domain.ErrorCodeOf(err))

	t.Setenv("PROVIDER_KEY", "secret")
	_, err = executor.resolveRuntimeProvider(domain.ModelRuntimeSnapshot{
		ProviderProfileID: "provider", ModelProfileID: "model", APIModel: "",
		BaseURL: "https://provider.test", CredentialRef: "env:PROVIDER_KEY",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderConfigurationInvalid, domain.ErrorCodeOf(err))
}
