package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// receive takes one live event, failing the test rather than hanging when the
// publisher never fired.
func receive(t *testing.T, live <-chan domain.LiveRunEvent) domain.LiveRunEvent {
	t.Helper()
	select {
	case event := <-live:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("no live event was published")
		return domain.LiveRunEvent{}
	}
}

// livePayloadBody is the payload the client reads: the identity the preview card is
// keyed off, plus the escaped text.
type livePayloadBody struct {
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Stream     string `json:"stream"`
	Text       string `json:"text"`
}

func decodeLivePayload(t *testing.T, payload []byte) livePayloadBody {
	t.Helper()
	var decoded livePayloadBody
	require.NoError(t, json.Unmarshal(payload, &decoded), string(payload))
	return decoded
}

func livePayloadText(t *testing.T, payload []byte) string {
	t.Helper()
	return decodeLivePayload(t, payload).Text
}

func TestRawJSONStringEscapesEveryControlCharacter(t *testing.T) {
	// One unescaped byte makes the payload unparseable, and the client discards a
	// frame it cannot parse, so that command's output stops streaming. ANSI escapes
	// are the common case: almost every CLI colours its output when it thinks it
	// has a terminal.
	var inputs []string
	for r := rune(0); r < 0x20; r++ {
		inputs = append(inputs, "before"+string(r)+"after")
	}
	inputs = append(inputs,
		"plain",
		`quote"here`,
		`back\slash`,
		"ansi\x1b[31mred\x1b[0m",
		"\x7fdelete",
		"中文与 emoji 🖖",
	)

	for _, input := range inputs {
		encoded := livePayload("call-1:stdout", "bash", []byte(input))
		require.True(t, json.Valid(encoded), "%q produced unparseable JSON: %s", input, encoded)

		assert.Equal(t, input, livePayloadText(t, encoded), "%q did not survive the round trip", input)
	}
}

func TestRawJSONStringReplacesUndecodableBytes(t *testing.T) {
	// Tool output is not always text. A truncated multi-byte sequence cannot be
	// round tripped, but it must not break the frame either: the encoder walks
	// runes, so each undecodable byte becomes U+FFFD and the result is still valid
	// JSON. "Lossy but safe" is the contract, so pin it rather than leave the next
	// reader guessing.
	invalid := []byte{'a', 0xff, 'b', 0xed, 0xa0, 0x80, 'c'}

	encoded := livePayload("call-1:stdout", "bash", invalid)
	require.True(t, json.Valid(encoded), string(encoded))
	// 0xff is one bad byte; ed a0 80 is a surrogate encoded as UTF-8, which the
	// decoder rejects byte by byte.
	assert.Equal(t, "a\uFFFDb\uFFFD\uFFFD\uFFFDc", livePayloadText(t, encoded))
}

func TestLiveCoalescerBatchesWithinTheWindow(t *testing.T) {
	hub := NewHub()
	live, unsubscribe := hub.SubscribeLive("run-1", 16)
	defer unsubscribe()
	coalescer := NewLiveCoalescer("run-1", hub, "bash")

	coalescer.Push("call-1:stdout", []byte("a"), time.Now())
	coalescer.Push("call-1:stdout", []byte("b"), time.Now())

	// Reading the single event is the assertion: if each push had flushed on its
	// own, the first event would carry "a" alone.
	event := receive(t, live)
	payload := decodeLivePayload(t, event.Payload)
	assert.Equal(t, "ab", payload.Text)
	// The identity travels with the text: the client keys the preview card off the
	// same (toolCallId, stream) pair the durable tool_call_* events use, so a
	// payload without it puts every delta in the run on one card.
	assert.Equal(t, "call-1", payload.ToolCallID)
	assert.Equal(t, "bash", payload.ToolName)
	assert.Equal(t, "stdout", payload.Stream)
	// The exact spelling matters: the client reads this with JSON.parse, which is
	// case-sensitive, while a Go struct decodes "toolCallID" just as happily. Pin
	// the keys themselves, not only the values.
	var keys map[string]any
	require.NoError(t, json.Unmarshal(event.Payload, &keys))
	assert.ElementsMatch(t, []string{"toolCallId", "toolName", "stream", "text"}, keysOf(keys))
	assert.Equal(t, "call-1:stdout", event.StreamID)
	assert.Equal(t, "run-1", event.RunID)
	assert.Equal(t, domain.LiveToolOutputDelta, event.Type)

	// A second batch is its own event, and the sequence moves.
	coalescer.Push("call-1:stdout", []byte("c"), time.Now())
	next := receive(t, live)
	assert.Equal(t, "c", livePayloadText(t, next.Payload))
	assert.Greater(t, next.LiveSeq, event.LiveSeq, "each flush advances the connection-local sequence")
}

