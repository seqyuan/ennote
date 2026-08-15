package api

import (
	"database/sql"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// graphRunDetail is one Graph Run with its task checkpoints and the frozen
// flow version number, for the session activity panel.
type graphRunDetail struct {
	Run         *domain.RunAgentFlow       `json:"run"`
	Nodes       []*domain.RunAgentFlowNode `json:"nodes"`
	FlowVersion int                        `json:"flowVersion"`
}

// sessionGraphRunRepo opens the owning Session database for a session-scoped
// Graph Runs handler.
func (s *Server) sessionGraphRunRepo(w http.ResponseWriter, r *http.Request) (*sql.DB, *store.AgentFlowRunRepo, bool) {
	if s.SessionStores == nil {
		writeError(w, r, http.StatusServiceUnavailable, "session_store_unavailable", "session store is unavailable", true)
		return nil, nil, false
	}
	if _, err := s.SessionStores.FindByID(r.Context(), r.PathValue("sessionID")); err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", "session not found", false)
		return nil, nil, false
	}
	db, err := s.SessionStores.OpenSession(r.Context(), r.PathValue("sessionID"))
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", "session not found", false)
		return nil, nil, false
	}
	return db, &store.AgentFlowRunRepo{DB: db}, true
}

// loadSessionGraphRun loads one flow run and verifies it belongs to the
// requested Session (fail-closed on cross-Session reads).
func (s *Server) loadSessionGraphRun(w http.ResponseWriter, r *http.Request,
	repo *store.AgentFlowRunRepo) (*domain.RunAgentFlow, bool) {
	run, err := repo.GetRun(r.Context(), r.PathValue("runID"))
	if err != nil || run.SessionID != r.PathValue("sessionID") {
		writeError(w, r, http.StatusNotFound, "graph_run_not_found", "graph run not found in this session", false)
		return nil, false
	}
	return run, true
}

// listSessionGraphRuns returns the Session's Graph Runs, newest first.
func (s *Server) listSessionGraphRuns(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.sessionGraphRunRepo(w, r)
	if !ok {
		return
	}
	runs, err := repo.ListSessionRuns(r.Context(), r.PathValue("sessionID"), 50)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if runs == nil {
		runs = []*domain.RunAgentFlow{}
	}
	writeData(w, http.StatusOK, runs)
}

// getSessionGraphRun returns one run plus its task checkpoints and the frozen
// flow version number.
func (s *Server) getSessionGraphRun(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.sessionGraphRunRepo(w, r)
	if !ok {
		return
	}
	run, ok := s.loadSessionGraphRun(w, r, repo)
	if !ok {
		return
	}
	nodes, err := repo.ListNodes(r.Context(), run.RunID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if nodes == nil {
		nodes = []*domain.RunAgentFlowNode{}
	}
	version, err := (&store.OrchestratorStore{Runs: repo}).GetVersion(r.Context(), run.FlowVersionID)
	flowVersion := 0
	if err == nil {
		flowVersion = version.Version
	}
	writeData(w, http.StatusOK, graphRunDetail{Run: run, Nodes: nodes, FlowVersion: flowVersion})
}

// cancelSessionGraphRun requests flow cancellation: the orchestrator's poll
// observes the cancel flag and hard-cancels remaining children. Active child
// Runs are cancelled immediately so their settlement is visible to the poll.
func (s *Server) cancelSessionGraphRun(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.sessionGraphRunRepo(w, r)
	if !ok {
		return
	}
	run, ok := s.loadSessionGraphRun(w, r, repo)
	if !ok {
		return
	}
	if run.State.Terminal() {
		writeData(w, http.StatusOK, run)
		return
	}
	if err := repo.SetCancelRequested(r.Context(), run.RunID); err != nil {
		writeInternal(w, r, err)
		return
	}
	if s.Control != nil {
		nodes, listErr := repo.ListNodes(r.Context(), run.RunID)
		if listErr == nil {
			for _, node := range nodes {
				if node.TerminalState == domain.FlowNodeRunning && node.ChildRunID != "" {
					_ = s.Control.Cancel(r.Context(), node.ChildRunID)
				}
			}
		}
	}
	writeData(w, http.StatusOK, run)
}

// resumeSessionGraphRun resets a cancelled/failed run to pending and restarts
// its orchestrator on the Worker lifetime context.
func (s *Server) resumeSessionGraphRun(w http.ResponseWriter, r *http.Request) {
	_, repo, ok := s.sessionGraphRunRepo(w, r)
	if !ok {
		return
	}
	run, ok := s.loadSessionGraphRun(w, r, repo)
	if !ok {
		return
	}
	resumed, err := repo.ResumeFlowRun(r.Context(), run.RunID)
	if err != nil {
		writeError(w, r, http.StatusConflict, "graph_run_resume_failed", err.Error(), false)
		return
	}
	if s.GraphRunResume != nil {
		if err := s.GraphRunResume(r.Context(), repo.DB, r.PathValue("sessionID"), run.RunID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "graph_run_resume_failed", err.Error(), false)
			return
		}
	}
	writeData(w, http.StatusOK, resumed)
}
