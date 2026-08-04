package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// listAttention returns pending-first attention items for a project.
func (s *Server) listAttention(w http.ResponseWriter, r *http.Request) {
	if s.Attention == nil {
		writeError(w, r, http.StatusServiceUnavailable, "attention_unavailable",
			"attention is unavailable", true)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.Attention.ListAttention(r.Context(), r.URL.Query().Get("projectId"),
		r.URL.Query().Get("sessionId"), r.URL.Query().Get("status"), limit)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "hasMore": false})
}

// listSessionAttention returns attention items for one session.
func (s *Server) listSessionAttention(w http.ResponseWriter, r *http.Request) {
	if s.Attention == nil {
		writeError(w, r, http.StatusServiceUnavailable, "attention_unavailable",
			"attention is unavailable", true)
		return
	}
	sessionID := r.PathValue("sessionID")
	if _, err := s.Sessions.FindByID(r.Context(), sessionID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	var projectID string
	if err := s.DB.QueryRow(`SELECT project_id FROM sessions WHERE id=?`, sessionID).Scan(&projectID); err != nil {
		writeInternal(w, r, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.Attention.ListAttention(r.Context(), projectID, sessionID,
		r.URL.Query().Get("status"), limit)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "hasMore": false})
}

type dismissAttentionRequest struct {
	ClientRequestID string `json:"clientRequestId"`
}

// dismissAttention dismisses a notification-only attention item. Approval and
// needs_input items are handled through their typed actions.
func (s *Server) dismissAttention(w http.ResponseWriter, r *http.Request) {
	if s.Attention == nil {
		writeError(w, r, http.StatusServiceUnavailable, "attention_unavailable",
			"attention is unavailable", true)
		return
	}
	var input dismissAttentionRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid dismiss payload", false)
		return
	}
	if input.ClientRequestID == "" {
		writeError(w, r, http.StatusBadRequest, "client_request_id_required", "clientRequestId is required", false)
		return
	}
	if err := s.Attention.Dismiss(r.Context(), r.PathValue("attentionID")); err != nil {
		switch err {
		case store.ErrAttentionNotFound:
			writeError(w, r, http.StatusNotFound, "attention_not_found", "attention item not found", false)
		case store.ErrAttentionConflict:
			writeError(w, r, http.StatusConflict, "attention_action_required",
				"this attention item requires its typed action", false)
		default:
			writeInternal(w, r, err)
		}
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "dismissed"})
}
