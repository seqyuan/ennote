package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateManualCompactionIsIdempotentAndDoesNotChangeMessages(t *testing.T) {
	db := SetupDB(t)
	seedCompactionSession(t, db)
	repo := &CompactionRepo{DB: db}
	input := domain.ManualCompactionInput{SessionID: "session", BaseMessageID: "m3",
		ClientRequestID: "compact-once", Instructions: "keep exact paths"}

	first, err := repo.CreateManual(context.Background(), input)
	require.NoError(t, err)
	second, err := repo.CreateManual(context.Background(), input)
	require.NoError(t, err)
	assert.False(t, first.Existing)
	assert.True(t, second.Existing)
	assert.Equal(t, first.RunID, second.RunID)
	assert.Equal(t, first.CompactionID, second.CompactionID)

	var runKind string
	var turnID sql.NullString
	require.NoError(t, db.QueryRow(`SELECT run_kind,turn_id FROM agent_runs WHERE id=?`, first.RunID).Scan(&runKind, &turnID))
	assert.Equal(t, string(domain.RunKindContextCompaction), runKind)
	assert.False(t, turnID.Valid)
	var messageCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id='session'`).Scan(&messageCount))
	assert.Equal(t, 3, messageCount)

	_, err = (&RunRepo{DB: db}).SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: "session", ClientRequestID: "blocked", Text: "new turn"})
	assert.ErrorIs(t, err, ErrSessionCompacting)
}

func TestLatestValidCompactionStaysOnCurrentBranch(t *testing.T) {
	db := SetupDB(t)
	now := "2026-07-28T00:00:00Z"
	_, err := db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('project','P',?,?);
		INSERT INTO sessions(id,project_id,active_leaf_message_id,created_at,updated_at) VALUES('session','project','a3',?,?);
		INSERT INTO messages(id,session_id,parent_message_id,role,created_at) VALUES
		('root','session',NULL,'user',?),('a2','session','root','assistant',?),('a3','session','a2','user',?),
		('b2','session','root','assistant',?),('b3','session','b2','user',?);`, now, now, now, now, now, now, now, now, now)
	require.NoError(t, err)
	summaryA, summaryB := "branch A summary", "branch B summary"
	policy := domain.PolicySnapshot{ID: "policy", Kind: domain.PolicyKindCompaction, Version: 1, Config: json.RawMessage(`{}`)}
	runtime := domain.ModelRuntimeSnapshot{ModelProfileID: "model", APIModel: "model", ContextTokens: 32000, MaxOutputTokens: 2000}
	effective, err := json.Marshal(domain.EffectiveRunConfig{CompactionPolicy: policy, CompactionRuntime: runtime})
	require.NoError(t, err)
	sourceDigest := agent.ComputeSourceDigest(nil, []domain.Message{{ID: "root", Role: "user"}}, "v1", "", runtime)
	contractDigest := agent.ComputeSummaryContractDigest(policy, runtime, "v1", "")
	_, err = db.Exec(`INSERT INTO context_compactions
		(id,session_id,status,reason,requested_config_json,effective_config_json,base_leaf_message_id,
		source_from_message_id,source_through_message_id,first_kept_message_id,source_digest,
		summary_contract_digest,summary,summary_digest,prompt_version,finished_at,created_at)
		VALUES
		('cp-a','session','completed','manual','{}',?,'a3','root','root','a2',?,?,?,?,'v1',?,?),
		('cp-b','session','completed','manual','{}',?,'b3','root','root','b2',?,?,?,?,'v1',?,?)`,
		string(effective), sourceDigest, contractDigest, summaryA, digestText(summaryA), now, now,
		string(effective), sourceDigest, contractDigest, summaryB, digestText(summaryB), now, "2026-07-28T01:00:00Z")
	require.NoError(t, err)
	lineage, err := (&MessageRepo{DB: db}).Lineage(context.Background(), "session", "a3")
	require.NoError(t, err)

	selected, err := (&CompactionRepo{DB: db}).LatestValid(context.Background(), "session", lineage)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "cp-a", selected.ID)

	_, err = db.Exec(`UPDATE context_compactions SET source_digest='tampered' WHERE id='cp-a'`)
	require.NoError(t, err)
	selected, err = (&CompactionRepo{DB: db}).LatestValid(context.Background(), "session", lineage)
	require.NoError(t, err)
	assert.Nil(t, selected)
}

func TestManualCompactionRejectsDisabledPolicy(t *testing.T) {
	db := SetupDB(t)
	seedCompactionSession(t, db)
	now := "2026-07-28T00:00:00Z"
	_, err := db.Exec(`INSERT INTO policy_profiles(id,name,kind,version,config_json,status,created_at,updated_at)
		VALUES('disabled','disabled','compaction',1,'{"mode":"disabled","triggerRatio":0.75,"keepRecentTurns":2,"tailTokenRatio":0.2,"tailMinTokens":8000,"tailMaxTokens":32000,"summaryInputRatio":0.7,"compactionModelProfileId":null,"summaryMaxOutputTokens":4096,"includeReasoning":false,"allowHistoryLookup":true,"allowOverflowRecovery":false,"maxOverflowRecoveries":0,"ineffectiveReclaimRatio":0.1,"ineffectiveLimit":3,"failureCooldownSeconds":600,"promptVersion":"v1"}','active',?,?);
		UPDATE sessions SET compaction_policy_profile_id='disabled' WHERE id='session'`, now, now)
	require.NoError(t, err)

	_, err = (&CompactionRepo{DB: db}).CreateManual(context.Background(), domain.ManualCompactionInput{
		SessionID: "session", BaseMessageID: "m3", ClientRequestID: "disabled"})
	assert.Equal(t, domain.ErrorCompactionNotAllowed, domain.ErrorCodeOf(err))
}

func seedCompactionSession(t *testing.T, db *sql.DB) {
	t.Helper()
	now := "2026-07-28T00:00:00Z"
	_, err := db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('project','P',?,?);
		INSERT INTO sessions(id,project_id,active_leaf_message_id,created_at,updated_at) VALUES('session','project','m3',?,?);
		INSERT INTO messages(id,session_id,parent_message_id,role,created_at) VALUES
		('m1','session',NULL,'user',?),('m2','session','m1','assistant',?),('m3','session','m2','user',?);
		INSERT INTO message_parts(id,message_id,ordinal,block_kind,payload_json) VALUES
		('p1','m1',0,'text','{"text":"first"}'),('p2','m2',0,'text','{"text":"answer"}'),('p3','m3',0,'text','{"text":"latest"}')`,
		now, now, now, now, now, now, now)
	require.NoError(t, err)
}
