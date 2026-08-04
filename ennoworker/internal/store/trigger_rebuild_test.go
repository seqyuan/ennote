package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRebuildPreservesCommitFormatTriggers(t *testing.T) {
	repo, submission := setupSubmittedRun(t, "trigger-rebuild")
	ctx := context.Background()
	_, err := repo.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = repo.DB.Exec(`UPDATE agent_runs SET commit_format_version=2 WHERE id=?`, submission.Run.ID)
	require.Error(t, err, "commit format immutability trigger must survive the migration 21 rebuild")
	_, err = repo.DB.Exec(`INSERT INTO agent_runs(id,session_id,run_kind,status,commit_format_version,created_at) VALUES('x','s','agent','queued',3,'2026-08-03T00:00:00Z')`)
	require.Error(t, err, "commit format validation trigger must survive the rebuild")
}
