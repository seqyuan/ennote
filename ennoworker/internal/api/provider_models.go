package api

import (
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/providerdoctor"
)

// discoverProviderModels fetches the model catalog from an OpenAI-compatible
// provider. Callers may reference an existing provider profile (its stored
// baseUrl + apiKey are used) or pass a one-off baseUrl/apiKey pair.
func (s *Server) discoverProviderModels(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProviderID string `json:"providerId"`
		BaseURL    string `json:"baseUrl"`
		APIKey     string `json:"apiKey"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if s.Doctor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "provider_doctor_unavailable", "provider diagnostics are unavailable", true)
		return
	}
	baseURL, apiKey := input.BaseURL, input.APIKey
	if input.ProviderID != "" {
		provider, err := s.Providers.FindByID(r.Context(), input.ProviderID)
		if err != nil {
			writeInternal(w, r, err)
			return
		}
		if provider == nil || provider.Status != "active" {
			writeError(w, r, http.StatusNotFound, "provider_profile_not_found", "provider profile not found", false)
			return
		}
		baseURL, apiKey = provider.BaseURL, provider.APIKey
	}
	models, err := s.Doctor.DiscoverModels(r.Context(), providerdoctor.DiscoverInput{
		BaseURL: baseURL, APIKey: apiKey,
	})
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "model_discovery_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusOK, models)
}
