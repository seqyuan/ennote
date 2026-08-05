package mcpclient

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// Session is the connection boundary used by both browse (settings) and Run
// connections. It owns the underlying process or HTTP transport and the SDK
// session; Close is idempotent.
type Session struct {
	Transport  string
	Endpoint   string
	Executable string
	session    *mcp.ClientSession
	cmd        *exec.Cmd
	procDone   chan struct{}
	done       chan struct{}
	closeOnce  chan struct{}
}

// ConnectOption carries per-connection credential resolution and environment
// policy. Values are resolved exactly once at process/request construction and
// never logged or persisted.
type ConnectOption struct {
	// ResolveSecret resolves a credential ref (env:/file:/keyring:).
	ResolveSecret func(ref string) (string, error)
	// Logger is optional; nil disables SDK logging.
	Logger *slog.Logger
	// EnvBlacklist names environment variables that must never reach stdio
	// servers. Ennote secrets are always excluded by default.
	EnvBlacklist []string
	// MaxConnectTime bounds handshake + initialize.
	MaxConnectTime time.Duration
	// AllowedPrivateNetworks explicitly permits private/link-local endpoints
	// for HTTP transports. Loopback is always permitted for managed profiles.
	AllowedPrivateNetworks bool
	// OnToolListChanged is invoked when the server notifies a tools/list
	// change. It only marks future catalogs stale; active Runs are never
	// hot-updated. May be nil.
	OnToolListChanged func()
}

