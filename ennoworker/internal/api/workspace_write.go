package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

// Writing the workspace is the one place the Worker changes user files without a
// tool call behind it, so every rule here is a refusal first:
//
//   - only inside the project's /workspace mount (read-only mounts refuse)
//   - the parent must already exist (a typo must not create a directory chain)
//   - an existing target is a conflict, never an overwrite
//   - the mount root itself is never a target
//
// The jail decides containment (ResolveForWrite / ResolveWorkspaceExisting); this
// file decides everything else.

// workspacePathPrefix is the only mount these handlers write into.
const workspacePathPrefix = "/workspace"

// workspaceJSONBodyLimit bounds a create/move body. The content cap is enforced
// on the decoded field; this one only keeps a hostile body from being buffered.
const workspaceJSONBodyLimit int64 = maxWorkspacePreviewBytes + (1 << 20)

// decodeWorkspaceCreate reads the create body, separating "too large" from
// "malformed": the two need different answers and only one of them is the
// caller's typo.
func decodeWorkspaceCreate(w http.ResponseWriter, r *http.Request) (workspaceFileCreateRequest, bool) {
	var request workspaceFileCreateRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, workspaceJSONBodyLimit))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request_body", "request body could not be read", false)
		return request, false
	}
	if int64(len(body)) >= workspaceJSONBodyLimit {
		writeError(w, r, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the 50 MiB preview limit", false)
		return request, false
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request_body", "request body must be a JSON object", false)
		return request, false
	}
	return request, true
}

type workspaceFileCreateRequest struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

type workspaceFileMoveRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// writableTarget resolves a path for writing and reports the virtual path it
// names, refusing the mount root and anything the jail keeps read-only.
func writableTarget(w http.ResponseWriter, r *http.Request, jail *workspace.Jail, requested string) (string, string, bool) {
	virtual := strings.TrimSpace(requested)
	if virtual == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_file_path", "path is required", false)
		return "", "", false
	}
	if strings.TrimRight(virtual, "/") == workspacePathPrefix {
		writeError(w, r, http.StatusBadRequest, "workspace_root_immutable", "the workspace root cannot be created, moved or deleted", false)
		return "", "", false
	}
	hostPath, err := jail.ResolveForWrite(virtual)
	if err != nil {
		writeWorkspaceWriteError(w, r, err)
		return "", "", false
	}
	return hostPath, virtual, true
}

// existingTarget resolves a path that must already exist and be writable.
func existingTarget(w http.ResponseWriter, r *http.Request, jail *workspace.Jail, requested string) (string, string, bool) {
	virtual := strings.TrimSpace(requested)
	if virtual == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_file_path", "path is required", false)
		return "", "", false
	}
	if strings.TrimRight(virtual, "/") == workspacePathPrefix {
		writeError(w, r, http.StatusBadRequest, "workspace_root_immutable", "the workspace root cannot be created, moved or deleted", false)
		return "", "", false
	}
	hostPath, err := jail.ResolveWorkspaceExisting(virtual)
	if err != nil {
		writeWorkspaceWriteError(w, r, err)
		return "", "", false
	}
	return hostPath, virtual, true
}

func (s *Server) createProjectFileEntry(w http.ResponseWriter, r *http.Request) {
	_, jail, ok := s.workspaceForProject(w, r)
	if !ok {
		return
	}
	request, ok := decodeWorkspaceCreate(w, r)
	if !ok {
		return
	}
	if request.Kind != "file" && request.Kind != "directory" {
		writeError(w, r, http.StatusBadRequest, "invalid_entry_kind", `kind must be "file" or "directory"`, false)
		return
	}
	hostPath, virtual, ok := writableTarget(w, r, jail, request.Path)
	if !ok {
		return
	}
	if _, statErr := os.Lstat(hostPath); statErr == nil {
		writeError(w, r, http.StatusConflict, "workspace_path_exists", "an entry already exists at that path", false)
		return
	} else if !errors.Is(statErr, os.ErrNotExist) {
		writeWorkspaceWriteError(w, r, statErr)
		return
	}
	parent := filepath.Dir(hostPath)
	if info, statErr := os.Stat(parent); statErr != nil || !info.IsDir() {
		writeError(w, r, http.StatusNotFound, "parent_not_found", "the parent directory does not exist", false)
		return
	}

	if request.Kind == "directory" {
		if err := os.Mkdir(hostPath, 0o755); err != nil {
			writeWorkspaceWriteError(w, r, err)
			return
		}
	} else {
		if int64(len(request.Content)) > maxWorkspacePreviewBytes {
			writeError(w, r, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the 50 MiB preview limit", false)
			return
		}
		// O_EXCL: the Lstat above is a race, and losing it must not overwrite.
		file, err := os.OpenFile(hostPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			writeWorkspaceWriteError(w, r, err)
			return
		}
		_, writeErr := io.WriteString(file, request.Content)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(hostPath)
			writeWorkspaceWriteError(w, r, errors.Join(writeErr, closeErr))
			return
		}
	}

	entry, ok := s.readWorkspaceEntry(w, r, jail, virtual)
	if !ok {
		return
	}
	writeData(w, http.StatusCreated, entry)
}

