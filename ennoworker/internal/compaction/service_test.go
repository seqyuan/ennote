package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManualServiceCommitsCallUsageAndCheckpoint(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	ctx := context.Background()
	now := "2026-07-28T00:00:00Z"
	_, err = db.Exec(`INSERT INTO provider_profiles
		(id,name,provider_type,base_url,credential_ref,created_at,updated_at)
		VALUES('provider','Provider','openai-compatible','https://example.test','env:TEST_KEY',?,?);
		INSERT INTO model_profiles
		(id,provider_id,model_name,display_name,context_window,max_output_tokens,created_at,updated_at)
		VALUES('model','provider','test-model','Test',32000,2000,?,?);
		INSERT INTO settings(key,value) VALUES('default_model_profile_id','model');
		INSERT INTO projects(id,name,created_at,updated_at) VALUES('project','P',?,?);
		INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('session','project',?,?)`,
		now, now, now, now, now, now, now, now)
	require.NoError(t, err)

	config := domain.DefaultCompactionPolicy()
	config.KeepRecentTurns = 1
	config.TailMinTokens = 1
	config.TailMaxTokens = 100
	config.SummaryMaxOutputTokens = 512
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	profile, err := (&store.PolicyRepo{DB: db}).CreateVersion(ctx, store.CreatePolicyInput{
		Name: "manual-small-tail", Kind: domain.PolicyKindCompaction, Config: encoded})
	require.NoError(t, err)
	_, err = (&store.SessionRepo{DB: db}).UpdateCompactionPolicy(ctx, "session", &profile.ID)
	require.NoError(t, err)

	messages := &store.MessageRepo{DB: db}
	parent := ""
	for index := 0; index < 4; index++ {
		message, createErr := messages.CreateUserMessage(ctx, "session", parent, strings.Repeat("sample path /data/run ", 100))
		require.NoError(t, createErr)
		parent = message.ID
	}
	require.NoError(t, (&store.SessionRepo{DB: db}).ActivateLeaf(ctx, "session", parent))
	compactions := &store.CompactionRepo{DB: db}
	submission, err := compactions.CreateManual(ctx, domain.ManualCompactionInput{
		SessionID: "session", BaseMessageID: parent, ClientRequestID: "manual-service"})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db}
	run, err := runs.Claim(ctx, submission.RunID)
	require.NoError(t, err)
	resolved, err := runs.ResolveAndFreezeConfig(ctx, run)
	require.NoError(t, err)

	summary := serviceSummary()
	provider := llm.NewFakeProvider(llm.FakeStep{Completion: domain.Completion{
		Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: summary}}, StopReason: "stop",
		ActualModel: "test-model", Usage: domain.Usage{InputTokens: 300, OutputTokens: 80},
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
	require.NoError(t, db.QueryRow(`SELECT status,purpose,input_tokens FROM model_calls WHERE id=?`,
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
	require.NoError(t, store.Migrate(db))
	ctx := context.Background()
	now := "2026-07-29T00:00:00Z"
	_, err = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('project','P',?,?);
		INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('session','project',?,?)`, now, now, now, now)
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
		Usage: domain.Usage{InputTokens: 500, OutputTokens: 100},
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
