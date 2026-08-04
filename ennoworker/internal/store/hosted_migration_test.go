package store_test

import (
	"testing"

	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostedSpeakerLedgerFoundationMigration(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, migration := range migrations.Sorted() {
		if migration.Version >= 18 {
			break
		}
		_, err = db.Exec(migration.SQL)
		require.NoError(t, err, "migration %d", migration.Version)
		_, err = db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?, '2026-08-03T00:00:00Z')`, migration.Version)
		require.NoError(t, err)
	}
	const now = "2026-08-03T00:00:00Z"
	_, err = db.Exec(`
		INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Project',?,?);
		INSERT INTO sessions(id,project_id,title,created_at,updated_at) VALUES('s','p','Existing',?,?);
		INSERT INTO messages(id,session_id,parent_message_id,role,created_at) VALUES
			('u','s',NULL,'user',?),
			('a1','s','u','assistant',?),
			('tool','s','a1','tool',?),
			('final','s','tool','assistant',?),
			('pending-user','s','final','user',?);
		INSERT INTO turns(id,session_id,client_request_id,user_message_id,base_message_id,status,created_at,updated_at)
			VALUES('t1','s','req-1','u',NULL,'succeeded',?,?),
			      ('t2','s','req-2','pending-user','final','waiting_for_approval',?,?);
		INSERT INTO agent_runs(id,turn_id,session_id,run_kind,base_message_id,status,assistant_message_id,created_at,finished_at)
			VALUES('r1','t1','s','agent','u','succeeded','final',?,?),
			      ('r2','t2','s','agent','pending-user','waiting_for_approval',NULL,?,NULL);
		UPDATE messages SET run_id='r1' WHERE id IN ('a1','tool','final');
		INSERT INTO session_branches(id,session_id,leaf_message_id,label,created_at,updated_at)
			VALUES('branch','s','pending-user','Main',?,?);
		UPDATE sessions SET active_branch_id='branch',active_leaf_message_id='pending-user' WHERE id='s';
		INSERT INTO context_compactions(id,run_id,session_id,status,reason,requested_config_json,effective_config_json,
			base_leaf_message_id,source_from_message_id,source_through_message_id,first_kept_message_id,
			source_digest,summary_contract_digest,summary,summary_digest,prompt_version,finished_at,created_at)
			VALUES('compact','r1','s','completed','manual','{}','{}','final','u','tool','final',
			'source','contract','summary','summary-digest','v1',?,?);
		INSERT INTO run_execution_checkpoints(id,run_id,schema_version,iteration,batch_digest,state_json,status,created_at)
			VALUES('checkpoint','r2',5,1,'batch','{}','pending',?);
		INSERT INTO tool_approval_requests(id,run_id,session_id,checkpoint_id,iteration,batch_digest,status,items_json,requested_at)
			VALUES('approval','r2','s','checkpoint',1,'batch','pending','[]',?);`,
		now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now, now)
	require.NoError(t, err)

	var beforeParent, beforeLeaf, beforeCompaction, beforeApproval string
	require.NoError(t, db.QueryRow(`SELECT parent_message_id FROM messages WHERE id='final'`).Scan(&beforeParent))
	require.NoError(t, db.QueryRow(`SELECT active_leaf_message_id FROM sessions WHERE id='s'`).Scan(&beforeLeaf))
	require.NoError(t, db.QueryRow(`SELECT id FROM context_compactions WHERE id='compact'`).Scan(&beforeCompaction))
	require.NoError(t, db.QueryRow(`SELECT id FROM tool_approval_requests WHERE id='approval'`).Scan(&beforeApproval))

	require.NoError(t, store.Migrate(db))
	var mode, parent, leaf, compactionID, approvalID string
	require.NoError(t, db.QueryRow(`SELECT mode,active_leaf_message_id FROM sessions WHERE id='s'`).Scan(&mode, &leaf))
	require.NoError(t, db.QueryRow(`SELECT parent_message_id FROM messages WHERE id='final'`).Scan(&parent))
	require.NoError(t, db.QueryRow(`SELECT id FROM context_compactions WHERE id='compact'`).Scan(&compactionID))
	require.NoError(t, db.QueryRow(`SELECT id FROM tool_approval_requests WHERE id='approval'`).Scan(&approvalID))
	assert.Equal(t, "hosted", mode)
	assert.Equal(t, beforeParent, parent)
	assert.Equal(t, beforeLeaf, leaf)
	assert.Equal(t, beforeCompaction, compactionID)
	assert.Equal(t, beforeApproval, approvalID)

	for id, visibility := range map[string]string{"a1": "legacy_execution", "tool": "legacy_execution", "final": "public"} {
		var speaker, actualVisibility string
		require.NoError(t, db.QueryRow(`SELECT speaker_kind,visibility FROM messages WHERE id=?`, id).Scan(&speaker, &actualVisibility))
		assert.Equal(t, "host", speaker)
		assert.Equal(t, visibility, actualVisibility)
	}
	var userSpeaker, addressee string
	require.NoError(t, db.QueryRow(`SELECT speaker_kind,addressee_kind FROM messages WHERE id='u'`).Scan(&userSpeaker, &addressee))
	assert.Equal(t, "user", userSpeaker)
	assert.Equal(t, "host", addressee)
	var format, shadows int
	var root, promptSnapshot, promptDigest string
	require.NoError(t, db.QueryRow(`SELECT commit_format_version,root_run_id,system_prompt_snapshot_json,system_prompt_digest
		FROM agent_runs WHERE id='r1'`).Scan(&format, &root, &promptSnapshot, &promptDigest))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_messages`).Scan(&shadows))
	assert.Equal(t, 1, format)
	assert.Equal(t, "r1", root)
	assert.NotEqual(t, `{}`, promptSnapshot)
	assert.NotEmpty(t, promptDigest)
	assert.Zero(t, shadows)

	_, err = db.Exec(`UPDATE sessions SET mode='room' WHERE id='s'`)
	require.Error(t, err)
	_, err = db.Exec(`UPDATE agent_runs SET commit_format_version=2 WHERE id='r1'`)
	require.Error(t, err)
}
