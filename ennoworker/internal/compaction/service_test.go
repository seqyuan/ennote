package compaction

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/google/uuid"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManualServiceCommitsCallUsageAndCheckpoint(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.MigrateFixtureSchema(db))
	ctx := context.Background()
	// V2: provider/model + compaction policy resolve from file stores; the
	// legacy global provider/model SQL path was removed.
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err = models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://example.test", APIKey: "sk-test",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "test-model", DisplayName: "Test",
		ContextWindow: 32000, MaxOutputTokens: 2000, IsDefault: true,
	})
	require.NoError(t, err)
	policies := &fileconfig.PolicyStore{Path: filepath.Join(home, "config", "policies.json")}
	session := sqlCreateSession(t, db, "project")

	config := domain.DefaultCompactionPolicy()
	config.KeepRecentTurns = 1
	config.TailMinTokens = 1
	config.TailMaxTokens = 100
	config.SummaryMaxOutputTokens = 512
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	profile, err := policies.CreateVersion(ctx, "manual-small-tail", domain.PolicyKindCompaction, encoded)
	require.NoError(t, err)
	// Set the session's compaction policy directly: UpdateCompactionPolicy
	// validates against the removed global policy SQL; the V2 file store is
	// the authority for resolution at CreateManual time.
	_, err = db.Exec(`UPDATE sessions SET compaction_policy_profile_id=? WHERE id=?`, profile.ID, session.ID)
	require.NoError(t, err)

	messages := &store.MessageRepo{DB: db}
	parent := ""
	for index := 0; index < 4; index++ {
		message, createErr := messages.CreateUserMessage(ctx, session.ID, parent, strings.Repeat("sample path /data/run ", 100))
		require.NoError(t, createErr)
		parent = message.ID
	}
	// V2: the Session row lives in this database; activate the leaf directly.
	_, err = db.Exec(`UPDATE session_branches SET leaf_message_id=? WHERE session_id=? AND label='Main'`, parent, session.ID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sessions SET active_leaf_message_id=? WHERE id=?`, parent, session.ID)
	require.NoError(t, err)
	compactions := &store.CompactionRepo{DB: db, Policies: policies}
	submission, err := compactions.CreateManual(ctx, domain.ManualCompactionInput{
		SessionID: session.ID, BaseMessageID: parent, ClientRequestID: "manual-service"})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db, Providers: &store.ProviderRepo{Files: models},
		Models: &store.ModelRepo{Files: models}, Policies: policies}
	run, err := runs.Claim(ctx, submission.RunID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, model.ID, resolved.Effective.ModelProfileID)

	summary := serviceSummary()
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: summary}}, StopReason: "stop",
		ActualModel: "test-model", Usage: domain.Usage{UncachedInputTokens: 300, OutputTokens: 80},
	}})
	service := &Service{Repo: compactions, Calls: &store.CallRepo{DB: db}, Messages: messages,
		Providers: func(domain.ModelRuntimeSnapshot) (llm.Provider, error) { return provider, nil }}
	require.NoError(t, service.ExecuteManual(ctx, run, resolved, "system", nil))

	checkpoint, err := compactions.Get(ctx, submission.CompactionID)
	require.NoError(t, err)
	assert.Equal(t, domain.CompactionCompleted, checkpoint.Status)
	assert.Equal(t, summary, checkpoint.Summary)
	require.NotNil(t, checkpoint.ModelCallID)
	var callStatus, purpose string
	var inputTokens int
	require.NoError(t, db.QueryRow(`SELECT status,purpose,uncached_input_tokens FROM model_calls WHERE id=?`,
		*checkpoint.ModelCallID).Scan(&callStatus, &purpose, &inputTokens))
	assert.Equal(t, "completed", callStatus)
	assert.Equal(t, string(domain.ModelCallContextCompaction), purpose)
	assert.Equal(t, 300, inputTokens)
	var usageCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM usage_records WHERE ref_id=?`, *checkpoint.ModelCallID).Scan(&usageCount))
	assert.Equal(t, 1, usageCount)
}

