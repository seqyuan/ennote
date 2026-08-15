package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	stores "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessagePagePaginatesActiveLineageInChronologicalOrder(t *testing.T) {
	db, manager, session := newSessionDB(t)
	ctx := context.Background()
	sessions := &stores.SessionRepo{Files: manager}
	messages := &stores.MessageRepo{DB: db}

	var lineage []*domain.Message
	parentID := ""
	for _, text := range []string{"one", "two", "three", "four", "five"} {
		message, createErr := messages.CreateUserMessage(ctx, session.ID, parentID, text)
		require.NoError(t, createErr)
		lineage = append(lineage, message)
		parentID = message.ID
	}
	require.NoError(t, sessions.ActivateLeaf(ctx, session.ID, lineage[4].ID))
	_, err := db.ExecContext(ctx, `INSERT INTO message_parts(id,message_id,ordinal,block_kind,payload_json)
		VALUES('extra-thinking',?,1,'thinking','{"text":"detail"}')`, lineage[4].ID)
	require.NoError(t, err)

	first, err := messages.Page(ctx, session.ID, lineage[4].ID, "", 2)
	require.NoError(t, err)
	require.Len(t, first.Messages, 2)
	assert.Equal(t, []string{lineage[3].ID, lineage[4].ID}, messageIDs(first.Messages))
	assert.True(t, first.HasMore)
	assert.Equal(t, lineage[3].ID, first.NextBeforeMessageID)
	assert.Equal(t, "four", first.Messages[0].Parts[0].Text)
	require.Len(t, first.Messages[1].Parts, 2)
	assert.Equal(t, domain.ContentThinking, first.Messages[1].Parts[1].Kind)
	assert.Equal(t, "detail", first.Messages[1].Parts[1].Text)

	second, err := messages.Page(ctx, session.ID, lineage[4].ID, first.NextBeforeMessageID, 2)
	require.NoError(t, err)
	require.Len(t, second.Messages, 2)
	assert.Equal(t, []string{lineage[1].ID, lineage[2].ID}, messageIDs(second.Messages))
	assert.True(t, second.HasMore)
	assert.Equal(t, lineage[1].ID, second.NextBeforeMessageID)

	third, err := messages.Page(ctx, session.ID, lineage[4].ID, second.NextBeforeMessageID, 2)
	require.NoError(t, err)
	require.Len(t, third.Messages, 1)
	assert.Equal(t, lineage[0].ID, third.Messages[0].ID)
	assert.False(t, third.HasMore)
	assert.Empty(t, third.NextBeforeMessageID)

	empty, err := messages.Page(ctx, session.ID, lineage[4].ID, lineage[0].ID, 2)
	require.NoError(t, err)
	assert.Empty(t, empty.Messages)
	assert.False(t, empty.HasMore)
}

func TestMessagePageRejectsOffBranchAndOtherSessionCursors(t *testing.T) {
	db, manager, session := newSessionDB(t)
	ctx := context.Background()
	sessions := &stores.SessionRepo{Files: manager}
	messages := &stores.MessageRepo{DB: db}
	root, err := messages.CreateUserMessage(ctx, session.ID, "", "root")
	require.NoError(t, err)
	active, err := messages.CreateUserMessage(ctx, session.ID, root.ID, "active")
	require.NoError(t, err)
	sibling, err := messages.CreateUserMessage(ctx, session.ID, root.ID, "sibling")
	require.NoError(t, err)
	require.NoError(t, sessions.ActivateLeaf(ctx, session.ID, active.ID))

	_, err = messages.Page(ctx, session.ID, active.ID, sibling.ID, 10)
	assert.ErrorIs(t, err, stores.ErrMessageCursorInvalid)

	otherSession := sqlCreateSession(t, db, "00000000-0000-4000-8000-000000000004")
	other, err := messages.CreateUserMessage(ctx, otherSession.ID, "", "other")
	require.NoError(t, err)
	_, err = messages.Page(ctx, session.ID, active.ID, other.ID, 10)
	assert.True(t, errors.Is(err, stores.ErrMessageCursorInvalid))

	emptySession := sqlCreateSession(t, db, "00000000-0000-4000-8000-000000000004")
	_, err = messages.Page(ctx, emptySession.ID, "", "fabricated", 10)
	assert.ErrorIs(t, err, stores.ErrMessageCursorInvalid)
}

func messageIDs(messages []domain.Message) []string {
	ids := make([]string, len(messages))
	for index := range messages {
		ids[index] = messages[index].ID
	}
	return ids
}
