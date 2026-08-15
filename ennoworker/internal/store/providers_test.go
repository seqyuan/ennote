package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newModelStore(t *testing.T) *fileconfig.ModelStore {
	t.Helper()
	return fileconfig.NewModelStore(
		filepath.Join(t.TempDir(), "config", "models.json"),
		filepath.Join(t.TempDir(), "config", "provider-auth.json"),
		filepath.Join(t.TempDir(), "config", "settings.json"),
	)
}

func TestProviderProfileStoresPlaintextAPIKey(t *testing.T) {
	ctx := context.Background()
	files := newModelStore(t)
	repo := &store.ProviderRepo{Files: files}
	profile, err := repo.Create(ctx, store.CreateProviderInput{
		Key: "deepseek", Name: "DeepSeek", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-deepseek-1234",
	})
	require.NoError(t, err)
	// V2: the plaintext key lives in the credential store; the profile only
	// reports that it is configured and never leaks the value.
	assert.True(t, profile.CredentialConfigured)
	assert.Empty(t, profile.APIKey)
	resolved, err := files.Credentials.Resolve(profile.ID)
	require.NoError(t, err)
	assert.Equal(t, "sk-deepseek-1234", resolved)

	profiles, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, profile.ID, profiles[0].ID)
	assert.True(t, profiles[0].CredentialConfigured)

	found, err := repo.FindByID(ctx, profile.ID)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.True(t, found.CredentialConfigured)
	// FindByID resolves the live credential (run-config path); the catalog
	// list and create profiles never leak it.
	assert.Equal(t, "sk-deepseek-1234", found.APIKey)
}

func TestProviderProfileRejectsUnsupportedType(t *testing.T) {
	repo := &store.ProviderRepo{Files: newModelStore(t)}
	_, err := repo.Create(context.Background(), store.CreateProviderInput{
		Key: "unknown", Name: "Unknown", ProviderType: "unknown", APIKey: "sk-KEY",
	})
	assert.ErrorContains(t, err, "unsupported provider type")
}
