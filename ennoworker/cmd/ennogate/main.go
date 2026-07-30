package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/runtimeinfo"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if err := run(); err != nil {
		slog.Error("ennogate stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	home := os.Getenv("ENNOTE_HOME")
	if home == "" {
		userDir, _ := os.UserHomeDir()
		if userDir == "" {
			userDir = "/tmp"
		}
		home = filepath.Join(userDir, ".ennote")
	}
	for _, directory := range []string{"config", "data", "logs"} {
		if err := os.MkdirAll(filepath.Join(home, directory), 0o700); err != nil {
			return fmt.Errorf("create %s directory: %w", directory, err)
		}
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "30142"
	}
	hostname := os.Getenv("ENNOTE_HOSTNAME")
	if hostname == "" {
		hostname = "127.0.0.1"
	}
	staticDir := os.Getenv("ENNOTE_STATIC_DIR")
	if staticDir == "" {
		staticDir = filepath.Join("..", "out")
	}
	if _, err := os.Stat(filepath.Join(staticDir, "index.html")); err != nil {
		return fmt.Errorf("static frontend is unavailable at %s: %w", staticDir, err)
	}

	// Claim the public port before touching the Worker lifecycle.
	listener, err := net.Listen("tcp", net.JoinHostPort(hostname, port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	worker, err := startOrReuseWorker(context.Background(), home)
	if err != nil {
		return err
	}
	defer stopManagedWorker(worker, 5*time.Second)

	gate := &Gate{
		HomeDir: home, WorkerURL: worker.state.URL,
		Token: worker.state.BootstrapToken, StaticDir: staticDir,
	}
	httpServer := &http.Server{
		Handler: gate.handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 90 * time.Second,
	}
	slog.Info("ennogate ready", "url", "http://"+net.JoinHostPort(hostname, port), "worker", worker.state.URL)

	serverErrors := make(chan error, 1)
	go func() { serverErrors <- httpServer.Serve(listener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	var runErr error
	select {
	case sig := <-signals:
		slog.Info("shutting down", "signal", sig)
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	case <-worker.exited:
		runErr = fmt.Errorf("ennoworker exited unexpectedly: %w", worker.exitError())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

type managedWorker struct {
	state   *runtimeinfo.WorkerState
	process *os.Process
	exited  <-chan struct{}
	errMu   sync.Mutex
	err     error
}

func (w *managedWorker) exitError() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.err
}

func startOrReuseWorker(ctx context.Context, home string) (*managedWorker, error) {
	statePath := filepath.Join(home, "data", "worker-state.json")
	state, err := runtimeinfo.Load(statePath)
	if err == nil {
		if !processAlive(state.PID) {
			_ = os.Remove(statePath)
		} else {
			if err := probeWorker(ctx, state); err != nil {
				return nil, fmt.Errorf("refuse to replace live ennoworker with invalid state: %w", err)
			}
			process, findErr := os.FindProcess(state.PID)
			if findErr != nil {
				return nil, fmt.Errorf("attach to ennoworker: %w", findErr)
			}
			return &managedWorker{state: state, process: process}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(statePath)
	}

	token, err := generateToken()
	if err != nil {
		return nil, err
	}
	workerPath, workerArgs := findWorker()
	command := exec.Command(workerPath, workerArgs...)
	command.Env = append(os.Environ(),
		"ENNOTE_HOME="+home,
		"ENNOTE_PORT=0",
		"ENNOTE_BOOTSTRAP_TOKEN="+token,
	)
	command.Stdout = os.Stderr
	workerStderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capture ennoworker stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start ennoworker: %w", err)
	}
	worker := &managedWorker{process: command.Process}
	exited := make(chan struct{})
	worker.exited = exited
	go func() {
		worker.errMu.Lock()
		worker.err = command.Wait()
		worker.errMu.Unlock()
		close(exited)
	}()
	go func() {
		scanner := bufio.NewScanner(workerStderr)
		for scanner.Scan() {
			slog.Info(scanner.Text(), "component", "worker")
		}
	}()

	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			stopManagedWorker(worker, 5*time.Second)
			return nil, ctx.Err()
		case <-worker.exited:
			return nil, fmt.Errorf("ennoworker exited prematurely: %w", worker.exitError())
		case <-deadline.C:
			stopManagedWorker(worker, 5*time.Second)
			return nil, errors.New("ennoworker failed to start within 30s")
		case <-ticker.C:
			state, loadErr := runtimeinfo.Load(statePath)
			if loadErr != nil || state.PID != command.Process.Pid || state.BootstrapToken != token {
				continue
			}
			if probeErr := probeWorker(ctx, state); probeErr != nil {
				continue
			}
			worker.state = state
			return worker, nil
		}
	}
}

func stopManagedWorker(worker *managedWorker, timeout time.Duration) {
	if worker == nil || worker.process == nil {
		return
	}
	if worker.exited != nil {
		select {
		case <-worker.exited:
			return
		default:
		}
	}
	_ = worker.process.Signal(syscall.SIGTERM)
	if worker.exited != nil {
		select {
		case <-worker.exited:
			return
		case <-time.After(timeout):
			_ = worker.process.Kill()
			<-worker.exited
			return
		}
	}
	deadline := time.Now().Add(timeout)
	for processAlive(worker.process.Pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(worker.process.Pid) {
		_ = worker.process.Kill()
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func probeWorker(ctx context.Context, state *runtimeinfo.WorkerState) error {
	if err := validateLoopbackWorkerURL(state.URL); err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	readyRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, state.URL+"/v1/health/ready", nil)
	if err != nil {
		return err
	}
	readyResponse, err := client.Do(readyRequest)
	if err != nil {
		return fmt.Errorf("worker readiness: %w", err)
	}
	defer readyResponse.Body.Close()
	if readyResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("worker readiness returned HTTP %d", readyResponse.StatusCode)
	}

	runtimeRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, state.URL+"/v1/runtime", nil)
	if err != nil {
		return err
	}
	runtimeRequest.Header.Set("Authorization", "Bearer "+state.BootstrapToken)
	runtimeResponse, err := client.Do(runtimeRequest)
	if err != nil {
		return fmt.Errorf("worker identity: %w", err)
	}
	defer runtimeResponse.Body.Close()
	if runtimeResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("worker identity returned HTTP %d", runtimeResponse.StatusCode)
	}
	var envelope struct {
		Data struct {
			InstanceID string `json:"instanceId"`
		} `json:"data"`
	}
	if err := json.NewDecoder(runtimeResponse.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode worker identity: %w", err)
	}
	if envelope.Data.InstanceID != state.InstanceID {
		return errors.New("worker instance identity does not match state")
	}
	return nil
}

func validateLoopbackWorkerURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil {
		return errors.New("worker URL is invalid")
	}
	address := net.ParseIP(parsed.Hostname())
	if address == nil || !address.IsLoopback() {
		return errors.New("worker URL must use a loopback IP address")
	}
	if parsed.Port() == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("worker URL contains unsupported components")
	}
	return nil
}

