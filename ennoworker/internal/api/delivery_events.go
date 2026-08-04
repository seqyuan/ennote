package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// streamDeliveryEvents replays durable delivery events for a session as SSE.
// The event id is the replay cursor; reconnect yields the same event ids, and
// clients dedupe by event id and source key. This follows the durable SSE
// catch-up pattern but never claims network exactly-once.
func (s *Server) streamDeliveryEvents(w http.ResponseWriter, r *http.Request) {
	if s.Delegations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_unavailable",
			"delivery events are unavailable", true)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "streaming_unsupported", "streaming is unavailable", false)
		return
	}
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		writeError(w, r, http.StatusBadRequest, "session_id_required", "sessionId is required", false)
		return
	}
	if _, err := s.Sessions.FindByID(r.Context(), sessionID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	cursor := parseCursor(r.Header.Get("Last-Event-ID"))
	if queryCursor := parseCursor(r.URL.Query().Get("after")); queryCursor > cursor {
		cursor = queryCursor
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		events, err := s.Delegations.DeliveryEventsAfter(r.Context(), sessionID, cursor, 1000)
		if err != nil {
			writeInternal(w, r, err)
			return
		}
		for _, event := range events {
			encoded, err := json.Marshal(event)
			if err != nil {
				writeInternal(w, r, err)
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: delivery\ndata: %s\n\n", event.EventID, string(encoded)); err != nil {
				return
			}
			cursor = event.EventID
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// getDelegationHandle returns one handle with its latest completion.
func (s *Server) getDelegationHandle(w http.ResponseWriter, r *http.Request) {
	if s.Delegations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_unavailable",
			"delegation inspection is unavailable", true)
		return
	}
	handle, err := s.Delegations.GetHandle(r.Context(), r.PathValue("handleID"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	completion, err := s.Delegations.CompletionForHandle(r.Context(), handle.ID)
	if err != nil && err != store.ErrDelegationGroupNotFound {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"handle": handle, "completion": completion})
}

// listSessionDelegationHandles paginates handles of a session.
func (s *Server) listSessionDelegationHandles(w http.ResponseWriter, r *http.Request) {
	if s.Delegations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_unavailable",
			"delegation inspection is unavailable", true)
		return
	}
	sessionID := r.PathValue("sessionID")
	if _, err := s.Sessions.FindByID(r.Context(), sessionID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, err := s.Delegations.ListHandles(r.Context(), sessionID, r.URL.Query().Get("status"),
		r.URL.Query().Get("before"), limit)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, page)
}
