package api

import (
	"encoding/json"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

type decideDelegationApprovalRequest struct {
	Decision        domain.ApprovalDecision `json:"decision"`
	ClientRequestID string                  `json:"clientRequestId"`
}

// decideDelegationApproval resolves a durable retry-budget authorization.
// Approved decisions materialize the frozen retry selection; rejected
// decisions terminalize the generation and rewind the group cursor.
func (s *Server) decideDelegationApproval(w http.ResponseWriter, r *http.Request) {
	if s.DelegationApprovals == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_approval_unavailable",
			"delegation approvals are unavailable", true)
		return
	}
	approvalID := r.PathValue("approvalID")
	if approvalID == "" {
		writeError(w, r, http.StatusBadRequest, "approval_id_required", "approval id is required", false)
		return
	}
	var input decideDelegationApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid decision payload", false)
		return
	}
	if input.Decision != domain.DecisionApproved && input.Decision != domain.DecisionRejected {
		writeError(w, r, http.StatusBadRequest, "invalid_decision", "decision must be approved or rejected", false)
		return
	}
	if input.ClientRequestID == "" {
		writeError(w, r, http.StatusBadRequest, "client_request_id_required", "clientRequestId is required", false)
		return
	}
	approval, children, err := s.DelegationApprovals.Decide(r.Context(), approvalID, input.Decision, input.ClientRequestID)
	if err != nil {
		switch {
		case err == store.ErrDelegationApprovalNotFound:
			writeError(w, r, http.StatusNotFound, "delegation_approval_not_found", "delegation approval not found", false)
		case err == store.ErrDelegationApprovalConflict:
			writeError(w, r, http.StatusConflict, "delegation_approval_conflict",
				"approval decision conflicts with the committed decision", false)
		case err == store.ErrDelegationNotAuthorized:
			writeError(w, r, http.StatusForbidden, "delegation_not_authorized", err.Error(), false)
		case err == store.ErrDelegationBudgetExceeded:
			writeError(w, r, http.StatusConflict, "delegation_budget_exceeded", err.Error(), false)
		default:
			writeInternal(w, r, err)
		}
		return
	}
	childIDs := make([]string, 0, len(children))
	for _, child := range children {
		childIDs = append(childIDs, child.ID)
	}
	writeData(w, http.StatusOK, map[string]any{
		"approval":    approval,
		"childRunIds": childIDs,
	})
}