type Gate struct {
	HomeDir    string
	WorkerURL  string
	Token      string
	StaticDir  string
	mu         sync.Mutex
	proxy      *httputil.ReverseProxy
	proxyReady bool
}

type contextKey struct{ name string }

var authKey = contextKey{"authenticated"}

func (g *Gate) handler() http.Handler {
	mux := http.NewServeMux()

	// Authentication is owned entirely by ennogate.
	mux.HandleFunc("GET /setup", g.authPage)
	mux.HandleFunc("GET /login", g.authPage)
	mux.HandleFunc("POST /api/auth/login", g.login)
	mux.HandleFunc("POST /api/auth/logout", g.logout)
	mux.HandleFunc("GET /api/auth/status", g.authStatus)
	mux.HandleFunc("POST /api/auth/setup", g.setupPassword)

	mux.HandleFunc("/api/worker/", g.requireAuth(g.proxyWorker))
	mux.HandleFunc("GET /api/runtime", g.requireAuth(g.runtimeInfo))
	mux.HandleFunc("GET /api/health", g.gateHealth)

	files := http.FileServer(http.Dir(g.StaticDir))
	mux.Handle("/", g.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" || !strings.Contains(path, ".") {
			indexPath := filepath.Join(g.StaticDir, path, "index.html")
			if _, err := os.Stat(indexPath); err == nil {
				http.ServeFile(w, r, indexPath)
				return
			}
			http.ServeFile(w, r, filepath.Join(g.StaticDir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})))

	return g.csrfMiddleware(mux)
}