func TestLiveCoalescerKeepsStreamsApart(t *testing.T) {
	hub := NewHub()
	live, unsubscribe := hub.SubscribeLive("run-1", 16)
	defer unsubscribe()
	coalescer := NewLiveCoalescer("run-1", hub, "bash")

	coalescer.Push("call-1:stdout", []byte("out"), time.Now())
	coalescer.Push("call-1:stderr", []byte("err"), time.Now())
	coalescer.Close()

	byStream := map[string]string{}
	for i := 0; i < 2; i++ {
		event := receive(t, live)
		byStream[event.StreamID] = livePayloadText(t, event.Payload)
	}
	assert.Equal(t, "out", byStream["call-1:stdout"])
	assert.Equal(t, "err", byStream["call-1:stderr"])
}

func TestLiveCoalescerFlushesAtTheByteThreshold(t *testing.T) {
	hub := NewHub()
	live, unsubscribe := hub.SubscribeLive("run-1", 4)
	defer unsubscribe()
	coalescer := NewLiveCoalescer("run-1", hub, "bash")

	// A chatty tool must not wait for the window: 32 KiB is the flush trigger.
	coalescer.Push("call-1:stdout", []byte(strings.Repeat("x", liveCoalesceBytes)), time.Now())

	event := receive(t, live)
	assert.Len(t, livePayloadText(t, event.Payload), liveCoalesceBytes)
}

func TestLiveCoalescerCloseDrainsAndStops(t *testing.T) {
	hub := NewHub()
	live, unsubscribe := hub.SubscribeLive("run-1", 8)
	defer unsubscribe()
	coalescer := NewLiveCoalescer("run-1", hub, "bash")

	coalescer.Push("call-1:stdout", []byte("buffered"), time.Now())
	coalescer.Close()

	event := receive(t, live)
	assert.Equal(t, "buffered", livePayloadText(t, event.Payload))

	// A push after Close is a no-op rather than a panic or a late event.
	coalescer.Push("call-1:stdout", []byte("late"), time.Now())
	coalescer.Push("call-1:stdout", []byte("later"), time.Now())
	coalescer.Close()
	assert.Empty(t, live)
}

func TestHubSubscribeLiveDeliversOnlyToItsRun(t *testing.T) {
	hub := NewHub()
	live, unsubscribe := hub.SubscribeLive("run-1", 4)
	defer unsubscribe()

	hub.PublishLive(domain.LiveRunEvent{RunID: "run-1", Type: domain.LiveTextDelta})
	assert.Equal(t, domain.LiveTextDelta, receive(t, live).Type)

	hub.PublishLive(domain.LiveRunEvent{RunID: "run-2", Type: domain.LiveTextDelta})
	assert.Empty(t, live, "another run's event must not be delivered")
}

func TestHubUnsubscribeClosesOnceAndStopsDelivery(t *testing.T) {
	hub := NewHub()
	live, unsubscribe := hub.SubscribeLive("run-1", 4)

	unsubscribe()
	unsubscribe()

	_, open := <-live
	assert.False(t, open, "unsubscribe closes the subscription channel")

	// Publishing to a removed subscriber must not panic on the closed channel.
	assert.NotPanics(t, func() {
		hub.PublishLive(domain.LiveRunEvent{RunID: "run-1"})
	})
}

func TestHubDropsTheOldestEventWhenTheSubscriberLags(t *testing.T) {
	hub := NewHub()
	live, unsubscribe := hub.SubscribeLive("run-1", 1)
	defer unsubscribe()

	hub.PublishLive(domain.LiveRunEvent{RunID: "run-1", StreamID: "first"})
	hub.PublishLive(domain.LiveRunEvent{RunID: "run-1", StreamID: "second"})

	// Non-blocking by design: a consumer that cannot keep up loses history, never
	// the newest value, and never the producer.
	assert.Equal(t, "second", receive(t, live).StreamID)
}

