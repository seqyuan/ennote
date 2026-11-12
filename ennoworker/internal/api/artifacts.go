package api

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

func (s *Server) getArtifact(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact_service_unavailable", "artifact service is unavailable", true)
		return
	}
	artifact, err := s.Artifacts.GetForSession(r.Context(), r.PathValue("artifactID"), r.PathValue("sessionID"))
	if err != nil {
		s.writeArtifactError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, artifact)
}

func (s *Server) previewArtifact(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact_service_unavailable", "artifact service is unavailable", true)
		return
	}
	artifact, err := s.Artifacts.GetForSession(r.Context(), r.PathValue("artifactID"), r.PathValue("sessionID"))
	if err != nil {
		s.writeArtifactError(w, r, err)
		return
	}
	setArtifactHeaders(w)
	switch artifact.Kind {
	case domain.ArtifactKindImage, "image_attachment":
		artifact, data, err := s.Artifacts.ReadForSession(r.Context(), artifact.ID, r.PathValue("sessionID"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", artifact.MIMEType)
		w.Header().Set("Content-Disposition", contentDisposition("inline", artifact.Name))
		http.ServeContent(w, r, artifact.Name, artifact.CreatedAt, bytes.NewReader(data))
	case domain.ArtifactKindTable:
		_, preview, err := s.Artifacts.PreviewTable(r.Context(), artifact.ID, r.PathValue("sessionID"), r.URL.Query().Get("sheet"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, preview)
	case domain.ArtifactKindStaticHTML:
		_, preview, err := s.Artifacts.PreviewHTML(r.Context(), artifact.ID, r.PathValue("sessionID"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", contentDisposition("inline", artifact.Name))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, preview)
	case domain.ArtifactKindWidget:
		_, preview, err := s.Artifacts.PreviewWidget(r.Context(), artifact.ID, r.PathValue("sessionID"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", contentDisposition("inline", artifact.Name))
		w.Header().Set("Content-Security-Policy", artifacts.WidgetContentSecurityPolicy)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, preview)
	case domain.ArtifactKindText:
		_, preview, err := s.Artifacts.PreviewText(r.Context(), artifact.ID, r.PathValue("sessionID"))
		if err != nil {
			s.writeArtifactError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, preview)
	default:
		writeError(w, r, http.StatusUnsupportedMediaType, "artifact_preview_unsupported", "artifact does not have a safe preview", false)
	}
}

func (s *Server) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	if s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact_service_unavailable", "artifact service is unavailable", true)
		return
	}
	artifact, data, err := s.Artifacts.ReadForSession(r.Context(), r.PathValue("artifactID"), r.PathValue("sessionID"))
	if err != nil {
		s.writeArtifactError(w, r, err)
		return
	}
	setArtifactHeaders(w)
	w.Header().Set("Content-Type", artifact.MIMEType)
	w.Header().Set("Content-Disposition", contentDisposition("attachment", artifact.Name))
	http.ServeContent(w, r, artifact.Name, artifact.CreatedAt, bytes.NewReader(data))
}

func setArtifactHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
}

func contentDisposition(disposition, name string) string {
	value := mime.FormatMediaType(disposition, map[string]string{"filename": name})
	if value == "" {
		return disposition
	}
	return value
}

func (s *Server) writeArtifactError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, artifacts.ErrArtifactNotFound):
		writeError(w, r, http.StatusNotFound, "artifact_not_found", "artifact not found", false)
	case errors.Is(err, artifacts.ErrArtifactCorrupt):
		writeError(w, r, http.StatusConflict, "artifact_corrupt", "artifact content is missing or corrupt", false)
	case errors.Is(err, artifacts.ErrPreviewUnsupported):
		writeError(w, r, http.StatusUnsupportedMediaType, "artifact_preview_unsupported", "artifact does not have a safe preview", false)
	default:
		writeInternal(w, r, err)
	}
}

// pinnedArtifactWorkers bounds the fan-out over Session databases. A Project-level
// answer for pinned artifacts is inherently one query per Session file, exactly
// like GET /v1/projects/{projectID}/active-runs, so it takes the same bound for the
// same reason: the per-Session queries run in parallel, while the manager
// serializes the opens behind its own lock.
const pinnedArtifactWorkers = projectActiveRunWorkers

// pinArtifact adds one artifact to the Project canvas. The write is Session-scoped
// (the Session database owns the artifact row) even though the canvas it feeds is
// Project-scoped; that asymmetry is what keeps the pin a Worker resource without a
// second authority outside the Session that stores the artifact.
func (s *Server) pinArtifact(w http.ResponseWriter, r *http.Request) {
	s.setArtifactPinned(w, r, true)
}

