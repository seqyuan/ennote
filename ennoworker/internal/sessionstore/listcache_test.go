package sessionstore_test

import (
	"context"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestListByProjectInvalidatesOnCreate(t *testing.T) {
	manager, _, projectID := setupManager(t)
	ctx := context.Background()

	session, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "A"})
	require.NoError(t, err)
	require.NotEmpty(t, session.ID)

	// First read populates the cache.
	sessions, err := manager.ListByProject(ctx, projectID, "active")
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	// A second create must invalidate the cached list.
	_, err = manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "B"})
	require.NoError(t, err)

	sessions, err = manager.ListByProject(ctx, projectID, "active")
	require.NoError(t, err)
	require.Len(t, sessions, 2)
}

func TestListByProjectCacheIsDefensive(t *testing.T) {
	manager, _, projectID := setupManager(t)
	ctx := context.Background()

	_, err := manager.Create(ctx, domain.CreateSessionInput{ProjectID: projectID, Title: "A"})
	require.NoError(t, err)

	first, err := manager.ListByProject(ctx, projectID, "active")
	require.NoError(t, err)
	require.Len(t, first, 1)

	// Mutate the returned slice in place (as SearchByProject's filtering does);
	// the cached copy must be unaffected.
	first = first[:0]

	second, err := manager.ListByProject(ctx, projectID, "active")
	require.NoError(t, err)
	require.Len(t, second, 1)
}
