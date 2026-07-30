package api

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

const maxWorkspacePreviewBytes int64 = 50 << 20

type workspaceFileEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	IsDir      bool      `json:"isDir"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

func (s *Server) workspaceForProject(w http.ResponseWriter, r *http.Request) (*domain.ProjectWorkspace, *workspace.Jail, bool) {
	record, err := s.Projects.FindWorkspaceByProjectID(r.Context(), r.PathValue("projectID"))
	if err != nil {
		writeInternal(w, r, err)
		return nil, nil, false
	}
	if record == nil {
		writeError(w, r, http.StatusNotFound, "workspace_not_found", "project workspace not found", false)
		return nil, nil, false
	}
	jail, err := workspace.NewJail(record.HostPath)
	if err != nil {
		writeError(w, r, http.StatusConflict, "workspace_unavailable", err.Error(), true)
		return nil, nil, false
	}
	return record, jail, true
}

func (s *Server) getProjectWorkspace(w http.ResponseWriter, r *http.Request) {
	record, _, ok := s.workspaceForProject(w, r)
	if !ok {
		return
	}
	writeData(w, http.StatusOK, record)
}

func (s *Server) listProjectFiles(w http.ResponseWriter, r *http.Request) {
	_, jail, ok := s.workspaceForProject(w, r)
	if !ok {
		return
	}
	requestedPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if requestedPath == "" {
		requestedPath = "/workspace"
	}
	hostPath, err := jail.ResolveExisting(requestedPath)
	if err != nil {
		writeWorkspacePathError(w, r, err)
		return
	}
	info, err := os.Stat(hostPath)
	if err != nil {
		writeWorkspacePathError(w, r, err)
		return
	}
	if !info.IsDir() {
		writeError(w, r, http.StatusBadRequest, "not_a_directory", "path is not a directory", false)
		return
	}
	children, err := os.ReadDir(hostPath)
	if err != nil {
		writeWorkspacePathError(w, r, err)
		return
	}
	entries := make([]workspaceFileEntry, 0, len(children))
	for _, child := range children {
		childInfo, infoErr := child.Info()
		if infoErr != nil {
			continue
		}
		virtualPath, displayErr := jail.DisplayPath(filepath.Join(hostPath, child.Name()))
		if displayErr != nil {
			continue
		}
		entries = append(entries, workspaceFileEntry{
			Name: child.Name(), Path: virtualPath, IsDir: child.IsDir(),
			Size: childInfo.Size(), ModifiedAt: childInfo.ModTime().UTC(),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	writeData(w, http.StatusOK, entries)
}

func (s *Server) readProjectFile(w http.ResponseWriter, r *http.Request) {
	_, jail, ok := s.workspaceForProject(w, r)
	if !ok {
		return
	}
	requestedPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if requestedPath == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_file_path", "path is required", false)
		return
	}
	hostPath, err := jail.ResolveExisting(requestedPath)
	if err != nil {
		writeWorkspacePathError(w, r, err)
		return
	}
	file, err := os.Open(hostPath)
	if err != nil {
		writeWorkspacePathError(w, r, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeWorkspacePathError(w, r, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, r, http.StatusBadRequest, "not_a_regular_file", "path is not a regular file", false)
		return
	}
	if info.Size() > maxWorkspacePreviewBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the 50 MiB preview limit", false)
		return
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Name())))
	if contentType == "" {
		buffer := make([]byte, 512)
		count, _ := file.Read(buffer)
		contentType = http.DetectContentType(buffer[:count])
		_, _ = file.Seek(0, 0)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": info.Name()}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func writeWorkspacePathError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, workspace.ErrPathEscape) {
		writeError(w, r, http.StatusBadRequest, "workspace_path_escape", "path must remain inside /workspace", false)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "workspace_path_not_found", "workspace path not found", false)
		return
	}
	writeError(w, r, http.StatusBadRequest, "invalid_workspace_path", err.Error(), false)
}
