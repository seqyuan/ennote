package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/storage"
)

// Config holds all ennoworker configuration.
type Config struct {
	// HomeDir is the ennote home directory ($ENNOTE_HOME).
	HomeDir string
	// ListenAddr is the loopback address and port for the HTTP server.
	ListenAddr string
	// MaxConcurrentRuns limits how many agent runs can execute in parallel.
	MaxConcurrentRuns int
	// Layout contains all V2 storage paths rooted under HomeDir.
	Layout storage.Layout
	// SandboxMode controls the workspace isolation strategy.
	SandboxMode string
	// LogLevel controls logging verbosity.
	LogLevel string
	// BootstrapToken authenticates the local BFF to the worker.
	BootstrapToken string
	// SkillsDir is the user-installed skills directory.
	SkillsDir string
	// BuiltinSkillsDir is the built-in skills directory.
	BuiltinSkillsDir string
	// HostRoots are extra directories outside the process user's home that the
	// host directory picker may browse and create projects in
	// ($ENNOTE_HOST_ROOTS). Home is always browsable; the entries widen the
	// confinement, they never replace it.
	HostRoots []string
}

// Load reads configuration from environment variables, returning
// a validated Config or an error.
func Load() (*Config, error) {
	home := os.Getenv("ENNOTE_HOME")
	if home == "" {
		userDir, _ := os.UserHomeDir()
		if userDir == "" {
			userDir = "/tmp"
		}
		home = filepath.Join(userDir, ".ennote")
	}
	home = filepath.Clean(home)

	port := os.Getenv("ENNOTE_PORT")
	if port == "" {
		port = "0"
	}

	cfg := &Config{
		HomeDir:           home,
		ListenAddr:        fmt.Sprintf("127.0.0.1:%s", port),
		MaxConcurrentRuns: 2,
		Layout:            storage.ForHome(home),
		SandboxMode:       sandboxMode(),
		LogLevel:          logLevel(),
		BootstrapToken:    os.Getenv("ENNOTE_BOOTSTRAP_TOKEN"),
		SkillsDir:         defaultSkillsDir(home),
		BuiltinSkillsDir:  os.Getenv("ENNOTE_BUILTIN_SKILLS_DIR"),
	}

	if _, err := ParseLogLevel(cfg.LogLevel); err != nil {
		return nil, err
	}

	if v := os.Getenv("ENNOTE_MAX_CONCURRENT_RUNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("max concurrent runs must be >= 1, got %q", v)
		}
		cfg.MaxConcurrentRuns = n
	}

	roots, err := hostRoots()
	if err != nil {
		return nil, err
	}
	cfg.HostRoots = roots

	return cfg, nil
}

// hostRoots reads ENNOTE_HOST_ROOTS: operating-system path-list separated extra
// roots for the host directory picker ("a:b" on Unix, "a;b" on Windows).
//
// An entry must be absolute. A relative entry would be resolved against the
// Worker's working directory, so the same configuration would confine the picker
// to a different root depending on how ennogate was started — the one failure mode
// worth refusing at startup, because the confinement is the only thing between the
// UI and the rest of the filesystem. Missing directories are accepted here: a
// root can be a mount that appears later, and every request re-resolves it.
func hostRoots() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv("ENNOTE_HOST_ROOTS"))
	if raw == "" {
		return nil, nil
	}
	roots := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, entry := range filepath.SplitList(raw) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !filepath.IsAbs(entry) {
			return nil, fmt.Errorf("ENNOTE_HOST_ROOTS entry %q must be an absolute path", entry)
		}
		cleaned := filepath.Clean(entry)
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		roots = append(roots, cleaned)
	}
	return roots, nil
}

func sandboxMode() string {
	if v, ok := os.LookupEnv("ENNOTE_SANDBOX"); ok {
		return v
	}
	return "bwrap"
}

// ParseLogLevel maps ENNOTE_LOG_LEVEL onto a slog level. Unknown values fail
// startup instead of silently falling back to info.
func ParseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid ENNOTE_LOG_LEVEL %q (want debug, info, warn, or error)", value)
	}
}

func logLevel() string {
	if v, ok := os.LookupEnv("ENNOTE_LOG_LEVEL"); ok {
		return v
	}
	return "info"
}

func envDefault(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// defaultSkillsDir resolves the ennote default user skills root:
// ENNOTE_SKILLS_DIR wins when set, otherwise $ENNOTE_HOME/skills.
// Other ecosystems (pi/claude/codex/cursor) are configured as additional
// roots through the Skills settings and never replace this default.
func defaultSkillsDir(home string) string {
	if v := os.Getenv("ENNOTE_SKILLS_DIR"); v != "" {
		return v
	}
	return filepath.Join(home, "skills")
}
