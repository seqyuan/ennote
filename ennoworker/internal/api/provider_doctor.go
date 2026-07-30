package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/providerdoctor"
)

type ProviderDiagnoser interface {
	Diagnose(context.Context, string, string) (domain.ProviderDiagnostic, error)
}

func (s *Server) testProviderProfile(w http.ResponseWriter, r *http.Request) {
	if s.Doctor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_doctor_unavailable", "provider diagnostics are unavailable", true)
		return
	}
	var input struct {
		ModelProfileID string `json:"modelProfileId"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &input) {
		return
	}
	diagnostic, err := s.Doctor.Diagnose(r.Context(), r.PathValue("providerID"), input.ModelProfileID)
	if err != nil {
		switch {
		case errors.Is(err, providerdoctor.ErrProviderNotFound):
			writeError(w, r, http.StatusNotFound, "provider_not_found", "provider profile not found", false)
		case errors.Is(err, providerdoctor.ErrModelNotFound):
			writeError(w, r, http.StatusBadRequest, "model_not_found", "active model profile not found", false)
		case errors.Is(err, providerdoctor.ErrModelMismatch):
			writeError(w, r, http.StatusBadRequest, "provider_model_mismatch", "model profile belongs to another provider", false)
		default:
			writeInternal(w, r, err)
		}
		return
	}
	writeData(w, http.StatusOK, diagnostic)
}
