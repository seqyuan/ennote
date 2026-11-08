package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSession creates a project and a session through the API, the way a client
// does, and returns the session id.
func seedSession(t *testing.T, server *Server, handler http.Handler) string {
	t.Helper()
	project := request(t, handler, http.MethodPost, "/v1/projects",
		map[string]any{"name": "events", "hostPath": hostWorkspace(t, server, "events")}, true)
	var created struct {
		Project domain.Project `json:"project"`
	}
	decodeData(t, project, &created)

	session := request(t, handler, http.MethodPost, "/v1/projects/"+created.Project.ID+"/sessions",
		map[string]any{"title": "events"}, true)
	var sessionBody domain.Session
	decodeData(t, session, &sessionBody)
	require.NotEmpty(t, sessionBody.ID)
	return sessionBody.ID
}

// sessionStreamRequest builds the SSE request the browser makes for a session.
func sessionStreamRequest(ctx context.Context, sessionID string, lastEventID string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/events", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	return request
}

func TestSessionEventsAnswers503WhenItsDependenciesAreMissing(t *testing.T) {
	// The feed spans runs, approvals, compactions, the queue and the durable log;
	// a process missing one of them has to say so instead of serving a feed that
	// silently omits a category.
	server, handler := setupServer(t, &fakeController{})
	sessionID := seedSession(t, server, handler)
	server.Queue = nil

	response := request(t, handler, http.MethodGet, "/v1/sessions/"+sessionID+"/events", nil, true)
	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "session_events_unavailable")
}

func TestSessionEventsRefusesAnUnknownSession(t *testing.T) {
	_, handler := setupServer(t, &fakeController{})

	response := request(t, handler, http.MethodGet,
		"/v1/sessions/00000000-0000-0000-0000-000000000000/events", nil, true)

	// The session middleware rejects it before the feed starts; the handler's own
	// not-found branch only covers a session removed underneath it.
	require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "session_resource_not_found")
}

