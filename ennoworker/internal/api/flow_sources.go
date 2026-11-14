package api

import (
	"net/http"
)

// The /v1/flows/* surface is deprecated and delegates to the unified
// /v1/graphs/* handlers. One catalog, one publish path, one run starter. The
// routes stay registered until no in-repo caller remains (see the
// implementation plan), and each handler simply re-keys flowID as graphID and
// forwards, so the response shapes are the Graph shapes.

func (s *Server) listGlobalFlows(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("graphID", r.PathValue("flowID"))
	s.listGlobalGraphs(w, r)
}

func (s *Server) getGlobalFlow(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("graphID", r.PathValue("flowID"))
	s.getGlobalGraph(w, r)
}

func (s *Server) publishGlobalFlow(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("graphID", r.PathValue("flowID"))
	s.publishGlobalGraph(w, r)
}

func (s *Server) listGlobalFlowVersions(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("graphID", r.PathValue("flowID"))
	s.listGlobalGraphVersions(w, r)
}

func (s *Server) runGlobalFlow(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("graphID", r.PathValue("flowID"))
	s.runGlobalGraph(w, r)
}
