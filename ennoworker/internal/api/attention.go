package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// listAttention returns pending-first attention items for a project. In the V2
// file-native layout each Session owns its own SQLite attention_items table, so
// the project-scoped bell fans out over every Session in the project and merges
// the rows. A sessionId query parameter narrows the fan-out to one Session.
func (s *Server) listAttention(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.listAttentionItems(r.Context(), r.URL.Query().Get("projectId"),
		r.URL.Query().Get("sessionId"), r.URL.Query().Get("status"), limit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, store.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "session_resource_not_found", "Session resource not found", false)
			return
		}
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"items": items, "hasMore": false})
}

// listAttentionItems aggregates attention rows across a project's Sessions or,
// when sessionID is non-empty, reads just that Session's rows.
func (s *Server) listAttentionItems(ctx context.Context, projectID, sessionID, status string, limit int) ([]domain.AttentionItem, error) {
	if s.SessionStores == nil || s.Attention == nil {
		return nil, fmt.Errorf("attention is unavailable")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	// Without a project scope there is nothing to aggregate; return an empty
	// page rather than failing the global bell on first paint.
	if projectID == "" {
		return []domain.AttentionItem{}, nil
	}

	if sessionID != "" {
		db, err := s.SessionStores.OpenSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		return (&store.AttentionRepo{DB: db}).ListAttention(ctx, projectID, sessionID, status, limit)
	}

	sessions, err := s.SessionStores.ListByProject(ctx, projectID, "")
	if err != nil {
		return nil, err
	}
	items := make([]domain.AttentionItem, 0, len(sessions))
	for _, session := range sessions {
		db, err := s.SessionStores.OpenSession(ctx, session.ID)
		if err != nil {
			// A Session that fails to open (for example, removed on disk) must
			// not break the cross-session bell; keep it best-effort.
			continue
		}
		sessionItems, err := (&store.AttentionRepo{DB: db}).ListAttention(ctx, projectID, session.ID, status, limit)
		if err != nil {
			continue
		}
		items = append(items, sessionItems...)
	}
	// Merge order: action-required first, then newest first (mirrors the
	// per-Session ORDER BY so the combined page stays stable).
	sort.Slice(items, func(i, j int) bool {
		if items[i].RequiresAction != items[j].RequiresAction {
			return items[i].RequiresAction
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
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
