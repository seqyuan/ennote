package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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

	if v := os.Getenv("ENNOTE_MAX_CONCURRENT_RUNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("max concurrent runs must be >= 1, got %q", v)
		}
		cfg.MaxConcurrentRuns = n
	}

	return cfg, nil
}

func sandboxMode() string {
	if v, ok := os.LookupEnv("ENNOTE_SANDBOX"); ok {
		return v
	}
	return "bwrap"
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
