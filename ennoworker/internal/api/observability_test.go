package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buffer := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}

func TestMiddlewareLogsAccessWithRequestID(t *testing.T) {
	logs := captureLogs(t)
	_, handler := setupServer(t, nil)

	response := request(t, handler, http.MethodGet, "/v1/projects", nil, true)
	require.Equal(t, http.StatusOK, response.Code)
	requestID := response.Header().Get("X-Request-ID")
	require.NotEmpty(t, requestID)

	output := logs.String()
	assert.Contains(t, output, "http request")
	assert.Contains(t, output, "path=/v1/projects")
	assert.Contains(t, output, "status=200")
	assert.Contains(t, output, "request_id="+requestID)
}

func TestWriteInternalLogsErrorWithRequestContext(t *testing.T) {
	logs := captureLogs(t)
	req, err := http.NewRequest(http.MethodGet, "http://test/v1/internal", nil)
	require.NoError(t, err)
	const requestID = "req-observability"
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey{}, requestID))

	writeInternal(newDiscardWriter(), req, errors.New("boom"))

	output := logs.String()
	assert.Contains(t, output, "internal worker error")
	assert.Contains(t, output, "request_id="+requestID)
	assert.Contains(t, output, "path=/v1/internal")
	assert.Contains(t, output, "error=boom")
}

type discardWriter struct {
	header http.Header
	status int
}

func newDiscardWriter() *discardWriter { return &discardWriter{header: http.Header{}} }

func (d *discardWriter) Header() http.Header            { return d.header }
func (d *discardWriter) Write(data []byte) (int, error) { return len(data), nil }
func (d *discardWriter) WriteHeader(status int)         { d.status = status }
