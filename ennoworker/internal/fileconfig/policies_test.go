package fileconfig_test

import (
	"context"
	"encoding/json"
	"path/filepath"
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