func (g *Gate) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authFile := filepath.Join(g.HomeDir, "config", "auth.json")
		_, statErr := os.Stat(authFile)
		if errors.Is(statErr, os.ErrNotExist) {
			if isAPI(r) {
				writeJSON(w, http.StatusPreconditionRequired, map[string]any{
					"error": map[string]string{"code": "setup_required", "message": "password setup is required"},
				})
				return
			}
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		if statErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "authentication state is unavailable"})
			return
		}
		cookie, err := r.Cookie("ennote_session")
		if err != nil || !validateSession(cookie.Value) {
			if isAPI(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error": map[string]string{"code": "unauthorized", "message": "login required"},
				})
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (g *Gate) authPage(w http.ResponseWriter, r *http.Request) {
	authFile := filepath.Join(g.HomeDir, "config", "auth.json")
	_, statErr := os.Stat(authFile)
	configured := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		http.Error(w, "authentication state is unavailable", http.StatusInternalServerError)
		return
	}
	if cookie, err := r.Cookie("ennote_session"); err == nil && validateSession(cookie.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.URL.Path == "/setup" && configured {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.URL.Path == "/login" && !configured {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, devHTML)
}

func (g *Gate) proxyWorker(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	if !g.proxyReady {
		target, _ := url.Parse(g.WorkerURL)
		g.proxy = httputil.NewSingleHostReverseProxy(target)
		g.proxy.ModifyResponse = func(resp *http.Response) error {
			// Remove worker auth header from responses
			resp.Header.Del("Authorization")
			return nil
		}
		g.proxyReady = true
	}
	p := g.proxy
	g.mu.Unlock()

	// Inject auth token
	r.Header.Set("Authorization", "Bearer "+g.Token)
	// Strip /api/worker prefix
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/worker")
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}

	// For SSE, don't buffer
	if r.Header.Get("Accept") == "text/event-stream" {
		w.Header().Set("X-Accel-Buffering", "no")
	}

	p.ServeHTTP(w, r)
}

func (g *Gate) login(w http.ResponseWriter, r *http.Request) {
	var input struct{ Password string }
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	authFile := filepath.Join(g.HomeDir, "config", "auth.json")
	data, err := os.ReadFile(authFile)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "no password set"})
		return
	}
	var stored struct{ Hash string }
	json.Unmarshal(data, &stored)
	if bcrypt.CompareHashAndPassword([]byte(stored.Hash), []byte(input.Password)) != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "wrong password"})
		return
	}
	token, err := generateToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to create login session"})
		return
	}
	storeSession(token)
	http.SetCookie(w, &http.Cookie{
		Name: "ennote_session", Value: token, Path: "/",
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode,
		MaxAge: 86400 * 7,
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (g *Gate) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("ennote_session"); err == nil {
		sessionStore.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: "ennote_session", Value: "", Path: "/",
		MaxAge: -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (g *Gate) authStatus(w http.ResponseWriter, r *http.Request) {
	authFile := filepath.Join(g.HomeDir, "config", "auth.json")
	_, err := os.Stat(authFile)
	requiresPassword := err == nil
	authenticated := false
	if cookie, err := r.Cookie("ennote_session"); err == nil {
		authenticated = validateSession(cookie.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requiresPassword": requiresPassword,
		"authenticated":    authenticated,
	})
}

func (g *Gate) setupPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
		return
	}
	authFile := filepath.Join(g.HomeDir, "config", "auth.json")
	if _, err := os.Stat(authFile); err == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "password already set"})
		return
	}
	var input struct{ Password string }
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Password) < 4 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "password must be at least 4 characters"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to hash password"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(authFile), 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to prepare authentication state"})
		return
	}
	encoded, err := json.Marshal(map[string]string{"hash": string(hash)})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to encode password"})
		return
	}
	if err := os.WriteFile(authFile, encoded, 0o600); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to save password"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "password set"})
}

func (g *Gate) runtimeInfo(w http.ResponseWriter, r *http.Request) {
	resp, err := httpGet(g.WorkerURL + "/v1/health/ready")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "worker unreachable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "worker is not ready"})
		return
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	writeJSON(w, http.StatusOK, map[string]any{
		"worker": body,
		"gate":   map[string]string{"staticDir": g.StaticDir},
	})
}

