package api

import (
	"encoding/json"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

func (s *Server) listRunChildren(w http.ResponseWriter, r *http.Request) {
	if s.Runs == nil || s.Delegations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_unavailable",
			"delegation activity is unavailable", true)
		return
	}
	runID := r.PathValue("runID")
	if _, err := s.Runs.Get(r.Context(), runID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	page, err := s.Delegations.ListActivity(r.Context(), runID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, page)
}

// inspectDelegation returns the parent-visible projection of one delegation
// group including generation history and valid actions.
func (s *Server) inspectDelegation(w http.ResponseWriter, r *http.Request) {
	if s.Delegations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_unavailable",
			"delegation inspection is unavailable", true)
		return
	}
	groupID := r.PathValue("groupID")
	inspection, err := s.Delegations.Inspect(r.Context(), groupID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, inspection)
}

// retryDelegation creates the next explicit retry generation for a settled
// group. A budget increase returns a pending authorization instead of child
// Runs.
func (s *Server) retryDelegation(w http.ResponseWriter, r *http.Request) {
	if s.Delegations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_unavailable",
			"delegation retry is unavailable", true)
		return
	}
	groupID := r.PathValue("groupID")
	var input domain.RetryDelegationInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid retry payload", false)
		return
	}
	if input.ClientRequestID == "" {
		writeError(w, r, http.StatusBadRequest, "client_request_id_required", "clientRequestId is required", false)
		return
	}
	generation, children, approval, err := s.Delegations.RetryGeneration(r.Context(), groupID, input)
	if err != nil {
		s.writeDelegationError(w, r, err)
		return
	}
	if err := s.enqueueChildRuns(r.Context(), children); err != nil {
		writeInternal(w, r, err)
		return
	}
	childIDs := make([]string, 0, len(children))
	for _, child := range children {
		childIDs = append(childIDs, child.ID)
	}
	writeData(w, http.StatusOK, map[string]any{
		"generation": generation, "childRunIds": childIDs, "approval": approval,
	})
}

// cancelDelegation cancels the active attempts of the current generation.
func (s *Server) cancelDelegation(w http.ResponseWriter, r *http.Request) {
	if s.Delegations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_unavailable",
			"delegation cancellation is unavailable", true)
		return
	}
	groupID := r.PathValue("groupID")
	if err := s.Delegations.CancelGroup(r.Context(), groupID); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) writeDelegationError(w http.ResponseWriter, r *http.Request, err error) {
	switch domain.ErrorCodeOf(err) {
	case domain.ErrorDelegationGenerationConflict:
		writeError(w, r, http.StatusConflict, "delegation_generation_conflict", err.Error(), false)
	case domain.ErrorDelegationRetryIneligible:
		writeError(w, r, http.StatusConflict, "delegation_retry_ineligible", err.Error(), false)
	case domain.ErrorDelegationRetryApprovalRequired:
		writeError(w, r, http.StatusAccepted, "delegation_retry_budget_approval_required", err.Error(), false)
	case domain.ErrorDelegationNotAuthorized:
		writeError(w, r, http.StatusForbidden, "delegation_not_authorized", err.Error(), false)
	case domain.ErrorDelegationBudgetExceeded:
		writeError(w, r, http.StatusConflict, "delegation_budget_exceeded", err.Error(), false)
	default:
		s.writeStoreError(w, r, err)
	}
}

var _ = store.ErrDelegationGroupNotFound
