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
	assert.False(t, IsBlocked(netip.MustParseAddr("127.0.0.1"), LoopbackAllowed))
	assert.False(t, IsBlocked(netip.MustParseAddr("::1"), LoopbackAllowed))
	assert.True(t, IsBlocked(netip.MustParseAddr("169.254.169.254"), LoopbackAllowed))
	assert.True(t, IsBlocked(netip.MustParseAddr("192.168.0.1"), LoopbackAllowed))
	assert.True(t, IsBlocked(netip.MustParseAddr("10.1.2.3"), LoopbackAllowed))
}

// TestIsBlockedBlocksLoopbackUnderBlockPolicy pins the policy the web_fetch
// path relies on: a page the model was asked to read must never reach a service
// on the host itself.
func TestIsBlockedBlocksLoopbackUnderBlockPolicy(t *testing.T) {
	for _, ipStr := range []string{
		"127.0.0.1", "127.9.9.9", "::1", "::ffff:127.0.0.1",
		"10.0.0.1", "192.168.1.1", "169.254.1.1", "0.0.0.0", "224.0.0.1",
		"255.255.255.255", "fc00::1", "2001:db8::1",
	} {
		ip, err := netip.ParseAddr(ipStr)
		require.NoError(t, err, ipStr)
		assert.True(t, IsBlocked(ip, LoopbackBlocked), "expected blocked: %s", ipStr)
	}
	for _, ipStr := range []string{
		"93.184.216.34", "1.1.1.1", "8.8.8.8", "2606:2800:220:1:248:1893:25c8:1946",
	} {
		ip, err := netip.ParseAddr(ipStr)
		require.NoError(t, err, ipStr)
		assert.False(t, IsBlocked(ip, LoopbackBlocked), "expected allowed: %s", ipStr)
	}
}

func TestClientForURLRejectsMetadataWithoutDialing(t *testing.T) {
	_, err := ClientForURL(context.Background(), "https://169.254.169.254/", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private or link-local")
	_, err = ClientForURL(context.Background(), "http://169.254.169.254/", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTPS")
}
