// Package ssrf rejects provider URLs that would let an authenticated caller
// probe private, link-local, or metadata addresses through the Worker.
// Loopback is allowed so local inference (Ollama, LM Studio) keeps working;
// non-loopback endpoints must use HTTPS.
package ssrf

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var blockedPrefixes = func() []netip.Prefix {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"fe80::/10",
		"0.0.0.0/8",
		"::/128",
		"224.0.0.0/4",
		"ff00::/8",
		"100.64.0.0/10",
		"198.18.0.0/15",
		"192.0.2.0/24",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"2001:db8::/32",
		"fc00::/7",
		"::ffff:0:0/96",
		"64:ff9b::/96",
		"2002::/16",
		"2001::/32",
		"255.255.255.255/32",
	}
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err == nil {
			out = append(out, prefix)
		}
	}
	return out
}()

// IsBlocked reports whether ip is a non-loopback address the Worker must not
// dial on behalf of a provider diagnose/discover request.
func IsBlocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
}

// HostnameIsLoopback reports whether host is localhost or a loopback IP.
func HostnameIsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// ValidateProviderURL checks scheme, host, and literal IP policy. It does not
// resolve DNS so saving a public hostname still works offline.
func ValidateProviderURL(raw string) error {
	parsed, err := parseHTTPURL(raw)
	if err != nil {
		return err
	}
	host := parsed.Hostname()
	loopback := HostnameIsLoopback(host)
	if parsed.Scheme == "http" && !loopback {
		return fmt.Errorf("provider URL must use HTTPS unless it is loopback")
	}
	if ip, err := netip.ParseAddr(host); err == nil && IsBlocked(ip) {
		return fmt.Errorf("provider URL must not target a private or link-local address")
	}
	return nil
}

// ClientForURL validates raw, resolves every answer, and returns a client that
// dials a pinned allowed address (no DNS rebinding, no ambient proxy).
func ClientForURL(ctx context.Context, raw string, timeout time.Duration) (*http.Client, error) {
	if err := ValidateProviderURL(raw); err != nil {
		return nil, err
	}
	parsed, err := parseHTTPURL(raw)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	approved, err := resolveAndValidate(ctx, parsed.Hostname())
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(approved, uint16(port)).String())
		},
		TLSHandshakeTimeout: timeout,
		ForceAttemptHTTP2:   true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if err := ValidateProviderURL(req.URL.String()); err != nil {
				return err
			}
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("cross-origin redirect refused")
			}
			return nil
		},
	}, nil
}

func parseHTTPURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("provider base URL must be an absolute HTTP URL")
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("provider base URL must be an absolute HTTP URL")
	}
	return parsed, nil
}

func resolveAndValidate(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if IsBlocked(ip) {
			return netip.Addr{}, fmt.Errorf("provider URL must not target a private or link-local address")
		}
		return ip, nil
	}
	if HostnameIsLoopback(host) {
		return netip.MustParseAddr("127.0.0.1"), nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("DNS resolution failed for %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("no IP addresses resolved for %s", host)
	}
	var first netip.Addr
	for i, addr := range addrs {
		ip, ok := netip.AddrFromSlice(addr.IP)
		if !ok {
			return netip.Addr{}, fmt.Errorf("invalid IP address resolved for %s", host)
		}
		ip = ip.Unmap()
		if IsBlocked(ip) {
			return netip.Addr{}, fmt.Errorf("resolved IP %s for %s is blocked", ip, host)
		}
		if i == 0 || (first.Is6() && ip.Is4()) {
			first = ip
		}
	}
	return first, nil
}
