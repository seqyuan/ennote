package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPolicyRepo(t *testing.T) *store.PolicyRepo {
	t.Helper()
	return &store.PolicyRepo{Files: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}
}

func TestPolicyRepoCreatesImmutableVersions(t *testing.T) {
	repo := newPolicyRepo(t)
	ctx := context.Background()
	first, err := repo.CreateVersion(ctx, store.CreatePolicyInput{Name: "workspace-safe", Kind: domain.PolicyKindTool,
		Config: json.RawMessage(`{"mode":"restricted","allowedTools":["read"]}`)})
	require.NoError(t, err)
	second, err := repo.CreateVersion(ctx, store.CreatePolicyInput{Name: "workspace-safe", Kind: domain.PolicyKindTool,
		Config: json.RawMessage(`{"mode":"restricted","allowedTools":["read","list"]}`)})
	require.NoError(t, err)
	assert.Equal(t, 1, first.Version)
	assert.Equal(t, 2, second.Version)
	assert.NotEqual(t, first.ID, second.ID)
	require.NoError(t, repo.Deactivate(ctx, first.ID))
	stored, err := repo.FindByID(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, "inactive", stored.Status)
}

func TestPolicyRepoAcceptsPermissionModes(t *testing.T) {
	repo := newPolicyRepo(t)
	for _, mode := range []domain.PermissionMode{domain.PermissionDiscuss, domain.PermissionAuto} {
		profile, err := repo.CreateVersion(context.Background(), store.CreatePolicyInput{Name: string(mode), Kind: domain.PolicyKindTool,
			Config: json.RawMessage(`{"mode":"` + string(mode) + `"}`)})
		require.NoError(t, err)
		assert.JSONEq(t, `{"mode":"`+string(mode)+`"}`, string(profile.Config))
	}
}

func TestPolicyRepoRejectsUnknownOrInvalidConfiguration(t *testing.T) {
	repo := newPolicyRepo(t)
	ctx := context.Background()
	_, err := repo.CreateVersion(ctx, store.CreatePolicyInput{Name: "bad", Kind: domain.PolicyKindTool,
		Config: json.RawMessage(`{"mode":"restricted","unknown":true}`)})
	assert.ErrorContains(t, err, "unknown field")
}

func TestPolicyBuiltinDefaultsCoverEveryKind(t *testing.T) {
	// The file-native store falls back to builtin defaults for every kind,
	// mirroring the legacy migration-created compatibility defaults.
	repo := newPolicyRepo(t)
	profiles, err := repo.List(context.Background(), "")
	require.NoError(t, err)
	kinds := map[domain.PolicyKind]bool{}
	for _, profile := range profiles {
		kinds[profile.Kind] = true
	}
	assert.True(t, kinds[domain.PolicyKindTool])
	assert.True(t, kinds[domain.PolicyKindTurn])
	assert.True(t, kinds[domain.PolicyKindVision])
	assert.True(t, kinds[domain.PolicyKindCompaction])
	assert.True(t, kinds[domain.PolicyKind("delegation")])
}
