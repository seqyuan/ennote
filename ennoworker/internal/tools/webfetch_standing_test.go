package tools

import (
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebFetchStandingApprovalScope_OriginV1(t *testing.T) {
	tool := &WebFetchTool{}

	tests := []struct {
		name    string
		url     string
		want    domain.StandingApprovalScope
		wantErr bool
	}{
		{
			name: "basic https",
			url:  "https://example.com/a",
			want: domain.StandingApprovalScope{
				Kind:         "origin",
				ScopeVersion: 1,
				Key:          "https://example.com:443",
				Display:      "example.com (all paths)",
			},
		},
		{
			name: "http upgraded to https",
			url:  "http://example.com/b?x=1",
			want: domain.StandingApprovalScope{
				Kind:         "origin",
				ScopeVersion: 1,
				Key:          "https://example.com:443",
				Display:      "example.com (all paths)",
			},
		},
		{
			name: "explicit port 443",
			url:  "https://example.com:443/c",
			want: domain.StandingApprovalScope{
				Kind:         "origin",
				ScopeVersion: 1,
				Key:          "https://example.com:443",
				Display:      "example.com (all paths)",
			},
		},
		{
			name: "host case insensitive",
			url:  "https://Example.COM/",
			want: domain.StandingApprovalScope{
				Kind:         "origin",
				ScopeVersion: 1,
				Key:          "https://example.com:443",
				Display:      "example.com (all paths)",
			},
		},
		{
			name: "trailing dot removed",
			url:  "https://example.com./path",
			want: domain.StandingApprovalScope{
				Kind:         "origin",
				ScopeVersion: 1,
				Key:          "https://example.com:443",
				Display:      "example.com (all paths)",
			},
		},
		{
			name: "IPv4 public address",
			url:  "https://93.184.216.34/",
			want: domain.StandingApprovalScope{
				Kind:         "origin",
				ScopeVersion: 1,
				Key:          "https://93.184.216.34:443",
				Display:      "93.184.216.34 (all paths)",
			},
		},
		{
			name: "IPv6 public address",
			url:  "https://[2606:2800:220:1:248:1893:25c8:1946]/",
			want: domain.StandingApprovalScope{
				Kind:         "origin",
				ScopeVersion: 1,
				Key:          "https://[2606:2800:220:1:248:1893:25c8:1946]:443",
				Display:      "2606:2800:220:1:248:1893:25c8:1946 (all paths)",
			},
		},
		{
			name: "IPv6 compressed form",
			url:  "https://[2001:4860:4860::8888]/",
			want: domain.StandingApprovalScope{
				Kind:         "origin",
				ScopeVersion: 1,
				Key:          "https://[2001:4860:4860::8888]:443",
				Display:      "2001:4860:4860::8888 (all paths)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"url": tt.url})
			require.NoError(t, err)

			scope, err := tool.StandingApprovalScope(args)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, scope)
		})
	}
}

func TestWebFetchStandingApprovalScope_Reject(t *testing.T) {
	tool := &WebFetchTool{}

	tests := []struct {
		name string
		url  string
	}{
		// Userinfo
		{"userinfo", "https://user:pass@example.com/"},
		// Non-443 port
		{"port 8080", "https://example.com:8080/"},
		{"port 80", "http://example.com:80/"},
		// Empty host
		{"empty host", "https:///path"},
		// Relative URL (no host)
		{"relative", "/path"},
		// Legacy numeric IPv4 forms
		{"single integer IPv4", "https://2130706433/"},
		{"two-segment IPv4", "https://127.1/"},
		{"octal IPv4", "https://0177.0.0.1/"},
		{"hex IPv4", "https://0x7f000001/"},
		{"mixed hex dotted", "https://0x7f.0.0.1/"},
		{"leading zero decimal", "https://010.0.0.1/"},
		// Blocked IP literals
		{"loopback IPv4", "https://127.0.0.1/"},
		{"loopback IPv6", "https://[::1]/"},
		{"private IPv4", "https://192.168.1.1/"},
		{"link-local", "https://169.254.1.1/"},
		// IPv6 zone
		{"IPv6 zone", "https://[fe80::1%eth0]/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"url": tt.url})
			require.NoError(t, err)

			_, err = tool.StandingApprovalScope(args)
			assert.Error(t, err, "expected error for URL: %s", tt.url)
		})
	}
}

