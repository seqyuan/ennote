package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// sessionSnapshot is the authoritative transient-state projection pushed on the
// session change feed: the subscribed baseline plus change-detected updates.
// It is a full replacement (not a delta) so reconnects and second tabs converge.
type sessionSnapshot struct {
	ActiveRun        *domain.AgentRun            `json:"activeRun,omitempty"`
	PendingApproval  *domain.ToolApprovalRequest `json:"pendingApproval,omitempty"`
	QueuedInputs     []domain.QueuedInput        `json:"queuedInputs"`
	Checkpoints      []domain.ContextCompaction  `json:"checkpoints"`
	DelegationActive bool                        `json:"delegationActive"`
}

// streamSessionEvents serves the session-level change feed (durable events
// whitelist + transient snapshots). It reuses the run SSE skeleton: poll the
// durable log on a ticker, emit id-scoped frames for Last-Event-ID resume, and
// push snapshots only when they change. Live rendering deltas stay on the run
// stream (design D7).
func (s *Server) streamSessionEvents(w http.ResponseWriter, r *http.Request) {
	if s.Runs == nil || s.Approvals == nil || s.Compactions == nil || s.Queue == nil || s.Events == nil {
		writeError(w, r, http.StatusServiceUnavailable, "session_events_unavailable", "session events are unavailable", true)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "streaming_unsupported", "streaming is unavailable", false)
		return
	}
	sessionID := r.PathValue("sessionID")
	session, err := s.Sessions.FindByID(r.Context(), sessionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if session == nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", "session not found", false)
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

	snapshot, err := s.buildSessionSnapshot(r.Context(), sessionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	lastSeq, err := s.sessionMessageSeqHighWater(r.Context(), sessionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if err := writeSessionSubscribed(w, flusher, s.InstanceID, lastSeq, snapshot); err != nil {
		return
	}
	lastSnapshot := encodeSessionSnapshot(snapshot)

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		events, err := s.Events.SessionEventsAfter(r.Context(), sessionID, cursor, 1000)
		if err != nil {
			return
		}
		for _, event := range events {
			cursor = event.EventID
			frame, err := s.sessionEventFrame(r.Context(), event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.EventID, frame); err != nil {
				return
			}
		}

		// Re-read the transient projection; push a full frame only on change.
		snapshot, err = s.buildSessionSnapshot(r.Context(), sessionID)
		if err != nil {
			return
		}
		if encoded := encodeSessionSnapshot(snapshot); encoded != lastSnapshot {
			lastSeq, err = s.sessionMessageSeqHighWater(r.Context(), sessionID)
			if err != nil {
				return
			}
			if err := writeSessionSnapshot(w, flusher, lastSeq, snapshot); err != nil {
				return
			}
			lastSnapshot = encoded
		}
		flusher.Flush()

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

// sessionEventFrame renders one whitelisted durable event. message_committed
// additionally carries the committed messages' seq range so the client can
// re-sync without re-pulling the whole page.
func (s *Server) sessionEventFrame(ctx context.Context, event domain.RunEvent) ([]byte, error) {
	if event.EventType == "message_committed" {
		var first, last int64
		if err := s.DB.QueryRowContext(ctx,
			`SELECT COALESCE(MIN(seq),0), COALESCE(MAX(seq),0) FROM messages WHERE run_id = ?`,
			event.RunID,
		).Scan(&first, &last); err != nil {
			return nil, fmt.Errorf("read committed message seq range: %w", err)
		}
		return json.Marshal(map[string]any{
			"type":      event.EventType,
			"runId":     event.RunID,
			"payload":   json.RawMessage(event.Payload),
			"firstSeq":  first,
			"lastSeq":   last,
			"createdAt": event.CreatedAt.Format(time.RFC3339Nano),
		})
	}
	return eventJSON(event), nil
}

func (s *Server) sessionMessageSeqHighWater(ctx context.Context, sessionID string) (int64, error) {
	var seq int64
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM messages WHERE session_id = ?`, sessionID,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("read message seq high water: %w", err)
	}
	return seq, nil
}

func (s *Server) buildSessionSnapshot(ctx context.Context, sessionID string) (*sessionSnapshot, error) {
	run, err := s.Runs.FindActiveBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var approval *domain.ToolApprovalRequest
	if run != nil {
		approval, err = s.Approvals.FindPendingBySession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	checkpoints, err := s.Compactions.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	queued := []domain.QueuedInput{}
	if run != nil {
		queued, err = s.Queue.ListQueuedBySession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
	}
	return &sessionSnapshot{
		ActiveRun:        run,
		PendingApproval:  approval,
		QueuedInputs:     queued,
		Checkpoints:      checkpoints,
		DelegationActive: run != nil && (run.Status == domain.RunWaitingChildren || run.Status == domain.RunWaitingDelegationAdmit),
	}, nil
}

func encodeSessionSnapshot(snapshot *sessionSnapshot) string {
	encoded, _ := json.Marshal(snapshot)
	return string(encoded)
}

func writeSessionSubscribed(w http.ResponseWriter, flusher http.Flusher, instanceID string, lastSeq int64, snapshot *sessionSnapshot) error {
	encoded, err := json.Marshal(map[string]any{
		"type": "subscribed", "instanceId": instanceID, "lastSeq": lastSeq, "snapshot": snapshot,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", encoded)
	flusher.Flush()
	return err
}

func writeSessionSnapshot(w http.ResponseWriter, flusher http.Flusher, lastSeq int64, snapshot *sessionSnapshot) error {
	encoded, err := json.Marshal(map[string]any{
		"type": "snapshot", "lastSeq": lastSeq, "snapshot": snapshot,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", encoded)
	flusher.Flush()
	return err
}
