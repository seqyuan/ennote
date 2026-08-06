package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all ennoworker configuration.
type Config struct {
	// HomeDir is the ennote home directory ($ENNOTE_HOME).
	HomeDir string
	// ListenAddr is the loopback address and port for the HTTP server.
	ListenAddr string
	// MaxConcurrentRuns limits how many agent runs can execute in parallel.
	MaxConcurrentRuns int
	// DatabasePath is the full path to the SQLite database file.
	DatabasePath string
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
		DatabasePath:      filepath.Join(home, "data", "ennote.db"),
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

// defaultSkillsDir resolves the user-installed skills root:
//  1. ENNOTE_SKILLS_DIR wins when set.
//  2. Otherwise, when the pi ecosystem global skills directory exists
//     (~/.pi/agent/skills), reuse it so marketplace-installed skills flow
//     into the ennote catalog and Role skill binding.
//  3. Otherwise fall back to $ENNOTE_HOME/skills.
func defaultSkillsDir(home string) string {
	if v := os.Getenv("ENNOTE_SKILLS_DIR"); v != "" {
		return v
	}
	if userDir, err := os.UserHomeDir(); err == nil && userDir != "" {
		pi := filepath.Join(userDir, ".pi", "agent", "skills")
		if st, statErr := os.Stat(pi); statErr == nil && st.IsDir() {
			return pi
		}
	}
	return filepath.Join(home, "skills")
}
