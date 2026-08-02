package events

import (
	"context"
	"sync"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type Appender interface {
	Append(context.Context, string, ...domain.PendingEvent) ([]domain.RunEvent, error)
}

// LivePublisher is the transient event delivery substrate. Implementations
// must never block the producer goroutine — a full/dropped event must not
// affect the run outcome.
type LivePublisher interface {
	PublishLive(event domain.LiveRunEvent)
}

type Writer struct {
	appender Appender
	hub      *Hub
}

func NewWriter(appender Appender, hub *Hub) *Writer {
	if hub == nil {
		hub = NewHub()
	}
	return &Writer{appender: appender, hub: hub}
}

func (w *Writer) Append(ctx context.Context, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	events, err := w.appender.Append(ctx, runID, pending...)
	if err != nil {
		return nil, err
	}
	w.hub.Publish(events...)
	return events, nil
}

func (w *Writer) Hub() *Hub { return w.hub }

// PublishLive is a convenience that delegates to the underlying Hub.
func (w *Writer) PublishLive(event domain.LiveRunEvent) {
	w.hub.PublishLive(event)
}

// LiveCoalescer aggregates live streaming output before publishing. It ensures
// that at most one event per stream is published within a window (100ms or 32KB
// threshold), preventing event storms on chatty tools.
type LiveCoalescer struct {
	runID   string
	hub     *Hub
	streams map[string]*coalesceBuf
	mu      sync.Mutex
	closed  bool
}

// NewLiveCoalescer creates a coalescer scoped to a single run. Call FlushAll
// when the run ends to drain remaining buffered output.
func NewLiveCoalescer(runID string, hub *Hub) *LiveCoalescer {
	return &LiveCoalescer{
		runID:   runID,
		hub:     hub,
		streams: make(map[string]*coalesceBuf),
	}
}

// Push appends data to the per-stream buffer and publishes when the threshold
// is hit. streamID is typically "{toolCallID}:{stdout|stderr}".
func (c *LiveCoalescer) Push(streamID string, data []byte, now time.Time) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	buf := c.streams[streamID]
	if buf == nil {
		buf = &coalesceBuf{streamID: streamID, runID: c.runID, hub: c.hub, seqBase: time.Now().UnixNano()}
		c.streams[streamID] = buf
	}
	c.mu.Unlock()
	buf.write(data, now)
}

// Close stops further writes (no-op) and flushes all remaining buffers.
func (c *LiveCoalescer) Close() {
	c.mu.Lock()
	c.closed = true
	bufs := make([]*coalesceBuf, 0, len(c.streams))
	for _, b := range c.streams {
		bufs = append(bufs, b)
	}
	c.streams = nil
	c.mu.Unlock()
	for _, b := range bufs {
		b.flush()
	}
}

type coalesceBuf struct {
	streamID string
	runID    string
	hub      *Hub
	seqBase  int64
	mu       sync.Mutex
	data     []byte
	seq      int64
}

const liveCoalesceInterval = 100 * time.Millisecond
const liveCoalesceBytes = 32 * 1024

func (b *coalesceBuf) write(p []byte, now time.Time) {
	b.mu.Lock()
	wasEmpty := len(b.data) == 0
	b.data = append(b.data, p...)
	shouldFlush := len(b.data) >= liveCoalesceBytes
	b.mu.Unlock()

	if wasEmpty {
		time.AfterFunc(liveCoalesceInterval, func() {
			b.mu.Lock()
			if len(b.data) == 0 {
				b.mu.Unlock()
				return
			}
			b.mu.Unlock()
			b.flush()
		})
	}
	if shouldFlush {
		b.flush()
	}
}

func (b *coalesceBuf) flush() {
	b.mu.Lock()
	if len(b.data) == 0 {
		b.mu.Unlock()
		return
	}
	event := domain.LiveRunEvent{
		RunID:     b.runID,
		Type:      domain.LiveToolOutputDelta,
		StreamID:  b.streamID,
		LiveSeq:   b.seqBase + b.seq,
		Payload:   rawJSONString(string(b.data)),
		CreatedAt: time.Now(),
	}
	b.seq++
	b.data = b.data[:0]
	b.mu.Unlock()
	b.hub.PublishLive(event)
}

// rawJSONString wraps a string as a raw JSON value (valid JSON string).
func rawJSONString(s string) []byte {
	// Manual JSON encoding to avoid importing encoding/json for each delta.
	// The writer owns the payload shape: a simple {"text": "..."} object.
	quoted := `"`
	for _, r := range s {
		switch r {
		case '"':
			quoted += `\"`
		case '\\':
			quoted += `\\`
		case '\n':
			quoted += `\n`
		case '\r':
			quoted += `\r`
		case '\t':
			quoted += `\t`
		default:
			quoted += string(r)
		}
	}
	quoted += `"`
	return []byte(`{"text":` + quoted + `}`)
}
