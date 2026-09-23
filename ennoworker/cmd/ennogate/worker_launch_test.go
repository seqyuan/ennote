package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workerModule seeds a module root that owns the worker command.
func workerModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/dsh\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "cmd", "ennoworker"), 0o700))
	return dir
}

func TestChooseWorkerPrefersAnExplicitPath(t *testing.T) {
	exeDir := t.TempDir()
	// An adjacent binary and a module root both exist, so only the explicit
	// override can win.
	require.NoError(t, os.WriteFile(filepath.Join(exeDir, "ennoworker"), []byte("x"), 0o700))
	moduleDir := workerModule(t)

	choice, err := chooseWorker(workerInputs{
		ExplicitPath: "/opt/ennoworker", ExeDir: exeDir, ModuleDir: moduleDir, HomeDir: t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, "/opt/ennoworker", choice.Path)
	assert.Empty(t, choice.Args)
	assert.False(t, choice.Build)
}

func TestChooseWorkerPrefersABinaryInstalledBesideTheGate(t *testing.T) {
	exeDir := t.TempDir()
	adjacent := filepath.Join(exeDir, "ennoworker")
	require.NoError(t, os.WriteFile(adjacent, []byte("x"), 0o700))
	moduleDir := workerModule(t)

	choice, err := chooseWorker(workerInputs{ExeDir: exeDir, ModuleDir: moduleDir, HomeDir: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, adjacent, choice.Path, "an installed binary is authoritative over a source tree")
	assert.False(t, choice.Build)
}

// The bug this covers: the go tool runs the worker as a child, so the gate could
// neither recognize the worker's PID in the state file nor signal it, which left
// the worker unowned and orphaned when the gate stopped. Dev mode therefore builds
// a real binary and launches that.
func TestChooseWorkerBuildsInDevModeInsteadOfUsingTheGoTool(t *testing.T) {
	moduleDir := workerModule(t)
	home := t.TempDir()

	choice, err := chooseWorker(workerInputs{ExeDir: t.TempDir(), ModuleDir: moduleDir, HomeDir: home})
	require.NoError(t, err)
	assert.True(t, choice.Build, "dev mode must build rather than wrap the worker in the go tool")
	assert.Equal(t, filepath.Join(home, "runtime", "worker"), choice.Path)
	assert.Empty(t, choice.Args, "a built binary takes no arguments")
	assert.Equal(t, moduleDir, choice.ModuleDir)

	// The go tool must never be the worker command: it would be the process the
	// gate owns, not the worker.
	assert.NotEqual(t, "go", filepath.Base(choice.Path))
}

// A directory that merely contains go.mod is not necessarily our module: building
// cmd/ennoworker elsewhere fails with a confusing message, so it is not a candidate.
func TestChooseWorkerIgnoresAModuleWithoutTheWorkerCommand(t *testing.T) {
	// A module root, but not ours: it has no cmd/ennoworker to build.
	unrelated := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(unrelated, "go.mod"), []byte("module example.com/other\n"), 0o600))

	choice, err := chooseWorker(workerInputs{
		ExeDir: t.TempDir(), ModuleDir: unrelated, HomeDir: t.TempDir(),
	})
	require.Error(t, err)
	assert.Empty(t, choice.Path, "an unrelated module must not be selected")
	assert.Contains(t, err.Error(), "ENNOTE_WORKER_PATH")
}

func TestChooseWorkerFailsLoudlyWithNoCandidate(t *testing.T) {
	_, err := chooseWorker(workerInputs{ExeDir: t.TempDir(), HomeDir: t.TempDir()})
	require.Error(t, err)
	// The message has to say how to fix it: this is what a misconfigured install
	// sees instead of a timeout with no explanation.
	assert.Contains(t, err.Error(), "ENNOTE_WORKER_PATH")
	assert.Contains(t, err.Error(), "ennoworker")
}
