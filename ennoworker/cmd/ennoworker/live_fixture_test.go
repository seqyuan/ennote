//go:build integration

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/require"
)

// liveStack is the V2 file-native live-qualification fixture: a real Provider
// credential + model wired through the file store, a file-native project and
// Session (per-Session SQLite), and the opened Session database. It never
// touches the removed global provider/model/role SQL tables.
type liveStack struct {
	Home      string
	Models    *fileconfig.ModelStore
	Providers *store.ProviderRepo
	ModelRepo *store.ModelRepo
	Policies  *fileconfig.PolicyStore
	Sources   *globalsource.Store
	Projects  *projectstore.Store
	Sessions  *sessionstore.Manager
	DB        *sql.DB
	Project   domain.Project
	Session   domain.Session
	ModelID   string
}

// newLiveStack builds the file-native live fixture. It requires
// ENNOTE_LIVE_BASE_URL / ENNOTE_LIVE_API_KEY / ENNOTE_LIVE_MODEL (the same
// contract as the existing live qualifications). The model is registered as a
// thinking-capable OpenAI-compatible profile with the reasoning-effort dialect.
func newLiveStack(t *testing.T, projectName string) *liveStack {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	t.Setenv("ENNOTE_LIVE_API_KEY", apiKey)

	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	providers := &store.ProviderRepo{Files: models}
	ctx := context.Background()
	provider, err := providers.Create(ctx, store.CreateProviderInput{
		Key: "live-provider", Name: "live-provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: baseURL, APIKey: apiKey,
	})
	require.NoError(t, err)
	modelRepo := &store.ModelRepo{Files: models}
	profile, err := modelRepo.Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: model, DisplayName: model,
		ContextWindow: 1000000, MaxOutputTokens: 4096,
		SupportsToolUse: true, SupportsThinking: true, IsDefault: true,
		ThinkingDialect:          domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium, domain.ThinkingHigh},
		// Non-zero pricing so delegation budgets with a cost ceiling can
		// resolve model rates (the live model otherwise reports pricing
		// unavailable and any MaxCostMicros > 0 fails the child admission).
		InputCostUSDMicrosPerMillion:  280,
		OutputCostUSDMicrosPerMillion: 420,
	})
	require.NoError(t, err)

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: projectName, HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: projectName, DefaultModelProfileID: &profile.ID,
	})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)

	return &liveStack{
		Home:      home,
		Models:    models,
		Providers: providers,
		ModelRepo: modelRepo,
		Policies:  &fileconfig.PolicyStore{Path: filepath.Join(home, "config", "policies.json")},
		Sources:   &globalsource.Store{HomeDir: home},
		Projects:  projects,
		Sessions:  sessions,
		DB:        db,
		Project:   *project,
		Session:   *session,
		ModelID:   profile.ID,
	}
}
