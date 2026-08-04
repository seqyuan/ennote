package api

import (
	"encoding/json"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// continueDelegationItem handles both typed continuation commands: input for
// needs_input attempts and follow-up for completed/blocked attempts.
func (s *Server) continueDelegationItem(w http.ResponseWriter, r *http.Request) {
	if s.Delegations == nil {
		writeError(w, r, http.StatusServiceUnavailable, "delegation_unavailable",
			"delegation continuation is unavailable", true)
		return
	}
	itemID := r.PathValue("itemID")
	command := r.PathValue("command")
	var input domain.DelegationInputCommand
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid continuation payload", false)
		return
	}
	if input.ClientRequestID == "" {
		writeError(w, r, http.StatusBadRequest, "client_request_id_required", "clientRequestId is required", false)
		return
	}
	var generation *domain.DelegationGeneration
	var child *domain.AgentRun
	var err error
	switch command {
	case "input":
		generation, child, err = s.Delegations.ContinueNeedsInput(r.Context(), itemID, input)
	case "follow-up":
		generation, child, err = s.Delegations.FollowUp(r.Context(), itemID, input)
	default:
		writeError(w, r, http.StatusNotFound, "command_not_found", "unknown continuation command", false)
		return
	}
	if err != nil {
		switch domain.ErrorCodeOf(err) {
		case domain.ErrorDelegationInputStale:
			writeError(w, r, http.StatusConflict, "delegation_input_stale", err.Error(), false)
		case domain.ErrorDelegationFollowUpForbidden:
			writeError(w, r, http.StatusConflict, "delegation_follow_up_forbidden", err.Error(), false)
		case domain.ErrorDelegationNotAuthorized:
			writeError(w, r, http.StatusForbidden, "delegation_not_authorized", err.Error(), false)
		case domain.ErrorDelegationBudgetExceeded:
			writeError(w, r, http.StatusConflict, "delegation_budget_exceeded", err.Error(), false)
		default:
			s.writeStoreError(w, r, err)
		}
		return
	}
	childID := ""
	if child != nil {
		childID = child.ID
	}
	writeData(w, http.StatusOK, map[string]any{
		"generation": generation, "childRunId": childID,
	})
}
