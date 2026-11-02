package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/runtimeinfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateLoopbackWorkerURL(t *testing.T) {
	for _, valid := range []string{"http://127.0.0.1:30145", "http://[::1]:30145"} {
		require.NoError(t, validateLoopbackWorkerURL(valid), valid)
	}
	for _, invalid := range []string{
		"https://127.0.0.1:30145", "http://localhost:30145", "http://192.0.2.10:30145",
		"http://127.0.0.1", "http://user@127.0.0.1:30145", "http://127.0.0.1:30145/path",
	} {
		assert.Error(t, validateLoopbackWorkerURL(invalid), invalid)
	}
}

func TestResolveGateAccessEnforcesLANOptInAndTLS(t *testing.T) {
	loopback, err := resolveGateAccess(gateAccessInput{Hostname: "127.0.0.1"})
	require.NoError(t, err)
	assert.False(t, loopback.TLS)
	assert.False(t, loopback.SecureCookie)

	_, err = resolveGateAccess(gateAccessInput{Hostname: "0.0.0.0"})
	require.ErrorContains(t, err, "ENNOTE_ALLOW_LAN=1")

	_, err = resolveGateAccess(gateAccessInput{Hostname: "0.0.0.0", AllowLAN: "1"})
	require.ErrorContains(t, err, "ENNOTE_ALLOW_INSECURE_LAN=1")

	insecure, err := resolveGateAccess(gateAccessInput{Hostname: "0.0.0.0", AllowLAN: "1", AllowInsecureLAN: "1"})
	require.NoError(t, err)
	assert.True(t, insecure.InsecureLAN)
	assert.False(t, insecure.SecureCookie)
	assert.Equal(t, "http", insecure.Scheme())

	tlsAccess, err := resolveGateAccess(gateAccessInput{
		Hostname: "0.0.0.0", AllowLAN: "1", TLSCert: "cert.pem", TLSKey: "key.pem",
	})
	require.NoError(t, err)
	assert.True(t, tlsAccess.TLS)
	assert.True(t, tlsAccess.SecureCookie)
	assert.False(t, tlsAccess.InsecureLAN)
	assert.Equal(t, "https", tlsAccess.Scheme())

	_, err = resolveGateAccess(gateAccessInput{Hostname: "0.0.0.0", AllowLAN: "1", TLSCert: "cert.pem"})
	require.ErrorContains(t, err, "ENNOTE_TLS_CERT and ENNOTE_TLS_KEY")
}

func TestSetSessionCookieSecureFollowsTLS(t *testing.T) {
	for _, secure := range []bool{true, false} {
		gate := &Gate{Port: "45131", SecureCookie: secure}
		recorder := httptest.NewRecorder()
		gate.setSessionCookie(recorder, "token", 60)
		cookies := recorder.Result().Cookies()
		require.Len(t, cookies, 1)
		assert.Equal(t, secure, cookies[0].Secure)
		assert.True(t, cookies[0].HttpOnly)
	}
}

