package store_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupBranchSession(t *testing.T) (*store.BranchRepo, *store.SessionRepo, *store.MessageRepo, *domain.Session) {
	t.Helper()
	db, manager, session := newSessionDB(t)
	sessions := &store.SessionRepo{Files: manager}
	messages := &store.MessageRepo{DB: db}
	return &store.BranchRepo{DB: db}, sessions, messages, &session
}

func TestCreateSessionCreatesStableMainBranch(t *testing.T) {
	branches, _, _, session := setupBranchSession(t)
	require.NotNil(t, session.ActiveBranchID)

	listed, err := branches.List(context.Background(), session.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, *session.ActiveBranchID, listed[0].ID)
	assert.Equal(t, "Main", listed[0].Label)
	assert.True(t, listed[0].Active)
	assert.Nil(t, listed[0].LeafMessageID)
}

func TestCreateAndActivateBranchPreservesCanonicalMessages(t *testing.T) {
	branches, sessions, messages, session := setupBranchSession(t)
	ctx := context.Background()
	root, err := messages.CreateUserMessage(ctx, session.ID, "", "root")
	require.NoError(t, err)
	leaf, err := messages.CreateUserMessage(ctx, session.ID, root.ID, "main leaf")
	require.NoError(t, err)
	require.NoError(t, sessions.ActivateLeaf(ctx, session.ID, leaf.ID))
	mainID := *session.ActiveBranchID

	var before int
	require.NoError(t, branches.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=?`, session.ID).Scan(&before))
	navigation, err := branches.Create(ctx, session.ID, root.ID, "")
	require.NoError(t, err)
	require.Len(t, navigation.Branches, 2)
	require.NotNil(t, navigation.Session.ActiveLeafMessageID)
	assert.Equal(t, root.ID, *navigation.Session.ActiveLeafMessageID)
	require.NotNil(t, navigation.Session.ActiveBranchID)
	assert.NotEqual(t, mainID, *navigation.Session.ActiveBranchID)
	assert.Equal(t, "Branch 2", navigation.Branches[1].Label)
	assert.Equal(t, 1, navigation.Branches[1].MessageCount)

	var after int
	require.NoError(t, branches.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id=?`, session.ID).Scan(&after))
	assert.Equal(t, before, after)

	navigation, err = branches.Activate(ctx, session.ID, mainID)
	require.NoError(t, err)
	require.NotNil(t, navigation.Session.ActiveLeafMessageID)
	assert.Equal(t, leaf.ID, *navigation.Session.ActiveLeafMessageID)
	assert.True(t, navigation.Branches[0].Active)
}

func TestBranchCreationRejectsInactiveAndCrossSessionPoints(t *testing.T) {
	branches, sessions, messages, session := setupBranchSession(t)
	ctx := context.Background()
	root, err := messages.CreateUserMessage(ctx, session.ID, "", "root")
	require.NoError(t, err)
	active, err := messages.CreateUserMessage(ctx, session.ID, root.ID, "active")
	require.NoError(t, err)
	sibling, err := messages.CreateUserMessage(ctx, session.ID, root.ID, "sibling")
	require.NoError(t, err)
	require.NoError(t, sessions.ActivateLeaf(ctx, session.ID, active.ID))

	_, err = branches.Create(ctx, session.ID, sibling.ID, "invalid")
	assert.ErrorIs(t, err, store.ErrBranchPointNotActive)

	other := sqlCreateSession(t, branches.DB, session.ProjectID)
	otherMessage, err := messages.CreateUserMessage(ctx, other.ID, "", "other")
	require.NoError(t, err)
	_, err = branches.Create(ctx, session.ID, otherMessage.ID, "invalid")
	assert.ErrorIs(t, err, store.ErrBranchPointNotActive)
}

func TestBranchNavigationRejectsActiveRun(t *testing.T) {
	branches, sessions, messages, session := setupBranchSession(t)
	ctx := context.Background()
	root, err := messages.CreateUserMessage(ctx, session.ID, "", "root")
	require.NoError(t, err)
	require.NoError(t, sessions.ActivateLeaf(ctx, session.ID, root.ID))
	submission, err := (&store.RunRepo{DB: branches.DB}).SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "active", Text: "work",
	})
	require.NoError(t, err)

	_, err = branches.Create(ctx, session.ID, root.ID, "blocked")
	assert.ErrorIs(t, err, store.ErrSessionRunActive)
	_, err = branches.Activate(ctx, session.ID, *session.ActiveBranchID)
	assert.ErrorIs(t, err, store.ErrSessionRunActive)

	_, err = (&store.RunRepo{DB: branches.DB}).Get(ctx, submission.Run.ID)
	require.NoError(t, err)
}
