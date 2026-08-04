package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostedCompatibilityRestartHasNoHalfTranscriptState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, store.Migrate(db))
	_, err = db.Exec(`UPDATE settings SET value='1' WHERE key='hosted_commit_format_version'`)
	require.NoError(t, err)

	projects := &store.ProjectRepo{DB: db}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "restart", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := &store.SessionRepo{DB: db}
	completedSession, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "completed"})
	require.NoError(t, err)
	runs := &store.RunRepo{DB: db}
	completed, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: completedSession.ID,
		ClientRequestID: "completed", Text: "run"})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, completed.Run.ID)
	require.NoError(t, err)
	require.NoError(t, runs.FinalizeSuccess(ctx, completed.Run.ID, domain.RunOutput{Messages: []domain.ChatMessage{{
		Role: domain.RoleAssistant, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}},
	}}}))

	interruptedSession, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "interrupted"})
	require.NoError(t, err)
	interrupted, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: interruptedSession.ID,
		ClientRequestID: "interrupted", Text: "run", RequestedConfig: []byte(`{"maxIterations":4}`)})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, interrupted.Run.ID)
	require.NoError(t, err)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO run_messages
		(id,run_id,ordinal,role,payload_json,visibility,created_at)
		VALUES('uncommitted',?,0,'assistant','[]','private',?)`, interrupted.Run.ID,
		time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, db.Close())

	reopened, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.NoError(t, store.Migrate(reopened))
	transcript, err := store.LoadRunTranscript(ctx, reopened, completed.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.TranscriptSourceShadow, transcript.Source)
	require.Len(t, transcript.Messages, 1)

	var halfRows int
	require.NoError(t, reopened.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, interrupted.Run.ID).Scan(&halfRows))
	assert.Zero(t, halfRows)
	reopenedRuns := &store.RunRepo{DB: reopened}
	requeued, err := reopenedRuns.RecoverActive(ctx)
	require.NoError(t, err)
	assert.Empty(t, requeued)
	stored, err := reopenedRuns.Get(ctx, interrupted.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunInterrupted, stored.Status)

	retry, err := reopenedRuns.Retry(ctx, interrupted.Run.ID, "retry-after-restart")
	require.NoError(t, err)
	assert.Equal(t, domain.CommitFormatLegacyV1, retry.Run.CommitFormatVersion)
	assert.JSONEq(t, `{"maxIterations":4}`, string(retry.Run.RequestedConfig))
	require.NoError(t, reopened.QueryRow(`SELECT COUNT(*) FROM run_messages WHERE run_id=?`, retry.Run.ID).Scan(&halfRows))
	assert.Zero(t, halfRows)
}
