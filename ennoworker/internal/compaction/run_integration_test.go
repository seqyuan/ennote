package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type integrationToolRunner struct{}

func (integrationToolRunner) Definitions() []domain.ToolDefinition {
	return []domain.ToolDefinition{{Name: "read_large", Description: "read a large result",
		Parameters: json.RawMessage(`{"type":"object","additionalProperties":false}`)}}
}

func (integrationToolRunner) Execute(_ context.Context, call domain.ToolCall) domain.ToolResult {
	return domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name,
		Content: strings.Repeat("sample S-42 exact path /data/run/value.tsv\n", 1000)}
}

func integrationToolCompletion(id string) domain.Completion {
	return domain.Completion{StopReason: domain.StopReasonToolCalls, ActualModel: "main",
		ToolCalls: []domain.ToolCall{{ID: id, Name: "read_large", Arguments: json.RawMessage(`{}`)}}}
}

func TestLoopRunCompactorFinalizesCompleteCanonicalTranscript(t *testing.T) {
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
		ClientRequestID: "integrated-mid-run", Text: strings.Repeat("analyze S-42 ", 200)})
	require.NoError(t, err)
	run, err := runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	config := domain.DefaultCompactionPolicy()
	config.Mode = domain.CompactionManualAndAuto
	config.KeepRecentTurns = 1
	config.TailMinTokens = 1
	config.TailMaxTokens = 6000
	config.SummaryInputRatio = 0.8
	config.SummaryMaxOutputTokens = 1000
	config.IneffectiveReclaimRatio = 0.01
	configJSON, err := json.Marshal(config)
	require.NoError(t, err)
	policy := domain.PolicySnapshot{ID: "policy", Kind: domain.PolicyKindCompaction, Version: 1, Config: configJSON}
	mainRuntime := domain.ModelRuntimeSnapshot{ProviderProfileID: "provider", ModelProfileID: "main",
		APIModel: "main", ContextTokens: 8000, MaxOutputTokens: 500}
	summaryRuntime := domain.ModelRuntimeSnapshot{ProviderProfileID: "provider", ModelProfileID: "summary",
		APIModel: "summary", ContextTokens: 32000, MaxOutputTokens: 2000}
	effective := domain.EffectiveRunConfig{ProviderProfileID: "provider", ModelProfileID: "main",
		APIModel: "main", ContextTokens: 8000, MaxOutputTokens: 500, MaxIterations: 5,
		InitialRuntime: mainRuntime, CompactionRuntime: summaryRuntime, CompactionPolicy: policy,
		Routing: domain.FrozenRoutingConfig{Candidates: []domain.ModelRuntimeSnapshot{mainRuntime}, Pinned: true}}
	run.EffectiveConfig, err = json.Marshal(effective)
	require.NoError(t, err)

	provider := llm.NewFakeProvider(
		llm.FakeStep{Completion: integrationToolCompletion("c1")},
		llm.FakeStep{Completion: integrationToolCompletion("c2")},
		llm.FakeStep{Completion: domain.Completion{Content: []domain.ContentBlock{{
			Kind: domain.ContentText, Text: serviceSummary()}}, StopReason: domain.StopReasonStop,
			ActualModel: "summary", Usage: domain.Usage{InputTokens: 600, OutputTokens: 100}}},
		llm.FakeStep{Completion: domain.Completion{Content: []domain.ContentBlock{{
			Kind: domain.ContentText, Text: "analysis complete"}}, StopReason: domain.StopReasonStop,
			ActualModel: "main"}},
	)
	hub := events.NewHub()
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	calls := &store.CallRepo{DB: db, Publisher: hub}
	service := &Service{RunRepo: &store.RunCompactionRepo{DB: db, Publisher: hub}, Calls: calls,
		Events: writer, Providers: func(domain.ModelRuntimeSnapshot) (llm.Provider, error) { return provider, nil }}
	runCompactor, err := NewRunCompactor(service, run, effective)
	require.NoError(t, err)
	lineage, err := (&store.MessageRepo{DB: db}).Lineage(ctx, run.SessionID, run.BaseMessageID)
	require.NoError(t, err)
	history := make([]domain.ChatMessage, len(lineage))
	for index := range lineage {
		history[index] = domain.ChatMessage{Role: domain.Role(lineage[index].Role), Content: lineage[index].Parts}
	}
	loop := &agent.Loop{Provider: provider, Tools: integrationToolRunner{}, Events: writer, Recorder: calls,
		MidRunCompactor: runCompactor, MaxIterations: 5, ContextTokens: 8000, MaxOutput: 500}
	result, err := loop.Run(ctx, agent.RunInput{RunID: run.ID, Model: "main", InitialRuntime: mainRuntime,
		SystemPrompt: "system", History: history})
	require.NoError(t, err)
	require.Len(t, result.Generated, 5)
	require.NoError(t, runs.FinalizeSuccess(ctx, run.ID, domain.RunOutput{Messages: result.Generated}))

	storedRun, err := runs.Get(ctx, run.ID)
	require.NoError(t, err)
	require.NotNil(t, storedRun.AssistantMessageID)
	canonical, err := (&store.MessageRepo{DB: db}).Lineage(ctx, run.SessionID, *storedRun.AssistantMessageID)
	require.NoError(t, err)
	assert.Len(t, canonical, 6, "user plus every original assistant/tool message must be committed")
	assert.Equal(t, []string{"user", "assistant", "tool", "assistant", "tool", "assistant"},
		[]string{canonical[0].Role, canonical[1].Role, canonical[2].Role, canonical[3].Role, canonical[4].Role, canonical[5].Role})
	var compactionCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_context_compactions
		WHERE run_id=? AND status='completed'`, run.ID).Scan(&compactionCount))
	assert.Equal(t, 1, compactionCount)
}
