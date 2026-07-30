package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderProfileStoresCredentialReferenceOnly(t *testing.T) {
	db := store.SetupDB(t)
	repo := &store.ProviderRepo{DB: db}
	profile, err := repo.Create(context.Background(), store.CreateProviderInput{
		Name: "DeepSeek", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://api.deepseek.com/v1", CredentialRef: "env:DEEPSEEK_API_KEY",
	})
	require.NoError(t, err)
	assert.Equal(t, "env:DEEPSEEK_API_KEY", profile.CredentialRef)

	profiles, err := repo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.Equal(t, profile.ID, profiles[0].ID)

	var stored string
	require.NoError(t, db.QueryRow(`SELECT credential_ref FROM provider_profiles WHERE id = ?`, profile.ID).Scan(&stored))
	assert.Equal(t, "env:DEEPSEEK_API_KEY", stored)
	assert.NotContains(t, stored, "sk-")
}

func TestProviderProfileRejectsUnsupportedType(t *testing.T) {
	db := store.SetupDB(t)
	repo := &store.ProviderRepo{DB: db}
	_, err := repo.Create(context.Background(), store.CreateProviderInput{
		Name: "Unknown", ProviderType: "unknown", CredentialRef: "env:KEY",
	})
	assert.ErrorContains(t, err, "unsupported provider type")
}
