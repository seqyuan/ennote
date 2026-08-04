package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func transcriptOutput() domain.RunOutput {
	call := domain.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"sample.txt"}`)}
	result := domain.ToolResult{ToolCallID: call.ID, ToolName: call.Name, Content: "contents"}
	return domain.RunOutput{Messages: []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "checking"}, {Kind: domain.ContentToolCall, ToolCall: &call}}},
		{Role: domain.RoleTool, Content: []domain.ContentBlock{{Kind: domain.ContentToolResult, ToolResult: &result}}},
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}},
	}}
}

func TestFinalizeSuccessWritesPrivateShadowWithCanonicalParity(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "shadow-parity")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, repo.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput()))

	transcript, err := store.LoadRunTranscript(ctx, repo.DB, submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.TranscriptSourceShadow, transcript.Source)
	assert.Equal(t, domain.CommitFormatLegacyV1, transcript.FormatVersion)
	require.Len(t, transcript.Messages, 3)
	assert.NotEmpty(t, transcript.Digest)
	for ordinal, message := range transcript.Messages {
		assert.Equal(t, ordinal, message.Ordinal)
		assert.Equal(t, domain.VisibilityPrivate, message.Visibility)
	}
	var eventCount int
	require.NoError(t, repo.DB.QueryRow(`SELECT COUNT(*) FROM run_events
		WHERE run_id=? AND event_type='run_transcript_committed'`, submission.Run.ID).Scan(&eventCount))
	assert.Equal(t, 1, eventCount)
}

func TestLoadRunTranscriptFallsBackAndFailsOnMismatch(t *testing.T) {
	t.Run("historical shadow missing", func(t *testing.T) {
		repo, submission := setupSubmittedRun(t, "shadow-missing")
		_, err := repo.Claim(context.Background(), submission.Run.ID)
		require.NoError(t, err)
		require.NoError(t, repo.FinalizeSuccess(context.Background(), submission.Run.ID, transcriptOutput()))
		_, err = repo.DB.Exec(`DELETE FROM run_messages WHERE run_id=?`, submission.Run.ID)
		require.NoError(t, err)
		transcript, err := store.LoadRunTranscript(context.Background(), repo.DB, submission.Run.ID)
		require.NoError(t, err)
		assert.Equal(t, store.TranscriptSourceLegacy, transcript.Source)
		require.Len(t, transcript.Messages, 3)
	})

	t.Run("shadow ordinal gap", func(t *testing.T) {
		repo, submission := setupSubmittedRun(t, "shadow-gap")
		_, err := repo.Claim(context.Background(), submission.Run.ID)
		require.NoError(t, err)
		require.NoError(t, repo.FinalizeSuccess(context.Background(), submission.Run.ID, transcriptOutput()))
		_, err = repo.DB.Exec(`DELETE FROM run_messages WHERE run_id=? AND ordinal=1`, submission.Run.ID)
		require.NoError(t, err)
		_, err = store.LoadRunTranscript(context.Background(), repo.DB, submission.Run.ID)
		require.Error(t, err)
		assert.Equal(t, domain.ErrorTranscriptCorrupt, domain.ErrorCodeOf(err))
	})

	t.Run("shadow payload mismatch", func(t *testing.T) {
		repo, submission := setupSubmittedRun(t, "shadow-mismatch")
		_, err := repo.Claim(context.Background(), submission.Run.ID)
		require.NoError(t, err)
		require.NoError(t, repo.FinalizeSuccess(context.Background(), submission.Run.ID, transcriptOutput()))
		_, err = repo.DB.Exec(`UPDATE run_messages SET payload_json='[{"type":"text","text":"changed"}]'
			WHERE run_id=? AND ordinal=0`, submission.Run.ID)
		require.NoError(t, err)
		_, err = store.LoadRunTranscript(context.Background(), repo.DB, submission.Run.ID)
		require.Error(t, err)
		assert.Equal(t, domain.ErrorTranscriptShadowMismatch, domain.ErrorCodeOf(err))
	})
}

func TestFinalizeRejectsFormatTwoBeforeAnyWrite(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "format-two-disabled")
	_, err := repo.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	_, err = repo.DB.Exec(`DROP TRIGGER agent_runs_commit_format_immutable`)
	require.NoError(t, err)
	_, err = repo.DB.Exec(`UPDATE agent_runs SET commit_format_version=2 WHERE id=?`, submission.Run.ID)
	require.NoError(t, err)

	err = repo.FinalizeSuccess(context.Background(), submission.Run.ID, transcriptOutput())
	require.Error(t, err)
	assert.Equal(t, domain.ErrorCommitFormatNotEnabled, domain.ErrorCodeOf(err))
	for _, table := range []string{"messages", "run_messages"} {
		var count int
		require.NoError(t, repo.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE run_id=?`, submission.Run.ID).Scan(&count))
		assert.Zero(t, count, table)
	}
}