func TestSessionEventsStreamsTheSubscribedSnapshotFirst(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	server.InstanceID = "stream-instance"
	sessionID := seedSession(t, server, handler)

	ctx, cancel := context.WithCancel(context.Background())
	recorder := newFlushingRecorder()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		handler.ServeHTTP(recorder, sessionStreamRequest(ctx, sessionID, ""))
	}()

	// Wait for the handler to say it flushed rather than for a guessed delay: the
	// body is only read after it returns, because a ResponseRecorder is not safe to
	// read while it is being written.
	select {
	case <-recorder.firstFlush:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("the change feed never flushed a frame")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the change feed did not stop when its request was cancelled")
	}

	body := recorder.Body.String()
	assert.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, recorder.Header().Get("Cache-Control"), "no-cache")

	payload, ok := firstEventPayload(t, body)
	require.True(t, ok, "no SSE frame was written: %q", body)

	var frame struct {
		Type       string         `json:"type"`
		InstanceID string         `json:"instanceId"`
		LastSeq    int64          `json:"lastSeq"`
		Snapshot   map[string]any `json:"snapshot"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &frame))
	assert.Equal(t, "subscribed", frame.Type)
	// The client keys its connection generation off this, so it has to be the
	// process identity and nothing else.
	assert.Equal(t, "stream-instance", frame.InstanceID)
	require.NotNil(t, frame.Snapshot)
	assert.Contains(t, frame.Snapshot, "queuedInputs")
	assert.Contains(t, frame.Snapshot, "checkpoints")
}

// flushingRecorder reports the first flush, so a streaming handler can be tested
// without sleeping and without reading its buffer concurrently.
type flushingRecorder struct {
	*httptest.ResponseRecorder
	once       sync.Once
	firstFlush chan struct{}
}

func newFlushingRecorder() *flushingRecorder {
	return &flushingRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		firstFlush:       make(chan struct{}),
	}
}

func (r *flushingRecorder) Flush() {
	r.ResponseRecorder.Flush()
	r.once.Do(func() { close(r.firstFlush) })
}

// firstEventPayload returns the data line of the first complete SSE frame.
func firstEventPayload(t *testing.T, body string) (string, bool) {
	t.Helper()
	for _, block := range strings.Split(body, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if payload, found := strings.CutPrefix(line, "data: "); found {
				return payload, true
			}
		}
	}
	return "", false
}

func TestSessionEventFrameCarriesTheCommittedSeqRange(t *testing.T) {
	// message_committed is the one durable frame that is not a plain event: the
	// client places the run in the timeline with firstSeq/lastSeq, and they are the
	// run's span rather than a per-frame delta.
	server, handler := setupServer(t, &fakeController{})
	sessionID := seedSession(t, server, handler)

	run := request(t, handler, http.MethodPost, "/v1/sessions/"+sessionID+"/turns",
		map[string]any{"text": "hello"}, true)
	require.Equal(t, http.StatusAccepted, run.Code, run.Body.String())

	db, err := server.SessionStores.OpenSession(context.Background(), sessionID)
	require.NoError(t, err)
	var runID string
	require.NoError(t, db.QueryRow(`SELECT id FROM agent_runs LIMIT 1`).Scan(&runID))

	frame, err := server.sessionEventFrame(context.Background(), domain.RunEvent{
		EventID: 7, RunID: runID, EventType: "message_committed",
		Payload: json.RawMessage(`{"assistantMessageId":"m-1"}`),
	})
	require.NoError(t, err)

	var decoded struct {
		Type     string          `json:"type"`
		RunID    string          `json:"runId"`
		Payload  json.RawMessage `json:"payload"`
		FirstSeq int64           `json:"firstSeq"`
		LastSeq  int64           `json:"lastSeq"`
	}
	require.NoError(t, json.Unmarshal(frame, &decoded))
	assert.Equal(t, "message_committed", decoded.Type)
	assert.Equal(t, runID, decoded.RunID)
	assert.JSONEq(t, `{"assistantMessageId":"m-1"}`, string(decoded.Payload))
	assert.GreaterOrEqual(t, decoded.LastSeq, decoded.FirstSeq)
}

func TestSessionEventFrameRendersEveryOtherEventPlainly(t *testing.T) {
	server, _ := setupServer(t, &fakeController{})

	frame, err := server.sessionEventFrame(context.Background(), domain.RunEvent{
		EventID: 12, RunID: "run-1", Seq: 3, EventType: "run_started",
	})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(frame, &decoded))
	assert.Equal(t, "run_started", decoded["type"])
	assert.Equal(t, float64(12), decoded["eventId"])
	assert.Equal(t, float64(3), decoded["seq"])
}

func TestSessionMessageSeqHighWaterIsZeroWithoutMessages(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	sessionID := seedSession(t, server, handler)

	high, err := server.sessionMessageSeqHighWater(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Zero(t, high, "an empty session reports no high-water mark, not an error")
}

func TestBuildSessionSnapshotReportsNoActiveRunForAFreshSession(t *testing.T) {
	server, handler := setupServer(t, &fakeController{})
	sessionID := seedSession(t, server, handler)

	snapshot, err := server.buildSessionSnapshot(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Nil(t, snapshot.ActiveRun)
	assert.Nil(t, snapshot.PendingApproval)
	assert.Empty(t, snapshot.QueuedInputs)
	assert.False(t, snapshot.DelegationActive)

	// The projection is a full replacement, so its encoding has to be stable: the
	// stream only pushes a frame when the encoded form changes.
	assert.Equal(t, encodeSessionSnapshot(snapshot), encodeSessionSnapshot(snapshot))
}

func TestSessionSnapshotFramesAreWellFormedSSE(t *testing.T) {
	recorder := httptest.NewRecorder()
	flusher, ok := http.ResponseWriter(recorder).(http.Flusher)
	require.True(t, ok, "the recorder has to be flushable for this test to mean anything")

	require.NoError(t, writeSessionSubscribed(recorder, flusher, "instance", 4, &sessionSnapshot{}))
	require.NoError(t, writeSessionSnapshot(recorder, flusher, 5, &sessionSnapshot{DelegationActive: true}))

	body := recorder.Body.String()
	assert.Equal(t, 2, strings.Count(body, "\n\n"), "each frame ends with a blank line")
	assert.Contains(t, body, "\"type\":\"subscribed\"")
	assert.Contains(t, body, "\"type\":\"snapshot\"")
	assert.Contains(t, body, "\"lastSeq\":5")
}

func TestParseCursorIgnoresWhatIsNotANumber(t *testing.T) {
	// Last-Event-ID comes from the browser and `after` from the query string; a
	// garbled one must resume from the beginning, not fail the stream.
	assert.Equal(t, int64(0), parseCursor(""))
	assert.Equal(t, int64(0), parseCursor("not-a-cursor"))
	assert.Equal(t, int64(42), parseCursor("42"))
	assert.Equal(t, int64(42), parseCursor(" 42 "))
}
