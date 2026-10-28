package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

type checkApprovalDecisionRequest struct {
	Decision        string `json:"decision"`
	ClientRequestID string `json:"clientRequestId"`
}

type checkApprovalDecisionResult struct {
	store.CheckApprovalRow
	Applied bool `json:"applied"`
}

// checkApprovalRunner opens the request's Session DB and returns a runner bound
// to it. The template runner only carries the policy/workspace wiring; the DB
// is request-scoped so cross-Session reads are impossible.
func (s *Server) checkApprovalRunner(w http.ResponseWriter, r *http.Request) (*store.CheckTaskRunner, *store.AgentFlowRunRepo, bool) {
	if s.CheckRunner == nil {
		writeError(w, r, http.StatusServiceUnavailable, "check_approvals_unavailable",
			"check approvals are unavailable on this Worker", true)
		return nil, nil, false
	}
	_, repo, ok := s.sessionGraphRunRepo(w, r)
	if !ok {
		return nil, nil, false
	}
	runner := *s.CheckRunner
	runner.DB = repo.DB
	return &runner, repo, true
}

func (s *Server) listSessionCheckApprovals(w http.ResponseWriter, r *http.Request) {
	runner, repo, ok := s.checkApprovalRunner(w, r)
	if !ok {
		return
	}
	run, ok := s.loadSessionGraphRun(w, r, repo)
	if !ok {
		return
	}
	approvals, err := runner.ListCheckApprovals(r.Context(), run.RunID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if approvals == nil {
		approvals = []store.CheckApprovalRow{}
	}
	writeData(w, http.StatusOK, approvals)
}

// decideSessionCheckApproval applies first-decision-wins. A repeated or
// conflicting decision returns the committed status with applied=false instead
// of disturbing the orchestrator's poll.
func (s *Server) decideSessionCheckApproval(w http.ResponseWriter, r *http.Request) {
	runner, repo, ok := s.checkApprovalRunner(w, r)
	if !ok {
		return
	}
	run, ok := s.loadSessionGraphRun(w, r, repo)
	if !ok {
		return
	}
	if run.State.Terminal() {
		writeError(w, r, http.StatusConflict, "graph_run_not_active", "flow run is no longer active", false)
		return
	}
	taskIndex, err := strconv.Atoi(r.PathValue("taskIndex"))
	if err != nil || taskIndex < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_task_index", "taskIndex must be a non-negative integer", false)
		return
	}
	var body checkApprovalDecisionRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	approved, valid := parseCheckDecision(body.Decision)
	if !valid {
		writeError(w, r, http.StatusBadRequest, "invalid_decision", "decision must be approved or rejected", false)
		return
	}
	clientRequestID := strings.TrimSpace(body.ClientRequestID)
	if clientRequestID == "" {
		clientRequestID = r.Header.Get("Idempotency-Key")
	}
	if clientRequestID == "" {
		clientRequestID = newID()
	}
	before, err := runner.GetCheckApproval(r.Context(), run.RunID, taskIndex)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "check_approval_not_found",
				"check approval not found for this run/task", false)
			return
		}
		writeInternal(w, r, err)
		return
	}
	status, err := runner.DecideCheckApproval(r.Context(), run.RunID, taskIndex, approved, clientRequestID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	after, err := runner.GetCheckApproval(r.Context(), run.RunID, taskIndex)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeData(w, http.StatusOK, checkApprovalDecisionResult{
		CheckApprovalRow: *after,
		Applied:          before.Status == "pending" && status != "" && after.Status != "pending",
	})
}

func parseCheckDecision(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "approved", "approve", "allow":
		return true, true
	case "rejected", "reject", "deny":
		return false, true
	default:
		return false, false
	}
}
