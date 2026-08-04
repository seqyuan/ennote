package api

import "net/http"

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
