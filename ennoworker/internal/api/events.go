package api

import (
	"fmt"
	"net/http"
	"time"
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
	var wake <-chan struct{}
	var unsubscribe func()
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
	}
	if wake == nil {
		never := make(chan struct{})
		wake = never
	}

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		terminal, err := s.flushEvents(r, w, flusher, runID, &cursor)
		if err != nil || terminal {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
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
