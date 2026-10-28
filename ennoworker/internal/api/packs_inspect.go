package api

import (
	"errors"
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/agentpack"
)

// packInspectSource is the path-only pack source. S1 deliberately has exactly
// one source kind; URL/git sources arrive in S6.
type packInspectSource struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type packInspectRequest struct {
	Source           packInspectSource `json:"source"`
	IncludeDocuments bool              `json:"includeDocuments"`
	ExpectedDigest   string            `json:"expectedDigest"`
}

// inspectPack reports a pack's structure without installing, executing, or
// trusting it.
//
// The handler depends only on HostHome: it never opens a store, database, model
// catalog, or network connection, so it works on a Worker whose stores are nil
// and cannot mutate Worker state.
func (s *Server) inspectPack(w http.ResponseWriter, r *http.Request) {
	var body packInspectRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Source.Kind != agentpack.SourceKindPath {
		writeError(w, r, http.StatusBadRequest, agentpack.CodePackFormatUnsupported,
			"source.kind must be \"path\"; S1 does not inspect URL or git sources", false)
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
	result, err := agentpack.Inspect(agentpack.Request{
		Source:           agentpack.Source{Kind: agentpack.SourceKindPath, Path: target},
		IncludeDocuments: body.IncludeDocuments,
		ExpectedDigest:   body.ExpectedDigest,
	})
	if err != nil {
		writePackInspectError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, result)
}

// writePackPathError maps the host-path confinement failures onto the pack
// error vocabulary: a relative path is a bad request, an escape is forbidden.
func writePackPathError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errHostDirectoryRelative):
		writeError(w, r, http.StatusBadRequest, agentpack.CodePackPathInvalid,
			"source.path must be fully qualified", false)
	case errors.Is(err, errHostDirectoryForbidden):
		writeError(w, r, http.StatusForbidden, agentpack.CodePackPathForbidden,
			"source.path must be inside the home directory", false)
	default:
		writeInternal(w, r, err)
	}
}

// writePackInspectError maps inspect failures onto HTTP statuses. No pack error
// is retryable: the bytes or the manifest must change first.
func writePackInspectError(w http.ResponseWriter, r *http.Request, err error) {
	var packErr *agentpack.Error
	if !errors.As(err, &packErr) {
		writeInternal(w, r, err)
		return
	}
	status := http.StatusUnprocessableEntity
	switch packErr.Code {
	case agentpack.CodePackNotFound:
		status = http.StatusNotFound
	case agentpack.CodePackPathForbidden:
		status = http.StatusForbidden
	case agentpack.CodePackFormatUnsupported:
		status = http.StatusBadRequest
	case agentpack.CodePackTooLarge, agentpack.CodePackTooManyFiles:
		status = http.StatusRequestEntityTooLarge
	}
	writeError(w, r, status, packErr.Code, packErr.Error(), false)
}
