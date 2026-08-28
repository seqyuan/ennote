package ssrf

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateProviderURLAllowsPublicHTTPSAndLoopbackHTTP(t *testing.T) {
	for _, raw := range []string{
		"https://api.openai.com/v1",
		"http://127.0.0.1:11434/v1",
		"http://localhost:11434/v1",
		"http://[::1]:11434/v1",
	} {
		assert.NoError(t, ValidateProviderURL(raw), raw)
	}
}

func TestValidateProviderURLRejectsPrivateAndCleartext(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/v1",
		"http://169.254.169.254/latest/meta-data/",
		"https://169.254.169.254/",
		"http://192.168.1.10:11434/v1",
		"https://10.0.0.5/v1",
		"http://[fd00::1]/v1",
		"not-a-url",
		"ftp://127.0.0.1/v1",
	} {
		assert.Error(t, ValidateProviderURL(raw), raw)
	}
}

func TestIsBlockedExemptsLoopback(t *testing.T) {
	assert.False(t, IsBlocked(netip.MustParseAddr("127.0.0.1")))
	assert.False(t, IsBlocked(netip.MustParseAddr("::1")))
	assert.True(t, IsBlocked(netip.MustParseAddr("169.254.169.254")))
	assert.True(t, IsBlocked(netip.MustParseAddr("192.168.0.1")))
	assert.True(t, IsBlocked(netip.MustParseAddr("10.1.2.3")))
}

func TestClientForURLRejectsMetadataWithoutDialing(t *testing.T) {
	_, err := ClientForURL(context.Background(), "https://169.254.169.254/", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private or link-local")
	_, err = ClientForURL(context.Background(), "http://169.254.169.254/", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTPS")
}
