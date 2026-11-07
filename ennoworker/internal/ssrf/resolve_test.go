package ssrf

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDNS is a DNS table a test can lie with: a name that resolves into the
// private space is exactly the case the address check exists for, and no public
// resolver will produce one.
type stubDNS struct {
	answers  map[string][]netip.Addr
	nxdomain map[string]bool
}

// startStubDNS serves the table over UDP on a loopback port.
func startStubDNS(t *testing.T, stub stubDNS) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buffer := make([]byte, 512)
		for {
			read, address, readErr := conn.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			response, ok := dnsResponse(buffer[:read], stub)
			if !ok {
				continue
			}
			_, _ = conn.WriteTo(response, address)
		}
	}()
	return conn.LocalAddr().String()
}

// dnsResponse answers one query: the question echoed back plus one record per
// address of the asked-for family, with no compression beyond the question name.
// Filtering by QTYPE matters — answering an A query with AAAA records as well would
// make the dual-stack case pass for the wrong reason.
func dnsResponse(query []byte, stub stubDNS) ([]byte, bool) {
	if len(query) < 12 {
		return nil, false
	}
	// Walk the QNAME labels to find the end of the question section.
	offset := 12
	var labels []string
	for offset < len(query) {
		length := int(query[offset])
		if length == 0 {
			offset++
			break
		}
		if offset+1+length > len(query) {
			return nil, false
		}
		labels = append(labels, string(query[offset+1:offset+1+length]))
		offset += 1 + length
	}
	if offset+4 > len(query) {
		return nil, false
	}
	questionEnd := offset + 4
	name := strings.Join(labels, ".")
	wantedType := binary.BigEndian.Uint16(query[offset : offset+2])

	rcode := byte(0x80)
	addresses := make([]netip.Addr, 0, len(stub.answers[name]))
	if stub.nxdomain[name] {
		rcode = 0x83
	}
	for _, address := range stub.answers[name] {
		if (wantedType == 1 && address.Is4()) || (wantedType == 28 && address.Is6()) {
			addresses = append(addresses, address)
		}
	}

	response := make([]byte, 0, questionEnd+len(addresses)*16)
	response = append(response, query[:2]...)               // transaction id
	response = append(response, 0x81, rcode)                // QR, RD, RA, and the rcode
	response = append(response, 0x00, 0x01)                 // QDCOUNT
	response = append(response, 0x00, byte(len(addresses))) // ANCOUNT
	response = append(response, 0x00, 0x00, 0x00, 0x00)     // NSCOUNT, ARCOUNT
	response = append(response, query[12:questionEnd]...)   // question

	for _, address := range addresses {
		response = append(response, 0xc0, 0x0c) // the question's name
		if address.Is4() {
			response = append(response, 0x00, 0x01, 0x00, 0x01, 0, 0, 0, 60, 0x00, 0x04)
			response = append(response, address.AsSlice()...)
			continue
		}
		response = append(response, 0x00, 0x1c, 0x00, 0x01, 0, 0, 0, 60, 0x00, 0x10)
		response = append(response, address.AsSlice()...)
	}
	return response, true
}

// useStubResolver points the process resolver at the stub for the test's duration.
//
// This mutates a package global, so a test that uses it must not run in parallel
// with anything else in this package: a new t.Parallel here would make every test's
// lookups go to whichever stub happens to be installed.
func useStubResolver(t *testing.T, address string) {
	t.Helper()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp", address)
		},
	}
	t.Cleanup(func() { net.DefaultResolver = previous })
}

func TestResolveAndValidateAcceptsAPublicLiteral(t *testing.T) {
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		address, err := resolveAndValidate(context.Background(), raw)
		require.NoError(t, err, raw)
		assert.Equal(t, raw, address.String())
	}
}

