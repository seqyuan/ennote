package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSessionManager seeds one project and returns the file-native Session
// manager plus the project id (V2 per-Session SQLite files).
func newSessionManager(t *testing.T) (*sessionstore.Manager, string) {
	t.Helper()
	ctx := context.Background()
	projects := &projectstore.Store{Root: t.TempDir()}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)
	manager := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	return manager, project.ID
}

func TestSessionSearchSeparatesLifecycleAndEscapesWildcards(t *testing.T) {
	manager, projectID := newSessionManager(t)
	otherID := "00000000-0000-4000-8000-00000000000a"
	sessions := &store.SessionRepo{Files: manager}
	ctx := context.Background()
	// The "other project" must exist in the same project store for the manager
	// to admit a Session under it.
	otherProject, _, err := sessions.Files.Projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Other", HostPath: t.TempDir()})
	require.NoError(t, err)
	otherID = otherProject.ID
	_ = otherID
	values := []string{"Alpha atlas", "alpha 100%", "under_score", "Unrelated"}
	created := make([]*domain.Session, 0, len(values))
	for _, title := range values {
		value, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: title})
		require.NoError(t, err)
		created = append(created, value)
	}
	_, err = sessions.Create(ctx, domain.CreateSessionInput{ProjectID: otherID, Title: "Alpha other project"})
	require.NoError(t, err)
	_, err = sessions.Archive(ctx, created[0].ID)
	require.NoError(t, err)

	active, err := sessions.SearchByProject(ctx, projectID, store.SessionStatusActive, "alpha")
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, "alpha 100%", active[0].Title)
	archived, err := sessions.SearchByProject(ctx, projectID, store.SessionStatusArchived, "ALPHA")
	require.NoError(t, err)
	require.Len(t, archived, 1)
	assert.Equal(t, "Alpha atlas", archived[0].Title)
	percent, err := sessions.SearchByProject(ctx, projectID, store.SessionStatusActive, "100%")
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha 100%"}, sessionTitles(percent))
	underscore, err := sessions.SearchByProject(ctx, projectID, store.SessionStatusActive, "_")
	require.NoError(t, err)
	assert.Equal(t, []string{"under_score"}, sessionTitles(underscore))
	_, err = sessions.SearchByProject(ctx, projectID, "all", "")
	assert.ErrorIs(t, err, store.ErrSessionSearchInvalid)
	_, err = sessions.SearchByProject(ctx, projectID, store.SessionStatusActive, strings.Repeat("x", 121))
	assert.ErrorIs(t, err, store.ErrSessionSearchInvalid)
}

func TestSessionSearchRejectsCorruptTimestamps(t *testing.T) {
	manager, projectID := newSessionManager(t)
	ctx := context.Background()
	repo := &store.SessionRepo{Files: manager}
	session, err := repo.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Corrupt"})
	require.NoError(t, err)
	db, err := manager.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sessions SET updated_at='not-a-timestamp' WHERE id=?`, session.ID)
	require.NoError(t, err)

	_, err = repo.SearchByProject(ctx, projectID, store.SessionStatusActive, "")
	require.ErrorContains(t, err, "cannot parse")
}

func TestSessionArchiveRestorePreservesCanonicalBranchState(t *testing.T) {
	manager, projectID := newSessionManager(t)
	ctx := context.Background()
	repo := &store.SessionRepo{Files: manager}
	session, err := repo.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Lifecycle"})
	require.NoError(t, err)
	db, err := manager.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	message, err := (&store.MessageRepo{DB: db}).CreateUserMessage(ctx, session.ID, "", "keep")
	require.NoError(t, err)
	require.NoError(t, repo.ActivateLeaf(ctx, session.ID, message.ID))
	before, err := repo.FindByID(ctx, session.ID)
	require.NoError(t, err)

	archived, err := repo.Archive(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionStatusArchived, archived.Status)
	assert.Equal(t, before.ActiveBranchID, archived.ActiveBranchID)
	assert.Equal(t, before.ActiveLeafMessageID, archived.ActiveLeafMessageID)
	_, err = repo.Archive(ctx, session.ID)
	assert.ErrorIs(t, err, store.ErrSessionStateConflict)

	restored, err := repo.Restore(ctx, session.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionStatusActive, restored.Status)
	assert.Equal(t, before.ActiveBranchID, restored.ActiveBranchID)
	assert.Equal(t, before.ActiveLeafMessageID, restored.ActiveLeafMessageID)
	_, err = repo.Restore(ctx, session.ID)
	assert.ErrorIs(t, err, store.ErrSessionStateConflict)
	var messages, branches int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=?`, session.ID).Scan(&messages))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM session_branches WHERE session_id=?`, session.ID).Scan(&branches))
	assert.Equal(t, 1, messages)
	assert.Equal(t, 1, branches)
}

func TestSessionArchiveRejectsEveryActiveRunState(t *testing.T) {
	for _, status := range []domain.RunStatus{domain.RunQueued, domain.RunRunning, domain.RunWaitingForApproval} {
		t.Run(string(status), func(t *testing.T) {
			manager, projectID := newSessionManager(t)
			ctx := context.Background()
			sessions := &store.SessionRepo{Files: manager}
			session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "Busy"})
			require.NoError(t, err)
			db, err := manager.OpenSession(ctx, session.ID)
			require.NoError(t, err)
			runs := &store.RunRepo{DB: db}
			submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "turn", Text: "run"})
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE agent_runs SET status=? WHERE id=?`, status, submission.Run.ID)
			require.NoError(t, err)
			_, err = sessions.Archive(ctx, session.ID)
			assert.True(t, errors.Is(err, store.ErrSessionRunActive) || errors.Is(err, store.ErrSessionBusy), err)
		})
	}
}

func sessionTitles(values []domain.Session) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = values[index].Title
	}
	return result
}
