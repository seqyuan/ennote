package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "streaming_unsupported", "streaming is unavailable", false)
		return
	}
	runID := r.PathValue("runID")
	if _, err := s.Runs.Get(r.Context(), runID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	cursor := parseCursor(r.Header.Get("Last-Event-ID"))
	if queryCursor := parseCursor(r.URL.Query().Get("after")); queryCursor > cursor {
		cursor = queryCursor
	}

	// Durable wake: Hub subscriber that wakes the loop to poll SQLite.
	var wake <-chan struct{}
	var unsubscribe func()
	var liveCh <-chan domain.LiveRunEvent
	var liveUnsub func()

	if s.Hub != nil {
		channel, stop := s.Hub.Subscribe(runID, 128)
		unsubscribe = stop
		defer unsubscribe()
		wakeChannel := make(chan struct{}, 1)
		go func() {
			for range channel {
				select {
				case wakeChannel <- struct{}{}:
				default:
				}
			}
		}()
		wake = wakeChannel

		// Live delta channel: non-durable rendering updates.
		lch, lStop := s.Hub.SubscribeLive(runID, 256)
		liveUnsub = lStop
		defer liveUnsub()
		liveCh = lch
	}
	if wake == nil {
		never := make(chan struct{})
		wake = never
	}

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// liveBuf holds unconsumed live events drained during durable catch-up.
	liveBuf := make([]domain.LiveRunEvent, 0, 64)

	for {
		// 1. Drain live channel into buffer (non-blocking).
		for {
			select {
			case ev := <-liveCh:
				if len(liveBuf) < cap(liveBuf) {
					liveBuf = append(liveBuf, ev)
				}
			default:
				goto afterLive
			}
		}
	afterLive:

		// 2. Catch up on durable events.
		terminal, err := s.flushEvents(r, w, flusher, runID, &cursor)
		if err != nil {
			return
		}

		// 3. Drain live buffer (now that cursor is caught up).
		for _, ev := range liveBuf {
			if err := writeLiveFrame(w, ev); err != nil {
				return
			}
		}
		liveBuf = liveBuf[:0]
		flusher.Flush()

		if terminal {
			// Drain remaining live events so the client sees final output,
			// then send a tail live event as completion signal.
			for {
				select {
				case ev := <-liveCh:
					_ = writeLiveFrame(w, ev)
				default:
					goto closed
				}
			}
		closed:
			return
		}

		// 4. Wait for next trigger.
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case ev := <-liveCh:
			// A lone live event arrived outside the batch drain; write it now.
			_ = writeLiveFrame(w, ev)
			flusher.Flush()
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) flushEvents(r *http.Request, w http.ResponseWriter, flusher http.Flusher, runID string, cursor *int64) (bool, error) {
	for {
		events, err := s.Events.After(r.Context(), runID, *cursor, 1000)
		if err != nil {
			return false, err
		}
		for _, event := range events {
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.EventID, eventJSON(event)); err != nil {
				return false, err
			}
			*cursor = event.EventID
			flusher.Flush()
			if isTerminalEvent(event.EventType) {
				return true, nil
			}
		}
		if len(events) < 1000 {
			return false, nil
		}
	}
}

// writeLiveFrame writes a LiveRunEvent as an SSE frame without an id line,
// so it never advances the client cursor. Uses the "event: live" field to
// distinguish from durable data frames.
func writeLiveFrame(w http.ResponseWriter, ev domain.LiveRunEvent) error {
	encoded, err := json.Marshal(map[string]any{
		"runId":     ev.RunID,
		"type":      ev.Type,
		"streamId":  ev.StreamID,
		"liveSeq":   ev.LiveSeq,
		"payload":   json.RawMessage(ev.Payload),
		"createdAt": ev.CreatedAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: live\ndata: %s\n\n", string(encoded))
	return err
}
