package api

import (
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/graphbuilder"
)

func (s *Server) builderService(w http.ResponseWriter, r *http.Request) *graphbuilder.Service {
	if s.GraphBuilder == nil {
		writeError(w, r, http.StatusServiceUnavailable, "graph_builder_unavailable", "Graph Builder is unavailable", true)
		return nil
	}
	return s.GraphBuilder
}

func (s *Server) getGraphBuilderThread(w http.ResponseWriter, r *http.Request) {
	service := s.builderService(w, r)
	if service == nil {
		return
	}
	if _, _, err := s.GlobalSources.ReadGraph(r.PathValue("graphID")); err != nil {
		writeError(w, r, http.StatusNotFound, "graph_not_found", err.Error(), false)
		return
	}
	thread, err := service.GetThread(r.Context(), r.PathValue("graphID"))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "graph_builder_load_failed", err.Error(), true)
		return
	}
	writeData(w, http.StatusOK, thread)
}

func (s *Server) sendGraphBuilderMessage(w http.ResponseWriter, r *http.Request) {
	service := s.builderService(w, r)
	if service == nil {
		return
	}
	var body struct {
		ModelProfileID string `json:"modelProfileId"`
		Instruction    string `json:"instruction"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	thread, err := service.Send(r.Context(), r.PathValue("graphID"), body.ModelProfileID, body.Instruction)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "graph_builder_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, thread)
}

func (s *Server) applyGraphBuilderProposal(w http.ResponseWriter, r *http.Request) {
	service := s.builderService(w, r)
	if service == nil {
		return
	}
	document, digest, err := service.Apply(r.Context(), r.PathValue("graphID"), r.PathValue("proposalID"))
	if err != nil {
		writeError(w, r, http.StatusConflict, "graph_builder_apply_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, globalGraphDetail{ID: document.ID, Name: document.Name, Path: s.GlobalSources.GraphsDir() + "/" + document.ID + "/graph.yaml", Digest: digest, Document: document})
}
