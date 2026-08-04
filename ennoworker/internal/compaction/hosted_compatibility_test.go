package compaction

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createHostedCompactionHistory(t *testing.T) (*sql.DB, string, []string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "hosted compaction", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: db}).Create(ctx,
		domain.CreateSessionInput{ProjectID: project.ID, Title: "history"})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db}
	runIDs := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
			SessionID: session.ID, ClientRequestID: fmt.Sprintf("turn-%d", index),
			Text: strings.Repeat("canonical user evidence ", 80),
		})
		require.NoError(t, err)
		_, err = runs.Claim(ctx, submission.Run.ID)
		require.NoError(t, err)
		require.NoError(t, runs.FinalizeSuccess(ctx, submission.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{{
			Role: domain.RoleAssistant, Content: []domain.ContentBlock{{
				Kind: domain.ContentText, Text: strings.Repeat("canonical assistant evidence ", 80),
			}},
		}}}))
		runIDs = append(runIDs, submission.Run.ID)
	}
	return db, session.ID, runIDs
}

func TestCompactionPlanIgnoresPrivateRunMessageShadow(t *testing.T) {
	db, sessionID, runIDs := createHostedCompactionHistory(t)
	ctx := context.Background()
	session, err := (&store.SessionRepo{DB: db}).FindByID(ctx, sessionID)
	require.NoError(t, err)
	require.NotNil(t, session.ActiveLeafMessageID)
	messages := &store.MessageRepo{DB: db}
	lineage, err := messages.HostedContextLineage(ctx, sessionID, *session.ActiveLeafMessageID)
	require.NoError(t, err)

	config := domain.DefaultCompactionPolicy()
	config.KeepRecentTurns = 1
	config.TailMinTokens = 1
	config.TailMaxTokens = 1
	config.SummaryMaxOutputTokens = 512
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	policy := domain.PolicySnapshot{ID: "policy", Kind: domain.PolicyKindCompaction, Version: 1, Config: encoded}
	runtime := domain.ModelRuntimeSnapshot{ModelProfileID: "model", APIModel: "model",
		ContextTokens: 32000, MaxOutputTokens: 1000}
	before, err := agent.BuildCompactionPlan(lineage, nil, policy, config, runtime, runtime, "system", nil, "")
	require.NoError(t, err)

	_, err = db.Exec(`UPDATE run_messages SET payload_json='[{"type":"text","text":"private shadow poison"}]'
		WHERE run_id IN (?,?)`, runIDs[0], runIDs[1])
	require.NoError(t, err)
	_, err = store.LoadRunTranscript(ctx, db, runIDs[0])
	require.Error(t, err, "the fixture must prove its private shadow differs from canonical history")

	afterLineage, err := messages.HostedContextLineage(ctx, sessionID, *session.ActiveLeafMessageID)
	require.NoError(t, err)
	after, err := agent.BuildCompactionPlan(afterLineage, nil, policy, config, runtime, runtime, "system", nil, "")
	require.NoError(t, err)
	assert.Equal(t, before.SourceDigest, after.SourceDigest)
	assert.Equal(t, before.SerializedSource, after.SerializedSource)
	assert.Equal(t, before.SourceFromMessageID, after.SourceFromMessageID)
	assert.Equal(t, before.SourceThroughMessageID, after.SourceThroughMessageID)
	assert.NotContains(t, after.SerializedSource, "private shadow poison")
}

func TestFormatTwoAncestorUsesMixedLedgerProjectionForNewTurnAndManualCompaction(t *testing.T) {
	db, sessionID, runIDs := createHostedCompactionHistory(t)
	ctx := context.Background()
	_, err := db.Exec(`DROP TRIGGER agent_runs_commit_format_immutable`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE agent_runs SET commit_format_version=2 WHERE id=?`, runIDs[len(runIDs)-1])
	require.NoError(t, err)

	runs := &store.RunRepo{DB: db}
	next, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: sessionID, ClientRequestID: "mixed-context", Text: "continue from mixed history",
	})
	require.NoError(t, err)
	claimed, err := runs.Claim(ctx, next.Run.ID)
	require.NoError(t, err)
	projected, err := (&store.ContextProjector{DB: db}).ProjectAndFreeze(ctx, *claimed)
	require.NoError(t, err)
	require.NotEmpty(t, projected.Messages)
	require.NoError(t, runs.Fail(ctx, claimed.ID, "qualification_complete", "projection qualification complete"))

	compactions := &store.CompactionRepo{DB: db}
	manual, err := compactions.CreateManual(ctx, domain.ManualCompactionInput{
		SessionID: sessionID, BaseMessageID: claimed.BaseMessageID, ClientRequestID: "mixed-compaction",
	})
	require.NoError(t, err)
	compactionRun, err := runs.Claim(ctx, manual.RunID)
	require.NoError(t, err)
	service := &Service{Repo: compactions, Messages: &store.MessageRepo{DB: db}}
	err = service.ExecuteManual(ctx, compactionRun, &store.ResolvedRunConfig{}, "system", nil)
	require.Error(t, err)
	assert.NotEqual(t, domain.ErrorContextProjectionNotEnabled, domain.ErrorCodeOf(err),
		"manual compaction must pass the mixed-ledger reader before validating runtime configuration")
}
