package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

func (s *Server) getSessionActiveRun(w http.ResponseWriter, r *http.Request) {
	if s.Runs == nil || s.Approvals == nil || s.Sessions == nil {
		writeError(w, r, http.StatusServiceUnavailable, "approval_unavailable", "active Run restoration is unavailable", true)
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
	run, err := s.Runs.FindActiveBySession(r.Context(), sessionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if run == nil {
		writeData(w, http.StatusOK, nil)
		return
	}
	approval, err := s.Approvals.FindPendingBySession(r.Context(), run.SessionID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if approval != nil && approval.RunID != run.ID {
		writeInternal(w, r, errors.New("pending approval does not belong to the active Run"))
		return
	}
	writeData(w, http.StatusOK, domain.ActiveRunState{Run: *run, PendingApproval: approval})
}

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	if s.Approvals == nil {
		writeError(w, r, http.StatusServiceUnavailable, "approval_unavailable", "Interactive Approval is unavailable", true)
		return
	}
	var input struct {
		Decision        domain.ApprovalDecision `json:"decision"`
		ClientRequestID string                  `json:"clientRequestId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = r.Header.Get("Idempotency-Key")
	}
	if input.ClientRequestID == "" {
		input.ClientRequestID = newID()
	}
	approval, err := s.Approvals.Decide(r.Context(), r.PathValue("approvalID"), input.Decision, input.ClientRequestID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrApprovalNotFound):
			writeError(w, r, http.StatusNotFound, "approval_not_found", "approval request not found", false)
		case errors.Is(err, store.ErrApprovalConflict), errors.Is(err, store.ErrApprovalStale), errors.Is(err, store.ErrInvalidRunState):
			writeError(w, r, http.StatusConflict, "approval_conflict", "approval request is no longer pending for this Run", false)
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_approval_decision", err.Error(), false)
		}
		return
	}
	run, err := s.Runs.Get(r.Context(), approval.RunID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if run.Status == domain.RunQueued && s.Control != nil {
		if err := s.Control.Enqueue(context.Background(), run.ID); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "run_enqueue_failed",
				"approval was stored but the Run could not be scheduled", true)
			return
		}
	}
	writeData(w, http.StatusOK, approval)
}
