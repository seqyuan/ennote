package events

import (
	"context"
	"strings"
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

// Writer durably appends events and then publishes them to the Hub. The
// "append then publish" ordering is orchestration, NOT a dispatch mode: the
// @mode contract (emit/waterfall/parallel/serial/serial-merge) applies only to
// listener dispatch, and this two-step sequence deliberately does not carry a
// @mode tag.
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
	runID    string
	hub      *Hub
	toolName string
	streams  map[string]*coalesceBuf
	mu       sync.Mutex
	closed   bool
}

// NewLiveCoalescer creates a coalescer scoped to a single run. Call Close when the
// run ends to drain remaining buffered output.
//
// toolName is carried into every payload because the live delta is a *different*
// event from the durable tool_call_started/completed pair, and the client keys one
// card off both. Without it the preview card keeps whatever name it was given, and
// the durable events are the only ones that know the real one.
func NewLiveCoalescer(runID string, hub *Hub, toolName string) *LiveCoalescer {
	return &LiveCoalescer{
		runID:    runID,
		hub:      hub,
		toolName: toolName,
		streams:  make(map[string]*coalesceBuf),
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
		buf = &coalesceBuf{
			streamID: streamID, runID: c.runID, hub: c.hub,
			toolName: c.toolName, seqBase: time.Now().UnixNano(),
		}
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
	toolName string
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
		Payload:   livePayload(b.streamID, b.toolName, b.data),
		CreatedAt: time.Now(),
	}
	b.seq++
	b.data = b.data[:0]
	b.mu.Unlock()
	b.hub.PublishLive(event)
}

// livePayload wraps one coalesced chunk for the client.
//
// The identity travels with the text. The client keys a tool's preview card off
// (toolCallId, stream) — the same key the durable tool_call_* events use — so a
// payload carrying only text would land every delta in the run on one card named
// after the fallback. It is not the coalescer's business to know what a tool is,
// but it is the payload builder's business to preserve the identity it was given.
func livePayload(streamID, toolName string, data []byte) []byte {
	toolCallID, stream := splitStreamID(streamID)

	var encoded strings.Builder
	encoded.Grow(len(data) + len(toolName) + len(streamID) + 64)
	encoded.WriteString(`{"toolCallId":`)
	writeJSONString(&encoded, toolCallID)
	encoded.WriteString(`,"toolName":`)
	writeJSONString(&encoded, toolName)
	encoded.WriteString(`,"stream":`)
	writeJSONString(&encoded, stream)
	encoded.WriteString(`,"text":`)
	writeJSONString(&encoded, string(data))
	encoded.WriteString(`}`)
	return []byte(encoded.String())
}

// splitStreamID reads "{toolCallId}:{stream}". The split is on the last colon
// because the stream name is ours and never contains one, while a tool call id
// comes from the provider and might.
func splitStreamID(streamID string) (toolCallID, stream string) {
	if index := strings.LastIndexByte(streamID, ':'); index >= 0 {
		if stream = streamID[index+1:]; stream != "" {
			return streamID[:index], stream
		}
		return streamID[:index], "stdout"
	}
	return streamID, "stdout"
}

// writeJSONString appends s as a JSON string literal.
//
// Manual encoding keeps encoding/json off the per-delta path. Every control
// character has to be escaped, not just the whitespace ones: tool output carries
// ANSI escapes and raw bytes, and one unescaped \x1b makes the whole frame
// unparseable — the run stream parses frames with a bare JSON.parse, so that throws
// out of the read loop instead of skipping one event.
func writeJSONString(encoded *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	encoded.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			encoded.WriteString(`\"`)
		case '\\':
			encoded.WriteString(`\\`)
		case '\n':
			encoded.WriteString(`\n`)
		case '\r':
			encoded.WriteString(`\r`)
		case '\t':
			encoded.WriteString(`\t`)
		default:
			if r < 0x20 {
				// Everything else below the space is not legal raw inside a JSON
				// string. \u00XX is always four hex digits here, so write them out
				// rather than paying for a format call per character.
				encoded.WriteString(`\u00`)
				encoded.WriteByte(hex[r>>4])
				encoded.WriteByte(hex[r&0xf])
				continue
			}
			encoded.WriteRune(r)
		}
	}
	encoded.WriteByte('"')
}