func (s *Server) moveProjectFileEntry(w http.ResponseWriter, r *http.Request) {
	_, jail, ok := s.workspaceForProject(w, r)
	if !ok {
		return
	}
	var request workspaceFileMoveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, workspaceJSONBodyLimit)).Decode(&request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request_body", "request body must be a JSON object", false)
		return
	}
	fromHost, _, ok := existingTarget(w, r, jail, request.From)
	if !ok {
		return
	}
	toHost, toVirtual, ok := writableTarget(w, r, jail, request.To)
	if !ok {
		return
	}
	if fromHost == toHost {
		writeError(w, r, http.StatusBadRequest, "invalid_move_target", "the destination is the source", false)
		return
	}
	// A directory cannot become its own descendant: the move would delete the
	// source as it goes.
	if strings.HasPrefix(toHost, fromHost+string(os.PathSeparator)) {
		writeError(w, r, http.StatusConflict, "invalid_move_target", "a directory cannot be moved inside itself", false)
		return
	}
	if info, statErr := os.Stat(filepath.Dir(toHost)); statErr != nil || !info.IsDir() {
		writeError(w, r, http.StatusNotFound, "parent_not_found", "the destination directory does not exist", false)
		return
	}
	if _, statErr := os.Lstat(toHost); statErr == nil {
		writeError(w, r, http.StatusConflict, "workspace_path_exists", "an entry already exists at the destination", false)
		return
	} else if !errors.Is(statErr, os.ErrNotExist) {
		writeWorkspaceWriteError(w, r, statErr)
		return
	}
	// Rename refuses to replace, on every platform: the check above is the
	// portable guarantee, os.Rename's behaviour differs.
	if err := os.Rename(fromHost, toHost); err != nil {
		writeWorkspaceWriteError(w, r, err)
		return
	}

	entry, ok := s.readWorkspaceEntry(w, r, jail, toVirtual)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, entry)
}

func (s *Server) deleteProjectFileEntry(w http.ResponseWriter, r *http.Request) {
	_, jail, ok := s.workspaceForProject(w, r)
	if !ok {
		return
	}
	hostPath, _, ok := existingTarget(w, r, jail, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	recursive := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("recursive")), "true")
	info, err := os.Lstat(hostPath)
	if err != nil {
		writeWorkspaceWriteError(w, r, err)
		return
	}
	if info.IsDir() && !recursive {
		children, readErr := os.ReadDir(hostPath)
		if readErr != nil {
			writeWorkspaceWriteError(w, r, readErr)
			return
		}
		if len(children) > 0 {
			writeError(w, r, http.StatusConflict, "directory_not_empty", "the directory is not empty; pass recursive=true to delete it with its contents", false)
			return
		}
	}
	if recursive {
		err = os.RemoveAll(hostPath)
	} else {
		err = os.Remove(hostPath)
	}
	if err != nil {
		writeWorkspaceWriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readWorkspaceEntry answers with the entry at a virtual path, in the same shape
// a directory listing uses, so a create or a move returns what the tree draws.
func (s *Server) readWorkspaceEntry(w http.ResponseWriter, r *http.Request, jail *workspace.Jail, virtual string) (workspaceFileEntry, bool) {
	hostPath, err := jail.ResolveExisting(virtual)
	if err != nil {
		writeWorkspaceWriteError(w, r, err)
		return workspaceFileEntry{}, false
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		writeWorkspaceWriteError(w, r, err)
		return workspaceFileEntry{}, false
	}
	displayPath, err := jail.DisplayPath(hostPath)
	if err != nil {
		writeWorkspaceWriteError(w, r, err)
		return workspaceFileEntry{}, false
	}
	return workspaceFileEntry{
		Name:       path.Base(displayPath),
		Path:       displayPath,
		IsDir:      info.IsDir(),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().UTC(),
	}, true
}

func writeWorkspaceWriteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, workspace.ErrReadOnlyMount):
		writeError(w, r, http.StatusForbidden, "workspace_read_only", "that path is on a read-only mount", false)
	case errors.Is(err, workspace.ErrPathEscape):
		writeError(w, r, http.StatusBadRequest, "workspace_path_escape", "path must remain inside /workspace", false)
	case errors.Is(err, os.ErrNotExist):
		writeError(w, r, http.StatusNotFound, "workspace_path_not_found", "workspace path not found", false)
	case errors.Is(err, os.ErrPermission):
		writeError(w, r, http.StatusForbidden, "workspace_permission_denied", "the worker cannot write that path", false)
	case errors.Is(err, os.ErrExist):
		writeError(w, r, http.StatusConflict, "workspace_path_exists", "an entry already exists at that path", false)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_workspace_path", err.Error(), false)
	}
}
