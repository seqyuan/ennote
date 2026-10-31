//go:build linux

package workspace

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBubblewrapCommandMapsWorkspaceWhenAvailable(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		if os.Getenv("ENNOTE_REQUIRE_BWRAP") == "1" {
			t.Fatalf("bubblewrap is required in this job: %v", err)
		}
		t.Skip("bubblewrap is not installed")
	}
	manager, err := NewManager(t.TempDir(), t.TempDir(), t.TempDir(), SandboxBubblewrap)
	require.NoError(t, err)
	cmd, err := manager.Command("/bin/sh", "pwd")
	require.NoError(t, err)
	output, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(output), "Permission denied") {
		if os.Getenv("ENNOTE_REQUIRE_BWRAP") == "1" {
			t.Fatalf("bubblewrap is required in this job but user namespaces are unavailable: %s", output)
		}
		t.Skipf("bubblewrap installed but user namespaces are unavailable: %s", output)
	}
	require.NoError(t, err, string(output))
	assert.Equal(t, "/workspace", strings.TrimSpace(string(output)))
}
