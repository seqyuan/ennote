//go:build linux

package workspace

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBubblewrapCommandMapsWorkspaceWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed")
	}
	manager, err := NewManager(t.TempDir(), t.TempDir(), t.TempDir(), SandboxBubblewrap)
	require.NoError(t, err)
	cmd, err := manager.Command("/bin/sh", "pwd")
	require.NoError(t, err)
	output, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(output), "Permission denied") {
		t.Skipf("bubblewrap installed but user namespaces are unavailable: %s", output)
	}
	require.NoError(t, err, string(output))
	assert.Equal(t, "/workspace", strings.TrimSpace(string(output)))
}
