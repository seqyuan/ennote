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

func TestProcessAliveRecognizesCurrentAndMissingProcess(t *testing.T) {
	assert.True(t, processAlive(os.Getpid()))
	assert.False(t, processAlive(-1))
}

func TestGateRequiresSetupAndLoginBeforeStaticOrWorkerAccess(t *testing.T) {
	home := t.TempDir()
	staticDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "config"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("STATIC_APP"), 0o600))
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
	handler.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusSeeOther, root.Code)
	assert.Equal(t, "/setup", root.Header().Get("Location"))

	setupPage := httptest.NewRecorder()
	handler.ServeHTTP(setupPage, httptest.NewRequest(http.MethodGet, "/setup", nil))
	assert.Equal(t, http.StatusOK, setupPage.Code)
	assert.Contains(t, setupPage.Body.String(), "Set Password")

	blockedProxy := httptest.NewRecorder()
	handler.ServeHTTP(blockedProxy, httptest.NewRequest(http.MethodGet, "/api/worker/v1/health/ready", nil))
	assert.Equal(t, http.StatusPreconditionRequired, blockedProxy.Code)
	assert.Zero(t, upstreamCalls.Load())

	foreignSetup := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(`{"password":"test-password"}`))
	foreignSetup.Header.Set("Content-Type", "application/json")
	foreignSetup.Header.Set("Origin", "https://attacker.example")
	foreignResponse := httptest.NewRecorder()
	handler.ServeHTTP(foreignResponse, foreignSetup)
	assert.Equal(t, http.StatusForbidden, foreignResponse.Code)

	setupRequest := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(`{"password":"test-password"}`))
	setupRequest.Header.Set("Content-Type", "application/json")
	setupRequest.Header.Set("Origin", "http://example.com")
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setupRequest)
	require.Equal(t, http.StatusOK, setupResponse.Code, setupResponse.Body.String())
	authInfo, err := os.Stat(filepath.Join(home, "config", "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), authInfo.Mode().Perm())

	loginRedirect := httptest.NewRecorder()
	handler.ServeHTTP(loginRedirect, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, "/login", loginRedirect.Header().Get("Location"))

	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"test-password"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("Origin", "http://example.com")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	require.Equal(t, http.StatusOK, loginResponse.Code, loginResponse.Body.String())
	result := loginResponse.Result()
	cookies := result.Cookies()
	require.Len(t, cookies, 1)
	sessionCookie := cookies[0]
	assert.True(t, sessionCookie.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, sessionCookie.SameSite)

	staticRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	staticRequest.AddCookie(sessionCookie)
	staticResponse := httptest.NewRecorder()
	handler.ServeHTTP(staticResponse, staticRequest)
	require.Equal(t, http.StatusOK, staticResponse.Code)
	assert.Equal(t, "STATIC_APP", staticResponse.Body.String())

	proxyRequest := httptest.NewRequest(http.MethodGet, "/api/worker/v1/health/ready", nil)
	proxyRequest.AddCookie(sessionCookie)
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, proxyRequest)
	require.Equal(t, http.StatusOK, proxyResponse.Code, proxyResponse.Body.String())
	assert.Equal(t, int32(1), upstreamCalls.Load())

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	logoutRequest.Header.Set("Origin", "http://example.com")
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	require.Equal(t, http.StatusOK, logoutResponse.Code)

	afterLogout := httptest.NewRequest(http.MethodGet, "/", nil)
	afterLogout.AddCookie(sessionCookie)
	afterLogoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(afterLogoutResponse, afterLogout)
	assert.Equal(t, http.StatusSeeOther, afterLogoutResponse.Code)
	assert.Equal(t, "/login", afterLogoutResponse.Header().Get("Location"))
}
