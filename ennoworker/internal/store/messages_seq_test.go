package store_test

import (
	"context"
	"fmt"
	"testing"

	stores "github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/require"
)

func TestMessageSeqMonotonicAndReturnedInPage(t *testing.T) {
	db, manager, session := newSessionDB(t)
	ctx := context.Background()
	messages := &stores.MessageRepo{DB: db}
	sessions := &stores.SessionRepo{Files: manager}

	var parent string
	for i := 0; i < 5; i++ {
		message, err := messages.CreateUserMessage(ctx, session.ID, parent, fmt.Sprintf("m%d", i))
		require.NoError(t, err)
		require.Equal(t, int64(i+1), message.Seq, "seq must be session-monotonic starting at 1")
		parent = message.ID
	}
	require.NoError(t, sessions.ActivateLeaf(ctx, session.ID, parent))

	page, err := messages.Page(ctx, session.ID, parent, "", 50)
	require.NoError(t, err)
	require.Len(t, page.Messages, 5)
	for index, message := range page.Messages {
		require.Equal(t, int64(index+1), message.Seq, "page must carry the assigned seq in lineage order")
	}
}