func (g *Gate) gateHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (g *Gate) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			origin := r.Header.Get("Origin")
			u, err := url.Parse(origin)
			if origin == "" || err != nil || u.Scheme == "" || u.Host != r.Host {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "CSRF check failed"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ----

func generateToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return hex.EncodeToString(value), nil
}

var sessionStore sync.Map

func storeSession(token string) { sessionStore.Store(token, time.Now().Add(7*24*time.Hour)) }
func validateSession(token string) bool {
	v, ok := sessionStore.Load(token)
	if !ok {
		return false
	}
	expires := v.(time.Time)
	if time.Now().After(expires) {
		sessionStore.Delete(token)
		return false
	}
	return true
}

func isAPI(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") ||
		r.Header.Get("Accept") == "application/json" ||
		r.Header.Get("Content-Type") == "application/json"
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func httpGet(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	return client.Get(url)
}

func findWorker() (string, []string) {
	// Try pre-built binary first
	if p := os.Getenv("ENNOTE_WORKER_PATH"); p != "" {
		return p, nil
	}
	// Dev mode: use go run
	if _, err := os.Stat("go.mod"); err == nil {
		return "go", []string{"run", "./cmd/ennoworker"}
	}
	// Look for ennoworker binary next to ennogate
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	workerBin := filepath.Join(dir, "ennoworker")
	if _, err := os.Stat(workerBin); err == nil {
		return workerBin, nil
	}
	return "go", []string{"run", "./cmd/ennoworker"}
}

const devHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>Ennote</title>
<style>:root{--bg:#0d1117;--bg-secondary:#161b22;--border:#30363d;--text:#e6edf3;--text-dim:#8b949e;--accent:#58a6ff;}
*{box-sizing:border-box;margin:0;padding:0;}
body{background:var(--bg);color:var(--text);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;}
.welcome{text-align:center;max-width:480px;padding:40px;}
.welcome h1{font-size:24px;margin-bottom:12px;}
.welcome p{color:var(--text-dim);margin-bottom:24px;line-height:1.6;}
.welcome input{width:100%;background:var(--bg-secondary);border:1px solid var(--border);border-radius:6px;padding:10px 14px;color:var(--text);font-size:14px;margin-bottom:12px;outline:none}
.welcome input:focus{border-color:var(--accent)}
.welcome button{width:100%;background:var(--accent);border:none;border-radius:6px;padding:10px;color:#fff;font-size:14px;font-weight:600;cursor:pointer}
.welcome button:hover{filter:brightness(1.1)}
.error{color:#f85149;margin-top:8px;font-size:13px;display:none}
</style></head><body>
<div class="welcome"><h1>Ennote</h1><p>AI-native bioinformatics agent workspace</p>
<div id="setup"><input id="pw" type="password" placeholder="Set a password (min 4 chars) to start"><button onclick="setup()">Set Password</button></div>
<div id="login" style="display:none"><input id="loginpw" type="password" placeholder="Password"><button onclick="login()">Login</button></div>
<div id="error" class="error"></div></div>
<script>
let needs="";
fetch("/api/auth/status").then(r=>r.json()).then(d=>{
  if(d.authenticated){document.body.innerHTML='<div style="text-align:center;padding:60px"><h1>Ennote</h1><p style="color:var(--text-dim)">Loading…</p></div>';window.location="/";return}
  if(d.requiresPassword){document.getElementById("setup").style.display="none";document.getElementById("login").style.display="block"}
});
function setup(){
  const pw=document.getElementById("pw").value;
  if(pw.length<4){showError("Minimum 4 characters");return}
  fetch("/api/auth/setup",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({password:pw})}).then(r=>r.json()).then(d=>{
    if(d.status==="password set"){document.getElementById("setup").style.display="none";document.getElementById("login").style.display="block"}
    else showError(d.error||"Failed")
  }).catch(e=>showError(e.message))
}
function login(){
  const pw=document.getElementById("loginpw").value;
  fetch("/api/auth/login",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({password:pw})}).then(r=>r.json()).then(d=>{
    if(d.status==="ok") window.location="/";
    else showError(d.error||"Wrong password")
  }).catch(e=>showError(e.message))
}
function showError(msg){const e=document.getElementById("error");e.textContent=msg;e.style.display="block"}
</script></body></html>`