func TestLooksLikeLegacyIPv4Literal(t *testing.T) {
	tests := []struct {
		host   string
		legacy bool
	}{
		// Legacy forms — should be rejected
		{"2130706433", true},
		{"127.1", true},
		{"10.0", true},
		{"0177.0.0.1", true},
		{"0x7f000001", true},
		{"0x7f.0.0.1", true},
		{"010.0.0.1", true},
		// Valid forms — should pass through
		{"127.0.0.1", false},
		{"192.168.1.1", false},
		{"93.184.216.34", false},
		{"example.com", false},
		{"sub.example.com", false},
		{"::1", false},
		{"2606:2800:220:1:248:1893:25c8:1946", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			assert.Equal(t, tt.legacy, looksLikeLegacyIPv4Literal(tt.host),
				"looksLikeLegacyIPv4Literal(%q) = %v, want %v", tt.host, looksLikeLegacyIPv4Literal(tt.host), tt.legacy)
		})
	}
}

func TestIsBlockedIPLiteral(t *testing.T) {
	blocked := []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "fc00::1", "2001:db8::1"}
	allowed := []string{"93.184.216.34", "1.1.1.1", "8.8.8.8", "2606:2800:220:1:248:1893:25c8:1946"}

	for _, ipStr := range blocked {
		ip, _ := netip.ParseAddr(ipStr)
		require.NotZero(t, ip, "could not parse %s", ipStr)
		assert.True(t, isBlockedIPLiteral(ip.Unmap()), "expected blocked: %s", ipStr)
	}
	for _, ipStr := range allowed {
		ip, _ := netip.ParseAddr(ipStr)
		require.NotZero(t, ip, "could not parse %s", ipStr)
		assert.False(t, isBlockedIPLiteral(ip.Unmap()), "expected allowed: %s", ipStr)
	}
}

func TestRegistryResolveStandingApprovalScope(t *testing.T) {
	tool := &WebFetchTool{}
	reg, err := NewRegistry(tool)
	require.NoError(t, err)

	// web_fetch should resolve.
	args, _ := json.Marshal(map[string]string{"url": "https://example.com/"})
	scope, ok, err := reg.ResolveStandingApprovalScope("web_fetch", args)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "origin", scope.Kind)

	// Unknown tool returns ok=false.
	scope, ok, err = reg.ResolveStandingApprovalScope("nonexistent", args)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, domain.StandingApprovalScope{}, scope)

	// Non-provider tool returns ok=false. Use ReadTool (doesn't implement
	// StandingApprovalScopeProvider).
	scope, ok, err = reg.ResolveStandingApprovalScope("read", args)
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestWebFetchScopeVersioning verifies that scope version is locked at 1.
func TestWebFetchScopeVersioning(t *testing.T) {
	tool := &WebFetchTool{}
	args, _ := json.Marshal(map[string]string{"url": "https://example.com/"})
	scope, err := tool.StandingApprovalScope(args)
	require.NoError(t, err)
	assert.Equal(t, 1, scope.ScopeVersion, "web_fetch origin scope version must be 1")
}

// TestWebFetchScopePathQueryNotInKey verifies path and query are excluded from scope key.
func TestWebFetchScopePathQueryNotInKey(t *testing.T) {
	tool := &WebFetchTool{}

	a, _ := json.Marshal(map[string]string{"url": "https://example.com/a?x=1"})
	b, _ := json.Marshal(map[string]string{"url": "https://example.com/b?y=2"})

	scopeA, err := tool.StandingApprovalScope(a)
	require.NoError(t, err)
	scopeB, err := tool.StandingApprovalScope(b)
	require.NoError(t, err)

	assert.Equal(t, scopeA.Key, scopeB.Key, "different paths/queries for same origin must produce identical scope key")
}

// TestWebFetchScopeSubdomainNotEqual verifies subdomains are distinct scopes.
func TestWebFetchScopeSubdomainNotEqual(t *testing.T) {
	tool := &WebFetchTool{}

	root, _ := json.Marshal(map[string]string{"url": "https://example.com/"})
	sub, _ := json.Marshal(map[string]string{"url": "https://api.example.com/"})

	scopeRoot, err := tool.StandingApprovalScope(root)
	require.NoError(t, err)
	scopeSub, err := tool.StandingApprovalScope(sub)
	require.NoError(t, err)

	assert.NotEqual(t, scopeRoot.Key, scopeSub.Key, "subdomain must not match parent domain")
}