func TestProbeWorkerRequiresReadyAuthenticatedMatchingInstance(t *testing.T) {
	const token = "private-bootstrap-token"
	const instanceID = "worker-instance"
	readyStatus := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health/ready":
			w.WriteHeader(readyStatus)
			fmt.Fprint(w, `{"data":{"status":"ready"}}`)
		case "/v1/runtime":
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"data":{"instanceId":%q}}`, instanceID)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	state := &runtimeinfo.WorkerState{
		Version: runtimeinfo.StateVersion, URL: server.URL, PID: os.Getpid(),
		InstanceID: instanceID, BootstrapToken: token, StartedAt: time.Now(),
	}
	require.NoError(t, probeWorker(context.Background(), state))

	wrongToken := *state
	wrongToken.BootstrapToken = "wrong-secret"
	err := probeWorker(context.Background(), &wrongToken)
	assert.ErrorContains(t, err, "HTTP 401")
	assert.NotContains(t, err.Error(), wrongToken.BootstrapToken)

	wrongInstance := *state
	wrongInstance.InstanceID = "other-instance"
	assert.ErrorContains(t, probeWorker(context.Background(), &wrongInstance), "does not match")

	readyStatus = http.StatusServiceUnavailable
	assert.ErrorContains(t, probeWorker(context.Background(), state), "HTTP 503")
}

func TestWorkerStatePathUsesRuntimeDirectory(t *testing.T) {
	home := t.TempDir()
	assert.Equal(t, filepath.Join(home, "runtime", "worker-state.json"), workerStatePath(home))
}

func TestProcessAliveRecognizesCurrentAndMissingProcess(t *testing.T) {
	assert.True(t, processAlive(os.Getpid()))
	assert.False(t, processAlive(-1))
}

// gateRequest builds a request the way a browser on the gate's own port sends
// it: Host and Origin are the loopback address. Tests that exercise the host
// guard override req.Host (and usually Origin) afterwards.
func gateRequest(method, target, body, origin, remote string) *http.Request {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:45131"+target, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = remote
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func TestGateRequiresSetupAndLoginBeforeStaticOrWorkerAccess(t *testing.T) {
	home := t.TempDir()
	staticDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "config"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("STATIC_APP"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(staticDir, "graphs.html"), []byte("GRAPHS_APP"), 0o600))
	var upstreamCalls atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		assert.Equal(t, "Bearer worker-token", r.Header.Get("Authorization"))
		assert.Equal(t, "/v1/health/ready", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"status":"ready"}}`)
	}))
	defer worker.Close()
	gate := &Gate{HomeDir: home, StaticDir: staticDir, WorkerURL: worker.URL, Token: "worker-token"}
	handler := gate.handler()

	root := httptest.NewRecorder()
	handler.ServeHTTP(root, gateRequest(http.MethodGet, "/", "", "", "127.0.0.1:9"))
	assert.Equal(t, http.StatusSeeOther, root.Code)
	assert.Equal(t, "/setup", root.Header().Get("Location"))

	setupPage := httptest.NewRecorder()
	handler.ServeHTTP(setupPage, gateRequest(http.MethodGet, "/setup", "", "", "127.0.0.1:9"))
	assert.Equal(t, http.StatusOK, setupPage.Code)
	assert.Contains(t, setupPage.Body.String(), "Set Password")

	blockedProxy := httptest.NewRecorder()
	handler.ServeHTTP(blockedProxy, gateRequest(http.MethodGet, "/api/worker/v1/health/ready", "", "", "127.0.0.1:9"))
	assert.Equal(t, http.StatusPreconditionRequired, blockedProxy.Code)
	assert.Zero(t, upstreamCalls.Load())

	foreignSetup := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"test-password"}`, "https://attacker.example", "127.0.0.1:9")
	foreignResponse := httptest.NewRecorder()
	handler.ServeHTTP(foreignResponse, foreignSetup)
	assert.Equal(t, http.StatusForbidden, foreignResponse.Code)

	lanSetup := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"test-password"}`, "http://192.0.2.10:45131", "192.0.2.10:9")
	lanSetup.Host = "192.0.2.10:45131"
	lanResponse := httptest.NewRecorder()
	handler.ServeHTTP(lanResponse, lanSetup)
	assert.Equal(t, http.StatusForbidden, lanResponse.Code)

	shortSetup := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"abcd"}`, "http://127.0.0.1:45131", "127.0.0.1:9")
	shortResponse := httptest.NewRecorder()
	handler.ServeHTTP(shortResponse, shortSetup)
	assert.Equal(t, http.StatusBadRequest, shortResponse.Code)

	setupRequest := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"test-password"}`, "http://127.0.0.1:45131", "127.0.0.1:9")
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setupRequest)
	require.Equal(t, http.StatusOK, setupResponse.Code, setupResponse.Body.String())
	authInfo, err := os.Stat(filepath.Join(home, "config", "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), authInfo.Mode().Perm())

	loginRedirect := httptest.NewRecorder()
	handler.ServeHTTP(loginRedirect, gateRequest(http.MethodGet, "/", "", "", "127.0.0.1:9"))
	assert.Equal(t, "/login", loginRedirect.Header().Get("Location"))

	loginRequest := gateRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`, "http://127.0.0.1:45131", "127.0.0.1:9")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	require.Equal(t, http.StatusOK, loginResponse.Code, loginResponse.Body.String())
	result := loginResponse.Result()
	cookies := result.Cookies()
	require.Len(t, cookies, 1)
	sessionCookie := cookies[0]
	assert.True(t, sessionCookie.HttpOnly)
	assert.False(t, sessionCookie.Secure)
	assert.Equal(t, http.SameSiteLaxMode, sessionCookie.SameSite)

	staticRequest := gateRequest(http.MethodGet, "/", "", "", "127.0.0.1:9")
	staticRequest.AddCookie(sessionCookie)
	staticResponse := httptest.NewRecorder()
	handler.ServeHTTP(staticResponse, staticRequest)
	require.Equal(t, http.StatusOK, staticResponse.Code)
	assert.Equal(t, "STATIC_APP", staticResponse.Body.String())

	graphsRequest := gateRequest(http.MethodGet, "/graphs", "", "", "127.0.0.1:9")
	graphsRequest.AddCookie(sessionCookie)
	graphsResponse := httptest.NewRecorder()
	handler.ServeHTTP(graphsResponse, graphsRequest)
	require.Equal(t, http.StatusOK, graphsResponse.Code)
	assert.Equal(t, "GRAPHS_APP", graphsResponse.Body.String())

	proxyRequest := gateRequest(http.MethodGet, "/api/worker/v1/health/ready", "", "", "127.0.0.1:9")
	proxyRequest.AddCookie(sessionCookie)
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, proxyRequest)
	require.Equal(t, http.StatusOK, proxyResponse.Code, proxyResponse.Body.String())
	assert.Equal(t, int32(1), upstreamCalls.Load())

	logoutRequest := gateRequest(http.MethodPost, "/api/auth/logout", "", "http://127.0.0.1:45131", "127.0.0.1:9")
	logoutRequest.AddCookie(sessionCookie)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	require.Equal(t, http.StatusOK, logoutResponse.Code)

	afterLogout := gateRequest(http.MethodGet, "/", "", "", "127.0.0.1:9")
	afterLogout.AddCookie(sessionCookie)
	afterLogoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(afterLogoutResponse, afterLogout)
	assert.Equal(t, http.StatusSeeOther, afterLogoutResponse.Code)
	assert.Equal(t, "/login", afterLogoutResponse.Header().Get("Location"))
}

func TestGateRejectsSetupPageFromNonLoopback(t *testing.T) {
	home := t.TempDir()
	staticDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "config"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("STATIC"), 0o600))
	gate := &Gate{HomeDir: home, StaticDir: staticDir, WorkerURL: "http://127.0.0.1:9", Token: "token"}
	handler := gate.handler()

	lanPage := httptest.NewRecorder()
	handler.ServeHTTP(lanPage, gateRequest(http.MethodGet, "/setup", "", "", "192.0.2.10:9"))
	assert.Equal(t, http.StatusForbidden, lanPage.Code)
	assert.Contains(t, lanPage.Body.String(), "localhost")
}

func TestGateLocksLoginAfterRepeatedFailures(t *testing.T) {
	home := t.TempDir()
	staticDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "config"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("STATIC"), 0o600))
	gate := &Gate{HomeDir: home, StaticDir: staticDir, WorkerURL: "http://127.0.0.1:9", Token: "token", LoginMaxFailures: 3, LoginLockout: time.Hour}
	handler := gate.handler()

	setup := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"test-password"}`, "http://127.0.0.1:45131", "127.0.0.1:9")
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setup)
	require.Equal(t, http.StatusOK, setupResponse.Code, setupResponse.Body.String())

	for i := 0; i < 3; i++ {
		wrong := gateRequest(http.MethodPost, "/api/auth/login", `{"password":"nope-nope"}`, "http://127.0.0.1:45131", "192.0.2.20:9")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, wrong)
		assert.Equal(t, http.StatusUnauthorized, resp.Code)
	}
	locked := gateRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`, "http://127.0.0.1:45131", "192.0.2.20:9")
	lockedResponse := httptest.NewRecorder()
	handler.ServeHTTP(lockedResponse, locked)
	assert.Equal(t, http.StatusTooManyRequests, lockedResponse.Code)
	assert.NotEmpty(t, lockedResponse.Header().Get("Retry-After"))
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:45131":  "127.0.0.1",
		"127.0.0.1":        "127.0.0.1",
		"LOCALHOST:45131":  "localhost",
		"localhost.":       "localhost",
		"localhost.:45131": "localhost",
		"[::1]:45131":      "::1",
		"[::1]":            "::1",
		"::1":              "::1",
		"evil.example:80":  "evil.example",
		"":                 "",
	}
	for input, want := range cases {
		assert.Equal(t, want, normalizeHost(input), input)
	}
}

func TestParseGateAllowedHostsNormalizesEntries(t *testing.T) {
	assert.Empty(t, parseGateAllowedHosts(""))
	assert.Empty(t, parseGateAllowedHosts(" , ,"))
	assert.Equal(t, []string{"ennote.example", "myhost.local"},
		parseGateAllowedHosts(" Ennote.Example:443 , myhost.local. ,"))
}

// TestGateHostAllowlist covers the DNS-rebinding hole: a rebound DNS name reaches
// the gate over 127.0.0.1 with Host == Origin == the attacker's domain, so CSRF
// and the loopback check would both pass. The host guard has to reject it first.
func TestGateHostAllowlist(t *testing.T) {
	const setupBody = `{"password":"test-password"}`
	cases := []struct {
		name          string
		bindHost      string
		allowedHosts  string
		method        string
		target        string
		host          string
		origin        string
		remote        string
		wantStatus    int
		wantBodyParts []string
	}{
		{
			name: "wildcard bind rejects rebound dns name", bindHost: "0.0.0.0",
			method: http.MethodPost, target: "/api/auth/setup", host: "evil.example",
			origin: "http://evil.example", remote: "127.0.0.1:9", wantStatus: http.StatusForbidden,
			wantBodyParts: []string{"host_not_allowed", "ENNOTE_GATE_ALLOWED_HOSTS"},
		},
		{
			name: "wildcard bind allows lan ip literal", bindHost: "0.0.0.0",
			method: http.MethodPost, target: "/api/auth/setup", host: "192.168.1.5:4123",
			origin: "http://192.168.1.5:4123", remote: "127.0.0.1:9", wantStatus: http.StatusOK,
		},
		{
			name: "wildcard bind rejects unlisted mdns name", bindHost: "0.0.0.0",
			method: http.MethodPost, target: "/api/auth/setup", host: "myhost.local:4123",
			origin: "http://myhost.local:4123", remote: "127.0.0.1:9", wantStatus: http.StatusForbidden,
			wantBodyParts: []string{"host_not_allowed"},
		},
		{
			name: "ipv6 wildcard bind behaves like ipv4 wildcard", bindHost: "::",
			method: http.MethodPost, target: "/api/auth/setup", host: "evil.example",
			origin: "http://evil.example", remote: "127.0.0.1:9", wantStatus: http.StatusForbidden,
		},
		{
			name: "allowlist admits configured dns name", bindHost: "0.0.0.0", allowedHosts: "myhost.local",
			method: http.MethodPost, target: "/api/auth/setup", host: "myhost.local:4123",
			origin: "http://myhost.local:4123", remote: "127.0.0.1:9", wantStatus: http.StatusOK,
		},
		{
			name: "allowlist entry is normalized before matching", bindHost: "0.0.0.0", allowedHosts: " MyHost.Local:443 ,",
			method: http.MethodPost, target: "/api/auth/setup", host: "myhost.local:4123",
			origin: "http://myhost.local:4123", remote: "127.0.0.1:9", wantStatus: http.StatusOK,
		},
		{
			name: "allowlist matches host without port", bindHost: "0.0.0.0", allowedHosts: "myhost.local",
			method: http.MethodPost, target: "/api/auth/setup", host: "myhost.local",
			origin: "http://myhost.local", remote: "127.0.0.1:9", wantStatus: http.StatusOK,
		},
		{
			name: "loopback bind allows localhost", bindHost: "127.0.0.1",
			method: http.MethodPost, target: "/api/auth/setup", host: "localhost:4123",
			origin: "http://localhost:4123", remote: "127.0.0.1:9", wantStatus: http.StatusOK,
		},
		{
			name: "loopback bind allows ipv4 loopback", bindHost: "127.0.0.1",
			method: http.MethodPost, target: "/api/auth/setup", host: "127.0.0.1:4123",
			origin: "http://127.0.0.1:4123", remote: "127.0.0.1:9", wantStatus: http.StatusOK,
		},
		{
			name: "loopback bind allows ipv6 loopback", bindHost: "127.0.0.1",
			method: http.MethodPost, target: "/api/auth/setup", host: "[::1]:4123",
			origin: "http://[::1]:4123", remote: "127.0.0.1:9", wantStatus: http.StatusOK,
		},
		{
			name: "trailing dot and uppercase are normalized", bindHost: "127.0.0.1",
			method: http.MethodPost, target: "/api/auth/setup", host: "LOCALHOST.:4123",
			origin: "http://LOCALHOST.:4123", remote: "127.0.0.1:9", wantStatus: http.StatusOK,
		},
		{
			name: "loopback bind rejects unlisted dns name", bindHost: "127.0.0.1",
			method: http.MethodPost, target: "/api/auth/setup", host: "evil.example",
			origin: "http://evil.example", remote: "127.0.0.1:9", wantStatus: http.StatusForbidden,
		},
		{
			name: "trailing dot does not bypass the allowlist", bindHost: "0.0.0.0",
			method: http.MethodPost, target: "/api/auth/setup", host: "evil.example.",
			origin: "http://evil.example.", remote: "127.0.0.1:9", wantStatus: http.StatusForbidden,
		},
		{
			name: "empty host is rejected", bindHost: "0.0.0.0",
			method: http.MethodPost, target: "/api/auth/setup", host: "",
			origin: "http://evil.example", remote: "127.0.0.1:9", wantStatus: http.StatusForbidden,
		},
		{
			name: "read request from rebound dns name is rejected", bindHost: "0.0.0.0",
			method: http.MethodGet, target: "/", host: "evil.example",
			remote: "127.0.0.1:9", wantStatus: http.StatusForbidden,
			wantBodyParts: []string{"host_not_allowed"},
		},
		{
			name: "host guard runs before authentication", bindHost: "0.0.0.0",
			method: http.MethodGet, target: "/api/worker/v1/health/ready", host: "evil.example",
			remote: "127.0.0.1:9", wantStatus: http.StatusForbidden,
			wantBodyParts: []string{"host_not_allowed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			staticDir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, "config"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("STATIC"), 0o600))
			gate := &Gate{
				HomeDir: home, Port: "45131", StaticDir: staticDir,
				WorkerURL: "http://127.0.0.1:9", Token: "token",
				BindHost: tc.bindHost, AllowedHosts: parseGateAllowedHosts(tc.allowedHosts),
			}
			handler := gate.handler()

			body := ""
			if tc.method == http.MethodPost {
				body = setupBody
			}
			request := gateRequest(tc.method, tc.target, body, tc.origin, tc.remote)
			request.Host = tc.host

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, tc.wantStatus, response.Code, response.Body.String())
			for _, part := range tc.wantBodyParts {
				assert.Contains(t, response.Body.String(), part)
			}
			authFile := filepath.Join(home, "config", "auth.json")
			if tc.wantStatus == http.StatusOK {
				assert.FileExists(t, authFile)
				return
			}
			assert.NoFileExists(t, authFile)
		})
	}
}
