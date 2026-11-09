package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/require"
)

// setupFileHeroDelegation wires a file-backed Hero source + model catalog for
// a Session-scoped DelegationRepo (V2 file-native hero resolution). It returns
// the source, the file-backed ModelRepo, the resolved model portable ref, and
// the temp home dir.
func setupFileHeroDelegation(t *testing.T) (*globalsource.Store, *store.ModelRepo, string, string) {
	t.Helper()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(context.Background(), fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-hero-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(context.Background(), fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "hero-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	return &globalsource.Store{HomeDir: home}, &store.ModelRepo{Files: models}, model.ID, home
}

// createFileHero writes and publishes one Hero document as an immutable file
// revision (V2). The returned handle is the hero's file id.
func createFileHero(t *testing.T, sources *globalsource.Store, document *heroesource.Document) error {
	t.Helper()
	_, _, err := sources.CreateHero(document)
	if err != nil {
		return err
	}
	_, err = sources.PublishHeroRevision(document.Handle)
	return err
}
