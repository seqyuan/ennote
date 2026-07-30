package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/compaction"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutorRecoversControlledFirstRequestOverflow(t *testing.T) {
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		var request struct {
			Model    string `json:"model"`
			Messages []any  `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, "qualification-model", request.Model)
		assert.NotEmpty(t, request.Messages)
		switch providerCalls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"message":"maximum context window exceeded","code":"context_length_exceeded"}}`)
		case 2:
			writeQualificationStream(w, qualificationSummary())
		case 3:
			writeQualificationStream(w, "ENNOTE_OVERFLOW_RECOVERED")
		default:
			http.Error(w, "unexpected provider call", http.StatusInternalServerError)
		}
	}))
	defer provider.Close()

	t.Setenv("ENNOTE_QUALIFICATION_KEY", "local-test-key")
	t.Setenv("ENNOTE_HOME", t.TempDir())
	ctx := context.Background()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))

	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "overflow qualification", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	providerProfile, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "controlled overflow", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: provider.URL + "/v1", CredentialRef: "env:ENNOTE_QUALIFICATION_KEY",
	})
	require.NoError(t, err)
	modelProfile, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: providerProfile.ID, ModelName: "qualification-model", DisplayName: "Qualification",
		ContextWindow: 32000, MaxOutputTokens: 512, IsDefault: true,
	})
	require.NoError(t, err)

	config := domain.DefaultCompactionPolicy()
	config.Mode = domain.CompactionManualAndAuto
	config.KeepRecentTurns = 1
	config.TailTokenRatio = 0.01
	config.TailMinTokens = 1
	config.TailMaxTokens = 100
	config.SummaryMaxOutputTokens = 512
	config.AllowOverflowRecovery = true
	config.MaxOverflowRecoveries = 1
	encodedPolicy, err := json.Marshal(config)
	require.NoError(t, err)
	policy, err := (&store.PolicyRepo{DB: db}).CreateVersion(ctx, store.CreatePolicyInput{
		Name: "controlled overflow", Kind: domain.PolicyKindCompaction, Config: encodedPolicy,
	})
	require.NoError(t, err)

	modelProfileID := modelProfile.ID
	sessionRepo := &store.SessionRepo{DB: db}
	session, err := sessionRepo.Create(ctx, domain.CreateSessionInput{
		ProjectID: project.ID, Title: "overflow", DefaultModelProfileID: &modelProfileID,
	})
	require.NoError(t, err)
	_, err = sessionRepo.UpdateCompactionPolicy(ctx, session.ID, &policy.ID)
	require.NoError(t, err)

	messageRepo := &store.MessageRepo{DB: db}
	parentID := ""
	for index := 0; index < 4; index++ {
		message, createErr := messageRepo.CreateUserMessage(ctx, session.ID, parentID,
			fmt.Sprintf("Historical turn %d: %s", index+1, strings.Repeat("sample context ", 80)))
		require.NoError(t, createErr)
		parentID = message.ID
	}
	require.NoError(t, sessionRepo.ActivateLeaf(ctx, session.ID, parentID))

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub}
	submission, err := runRepo.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "controlled-overflow",
		Text:            "Return the qualification result after recovery.",
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
		db: db, writer: writer, runs: runRepo, calls: callRepo,
		sessionDB: sessionRepo, msgRepo: messageRepo,
		skillRepo: &store.SkillSnapshotRepo{DB: db}, skillsDir: emptySkills,
		builtinDir: emptySkills, sandbox: "none",
	}
	executor.compaction = &compaction.Service{Repo: compactionRepo,
		RunRepo: &store.RunCompactionRepo{DB: db}, Calls: callRepo,
		Messages: messageRepo, Events: writer, Providers: executor.resolveRuntimeProvider}
	output, err := executor.Execute(ctx, run)
	require.NoError(t, err)
	require.NoError(t, runRepo.FinalizeSuccess(ctx, run.ID, output))
	assert.Equal(t, int32(3), providerCalls.Load())

	storedRun, err := runRepo.Get(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, storedRun.Status)
	require.NotNil(t, storedRun.AssistantMessageID)
	lineage, err := messageRepo.Lineage(ctx, session.ID, *storedRun.AssistantMessageID)
	require.NoError(t, err)
	assert.Contains(t, messageTextForQualification(lineage[len(lineage)-1]), "ENNOTE_OVERFLOW_RECOVERED")

	var completedCompactions int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM context_compactions WHERE run_id = ? AND reason = 'overflow' AND status = 'completed'`, run.ID).
		Scan(&completedCompactions))
	assert.Equal(t, 1, completedCompactions)
	var started, completed int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'context_overflow_recovery_started'`, run.ID).
		Scan(&started))
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'context_overflow_recovery_completed'`, run.ID).
		Scan(&completed))
	assert.Equal(t, 1, started)
	assert.Equal(t, 1, completed)
}

func writeQualificationStream(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: {\"id\":\"qualification\",\"model\":\"qualification-model\",\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", text)
	_, _ = fmt.Fprint(w, "data: {\"id\":\"qualification\",\"model\":\"qualification-model\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20}}\n\n")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func qualificationSummary() string {
	return "## Goal\nRecover the controlled overflow.\n\n## Constraints & Preferences\nKeep exact identifiers.\n\n" +
		"## Critical Data\nThe qualification model is controlled.\n\n## Progress\n### Done\nHistory loaded.\n" +
		"### In Progress\nRecovery.\n### Blocked\nNone.\n\n## Key Decisions\nUse one retry generation.\n\n" +
		"## Files & Artifacts\nNone.\n\n## Next Steps\nReturn the qualification result."
}

func messageTextForQualification(message domain.Message) string {
	var value strings.Builder
	for _, part := range message.Parts {
		if part.Kind == domain.ContentText {
			value.WriteString(part.Text)
		}
	}
	return value.String()
}
