package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBubblewrapArgsIsolateNetworkAndDeviceTree(t *testing.T) {
	jail, err := NewJail(t.TempDir())
	require.NoError(t, err)
	manager := &Manager{Jail: jail, Mode: SandboxBubblewrap}

	args := buildBwrapArgs(manager)
	assert.Contains(t, args, "--unshare-net")
	assert.Contains(t, args, "--unshare-pid")
	dev := -1
	for index, value := range args {
		if value == "--dev" {
			dev = index
		}
	}
	require.NotEqual(t, -1, dev)
	require.Less(t, dev+1, len(args))
	assert.Equal(t, "/dev", args[dev+1])
	assert.Contains(t, args, "--ro-bind")
	assert.NotContains(t, args, "/host")
}

func TestSandboxNoneRejectsProtectedHomePaths(t *testing.T) {
	manager, err := NewManager(t.TempDir(), t.TempDir(), "", SandboxNone)
	require.NoError(t, err)
	home := t.TempDir()
	manager.ProtectHome(home)

	require.ErrorContains(t, manager.ValidateCommand("cat "+home+"/config/provider-auth.json"), "cannot access the Ennote home")
	require.ErrorContains(t, manager.ValidateCommand("cat ~/.ennote/config/provider-auth.json"), "cannot access the Ennote home")
	require.ErrorContains(t, manager.ValidateCommand(`cat "$HOME/.ennote/config/provider-auth.json"`), "cannot access the Ennote home")
	require.ErrorContains(t, manager.ValidateCommand("echo $PATH"), "cannot access the Ennote home")
	require.NoError(t, manager.ValidateCommand("echo hello"))
	require.NoError(t, manager.ValidateArgv([]string{"echo", "$PATH"}))
	require.ErrorContains(t, manager.ValidateArgv([]string{"cat", home + "/config/provider-auth.json"}), "cannot access the Ennote home")

	manager.Mode = SandboxBubblewrap
	require.NoError(t, manager.ValidateCommand("cat "+home+"/config/provider-auth.json"))
	require.NoError(t, manager.ValidateCommand("echo $PATH"))
}