func TestRunCompactorCommitsSummaryAndReusesDigest(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.MigrateFixtureSchema(db))
	ctx := context.Background()
	now := "2026-07-29T00:00:00Z"
	_, err = db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('session','project',?,?)`, now, now, now, now)
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: "session",
		ClientRequestID: "run-compact", Text: "analyze"})
	require.NoError(t, err)
	run, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	config := domain.DefaultCompactionPolicy()
	config.Mode = domain.CompactionManualAndAuto
	config.KeepRecentTurns = 1
	config.TailMinTokens = 1
	config.TailMaxTokens = 2000
	config.SummaryInputRatio = 0.8
	config.SummaryMaxOutputTokens = 1000
	config.IneffectiveReclaimRatio = 0.01
	configJSON, err := json.Marshal(config)
	require.NoError(t, err)
	policy := domain.PolicySnapshot{ID: "policy", Kind: domain.PolicyKindCompaction, Version: 1, Config: configJSON}
	mainRuntime := domain.ModelRuntimeSnapshot{ProviderProfileID: "provider", ModelProfileID: "main",
		APIModel: "main", ContextTokens: 12000, MaxOutputTokens: 1000}
	summaryRuntime := domain.ModelRuntimeSnapshot{ProviderProfileID: "provider", ModelProfileID: "summary",
		APIModel: "summary", ContextTokens: 32000, MaxOutputTokens: 2000}
	effective := domain.EffectiveRunConfig{CompactionPolicy: policy, InitialRuntime: mainRuntime,
		CompactionRuntime: summaryRuntime}
	run.EffectiveConfig, err = json.Marshal(effective)
	require.NoError(t, err)

	large := strings.Repeat("sample path /data/run and exact value 42 ", 500)
	toolMessage := func(id string) (domain.ChatMessage, domain.ChatMessage) {
		call := domain.ToolCall{ID: id, Name: "read", Arguments: json.RawMessage(`{"path":"data.txt"}`)}
		result := domain.ToolResult{ToolCallID: id, ToolName: "read", Content: large}
		return domain.ChatMessage{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentToolCall, ToolCall: &call}}},
			domain.ChatMessage{Role: domain.RoleTool, Content: []domain.ContentBlock{{Kind: domain.ContentToolResult, ToolResult: &result}}}
	}
	a1, t1 := toolMessage("c1")
	a2, t2 := toolMessage("c2")
	messages := []domain.ChatMessage{{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "analyze"}}}, a1, t1, a2, t2}
	generated := append([]domain.ChatMessage(nil), messages[1:]...)

	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content:    []domain.ContentBlock{{Kind: domain.ContentText, Text: serviceSummary()}},
		StopReason: domain.StopReasonStop, ActualModel: "summary",
		Usage: domain.Usage{UncachedInputTokens: 500, OutputTokens: 100},
	}})
	service := &Service{RunRepo: &store.RunCompactionRepo{DB: db}, Calls: &store.CallRepo{DB: db},
		Providers: func(domain.ModelRuntimeSnapshot) (llm.Provider, error) { return provider, nil }}
	compactor, err := NewRunCompactor(service, run, effective)
	require.NoError(t, err)
	request := agent.MidRunCompactionRequest{RunID: run.ID, Iteration: 3, RequestGeneration: 1,
		Reason: agent.MidRunCompactionThreshold, SystemPrompt: "system", Messages: messages,
		Generated: generated, Current: mainRuntime}
	first, err := compactor.CompactRunContext(ctx, request)
	require.NoError(t, err)
	assert.True(t, first.Compacted)
	assert.NotEmpty(t, first.State.ID)
	assert.Len(t, provider.Requests, 1)
	assert.Equal(t, len(generated), 4, "the caller's generated transcript remains complete")

	second, err := compactor.CompactRunContext(ctx, request)
	require.NoError(t, err)
	assert.True(t, second.Compacted)
	assert.Equal(t, first.State.ID, second.State.ID)
	assert.Len(t, provider.Requests, 1, "matching digest must reuse the completed summary")
}

func serviceSummary() string {
	return "## Goal\nPreserve the analysis task.\n\n## Constraints & Preferences\nKeep exact identifiers.\n\n" +
		"## Critical Data\nSample S-1 uses /data/run.\n\n## Progress\n### Done\nLoaded inputs.\n" +
		"### In Progress\nAnalysis.\n### Blocked\nNone.\n\n## Key Decisions\nKeep canonical history.\n\n" +
		"## Files & Artifacts\n/data/run\n\n## Next Steps\nContinue analysis."
}

// sqlCreateSession inserts a Session row + Main branch directly on the caller's
// database (V2 per-Session SQLite file or a test database with the Session
// schema).
func sqlCreateSession(t *testing.T, db *sql.DB, projectID string) domain.Session {
	t.Helper()
	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	id, branchID := uuid.NewString(), uuid.NewString()
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, title, status, mode, active_branch_id, created_at, updated_at)
		VALUES (?,?,?, 'active','hosted',NULL,?,?)`, id, projectID, "session", timestamp, timestamp)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO session_branches (id,session_id,label,created_at,updated_at) VALUES(?,?,'Main',?,?)`,
		branchID, id, timestamp, timestamp)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sessions SET active_branch_id=? WHERE id=?`, branchID, id)
	require.NoError(t, err)
	return domain.Session{ID: id, ProjectID: projectID, Title: "session", Status: "active",
		Mode: domain.SessionModeHosted, ActiveBranchID: &branchID, CreatedAt: now, UpdatedAt: now}
}
