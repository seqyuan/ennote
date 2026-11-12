package artifacts

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinTestDB builds one Session database, because that is the unit the pin state
// lives in: `pinned_at` is a column of `artifacts`, and every Session owns its own
// file. A test that shared one database across Sessions would not be able to show
// that a read is confined to the Session whose file it opened.
func pinTestDB(t *testing.T, sessionID, projectID string) *sql.DB {
	t.Helper()
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.MigrateFixtureSchema(db))
	seedPinTestSession(t, db, sessionID, projectID)
	return db
}

func seedPinTestSession(t *testing.T, db *sql.DB, sessionID, projectID string) {
	t.Helper()
	const now = "2026-11-11T00:00:00Z"
	_, err := db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES(?,?,?,?)`,
		sessionID, projectID, now, now)
	require.NoError(t, err)
}

func pinTestArtifact(t *testing.T, service *Service, projectID, sessionID, name string) string {
	t.Helper()
	artifact, err := service.Store(context.Background(), PublishInput{
		ProjectID: projectID, SessionID: sessionID, Name: name,
	}, strings.NewReader("payload of "+name))
	require.NoError(t, err)
	require.Nil(t, artifact.PinnedAt, "a freshly stored artifact is not pinned")
	return artifact.ID
}

// A repeated pin must not move the timestamp: the canvas is ordered by the first
// pin, and a client that repaints by re-pinning (or a user who taps twice on a
// phone) would otherwise reshuffle it. A repeated unpin must not fail either.
func TestSetPinnedIsIdempotentInBothDirections(t *testing.T) {
	ctx := context.Background()
	db := pinTestDB(t, "s", "p")
	service := &Service{DB: db, Root: t.TempDir()}
	artifactID := pinTestArtifact(t, service, "p", "s", "note.txt")

	pinned, err := service.SetPinned(ctx, artifactID, "s", true)
	require.NoError(t, err)
	require.NotNil(t, pinned.PinnedAt)

	// Distinct wall clock times, so a second pin that overwrote the column would be
	// visible as a different value rather than as a lucky nanosecond collision.
	time.Sleep(2 * time.Millisecond)
	again, err := service.SetPinned(ctx, artifactID, "s", true)
	require.NoError(t, err, "pinning an already pinned artifact is not an error")
	require.NotNil(t, again.PinnedAt)
	assert.True(t, pinned.PinnedAt.Equal(*again.PinnedAt),
		"the first pin owns the timestamp: %v vs %v", pinned.PinnedAt, again.PinnedAt)

	unpinned, err := service.SetPinned(ctx, artifactID, "s", false)
	require.NoError(t, err)
	assert.Nil(t, unpinned.PinnedAt)

	twice, err := service.SetPinned(ctx, artifactID, "s", false)
	require.NoError(t, err, "unpinning an unpinned artifact is not an error")
	assert.Nil(t, twice.PinnedAt)

	// The answer is the refreshed row, not a locally patched struct: a plain read
	// has to agree with it.
	read, err := service.GetForSession(ctx, artifactID, "s")
	require.NoError(t, err)
	assert.Nil(t, read.PinnedAt)
}

// Pinning is authorized by the same Session-scoped read GET /artifacts/{id} uses,
// so it cannot become a route for addressing a row the caller could not read.
func TestSetPinnedRejectsAnotherSessionsArtifact(t *testing.T) {
	ctx := context.Background()
	db := pinTestDB(t, "s", "p")
	// A sibling Session of the same project and a Session of another project: the
	// two ways a caller can name a Session that does not own the row.
	seedPinTestSession(t, db, "sibling", "p")
	seedPinTestSession(t, db, "foreign", "other")
	service := &Service{DB: db, Root: t.TempDir()}
	artifactID := pinTestArtifact(t, service, "p", "s", "note.txt")

	for _, sessionID := range []string{"sibling", "foreign"} {
		_, err := service.SetPinned(ctx, artifactID, sessionID, true)
		assert.ErrorIs(t, err, ErrArtifactNotFound, "pinning through Session %s", sessionID)
	}

	_, err := service.SetPinned(ctx, "no-such-artifact", "s", true)
	assert.ErrorIs(t, err, ErrArtifactNotFound)

	// A refused pin writes nothing: the row is still unpinned, so a later read
	// cannot show a side effect of the rejected call.
	read, err := service.GetForSession(ctx, artifactID, "s")
	require.NoError(t, err)
	assert.Nil(t, read.PinnedAt)
}

// ListPinned answers one Session's database and nothing else. The Project-level
// merge deliberately does not happen here: this layer does not know which Sessions
// a Project has, and a merge that knew it would be a second project-wide authority.
func TestListPinnedReadsOneSessionNewestFirst(t *testing.T) {
	ctx := context.Background()
	service := &Service{Root: t.TempDir()}
	db := pinTestDB(t, "s", "p")
	service.DB = db
	first := pinTestArtifact(t, service, "p", "s", "first.txt")
	never := pinTestArtifact(t, service, "p", "s", "never.txt")
	time.Sleep(2 * time.Millisecond)
	second := pinTestArtifact(t, service, "p", "s", "second.txt")

	for _, artifactID := range []string{first, second} {
		_, err := service.SetPinned(ctx, artifactID, "s", true)
		require.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	pinned, err := service.ListPinned(ctx, db)
	require.NoError(t, err)
	require.Len(t, pinned, 2, "an unpinned artifact must not appear")
	assert.Equal(t, second, pinned[0].ID, "newest pin first")
	assert.Equal(t, first, pinned[1].ID)
	assert.NotEqual(t, never, pinned[0].ID)

	// Another Session's database holds its own pins; the read is scoped to the file
	// it was handed, which is what makes the API's per-Session fan-out correct.
	otherDB := pinTestDB(t, "other-session", "p")
	otherService := &Service{DB: otherDB, Root: t.TempDir()}
	other := pinTestArtifact(t, otherService, "p", "other-session", "other.txt")
	_, err = otherService.SetPinned(ctx, other, "other-session", true)
	require.NoError(t, err)
	otherPinned, err := otherService.ListPinned(ctx, otherDB)
	require.NoError(t, err)
	require.Len(t, otherPinned, 1)
	assert.Equal(t, other, otherPinned[0].ID)

	stillTwo, err := service.ListPinned(ctx, db)
	require.NoError(t, err)
	assert.Len(t, stillTwo, 2)
}

// The contract declares an array, and a nil slice marshals as null — which the
// generated types do not accept. An empty answer has to be [].
func TestListPinnedAnswersAnEmptySlice(t *testing.T) {
	ctx := context.Background()
	db := pinTestDB(t, "s", "p")
	service := &Service{DB: db, Root: t.TempDir()}
	unused := pinTestArtifact(t, service, "p", "s", "unpinned.txt")
	require.NotEmpty(t, unused)

	pinned, err := service.ListPinned(ctx, db)
	require.NoError(t, err)
	assert.NotNil(t, pinned, "an empty answer must be [] and not null")
	assert.Empty(t, pinned)
}
