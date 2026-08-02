//go:build integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/compaction"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveDeepSeekExecutorFreezesProfileAndCommitsProjections(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_API_KEY"))
	model := strings.TrimSpace(os.Getenv("ENNOTE_LIVE_MODEL"))
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("ENNOTE_LIVE_BASE_URL, ENNOTE_LIVE_API_KEY, and ENNOTE_LIVE_MODEL are required")
	}
	t.Setenv("ENNOTE_LIVE_API_KEY", apiKey)
	t.Setenv("ENNOTE_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))

	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "deepseek-live", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "deepseek-live", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: baseURL, CredentialRef: "env:ENNOTE_LIVE_API_KEY",
	})
	require.NoError(t, err)
	modelProfile, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: model, DisplayName: model,
		ContextWindow: 1000000, MaxOutputTokens: 128,
		SupportsToolUse: true, SupportsThinking: true, IsDefault: true,
	})
	require.NoError(t, err)
	modelProfileID := modelProfile.ID
	sessionRepo := &store.SessionRepo{DB: db}
	session, err := sessionRepo.Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "deepseek live", DefaultModelProfileID: &modelProfileID,
	})
	require.NoError(t, err)

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub}
	messageRepo := &store.MessageRepo{DB: db}
	submission, err := runRepo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "deepseek-live-request",
		Text:            "Reply with exactly ENNOTE_E2E and no other text. Do not call tools.",
		RequestedConfig: json.RawMessage(`{"maxIterations":2,"maxOutputTokens":128}`),
	})
	require.NoError(t, err)
	run, err := runRepo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	callRepo := &store.CallRepo{DB: db, Publisher: hub}
	compactionRepo := &store.CompactionRepo{DB: db, Publisher: hub}
	emptySkills := t.TempDir()
	executor := &agentExecutor{
		db: db, writer: writer, homeDir: t.TempDir(), runs: runRepo, calls: callRepo,
		sessionDB: sessionRepo, msgRepo: messageRepo,
		skillRepo: &store.SkillSnapshotRepo{DB: db}, skillsDir: emptySkills,
		builtinDir: emptySkills, sandbox: "none",
	}
	trustStore, err := workspace.NewTrustStore(executor.homeDir)
	require.NoError(t, err)
	executor.trustStore = trustStore
	executor.compaction = &compaction.Service{Repo: compactionRepo,
		RunRepo: &store.RunCompactionRepo{DB: db}, Calls: callRepo,
		Messages: messageRepo, Events: writer, Providers: executor.resolveRuntimeProvider}
	output, err := executor.Execute(ctx, run)
	require.NoError(t, err)
	require.NotEmpty(t, output.Messages)
	require.NoError(t, runRepo.FinalizeSuccess(ctx, run.ID, output))

	storedRun, err := runRepo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, storedRun.Status)
	var effective struct {
		ModelProfileID string `json:"modelProfileId"`
		APIModel       string `json:"apiModel"`
	}
	require.NoError(t, json.Unmarshal(storedRun.EffectiveConfig, &effective))
	assert.Equal(t, modelProfileID, effective.ModelProfileID)
	assert.Equal(t, model, effective.APIModel)
	var callStatus, actualModel string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, actual_model FROM model_calls WHERE run_id = ?`, run.ID).
		Scan(&callStatus, &actualModel))
	assert.Equal(t, "completed", callStatus)
	assert.NotEmpty(t, actualModel)
	var usageCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_records WHERE run_id = ?`, run.ID).Scan(&usageCount))
	assert.Positive(t, usageCount)
	require.NotNil(t, storedRun.AssistantMessageID)
	lineage, err := messageRepo.Lineage(ctx, session.ID, *storedRun.AssistantMessageID)
	require.NoError(t, err)
	require.NotEmpty(t, lineage)
	var assistantText strings.Builder
	for _, part := range lineage[len(lineage)-1].Parts {
		if part.Kind == domain.ContentText {
			assistantText.WriteString(part.Text)
		}
	}
	assert.Contains(t, assistantText.String(), "ENNOTE_E2E")
}
