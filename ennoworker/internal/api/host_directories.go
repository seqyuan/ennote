package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const hostDirectoryListingLimit = 1000

type hostDirectoryEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Hidden bool   `json:"hidden"`
}

type hostDirectoryListing struct {
	Path      string               `json:"path"`
	Home      string               `json:"home"`
	Crumbs    []hostDirectoryEntry `json:"crumbs"`
	Entries   []hostDirectoryEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
}

func (s *Server) listHostDirectories(w http.ResponseWriter, r *http.Request) {
	home, err := os.UserHomeDir()
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("path"))
	if target == "" {
		target = home
	}
	if !filepath.IsAbs(target) {
		writeError(w, r, http.StatusBadRequest, "invalid_host_directory", "path must be fully qualified", false)
		return
	}
	target = filepath.Clean(target)
	entries, err := os.ReadDir(target)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "host_directory_unreadable", err.Error(), false)
		return
	}

	directories := make([]hostDirectoryEntry, 0, min(len(entries), hostDirectoryListingLimit))
	truncated := false
	for _, entry := range entries {
		isDirectory := entry.IsDir()
		if !isDirectory && entry.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(filepath.Join(target, entry.Name()))
			isDirectory = statErr == nil && info.IsDir()
		}
		if !isDirectory {
			continue
		}
		if len(directories) == hostDirectoryListingLimit {
			truncated = true
			break
		}
		directories = append(directories, hostDirectoryEntry{
			Name: entry.Name(), Path: filepath.Join(target, entry.Name()), Hidden: strings.HasPrefix(entry.Name(), "."),
		})
	}
	sort.SliceStable(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})

	writeData(w, http.StatusOK, hostDirectoryListing{
		Path: target, Home: filepath.Clean(home), Crumbs: hostDirectoryCrumbs(target), Entries: directories, Truncated: truncated,
	})
}

func (s *Server) createHostDirectory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	parent := filepath.Clean(input.Path)
	name := input.Name
	if !filepath.IsAbs(parent) {
		writeError(w, r, http.StatusBadRequest, "invalid_host_directory", "path must be fully qualified", false)
		return
	}
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		writeError(w, r, http.StatusBadRequest, "invalid_host_directory_name", "name must be one non-blank path segment", false)
		return
	}
	target := filepath.Join(parent, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeError(w, r, http.StatusConflict, "host_directory_exists", target+" already exists", false)
			return
		}
		writeError(w, r, http.StatusBadRequest, "host_directory_create_failed", err.Error(), false)
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"path": target})
}

func hostDirectoryCrumbs(target string) []hostDirectoryEntry {
	current := filepath.Clean(target)
	crumbs := make([]hostDirectoryEntry, 0, 8)
	for {
		parent := filepath.Dir(current)
		name := filepath.Base(current)
		if parent == current {
			name = current
		}
		crumbs = append(crumbs, hostDirectoryEntry{Name: name, Path: current, Hidden: false})
		if parent == current {
			break
		}
		current = parent
	}
	for left, right := 0, len(crumbs)-1; left < right; left, right = left+1, right-1 {
		crumbs[left], crumbs[right] = crumbs[right], crumbs[left]
	}
	return crumbs
}