func TestFormatTwoFixtureIsPubliclyFilteredButRejectedForExecutionLineage(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "format-two-page")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, repo.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput()))
	_, err = repo.DB.Exec(`DROP TRIGGER agent_runs_commit_format_immutable`)
	require.NoError(t, err)
	_, err = repo.DB.Exec(`UPDATE agent_runs SET commit_format_version=2 WHERE id=?`, submission.Run.ID)
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: repo.DB}).FindByID(ctx, submission.Run.SessionID)
	require.NoError(t, err)
	require.NotNil(t, session.ActiveLeafMessageID)

	messages := &store.MessageRepo{DB: repo.DB}
	page, err := messages.Page(ctx, session.ID, *session.ActiveLeafMessageID, "", 1)
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	assert.Equal(t, domain.VisibilityPublic, page.Messages[0].Visibility)
	assert.True(t, page.HasMore)
	older, err := messages.Page(ctx, session.ID, *session.ActiveLeafMessageID, page.NextBeforeMessageID, 1)
	require.NoError(t, err)
	require.Len(t, older.Messages, 1)
	assert.Equal(t, "user", older.Messages[0].Role)
	assert.False(t, older.HasMore)
	_, err = (&store.MessageRepo{DB: repo.DB}).Lineage(ctx, session.ID, *session.ActiveLeafMessageID)
	require.Error(t, err)
	assert.Equal(t, domain.ErrorContextProjectionNotEnabled, domain.ErrorCodeOf(err))
}

func TestTranscriptAndCanonicalWritesRollbackTogether(t *testing.T) {
	failureTrigger := func(point, runID string) string {
		switch point {
		case "run_messages_insert":
			return `CREATE TRIGGER fail_transcript_finalization BEFORE INSERT ON run_messages
				BEGIN SELECT RAISE(ABORT, 'injected shadow failure'); END`
		case "canonical_messages_insert":
			return fmt.Sprintf(`CREATE TRIGGER fail_transcript_finalization BEFORE INSERT ON messages
				WHEN NEW.run_id='%s' BEGIN SELECT RAISE(ABORT, 'injected canonical failure'); END`, runID)
		default:
			return fmt.Sprintf(`CREATE TRIGGER fail_transcript_finalization BEFORE INSERT ON run_events
				WHEN NEW.event_type='%s' BEGIN SELECT RAISE(ABORT, 'injected event failure'); END`, point)
		}
	}
	for _, point := range []string{
		"run_messages_insert", "canonical_messages_insert", "run_transcript_committed", "run_succeeded",
	} {
		t.Run(point, func(t *testing.T) {
			repo, submission := setupSubmittedRun(t, "rollback-"+point)
			ctx := context.Background()
			_, err := repo.Claim(ctx, submission.Run.ID)
			require.NoError(t, err)
			var originalSessionLeaf, originalBranchLeaf string
			require.NoError(t, repo.DB.QueryRow(`SELECT active_leaf_message_id FROM sessions WHERE id=?`,
				submission.Run.SessionID).Scan(&originalSessionLeaf))
			require.NoError(t, repo.DB.QueryRow(`SELECT leaf_message_id FROM session_branches
				WHERE id=(SELECT active_branch_id FROM sessions WHERE id=?)`, submission.Run.SessionID).Scan(&originalBranchLeaf))
			_, err = repo.DB.Exec(failureTrigger(point, submission.Run.ID))
			require.NoError(t, err)
			require.Error(t, repo.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput()))
			for _, table := range []string{"messages", "run_messages"} {
				var count int
				require.NoError(t, repo.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE run_id=?`, submission.Run.ID).Scan(&count))
				assert.Zero(t, count, table)
			}
			var sessionLeaf, branchLeaf string
			var terminalEvents int
			require.NoError(t, repo.DB.QueryRow(`SELECT active_leaf_message_id FROM sessions WHERE id=?`,
				submission.Run.SessionID).Scan(&sessionLeaf))
			require.NoError(t, repo.DB.QueryRow(`SELECT leaf_message_id FROM session_branches
				WHERE id=(SELECT active_branch_id FROM sessions WHERE id=?)`, submission.Run.SessionID).Scan(&branchLeaf))
			require.NoError(t, repo.DB.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id=?
				AND event_type IN ('message_committed','run_transcript_committed','run_telemetry','run_succeeded')`,
				submission.Run.ID).Scan(&terminalEvents))
			assert.Equal(t, originalSessionLeaf, sessionLeaf)
			assert.Equal(t, originalBranchLeaf, branchLeaf)
			assert.Zero(t, terminalEvents)
			run, err := repo.Get(ctx, submission.Run.ID)
			require.NoError(t, err)
			assert.Equal(t, domain.RunRunning, run.Status)
		})
	}
}