// unpinArtifact removes one artifact from the Project canvas.
func (s *Server) unpinArtifact(w http.ResponseWriter, r *http.Request) {
	s.setArtifactPinned(w, r, false)
}

func (s *Server) setArtifactPinned(w http.ResponseWriter, r *http.Request, pinned bool) {
	if s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "artifact_service_unavailable", "artifact service is unavailable", true)
		return
	}
	artifact, err := s.Artifacts.SetPinned(r.Context(), r.PathValue("artifactID"), r.PathValue("sessionID"), pinned)
	if err != nil {
		s.writeArtifactError(w, r, err)
		return
	}
	// Both verbs answer 200 with the refreshed artifact, and DELETE is 200 rather
	// than the more conventional 204: the state a client shows after unpinning is
	// the artifact's, not the absence of a body. A 204 would force every client to
	// branch into "no content, assume the change applied" and then either trust
	// itself or fetch again, and two branches for one state change is how a canvas
	// ends up painting an artifact it still believes is pinned.
	writeData(w, http.StatusOK, artifact)
}

// listPinnedArtifacts is the Project canvas: every pinned artifact of every Session
// in the Project, newest pin first.
//
// It is an aggregation because the state is not in one place — each Session's
// SQLite file owns the `pinned_at` of the artifacts it published — and it follows
// the same shape as listProjectActiveRuns: resolve the Sessions, fan out over their
// databases with a bounded worker pool, then merge in the handler. There is no
// project-wide index of pinned artifacts, on purpose: a second one would be a second
// authority for a fact each database already states.
func (s *Server) listPinnedArtifacts(w http.ResponseWriter, r *http.Request) {
	if s.SessionStores == nil || s.Artifacts == nil {
		writeError(w, r, http.StatusServiceUnavailable, "pinned_artifacts_unavailable",
			"pinned artifact discovery is unavailable", true)
		return
	}
	projectID := r.PathValue("projectID")
	sessions, err := s.SessionStores.ListByProject(r.Context(), projectID, "")
	if err != nil {
		writeInternal(w, r, err)
		return
	}

	// Non-nil even when nothing is pinned: the contract declares an array, and null
	// would fail every strict consumer of it.
	pinned := make([]domain.Artifact, 0, 4)
	if len(sessions) > 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, pinnedArtifactWorkers)
		for _, session := range sessions {
			sessionID := session.ID
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				db, openErr := s.SessionStores.OpenSession(r.Context(), sessionID)
				if openErr != nil {
					// Best effort, the rule listProjectActiveRuns already follows: a
					// Session whose file cannot be opened is not a reason to answer
					// "nothing is pinned" for the whole Project, and it is not a reason
					// to fail the request either. Dropping the one Session and saying so
					// in the log keeps the rest of the canvas visible, which is what the
					// user asked for; the next poll retries the missing Session.
					logPinnedArtifactSkip(r, sessionID, "open Session database", openErr)
					return
				}
				rows, listErr := s.Artifacts.ListPinned(r.Context(), db)
				if listErr != nil {
					logPinnedArtifactSkip(r, sessionID, "list pinned artifacts", listErr)
					return
				}
				for index := range rows {
					// A row with no session_id is project-scoped (an image uploaded
					// against the project), which GetForSession authorizes from any
					// Session of the project. Name the Session whose database answered,
					// otherwise the canvas would list an entry no session route can be
					// built for.
					if rows[index].SessionID == "" {
						rows[index].SessionID = sessionID
					}
				}
				mu.Lock()
				pinned = append(pinned, rows...)
				mu.Unlock()
			}()
		}
		wg.Wait()
	}

	sort.SliceStable(pinned, func(i, j int) bool {
		left, right := pinned[i].PinnedAt, pinned[j].PinnedAt
		if left != nil && right != nil && !left.Equal(*right) {
			return left.After(*right)
		}
		if (left == nil) != (right == nil) {
			return left != nil
		}
		// The tie-break is the id, so two pins written in the same nanosecond still
		// answer in one order instead of whatever the goroutines happened to do.
		return pinned[i].ID < pinned[j].ID
	})
	writeData(w, http.StatusOK, pinned)
}

// logPinnedArtifactSkip records a Session the canvas aggregation could not read.
// The answer is still served (the rest of the Project's pin state is correct), so
// the log is the only place the omission is visible — it has to say which Session
// and which step, not just that something went wrong.
func logPinnedArtifactSkip(r *http.Request, sessionID, step string, err error) {
	requestID, _ := r.Context().Value(requestIDKey{}).(string)
	slog.Warn("skipped a Session while aggregating pinned artifacts",
		"request_id", requestID, "session_id", sessionID, "step", step, "error", err)
}
