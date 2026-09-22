package fileconfig_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyStoreProvidesBuiltinsAndCustomVersions(t *testing.T) {
	store := &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}
	ctx := context.Background()
	tool, err := store.Resolve(ctx, "", domain.PolicyKindTool)
	require.NoError(t, err)
	assert.Equal(t, "builtin-tool-allow-existing-v1", tool.ID)

	custom, err := store.CreateVersion(ctx, "Team Policy", domain.PolicyKindTool, json.RawMessage(`{"mode":"discuss"}`))
	require.NoError(t, err)
	assert.Equal(t, 1, custom.Version)
	require.NoError(t, store.SetDefaultProfile(custom.ID))
	resolved, err := store.Resolve(ctx, "", domain.PolicyKindTool)
	require.NoError(t, err)
	assert.Equal(t, custom.ID, resolved.ID)

	second, err := store.CreateVersion(ctx, "Team Policy", domain.PolicyKindTool, json.RawMessage(`{"mode":"auto"}`))
	require.NoError(t, err)
	assert.Equal(t, 2, second.Version)
	require.NoError(t, store.DeactivateProfile(custom.ID))
	_, err = store.Resolve(ctx, custom.ID, domain.PolicyKindTool)
	assert.ErrorContains(t, err, "not found")
}

func TestPolicyStoreDoesNotAllowBuiltinDeactivation(t *testing.T) {
	store := &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "policies.json")}
	err := store.DeactivateProfile("builtin-tool-allow-existing-v1")
	assert.ErrorContains(t, err, "builtin")
}

// TestBuiltinToolPoliciesCoverEveryPermissionMode pins the catalog contract the
// Web composer depends on: each permission mode has exactly one active builtin
// tool policy whose id is builtin-tool-<mode>-v<digits> and whose config.mode is
// that mode. lib/permission-mode.ts matches the same pattern, so a rename or
// version bump here has to be mirrored there.
func TestBuiltinToolPoliciesCoverEveryPermissionMode(t *testing.T) {
	store := &fileconfig.PolicyStore{}
	profiles, err := store.Profiles(context.Background(), domain.PolicyKindTool)
	require.NoError(t, err)
	for _, mode := range []domain.PermissionMode{domain.PermissionDiscuss, domain.PermissionAsk, domain.PermissionAuto} {
		pattern := regexp.MustCompile(`^builtin-tool-` + string(mode) + `-v[0-9]+$`)
		matches := 0
		for _, profile := range profiles {
			if profile.Status != "active" || !pattern.MatchString(profile.ID) {
				continue
			}
			var config domain.ToolPolicyConfig
			require.NoError(t, json.Unmarshal(profile.Config, &config))
			assert.Equal(t, string(mode), config.Mode, profile.ID)
			matches++
		}
		assert.Equal(t, 1, matches, "expected exactly one active builtin %s tool policy", mode)
	}
}

// TestBuiltinToolPolicyDefaultIsResolvable guards the empty-id fallback that
// runs which omit toolPolicyProfileId depend on.
func TestBuiltinToolPolicyDefaultIsResolvable(t *testing.T) {
	store := &fileconfig.PolicyStore{}
	snapshot, err := store.Resolve(context.Background(), "", domain.PolicyKindTool)
	require.NoError(t, err)
	assert.Equal(t, "builtin-tool-allow-existing-v1", snapshot.ID)
}
