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
	home, err := s.hostHomeDir()
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("path"))
	if target == "" {
		target = home
	}
	confined, err := confineHostPath(home, target)
	if err != nil {
		writeHostDirectoryError(w, r, err)
		return
	}
	entries, err := os.ReadDir(confined)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "host_directory_unreadable", err.Error(), false)
		return
	}

	directories := make([]hostDirectoryEntry, 0, min(len(entries), hostDirectoryListingLimit))
	truncated := false
	for _, entry := range entries {
		isDirectory := entry.IsDir()
		child := filepath.Join(confined, entry.Name())
		if !isDirectory && entry.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(child)
			isDirectory = statErr == nil && info.IsDir()
		}
		if !isDirectory {
			continue
		}
		if _, err := confineHostPath(home, child); err != nil {
			continue
		}
		if len(directories) == hostDirectoryListingLimit {
			truncated = true
			break
		}
		directories = append(directories, hostDirectoryEntry{
			Name: entry.Name(), Path: child, Hidden: strings.HasPrefix(entry.Name(), "."),
		})
	}
	sort.SliceStable(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})

	writeData(w, http.StatusOK, hostDirectoryListing{
		Path: confined, Home: home, Crumbs: hostDirectoryCrumbs(home, confined), Entries: directories, Truncated: truncated,
	})
}

func (s *Server) createHostDirectory(w http.ResponseWriter, r *http.Request) {
	home, err := s.hostHomeDir()
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	var input struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	parent, err := confineHostPath(home, input.Path)
	if err != nil {
		writeHostDirectoryError(w, r, err)
		return
	}
	name := input.Name
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		writeError(w, r, http.StatusBadRequest, "invalid_host_directory_name", "name must be one non-blank path segment", false)
		return
	}
	target := filepath.Join(parent, name)
	if _, err := confineHostPath(home, target); err != nil {
		writeHostDirectoryError(w, r, err)
		return
	}
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

func (s *Server) hostHomeDir() (string, error) {
	home := strings.TrimSpace(s.HostHome)
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = userHome
	}
	absolute, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		canonical = filepath.Clean(absolute)
	}
	return filepath.Clean(canonical), nil
}

func confineHostPath(home, target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", errHostDirectoryRelative
	}
	if !filepath.IsAbs(target) {
		return "", errHostDirectoryRelative
	}
	cleaned := filepath.Clean(target)
	if !pathInsideHome(home, cleaned) {
		return "", errHostDirectoryForbidden
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			parent, parentErr := filepath.EvalSymlinks(filepath.Dir(cleaned))
			if parentErr != nil {
				if !pathInsideHome(home, filepath.Dir(cleaned)) {
					return "", errHostDirectoryForbidden
				}
				return cleaned, nil
			}
			joined := filepath.Join(parent, filepath.Base(cleaned))
			if !pathInsideHome(home, joined) {
				return "", errHostDirectoryForbidden
			}
			return joined, nil
		}
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if !pathInsideHome(home, resolved) {
		return "", errHostDirectoryForbidden
	}
	return resolved, nil
}

func pathInsideHome(home, path string) bool {
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

var (
	errHostDirectoryRelative  = errors.New("path must be fully qualified")
	errHostDirectoryForbidden = errors.New("path must be inside the home directory")
)

func writeHostDirectoryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errHostDirectoryRelative) {
		writeError(w, r, http.StatusBadRequest, "invalid_host_directory", err.Error(), false)
		return
	}
	if errors.Is(err, errHostDirectoryForbidden) {
		writeError(w, r, http.StatusForbidden, "host_directory_forbidden", err.Error(), false)
		return
	}
	writeError(w, r, http.StatusBadRequest, "host_directory_unreadable", err.Error(), false)
}

func hostDirectoryCrumbs(home, target string) []hostDirectoryEntry {
	current := filepath.Clean(target)
	home = filepath.Clean(home)
	crumbs := make([]hostDirectoryEntry, 0, 8)
	for {
		name := filepath.Base(current)
		if current == home {
			name = home
		}
		crumbs = append(crumbs, hostDirectoryEntry{Name: name, Path: current, Hidden: false})
		if current == home {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || !pathInsideHome(home, parent) {
			break
		}
		current = parent
	}
	for left, right := 0, len(crumbs)-1; left < right; left, right = left+1, right-1 {
		crumbs[left], crumbs[right] = crumbs[right], crumbs[left]
	}
	return crumbs
}