// Connect dials an MCP server from an immutable profile version. It performs
// initialize/negotiation and returns a live session. The caller must Close it.
//
// The SDK binds the provided context to the connection lifecycle (notably the
// legacy SSE GET stream), so the caller's ctx governs the connection lifetime:
// canceling it closes the transport. MaxConnectTime only bounds the
// handshake+initialize phase via a timed select; a successful connect never
// cancels the context (that would immediately kill an SSE stream).
func Connect(ctx context.Context, v *domain.MCPServerProfileVersion, opts ConnectOption) (*Session, error) {
	if v == nil {
		return nil, fmt.Errorf("mcp profile version is required")
	}
	if opts.MaxConnectTime <= 0 {
		opts.MaxConnectTime = 30 * time.Second
	}
	connectCtx, cancel := context.WithCancel(ctx)
	// Deferred via closure so a successful connect can swap cancel for a no-op
	// (the SDK binds the connect context to the transport lifecycle; cancelling
	// would immediately kill an SSE stream). Error paths call cancel explicitly
	// and the no-op swap makes the deferred call harmless.
	defer func() { cancel() }()

	s := &Session{Transport: v.Transport, Endpoint: v.Endpoint, Executable: v.Executable,
		closeOnce: make(chan struct{}), done: make(chan struct{}), procDone: make(chan struct{})}
	clientOpts := &mcp.ClientOptions{Logger: opts.Logger}
	if opts.OnToolListChanged != nil {
		clientOpts.ToolListChangedHandler = func(_ context.Context, _ *mcp.ToolListChangedRequest) {
			opts.OnToolListChanged()
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "ennote-worker", Version: "1"}, clientOpts)

	var transport mcp.Transport
	switch v.Transport {
	case domain.MCPTransportStdio:
		if err := validateStdioConfig(v); err != nil {
			return nil, err
		}
		cmd, err := buildStdioCmd(v, opts)
		if err != nil {
			return nil, err
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("stdin pipe: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("stdout pipe: %w", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, fmt.Errorf("stderr pipe: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start mcp server: %w", err)
		}
		s.cmd = cmd
		// Drain stderr in the background, bounded and discarded.
		go drainBounded(stderr)
		go func() {
			_ = cmd.Wait()
			close(s.procDone)
		}()
		transport = &mcp.IOTransport{Reader: stdout, Writer: stdin}
	case domain.MCPTransportStreamableHTTP:
		if err := validateHTTPConfig(v, opts); err != nil {
			return nil, err
		}
		httpClient, err := buildHTTPClient(v, opts)
		if err != nil {
			return nil, err
		}
		transport = &mcp.StreamableClientTransport{
			Endpoint:   v.Endpoint,
			HTTPClient: httpClient,
			MaxRetries: -1, // our caller owns reconnect policy
		}
	case domain.MCPTransportLegacySSE:
		if err := validateHTTPConfig(v, opts); err != nil {
			return nil, err
		}
		httpClient, err := buildHTTPClient(v, opts)
		if err != nil {
			return nil, err
		}
		transport = &mcp.SSEClientTransport{Endpoint: v.Endpoint, HTTPClient: httpClient}
	default:
		return nil, fmt.Errorf("unsupported mcp transport: %s", v.Transport)
	}

	type connectResult struct {
		session *mcp.ClientSession
		err     error
	}
	resultCh := make(chan connectResult, 1)
	go func() {
		sess, err := client.Connect(connectCtx, transport, nil)
		resultCh <- connectResult{session: sess, err: err}
	}()
	var session *mcp.ClientSession
	select {
	case res := <-resultCh:
		if res.err != nil {
			// Best-effort cleanup of any spawned process.
			cancel()
			s.Close()
			return nil, fmt.Errorf("mcp connect: %w", res.err)
		}
		session = res.session
		// The SDK binds the connect context to the transport lifecycle (the
		// SSE GET stream stays open for the lifetime of that context); the
		// caller's ctx owns the connection lifetime, so we never cancel here.
		// Parent ctx cancellation still propagates to connectCtx by inheritance.
		cancel = func() {}
	case <-time.After(opts.MaxConnectTime):
		cancel()
		s.Close()
		return nil, fmt.Errorf("mcp connect timed out after %s", opts.MaxConnectTime)
	}
	s.session = session
	// NOTE: on success we deliberately do NOT call cancel(): the SDK binds the
	// connect context to the transport lifecycle (the SSE GET stream stays
	// open for the lifetime of that context). The caller's ctx owns the
	// connection lifetime; MaxConnectTime only bounded the initialize phase.
	// Detect connection death for every transport: the SDK session Wait
	// returns when the underlying connection closes (stdio EOF, HTTP/SSE
	// transport failure, or explicit Close). This is the signal that lets
	// RunConnectionSet rebuild with a fresh generation instead of reusing a
	// dead session.
	go func() {
		if s.session != nil {
			_ = s.session.Wait()
		}
		select {
		case <-s.procDone:
			close(s.done)
		case <-s.closeOnce:
			// Explicit Close already signals done.
		case <-s.done:
		default:
			close(s.done)
		}
	}()
	return s, nil
}

// Close terminates the session and any spawned process. Idempotent. After
// Close returns, Done() is closed so connection owners can detect that the
// session must be rebuilt (with a new generation) rather than reused.
func (s *Session) Close() {
	select {
	case <-s.closeOnce:
		return
	default:
		close(s.closeOnce)
	}
	if s.session != nil {
		_ = s.session.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		// Kill the whole process group and wait for reaping.
		if s.cmd.Process.Pid > 0 {
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = s.cmd.Process.Kill()
		select {
		case <-s.procDone:
		case <-time.After(3 * time.Second):
		}
	}
	// Signal termination to connection owners (idempotent via select on done).
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// Done reports when the underlying transport has terminated.
func (s *Session) Done() <-chan struct{} { return s.done }

// Session exposes the underlying SDK session.
func (s *Session) Session() *mcp.ClientSession { return s.session }

// ListTools fetches the full bounded tool catalog with pagination caps.
func (s *Session) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	var all []*mcp.Tool
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < MaxPaginationPages; page++ {
		result, err := s.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		for _, tool := range result.Tools {
			if tool == nil {
				continue
			}
			if seen[tool.Name] {
				return nil, fmt.Errorf("duplicate tool name %q across pagination pages", tool.Name)
			}
			seen[tool.Name] = true
			all = append(all, tool)
			if len(all) > MaxCatalogTools {
				return nil, fmt.Errorf("server exposes more than %d tools", MaxCatalogTools)
			}
		}
		if result.NextCursor == "" || result.NextCursor == cursor {
			break
		}
		cursor = result.NextCursor
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("server exposed an empty tool catalog")
	}
	return all, nil
}

// CallTool invokes a tool on the session.
func (s *Session) CallTool(ctx context.Context, name string, arguments any) (*mcp.CallToolResult, error) {
	return s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
}

// validateStdioConfig enforces structured argv, never a shell string.
func validateStdioConfig(v *domain.MCPServerProfileVersion) error {
	if strings.TrimSpace(v.Executable) == "" {
		return fmt.Errorf("stdio server requires an executable")
	}
	if len(v.Argv) > MaxStdioArgv {
		return fmt.Errorf("stdio argv exceeds %d entries", MaxStdioArgv)
	}
	for _, a := range v.Argv {
		if strings.ContainsAny(a, "\x00") {
			return fmt.Errorf("stdio argv contains NUL byte")
		}
	}
	for name := range v.EnvLiterals {
		if IsSecretLikeEnvName(name) {
			return fmt.Errorf("environment variable %s must use a credential reference, not a literal value", name)
		}
	}
	return nil
}

// buildStdioCmd constructs the child process from a minimal environment: only
// explicit literals, credential-resolved values, and a small allowlist of
// benign variables. Ennote secrets are never inherited.
func buildStdioCmd(v *domain.MCPServerProfileVersion, opts ConnectOption) (*exec.Cmd, error) {
	argv := make([]string, 0, len(v.Argv)+1)
	argv = append(argv, v.Executable)
	argv = append(argv, v.Argv...)
	cmd := exec.Command(argv[0], argv[1:]...)
	if v.CWD != "" {
		cmd.Dir = v.CWD
	}
	// Minimal environment.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
	}
	if home, ok := os.LookupEnv("HOME"); ok {
		env = append(env, "HOME="+home)
	}
	if lang, ok := os.LookupEnv("LANG"); ok {
		env = append(env, "LANG="+lang)
	}
	for name, value := range v.EnvLiterals {
		if IsSecretLikeEnvName(name) {
			continue // unreachable after validateStdioConfig; defense in depth
		}
		env = append(env, name+"="+value)
	}
	for name, ref := range v.EnvCredentials {
		value, err := resolveCredential(ref, opts)
		if err != nil {
			return nil, fmt.Errorf("resolve credential for %s: %w", name, err)
		}
		env = append(env, name+"="+value)
	}
	cmd.Env = env
	// Isolate into its own process group so Close can kill the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}

// resolveCredential resolves a credential reference; nil resolver falls back
// to environment lookup only for env: refs (never file/keyring).
func resolveCredential(ref string, opts ConnectOption) (string, error) {
	if opts.ResolveSecret != nil {
		return opts.ResolveSecret(ref)
	}
	scheme, value, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || value == "" {
		return "", fmt.Errorf("invalid credential reference")
	}
	if scheme == "env" {
		secret, found := os.LookupEnv(value)
		if !found || strings.TrimSpace(secret) == "" {
			return "", fmt.Errorf("environment variable %s not found", value)
		}
		return secret, nil
	}
	return "", fmt.Errorf("credential resolver required for %s references", scheme)
}

// drainBounded consumes stderr with a byte cap for logging purposes, but keeps
// reading past the cap and discarding so the child process never blocks on a
// full stderr pipe (which would deadlock the MCP session).
func drainBounded(r io.Reader) {
	buf := make([]byte, 32*1024)
	var total int
	for {
		n, err := r.Read(buf)
		total += n
		if total > MaxStderrBytes {
			// Past the cap: keep draining to avoid pipe deadlock, discard data.
			_, _ = io.Copy(io.Discard, r)
			return
		}
		if err != nil {
			return
		}
	}
}

// validateHTTPConfig enforces HTTPS-by-default, network policy, endpoint
// bounds, and rejects URL-embedded credentials.
func validateHTTPConfig(v *domain.MCPServerProfileVersion, opts ConnectOption) error {
	if len(v.Endpoint) > MaxEndpointBytes {
		return fmt.Errorf("endpoint too long")
	}
	parsed, err := url.Parse(v.Endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("endpoint scheme must be http or https")
	}
	if err := validateEndpointNoUserinfo(v.Endpoint); err != nil {
		return err
	}
	if parsed.Scheme == "http" && !isLoopback(parsed.Hostname()) {
		return fmt.Errorf("endpoint must use https unless it is loopback")
	}
	// Resolve and validate the host NOW so DNS rebinding cannot redirect a
	// validated public endpoint to a private address at dial time.
	if err := validateResolvedHost(parsed.Hostname(), opts.AllowedPrivateNetworks, v.NetworkPolicy); err != nil {
		return err
	}
	return nil
}

// validateEndpointNoUserinfo rejects URLs that embed credentials (user:pass@)
// so secrets cannot leak into SQLite, API responses, or error messages.
func validateEndpointNoUserinfo(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint: %w", err)
	}
	if parsed.User != nil {
		return fmt.Errorf("endpoint must not embed credentials (userinfo); use headerCredentials instead")
	}
	return nil
}

// validateResolvedHost resolves a hostname and enforces the network policy on
// every resolved address. Hostnames resolving to loopback/private addresses
// are treated exactly like literal private IPs (SSRF/DNS rebinding guard).
func validateResolvedHost(host string, allowPrivate bool, networkPolicy string) error {
	if isLoopback(host) {
		return nil // loopback always allowed (managed profiles)
	}
	if ip := net.ParseIP(host); ip != nil {
		return checkIPPolicy(ip, allowPrivate, networkPolicy)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve endpoint host: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("endpoint host resolved to no addresses")
	}
	for _, ip := range ips {
		if err := checkIPPolicy(ip, allowPrivate, networkPolicy); err != nil {
			return err
		}
	}
	return nil
}

func checkIPPolicy(ip net.IP, allowPrivate bool, networkPolicy string) error {
	if isLoopback(ip.String()) {
		return nil
	}
	if isPrivate(ip.String()) && networkPolicy != "loopback" && !allowPrivate {
		return fmt.Errorf("endpoint resolves to a private network address (%s) which requires an explicit network policy", ip)
	}
	return nil
}

// buildHTTPClient builds a client with header injection (literals + resolved
// credential refs), no cross-origin redirects, DNS pinning guard, and strict
// TLS by default. Custom CA / insecure come from network policy.
func buildHTTPClient(v *domain.MCPServerProfileVersion, opts ConnectOption) (*http.Client, error) {
	parsed, err := url.Parse(v.Endpoint)
	if err != nil {
		return nil, err
	}
	host := parsed.Hostname()
	// Resolve the host ONCE during validation and reuse the pinned address at
	// dial time to defend against DNS rebinding.
	pinnedHost := host
	if ip := net.ParseIP(host); ip == nil && !isLoopback(host) {
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("resolve endpoint host: %w", err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("endpoint host resolved to no addresses")
		}
		// Prefer a globally routable address; fall back to the first result.
		pinnedHost = ips[0].String()
		for _, ip := range ips {
			if !isPrivate(ip.String()) && !ip.IsLoopback() {
				pinnedHost = ip.String()
				break
			}
		}
	}
	transport := &http.Transport{
		Proxy:               nil, // no ambient proxy: MCP endpoints are explicit
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			d := &net.Dialer{Timeout: 15 * time.Second}
			// Dial the pinned address, never a re-resolved hostname. TLS SNI and
			// the Host header still come from the request URL (original host).
			return d.DialContext(ctx, network, net.JoinHostPort(pinnedHost, port))
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(v.TimeoutMS) * time.Millisecond,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return fmt.Errorf("too many redirects")
			}
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				return fmt.Errorf("cross-origin redirect refused")
			}
			return nil
		},
	}
	// Inject literal headers and resolved credential headers. Never log them.
	headers, err := buildHeaders(v, opts)
	if err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		client.Transport = &headerInjectingTransport{base: transport, headers: headers}
	}
	return client, nil
}

// headerInjectingTransport attaches configured headers to every outbound
// request (including redirects on the same origin, which keep Host).
type headerInjectingTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t *headerInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, vs := range t.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return t.base.RoundTrip(req)
}

// buildHeaders merges literal and credential-resolved headers with a size
// cap per value.
func buildHeaders(v *domain.MCPServerProfileVersion, opts ConnectOption) (http.Header, error) {
	headers := http.Header{}
	for name, value := range v.HeaderLiterals {
		if len(value) > MaxHeaderBytes {
			return nil, fmt.Errorf("header %s exceeds size limit", name)
		}
		headers.Set(name, value)
	}
	for name, ref := range v.HeaderCreds {
		value, err := resolveCredential(ref, opts)
		if err != nil {
			return nil, fmt.Errorf("resolve credential for header %s: %w", name, err)
		}
		if len(value) > MaxHeaderBytes {
			return nil, fmt.Errorf("header %s exceeds size limit", name)
		}
		headers.Set(name, value)
	}
	return headers, nil
}

func isLoopback(host string) bool {
	if host == "localhost" || host == "::1" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func isPrivate(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false // hostnames are treated as public unless loopback
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
