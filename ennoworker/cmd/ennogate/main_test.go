package main

import (
	"context"
	"fmt"
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

func gateRequest(method, target, body, origin, remote string) *http.Request {
	var bodyReader *strings.Reader
	req := httptest.NewRequest(method, target, nil)
	if body != "" {
		bodyReader = strings.NewReader(body)
		req = httptest.NewRequest(method, target, bodyReader)
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

	lanSetup := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"test-password"}`, "http://example.com", "192.0.2.10:9")
	lanResponse := httptest.NewRecorder()
	handler.ServeHTTP(lanResponse, lanSetup)
	assert.Equal(t, http.StatusForbidden, lanResponse.Code)

	shortSetup := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"abcd"}`, "http://example.com", "127.0.0.1:9")
	shortResponse := httptest.NewRecorder()
	handler.ServeHTTP(shortResponse, shortSetup)
	assert.Equal(t, http.StatusBadRequest, shortResponse.Code)

	setupRequest := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"test-password"}`, "http://example.com", "127.0.0.1:9")
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setupRequest)
	require.Equal(t, http.StatusOK, setupResponse.Code, setupResponse.Body.String())
	authInfo, err := os.Stat(filepath.Join(home, "config", "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), authInfo.Mode().Perm())

	loginRedirect := httptest.NewRecorder()
	handler.ServeHTTP(loginRedirect, gateRequest(http.MethodGet, "/", "", "", "127.0.0.1:9"))
	assert.Equal(t, "/login", loginRedirect.Header().Get("Location"))

	loginRequest := gateRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`, "http://example.com", "127.0.0.1:9")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	require.Equal(t, http.StatusOK, loginResponse.Code, loginResponse.Body.String())
	result := loginResponse.Result()
	cookies := result.Cookies()
	require.Len(t, cookies, 1)
	sessionCookie := cookies[0]
	assert.True(t, sessionCookie.HttpOnly)
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

	logoutRequest := gateRequest(http.MethodPost, "/api/auth/logout", "", "http://example.com", "127.0.0.1:9")
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

	setup := gateRequest(http.MethodPost, "/api/auth/setup", `{"password":"test-password"}`, "http://example.com", "127.0.0.1:9")
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setup)
	require.Equal(t, http.StatusOK, setupResponse.Code, setupResponse.Body.String())

	for i := 0; i < 3; i++ {
		wrong := gateRequest(http.MethodPost, "/api/auth/login", `{"password":"nope-nope"}`, "http://example.com", "192.0.2.20:9")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, wrong)
		assert.Equal(t, http.StatusUnauthorized, resp.Code)
	}
	locked := gateRequest(http.MethodPost, "/api/auth/login", `{"password":"test-password"}`, "http://example.com", "192.0.2.20:9")
	lockedResponse := httptest.NewRecorder()
	handler.ServeHTTP(lockedResponse, locked)
	assert.Equal(t, http.StatusTooManyRequests, lockedResponse.Code)
	assert.NotEmpty(t, lockedResponse.Header().Get("Retry-After"))
}
