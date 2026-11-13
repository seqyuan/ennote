package api

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinTestSession creates a Session in the given Project and returns it with the
// database and artifact root its routes will use. Storing through
// `ForScope(...)` rather than through `server.Artifacts` is the point: that service
// is bound to one Session, and an artifact written there is invisible to another
// Session's routes — which is exactly the separation the canvas aggregation has to
// cross.
func pinTestSession(t *testing.T, server *Server, projectID string) (string, *sql.DB, string) {
	t.Helper()
	ctx := context.Background()
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: projectID})
	require.NoError(t, err)
	db, err := server.SessionStores.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	path, err := server.SessionStores.SessionPath(session.ID)
	require.NoError(t, err)
	return session.ID, db, filepath.Join(path, "artifacts")
}

func pinTestStore(t *testing.T, server *Server, db *sql.DB, root, projectID, sessionID, name string) string {
	t.Helper()
	artifact, err := server.Artifacts.ForScope(db, root).Store(context.Background(), artifacts.PublishInput{
		ProjectID: projectID, SessionID: sessionID, Name: name,
	}, strings.NewReader("payload of "+name))
	require.NoError(t, err)
	return artifact.ID
}

// pinnedArtifactsAnswer is the wire shape of the 200 answer: an object carrying the
// merged artifact list plus, only when a Session was skipped, how many.
type pinnedArtifactsAnswer struct {
	Artifacts           []domain.Artifact `json:"artifacts"`
	SkippedSessionCount int               `json:"skippedSessionCount"`
}

// corruptSessionManifest rewrites a Session's manifest with a different identity so
// the next OpenSession fails closed ("session manifest identity does not match
// directory"). It is the same corruption TestSessionManifestIdentityMismatchFailsClosed
// uses; the first occurrence of the id is the manifest's own sessionId field.
func corruptSessionManifest(t *testing.T, server *Server, sessionID string) {
	t.Helper()
	path, err := server.SessionStores.SessionPath(sessionID)
	require.NoError(t, err)
	manifestPath := filepath.Join(path, "session.json")
	contents, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	contents = []byte(strings.Replace(string(contents), sessionID, uuid.NewString(), 1))
	require.NoError(t, os.WriteFile(manifestPath, contents, 0o600))
}

// The pin is Worker state on the artifact, so the two routes and the artifact read
// have to agree: pinned after a pin, absent (not null) after an unpin. The pair is
// one test because a client reads them together — pin, then render the artifact it
// got back.
func TestArtifactPinAndUnpinAreVisibleOnTheArtifact(t *testing.T) {
	ctx := context.Background()
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "canvas", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	sessionID, db, root := pinTestSession(t, server, project.ID)
	artifactID := pinTestStore(t, server, db, root, project.ID, sessionID, "note.txt")
	base := "/v1/sessions/" + sessionID + "/artifacts/" + artifactID

	pinned := request(t, handler, http.MethodPost, base+"/pin", nil, true)
	require.Equal(t, http.StatusOK, pinned.Code, pinned.Body.String())
	var pinnedArtifact domain.Artifact
	decodeData(t, pinned, &pinnedArtifact)
	require.NotNil(t, pinnedArtifact.PinnedAt, "pin answers the refreshed artifact")
	assert.Equal(t, artifactID, pinnedArtifact.ID)

	// The pin is on the row, not in the answer: GET must show it too, otherwise a
	// client that reloads would render the canvas differently from the one that
	// pinned.
	read := request(t, handler, http.MethodGet, base, nil, true)
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	assert.Contains(t, read.Body.String(), `"pinnedAt"`)

	// Idempotent: a second pin keeps the first timestamp.
	time.Sleep(2 * time.Millisecond)
	again := request(t, handler, http.MethodPost, base+"/pin", nil, true)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	var pinnedAgain domain.Artifact
	decodeData(t, again, &pinnedAgain)
	require.NotNil(t, pinnedAgain.PinnedAt)
	assert.True(t, pinnedArtifact.PinnedAt.Equal(*pinnedAgain.PinnedAt))

	unpinned := request(t, handler, http.MethodDelete, base+"/pin", nil, true)
	require.Equal(t, http.StatusOK, unpinned.Code, unpinned.Body.String())
	var unpinnedArtifact domain.Artifact
	decodeData(t, unpinned, &unpinnedArtifact)
	assert.Nil(t, unpinnedArtifact.PinnedAt)
	// Absent, not `null`: the field carries `omitempty` and the generated types
	// treat it as an optional string.
	assert.NotContains(t, unpinned.Body.String(), "pinnedAt")

	afterUnpin := request(t, handler, http.MethodGet, base, nil, true)
	require.Equal(t, http.StatusOK, afterUnpin.Code, afterUnpin.Body.String())
	assert.NotContains(t, afterUnpin.Body.String(), "pinnedAt")

	twice := request(t, handler, http.MethodDelete, base+"/pin", nil, true)
	assert.Equal(t, http.StatusOK, twice.Code, "unpinning twice is not an error")

	// A Session that does not own the artifact cannot pin it, and neither can a
	// Session of another Project: the write is authorized by the Session-scoped read.
	otherProject, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "elsewhere", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	siblingID, _, _ := pinTestSession(t, server, project.ID)
	foreignID, _, _ := pinTestSession(t, server, otherProject.ID)
	for _, session := range []string{siblingID, foreignID} {
		crossed := request(t, handler, http.MethodPost,
			"/v1/sessions/"+session+"/artifacts/"+artifactID+"/pin", nil, true)
		assert.Equal(t, http.StatusNotFound, crossed.Code, "pin through Session %s", session)
	}
}

