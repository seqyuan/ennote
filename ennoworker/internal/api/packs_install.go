package api

import (
	"net/http"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentpack"
	"github.com/seqyuan/ennote/ennoworker/internal/packinstall"
)

// packInstallRequest is the POST /v1/packs/install body. The source is path-only
// for the same reason inspect is: S2a installs bytes the user already has on the
// Worker host, and S6 adds URL/git fetching.
type packInstallRequest struct {
	Source         packInspectSource `json:"source"`
	ExpectedDigest string            `json:"expectedDigest"`
	DryRun         bool              `json:"dryRun"`
	Renames        *packRenames      `json:"renames"`
}

// packRenames changes the installed store ids only. Pack-internal references are
// never rewritten, so a rename is a collision workaround, not a refactor.
type packRenames struct {
	Flows map[string]string `json:"flows"`
	Roles map[string]string `json:"roles"`
}

// installPack plans and (unless dryRun) applies one Agent Pack install.
//
// A dry run always answers 200 with the plan, blockers included: the caller asked
// what would happen, and "would be refused" is the answer. A real install answers
// 422 with the first stable blocker code when anything blocks it, 409 when a
// concurrent install holds the installer, and the S1 statuses for path errors.
func (s *Server) installPack(w http.ResponseWriter, r *http.Request) {
	if s.Packs == nil {
		writePackInstallUnavailable(w, r)
		return
	}
	var body packInstallRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Source.Kind != agentpack.SourceKindPath {
		writeError(w, r, http.StatusBadRequest, agentpack.CodePackFormatUnsupported,
			"source.kind must be \"path\"; S2a installs only local path sources", false)
		return
	}
	home, err := s.hostHomeDir()
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	target, err := confineHostPath(home, body.Source.Path)
	if err != nil {
		writePackPathError(w, r, err)
		return
	}
	request := packinstall.Request{
		SourcePath:     target,
		ExpectedDigest: body.ExpectedDigest,
		DryRun:         body.DryRun,
	}
	if body.Renames != nil {
		request.Renames = packinstall.Renames{Flows: body.Renames.Flows, Roles: body.Renames.Roles}
	}
	plan, err := s.Packs.Install(r.Context(), request)
	if err != nil {
		writePackInstallError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, plan)
}

// listPacks reports every installed pack from its state file only.
func (s *Server) listPacks(w http.ResponseWriter, r *http.Request) {
	if s.Packs == nil {
		writePackInstallUnavailable(w, r)
		return
	}
	summaries, err := s.Packs.List(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if summaries == nil {
		summaries = []packinstall.StateSummary{}
	}
	writeData(w, http.StatusOK, summaries)
}

// removePack uninstalls one pack. Removal is a destructive operation already
// guarded by the installer (active Runs, locally edited objects, shared global
// Roles), so the API adds only the explicit --force override.
func (s *Server) removePack(w http.ResponseWriter, r *http.Request) {
	if s.Packs == nil {
		writePackInstallUnavailable(w, r)
		return
	}
	packID, ok := packIDFromRoute(r.PathValue("publisher"), r.PathValue("name"))
	if !ok {
		writeError(w, r, http.StatusBadRequest, agentpack.CodePackIdentityInvalid,
			"publisher and name must be single non-blank path segments", false)
		return
	}
	if err := s.Packs.Remove(r.Context(), packID, queryFlag(r, "force")); err != nil {
		writePackRemoveError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// packIDFromRoute rebuilds the "<publisher>/<name>" pack id from the two path
// segments, rejecting anything that could escape the state root.
func packIDFromRoute(publisher, name string) (string, bool) {
	if !validPackIDSegment(publisher) || !validPackIDSegment(name) {
		return "", false
	}
	return publisher + "/" + name, true
}

func validPackIDSegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 64 {
		return false
	}
	return !strings.ContainsAny(value, `/\`)
}

// queryFlag reads a boolean query parameter spelled ?x=1 / ?x=true / ?x=yes.
// An absent or negative value is false.
func queryFlag(r *http.Request, name string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func writePackInstallUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "pack_install_unavailable",
		"Agent Pack installation is unavailable on this Worker", true)
}

// writePackInstallError maps installer failures onto HTTP statuses. Busy and
// identity conflicts are 409 (the caller can retry after the other transaction
// or resolve the id); every other stable pack code is a non-retryable 422 that
// names the exact reason. Inspect/parse failures keep the S1 vocabulary.
func writePackInstallError(w http.ResponseWriter, r *http.Request, err error) {
	code := packinstall.Code(err)
	if code == "" {
		writePackInspectError(w, r, err)
		return
	}
	status := http.StatusUnprocessableEntity
	switch code {
	case packinstall.CodeInstallBusy, packinstall.CodeAlreadyInstalled, packinstall.CodeVersionConflict:
		status = http.StatusConflict
	case packinstall.CodeNotInstalled:
		status = http.StatusNotFound
	}
	writeError(w, r, status, code, err.Error(), false)
}

// writePackRemoveError maps removal failures: an unknown pack is 404, a refused
// removal (active Run, locally edited object, shared Role) is 409.
func writePackRemoveError(w http.ResponseWriter, r *http.Request, err error) {
	switch packinstall.Code(err) {
	case packinstall.CodeNotInstalled:
		writeError(w, r, http.StatusNotFound, packinstall.CodeNotInstalled, err.Error(), false)
	case packinstall.CodeInstallBlocked:
		writeError(w, r, http.StatusConflict, packinstall.CodeInstallBlocked, err.Error(), false)
	default:
		writeInternal(w, r, err)
	}
}
