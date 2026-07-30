package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	store "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionSearchSeparatesLifecycleAndEscapesWildcards(t *testing.T) {
	db := store.SetupDB(t)
	projects := &store.ProjectRepo{DB: db}
	sessions := &store.SessionRepo{DB: db}
	project, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)
	other, _, err := projects.CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "Other", HostPath: t.TempDir()})
	require.NoError(t, err)
	values := []string{"Alpha atlas", "alpha 100%", "under_score", "Unrelated"}
	created := make([]*domain.Session, 0, len(values))
	for _, title := range values {
		value, err := sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: title})
		require.NoError(t, err)
		created = append(created, value)
	}
	_, err = sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: other.ID, Title: "Alpha other project"})
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sessions SET status='archived' WHERE id=?`, created[0].ID)
	require.NoError(t, err)

	active, err := sessions.SearchByProject(context.Background(), project.ID, store.SessionStatusActive, "alpha")
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, "alpha 100%", active[0].Title)
	archived, err := sessions.SearchByProject(context.Background(), project.ID, store.SessionStatusArchived, "ALPHA")
	require.NoError(t, err)
	require.Len(t, archived, 1)
	assert.Equal(t, "Alpha atlas", archived[0].Title)
	percent, err := sessions.SearchByProject(context.Background(), project.ID, store.SessionStatusActive, "100%")
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha 100%"}, sessionTitles(percent))
	underscore, err := sessions.SearchByProject(context.Background(), project.ID, store.SessionStatusActive, "_")
	require.NoError(t, err)
	assert.Equal(t, []string{"under_score"}, sessionTitles(underscore))
	_, err = sessions.SearchByProject(context.Background(), project.ID, "all", "")
	assert.ErrorIs(t, err, store.ErrSessionSearchInvalid)
	_, err = sessions.SearchByProject(context.Background(), project.ID, store.SessionStatusActive, strings.Repeat("x", 121))
	assert.ErrorIs(t, err, store.ErrSessionSearchInvalid)
}

func TestSessionSearchRejectsCorruptTimestamps(t *testing.T) {
	db := store.SetupDB(t)
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)
	repo := &store.SessionRepo{DB: db}
	session, err := repo.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Corrupt"})
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sessions SET updated_at='not-a-timestamp' WHERE id=?`, session.ID)
	require.NoError(t, err)

	_, err = repo.SearchByProject(context.Background(), project.ID, store.SessionStatusActive, "")
	require.ErrorContains(t, err, "parse session updated_at")
}

func TestSessionArchiveRestorePreservesCanonicalBranchState(t *testing.T) {
	db := store.SetupDB(t)
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
	require.NoError(t, err)
	repo := &store.SessionRepo{DB: db}
	session, err := repo.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Lifecycle"})
	require.NoError(t, err)
	message, err := (&store.MessageRepo{DB: db}).CreateUserMessage(context.Background(), session.ID, "", "keep")
	require.NoError(t, err)
	require.NoError(t, repo.ActivateLeaf(context.Background(), session.ID, message.ID))
	before, err := repo.FindByID(context.Background(), session.ID)
	require.NoError(t, err)

	archived, err := repo.Archive(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionStatusArchived, archived.Status)
	assert.Equal(t, before.ActiveBranchID, archived.ActiveBranchID)
	assert.Equal(t, before.ActiveLeafMessageID, archived.ActiveLeafMessageID)
	_, err = repo.Archive(context.Background(), session.ID)
	assert.ErrorIs(t, err, store.ErrSessionStateConflict)

	restored, err := repo.Restore(context.Background(), session.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionStatusActive, restored.Status)
	assert.Equal(t, before.ActiveBranchID, restored.ActiveBranchID)
	assert.Equal(t, before.ActiveLeafMessageID, restored.ActiveLeafMessageID)
	_, err = repo.Restore(context.Background(), session.ID)
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
			db := store.SetupDB(t)
			project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(context.Background(), domain.CreateProjectInput{Name: "P", HostPath: t.TempDir()})
			require.NoError(t, err)
			sessions := &store.SessionRepo{DB: db}
			session, err := sessions.Create(context.Background(), domain.CreateSessionInput{ProjectID: project.ID, Title: "Busy"})
			require.NoError(t, err)
			runs := &store.RunRepo{DB: db}
			submission, err := runs.SubmitTurn(context.Background(), domain.SubmitTurnInput{SessionID: session.ID, ClientRequestID: "turn", Text: "run"})
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE agent_runs SET status=? WHERE id=?`, status, submission.Run.ID)
			require.NoError(t, err)
			_, err = sessions.Archive(context.Background(), session.ID)
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