func TestResolveAndValidateRefusesAPrivateLiteral(t *testing.T) {
	for _, raw := range []string{"10.0.0.5", "192.168.1.10", "169.254.169.254", "fd00::1", "::ffff:10.0.0.5"} {
		_, err := resolveAndValidate(context.Background(), raw)
		require.Error(t, err, raw)
		assert.Contains(t, err.Error(), "private or link-local", raw)
	}
}

func TestResolveAndValidateMapsLocalhostToLoopback(t *testing.T) {
	address, err := resolveAndValidate(context.Background(), "localhost")
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", address.String())
}

func TestResolveAndValidateReportsADNSFailure(t *testing.T) {
	// The stub says NXDOMAIN, so this does not depend on the machine's resolver or
	// on the network: a CI box behind a resolver that hijacks NXDOMAIN would
	// otherwise turn this into a false failure.
	useStubResolver(t, startStubDNS(t, stubDNS{nxdomain: map[string]bool{"nope.example.test": true}}))

	_, err := resolveAndValidate(context.Background(), "nope.example.test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DNS resolution failed")
}

func TestResolveAndValidateRefusesADNSAnswerInPrivateSpace(t *testing.T) {
	// The rebinding case: the name is public, the answer is not. Validation has to
	// happen on the resolved address, not on the hostname.
	useStubResolver(t, startStubDNS(t, stubDNS{answers: map[string][]netip.Addr{
		"metadata.example.test": {netip.MustParseAddr("169.254.169.254")},
	}}))

	_, err := resolveAndValidate(context.Background(), "metadata.example.test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolved IP 169.254.169.254")
}

func TestResolveAndValidatePrefersIPv4InADualStackAnswer(t *testing.T) {
	// A dual-stack answer that starts with IPv6 still pins an IPv4 address: the
	// dialer is handed one address, and IPv6-first is the path that usually has no
	// route.
	useStubResolver(t, startStubDNS(t, stubDNS{answers: map[string][]netip.Addr{
		"dual.example.test": {netip.MustParseAddr("2606:4700:4700::1111"), netip.MustParseAddr("1.1.1.1")},
	}}))

	address, err := resolveAndValidate(context.Background(), "dual.example.test")
	require.NoError(t, err)
	assert.Equal(t, "1.1.1.1", address.String())
}

func TestClientForURLDialsThePinnedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "reached the provider")
	}))
	defer server.Close()

	// An ambient proxy would be followed by a client that left Proxy at its zero
	// value; this one refuses to consult it, so a loopback base URL stays loopback.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")

	client, err := ClientForURL(context.Background(), server.URL+"/v1", 0)
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, client.Timeout, "an unset timeout has a bound")

	response, err := client.Get(server.URL + "/v1/models")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "reached the provider")
}

func TestClientForURLRefusesACrossOriginRedirect(t *testing.T) {
	// A provider that bounces the request to another host is how a pinned dial
	// would be defeated: the transport is already committed to one address.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "should not be reached")
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client, err := ClientForURL(context.Background(), redirector.URL, 0)
	require.NoError(t, err)

	_, err = client.Get(redirector.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-origin redirect refused")
}

func TestClientForURLRefusesARedirectIntoPrivateSpace(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTTPS, so the refusal cannot come from the cleartext rule: the address
		// itself is what has to stop the redirect.
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client, err := ClientForURL(context.Background(), redirector.URL, 0)
	require.NoError(t, err)

	_, err = client.Get(redirector.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private or link-local")
}

func TestClientForURLRefusesARedirectLoop(t *testing.T) {
	var loop *httptest.Server
	loop = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loop.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer loop.Close()

	client, err := ClientForURL(context.Background(), loop.URL, 0)
	require.NoError(t, err)

	_, err = client.Get(loop.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many redirects")
}

func TestClientForURLRejectsAMalformedURLBeforeDialing(t *testing.T) {
	for _, raw := range []string{
		"",
		"not-a-url",
		"ftp://example.com/v1",
		"https://user:password@example.com/v1",
	} {
		_, err := ClientForURL(context.Background(), raw, 0)
		assert.Error(t, err, raw)
	}
}