// The canvas: pinned artifacts of every Session in one Project, globally ordered by
// pin time. Sessions are separate SQLite files, so this is the aggregation the
// endpoint exists for.
func TestListPinnedArtifactsMergesSessionsOfOneProject(t *testing.T) {
	ctx := context.Background()
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "canvas", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	firstSession, firstDB, firstRoot := pinTestSession(t, server, project.ID)
	secondSession, secondDB, secondRoot := pinTestSession(t, server, project.ID)
	first := pinTestStore(t, server, firstDB, firstRoot, project.ID, firstSession, "first.txt")
	second := pinTestStore(t, server, secondDB, secondRoot, project.ID, secondSession, "second.txt")
	third := pinTestStore(t, server, firstDB, firstRoot, project.ID, firstSession, "third.txt")
	unpinned := pinTestStore(t, server, secondDB, secondRoot, project.ID, secondSession, "never.txt")

	pin := func(sessionID, artifactID string) {
		t.Helper()
		response := request(t, handler, http.MethodPost,
			"/v1/sessions/"+sessionID+"/artifacts/"+artifactID+"/pin", nil, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	}
	// Interleaved on purpose: the merge has to order by pinnedAt across databases,
	// not by Session.
	pin(firstSession, first)
	time.Sleep(2 * time.Millisecond)
	pin(secondSession, second)
	time.Sleep(2 * time.Millisecond)
	pin(firstSession, third)

	// A third Session of another Project, pinned: it must not reach this canvas.
	otherProject, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "elsewhere", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	foreignSession, foreignDB, foreignRoot := pinTestSession(t, server, otherProject.ID)
	foreign := pinTestStore(t, server, foreignDB, foreignRoot, otherProject.ID, foreignSession, "foreign.txt")
	pin(foreignSession, foreign)

	list := func(projectID string) pinnedArtifactsAnswer {
		t.Helper()
		response := request(t, handler, http.MethodGet, "/v1/projects/"+projectID+"/pinned-artifacts", nil, true)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var answer pinnedArtifactsAnswer
		decodeData(t, response, &answer)
		return answer
	}

	canvas := list(project.ID)
	require.Len(t, canvas.Artifacts, 3, "two Sessions, three pinned artifacts, no other Project")
	assert.Zero(t, canvas.SkippedSessionCount, "a fully readable canvas carries no skipped count")
	assert.Equal(t, []string{third, second, first},
		[]string{canvas.Artifacts[0].ID, canvas.Artifacts[1].ID, canvas.Artifacts[2].ID}, "newest pin first, across Sessions")
	assert.Equal(t, firstSession, canvas.Artifacts[0].SessionID)
	assert.Equal(t, secondSession, canvas.Artifacts[1].SessionID)
	for _, artifact := range canvas.Artifacts {
		require.NotNil(t, artifact.PinnedAt)
		assert.NotEqual(t, unpinned, artifact.ID, "an unpinned artifact is not on the canvas")
		assert.NotEqual(t, foreign, artifact.ID)
	}

	// A Project with nothing pinned answers an empty array inside the object, not
	// null: the contract declares an array and the generated types reject null.
	empty := request(t, handler, http.MethodGet,
		"/v1/projects/00000000-0000-0000-0000-000000000000/pinned-artifacts", nil, true)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	assert.Contains(t, empty.Body.String(), `"data":{"artifacts":[]}`)

	unauthenticated := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/pinned-artifacts", nil, false)
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
}

// Every Session failing to open must not look like "nothing is pinned": the canvas
// answers 503 with artifacts_unavailable instead of an empty 200. A Project with no
// Sessions is the one case that stays 200 with an empty list.
func TestListPinnedArtifactsAllSessionsUnreadableAnswers503(t *testing.T) {
	ctx := context.Background()
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "canvas", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)

	// Prime the per-project session list cache while the Session is still readable,
	// then drop the open handle so the next read has to re-open (and re-verify) the
	// manifest. Without the cache, ListByProject itself would fail closed first.
	_, err = server.SessionStores.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.NoError(t, server.SessionStores.Close())
	corruptSessionManifest(t, server, session.ID)

	response := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/pinned-artifacts", nil, true)
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), `"code":"artifacts_unavailable"`)
}

// One unreadable Session among several must not hide the rest: the answer stays 200,
// carries skippedSessionCount=1, and still lists the healthy Session's pinned
// artifact.
func TestListPinnedArtifactsPartialFailureCarriesSkippedCount(t *testing.T) {
	ctx := context.Background()
	server, handler := setupServer(t, nil)
	project, _, err := server.Projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "canvas", HostPath: t.TempDir(),
	})
	require.NoError(t, err)

	healthySession, healthyDB, healthyRoot := pinTestSession(t, server, project.ID)
	artifactID := pinTestStore(t, server, healthyDB, healthyRoot, project.ID, healthySession, "healthy.txt")
	pin := request(t, handler, http.MethodPost,
		"/v1/sessions/"+healthySession+"/artifacts/"+artifactID+"/pin", nil, true)
	require.Equal(t, http.StatusOK, pin.Code, pin.Body.String())

	corruptedSession, err := server.Sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)

	// Prime the cache with both Sessions, then drop the handles so the corrupted
	// one is re-opened and fails while the healthy one re-opens and succeeds.
	_, err = server.SessionStores.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.NoError(t, server.SessionStores.Close())
	corruptSessionManifest(t, server, corruptedSession.ID)

	response := request(t, handler, http.MethodGet, "/v1/projects/"+project.ID+"/pinned-artifacts", nil, true)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var answer pinnedArtifactsAnswer
	decodeData(t, response, &answer)
	require.Len(t, answer.Artifacts, 1)
	assert.Equal(t, artifactID, answer.Artifacts[0].ID)
	assert.Equal(t, 1, answer.SkippedSessionCount)
}
