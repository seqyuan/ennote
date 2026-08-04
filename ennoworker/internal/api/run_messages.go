package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

func (s *Server) listRunMessages(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_page", "limit must be between 1 and 200", false)
			return
		}
		limit = parsed
	}
	transcript, err := store.LoadRunTranscript(r.Context(), s.DB, r.PathValue("runID"))
	if err != nil {
		if errors.Is(err, store.ErrRunNotFound) {
			writeError(w, r, http.StatusNotFound, "run_not_found", "run not found", false)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "run_transcript_unavailable",
			"run transcript is unavailable", false)
		return
	}
	end := len(transcript.Messages)
	if value := strings.TrimSpace(r.URL.Query().Get("beforeOrdinal")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > len(transcript.Messages) {
			writeError(w, r, http.StatusBadRequest, "invalid_page", "beforeOrdinal is outside the transcript", false)
			return
		}
		end = parsed
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	messages := transcript.Messages[start:end]
	response := map[string]any{
		"runId": transcript.RunID, "formatVersion": transcript.FormatVersion,
		"source": transcript.Source, "digest": transcript.Digest,
		"messages": messages, "hasMore": start > 0,
	}
	if start > 0 {
		response["nextBeforeOrdinal"] = start
	}
	writeData(w, http.StatusOK, response)
}