func TestResumeMessagesUsesProviderToolCallIDAndFoldedResult(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "resume-tool-result")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)

	providerToolCallID := "call_provider_assigned"
	internalRecordID := "internal-record-uuid"
	call := domain.ToolCall{ID: providerToolCallID, Name: "delegate_roles",
		Arguments: json.RawMessage(`{"delegations":[]}`)}
	placeholder := domain.ToolResult{ToolCallID: providerToolCallID, ToolName: call.Name,
		Content: `{"status":"delegated"}`}
	waitingMessages := []domain.ChatMessage{
		{Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentToolCall, ToolCall: &call}}},
		{Role: domain.RoleTool, Content: []domain.ContentBlock{{Kind: domain.ContentToolResult, ToolResult: &placeholder}}},
	}
	tx, err := repo.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, _, err = store.AppendRunMessagesTx(ctx, tx, submission.Run.ID,
		domain.CommitFormatLegacyV1, waitingMessages, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	calls := &store.CallRepo{DB: repo.DB}
	require.NoError(t, calls.ToolStarted(ctx, domain.ToolCallStart{ID: internalRecordID,
		RunID: submission.Run.ID, Iteration: 1, CallIndex: 0, Call: call}))
	folded := `{"children":[{"name":"explore","status":"succeeded"}]}`
	require.NoError(t, calls.ToolCompleted(ctx, domain.ToolCallFinish{ID: internalRecordID,
		RunID: submission.Run.ID, Iteration: 1, CallIndex: 0, Call: call,
		Result: domain.ToolResult{ToolCallID: providerToolCallID, ToolName: call.Name, Content: folded}}))

	messages, err := (&store.RunMessageRepo{DB: repo.DB}).ResumeMessages(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Len(t, messages[0].Content, 1)
	require.Len(t, messages[1].Content, 1)
	require.NotNil(t, messages[0].Content[0].ToolCall)
	require.NotNil(t, messages[1].Content[0].ToolResult)
	assert.Equal(t, providerToolCallID, messages[0].Content[0].ToolCall.ID)
	assert.Equal(t, providerToolCallID, messages[1].Content[0].ToolResult.ToolCallID)
	assert.NotEqual(t, internalRecordID, messages[1].Content[0].ToolResult.ToolCallID)
	assert.Equal(t, folded, messages[1].Content[0].ToolResult.Content)

	complete := append(messages, domain.ChatMessage{Role: domain.RoleAssistant,
		Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "Child completed."}}})
	tx, err = repo.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	committed, _, err := store.AppendRunMessagesTx(ctx, tx, submission.Run.ID,
		domain.CommitFormatLegacyV1, complete, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.Len(t, committed, 3)
	assert.Equal(t, folded, committed[1].Content[0].ToolResult.Content)

	transcript, err := (&store.RunMessageRepo{DB: repo.DB}).List(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.Len(t, transcript.Messages, 3)
	assert.Equal(t, folded, transcript.Messages[1].Content[0].ToolResult.Content)
	assert.Equal(t, "Child completed.", transcript.Messages[2].Content[0].Text)
}

func TestRunMessageConstraintsAndCanonicalBranchFacts(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "shadow-constraints")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	require.NoError(t, repo.FinalizeSuccess(ctx, submission.Run.ID, transcriptOutput()))

	branches, err := (&store.BranchRepo{DB: repo.DB}).List(ctx, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, branches, 1)
	assert.Equal(t, 4, branches[0].MessageCount, "branch count is user input plus three canonical outputs")
	var shadowCount int
	require.NoError(t, repo.DB.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, submission.Run.ID).Scan(&shadowCount))
	assert.Equal(t, 3, shadowCount)

	_, err = repo.DB.Exec(`INSERT INTO run_messages(id,run_id,ordinal,role,payload_json,visibility,created_at)
		VALUES('duplicate',?,0,'assistant','[]','private','2026-08-03T00:00:00Z')`, submission.Run.ID)
	require.Error(t, err)
	_, err = repo.DB.Exec(`INSERT INTO run_messages(id,run_id,ordinal,role,payload_json,visibility,created_at)
		VALUES('orphan','missing-run',0,'assistant','[]','private','2026-08-03T00:00:00Z')`)
	require.Error(t, err)

	after, err := (&store.BranchRepo{DB: repo.DB}).List(ctx, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, branches[0].MessageCount, after[0].MessageCount)
	assert.Equal(t, branches[0].LeafMessageID, after[0].LeafMessageID)
}
