package sessionstore

import (
	"context"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/stretchr/testify/require"
)

func TestBackfillMessageSeqAssignsMonotonicByCreatedAt(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	projects := &projectstore.Store{Root: home + "/projects"}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "p", HostPath: t.TempDir()})
	require.NoError(t, err)
	manager := NewManager(projects.Root, projects)
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "s"})
	require.NoError(t, err)
	db, err := manager.OpenSession(ctx, session.ID)
	require.NoError(t, err)

	// Simulate legacy rows with seq=0 (the migration default), inserted out of
	// created_at order to prove ordering follows (created_at, id).
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		id := "legacy-" + string(rune('a'+i))
		_, err := db.ExecContext(ctx, `INSERT INTO messages (id, session_id, parent_message_id, role, status, speaker_kind, speaker_snapshot_json, visibility, created_at)
			VALUES (?,?,NULL,'user','complete','user','{"kind":"user"}','public',?)`,
			id, session.ID, now.Add(time.Duration(i)*time.Second).Format(time.RFC3339Nano))
		require.NoError(t, err)
	}

	require.NoError(t, backfillMessageSeq(db))

	rows, err := db.Query(`SELECT id, seq FROM messages ORDER BY seq`)
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	var index int64
	for rows.Next() {
		var id string
		var seq int64
		require.NoError(t, rows.Scan(&id, &seq))
		index++
		require.Equal(t, index, seq, "backfilled seq must be contiguous from 1")
		ids = append(ids, id)
	}
	require.Equal(t, []string{"legacy-a", "legacy-b", "legacy-c"}, ids)
}

func TestBackfillMessageSeqNoopWhenAllSequenced(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	projects := &projectstore.Store{Root: home + "/projects"}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "p", HostPath: t.TempDir()})
	require.NoError(t, err)
	manager := NewManager(projects.Root, projects)
	t.Cleanup(func() { _ = manager.Close() })
	session, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "s"})
	require.NoError(t, err)
	db, err := manager.OpenSession(ctx, session.ID)
	require.NoError(t, err)

	// Fresh session has zero messages; backfill must be a no-op.
	require.NoError(t, backfillMessageSeq(db))
}
