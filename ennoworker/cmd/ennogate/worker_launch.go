package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// workerInputs is everything worker selection reads from the environment, passed
// explicitly so the decision is testable.
type workerInputs struct {
	// ExplicitPath is ENNOTE_WORKER_PATH: an operator's override, always honoured.
	ExplicitPath string
	// ExeDir is the directory holding the running gate binary. A released install
	// ships the worker beside it.
	ExeDir string
	// ModuleDir is the module root that owns ./cmd/ennoworker, or "" when the gate
	// does not run from the source tree.
	ModuleDir string
	// HomeDir is where a development build is written.
	HomeDir string
}

// workerChoice is the command that will run the worker.
type workerChoice struct {
	Path string
	Args []string
	// Build asks the caller to build ModuleDir's ./cmd/ennoworker into Path first.
	Build     bool
	ModuleDir string
}

// chooseWorker decides how to start the worker.
//
// It never selects the go tool. `go run` would make the gate's child the go tool
// and the worker a grandchild, so the gate could not recognize the worker's PID in
// the runtime state file and could not signal it on shutdown: the ownership check
// never matched (the gate waited out its start timeout with a healthy worker
// running) and stopping the gate orphaned the worker. Dev mode builds a real
// binary instead, which the gate owns outright.
func chooseWorker(in workerInputs) (workerChoice, error) {
	if in.ExplicitPath != "" {
		return workerChoice{Path: in.ExplicitPath}, nil
	}
	if in.ExeDir != "" {
		adjacent := filepath.Join(in.ExeDir, "ennoworker")
		if _, err := os.Stat(adjacent); err == nil {
			return workerChoice{Path: adjacent}, nil
		}
	}
	if ownsWorkerCommand(in.ModuleDir) {
		return workerChoice{
			Path:      filepath.Join(in.HomeDir, "runtime", "worker"),
			Build:     true,
			ModuleDir: in.ModuleDir,
		}, nil
	}
	return workerChoice{}, errors.New(
		"ennoworker binary not found: set ENNOTE_WORKER_PATH, install ennoworker beside ennogate, " +
			"or run the gate from the module root that contains cmd/ennoworker")
}

// ownsWorkerCommand reports whether dir is a module root that actually contains
// the worker command. A directory that merely has a go.mod belongs to some other
// module, and building cmd/ennoworker there fails with a message nobody can act on.
func ownsWorkerCommand(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "cmd", "ennoworker"))
	return err == nil && info.IsDir()
}

// findWorker resolves the worker command for this process and, in dev mode,
// builds it. The build is explicit rather than delegated to `go run` so that the
// launched process is the worker itself (see chooseWorker).
func findWorker(home string) (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	moduleDir := ""
	if wd, wdErr := os.Getwd(); wdErr == nil {
		moduleDir = wd
	}
	choice, err := chooseWorker(workerInputs{
		ExplicitPath: os.Getenv("ENNOTE_WORKER_PATH"),
		ExeDir:       filepath.Dir(exe),
		ModuleDir:    moduleDir,
		HomeDir:      home,
	})
	if err != nil {
		return "", nil, err
	}
	if !choice.Build {
		return choice.Path, choice.Args, nil
	}
	if err := buildWorker(choice.Path, choice.ModuleDir); err != nil {
		return "", nil, err
	}
	return choice.Path, choice.Args, nil
}

// buildWorker compiles the worker from the local module into target. A rebuild
// happens on every start, matching what `go run` did: local source changes take
// effect on the next gate start, and the build cache makes an unchanged rebuild
// cheap.
func buildWorker(target, moduleDir string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create worker build directory: %w", err)
	}
	command := exec.Command("go", "build", "-o", target, "./cmd/ennoworker")
	command.Dir = moduleDir
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build ennoworker from %s: %w", moduleDir, err)
	}
	return nil
}
