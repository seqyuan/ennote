package api

import (
	"net/http"
	"sort"
	"sync"
)

// projectActiveRunWorkers bounds how many Session databases one poll opens at a
// time. Run state lives in each Session's own SQLite file, so a project-wide
// answer is inherently a fan-out over Sessions; the concurrency cap keeps that
// fan-out from becoming a burst of simultaneous opens.
const projectActiveRunWorkers = 8

// projectActiveRuns is the project-wide answer the sidebar needs: which Sessions
// currently own an active Run. It exists so a client can ask once per project
// instead of once per Session.
type projectActiveRuns struct {
	SessionIDs []string `json:"sessionIds"`
}

// listProjectActiveRuns reports the Sessions of a project that own an active
// Run (queued, running, waiting for approval or children).
//
// This is a read of the same predicate GET /v1/sessions/{sessionID}/active-run
// applies, so the two cannot disagree about what "active" means. It is not an
// authority of its own: the per-Session change feed still carries the
// authoritative transient projection, and this endpoint only exists because a
// sidebar needs a coarse answer for Sessions it has not opened.
func (s *Server) listProjectActiveRuns(w http.ResponseWriter, r *http.Request) {
	if s.SessionStores == nil || s.Runs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "active_runs_unavailable",
			"active Run discovery is unavailable", true)
		return
	}
	projectID := r.PathValue("projectID")
	sessions, err := s.SessionStores.ListByProject(r.Context(), projectID, "")
	if err != nil {
		writeInternal(w, r, err)
		return
	}

	sessionIDs := make([]string, 0, 4)
	if len(sessions) > 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, projectActiveRunWorkers)
		for _, session := range sessions {
			sessionID := session.ID
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				db, openErr := s.SessionStores.OpenSession(r.Context(), sessionID)
				if openErr != nil {
					// A Session that cannot be opened is not a reason to fail the
					// whole poll: the same best-effort rule the attention
					// aggregation follows.
					return
				}
				repo := *s.Runs
				repo.DB = db
				run, findErr := repo.FindActiveBySession(r.Context(), sessionID)
				if findErr != nil || run == nil {
					return
				}
				mu.Lock()
				sessionIDs = append(sessionIDs, sessionID)
				mu.Unlock()
			}()
		}
		wg.Wait()
	}
	sort.Strings(sessionIDs)
	writeData(w, http.StatusOK, projectActiveRuns{SessionIDs: sessionIDs})
}