func TestHubSubscribeLiveDefaultsItsCapacity(t *testing.T) {
	hub := NewHub()
	live, unsubscribe := hub.SubscribeLive("run-1", 0)
	defer unsubscribe()
	assert.Equal(t, 64, cap(live))
}

type stubAppender struct {
	appended []domain.PendingEvent
	err      error
}

func (s *stubAppender) Append(_ context.Context, runID string, pending ...domain.PendingEvent) ([]domain.RunEvent, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.appended = append(s.appended, pending...)
	events := make([]domain.RunEvent, 0, len(pending))
	for _, item := range pending {
		events = append(events, domain.RunEvent{RunID: runID, EventType: item.EventType})
	}
	return events, nil
}

func TestWriterPublishesOnlyWhatItAppended(t *testing.T) {
	hub := NewHub()
	durable, unsubscribe := hub.Subscribe("run-1", 8)
	defer unsubscribe()
	writer := NewWriter(&stubAppender{}, hub)

	assert.Same(t, hub, writer.Hub())

	_, err := writer.Append(context.Background(), "run-1", domain.PendingEvent{EventType: "run_started"})
	require.NoError(t, err)
	assert.Equal(t, "run_started", receiveDurable(t, durable).EventType)
}

func TestWriterStaysSilentWhenTheAppendFails(t *testing.T) {
	hub := NewHub()
	durable, unsubscribe := hub.Subscribe("run-1", 8)
	defer unsubscribe()
	writer := NewWriter(&stubAppender{err: assert.AnError}, hub)

	_, err := writer.Append(context.Background(), "run-1", domain.PendingEvent{EventType: "run_started"})
	require.Error(t, err)
	// Nothing is published: subscribers must not see an event the store refused.
	assert.Empty(t, durable)
}

func TestWriterDefaultsItsHubAndPublishesLive(t *testing.T) {
	writer := NewWriter(&stubAppender{}, nil)
	require.NotNil(t, writer.Hub())

	live, unsubscribe := writer.Hub().SubscribeLive("run-1", 4)
	defer unsubscribe()
	writer.PublishLive(domain.LiveRunEvent{RunID: "run-1", Type: domain.LiveThinkingDelta})
	assert.Equal(t, domain.LiveThinkingDelta, receive(t, live).Type)
}

func receiveDurable(t *testing.T, durable <-chan domain.RunEvent) domain.RunEvent {
	t.Helper()
	select {
	case event := <-durable:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("no durable event was published")
		return domain.RunEvent{}
	}
}

func TestSplitStreamIDKeepsTheStreamNameLast(t *testing.T) {
	// A tool call id comes from the provider and could contain a colon; the stream
	// name is ours and never does, so the split is on the last one.
	toolCallID, stream := splitStreamID("call:1:stderr")
	assert.Equal(t, "call:1", toolCallID)
	assert.Equal(t, "stderr", stream)

	toolCallID, stream = splitStreamID("call-1")
	assert.Equal(t, "call-1", toolCallID)
	assert.Equal(t, "stdout", stream, "a stream without a name is stdout")

	// An empty stream name violates the payload contract's enum, and the client drops
	// a frame that does not match it — so the fallback belongs here, on the producing
	// side, where it can still be corrected.
	toolCallID, stream = splitStreamID("call-1:")
	assert.Equal(t, "call-1", toolCallID)
	assert.Equal(t, "stdout", stream)
}

func TestLivePayloadEscapesTheIdentityToo(t *testing.T) {
	// The tool call id is provider-supplied and the tool name is configuration, so
	// neither may be trusted to be quote-free.
	payload := decodeLivePayload(t, livePayload(`call"1:stdout`, "we\"ird\n", []byte("text")))

	assert.Equal(t, `call"1`, payload.ToolCallID)
	assert.Equal(t, "we\"ird\n", payload.ToolName)
	assert.Equal(t, "stdout", payload.Stream)
}

func keysOf(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
