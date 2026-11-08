package api

import (
	"errors"
	"fmt"
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
	Path string `json:"path"`
	// Home is the primary root: the directory the picker lands on when no path is
	// requested. Roots is the full allowed set, Home first, so a client can offer
	// the configured extra roots as jumps instead of making the user type them.
	Home      string               `json:"home"`
	Roots     []string             `json:"roots"`
	Crumbs    []hostDirectoryEntry `json:"crumbs"`
	Entries   []hostDirectoryEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
}

func (s *Server) listHostDirectories(w http.ResponseWriter, r *http.Request) {
	roots, err := s.hostRoots()
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("path"))
	if target == "" {
		target = roots[0]
	}
	confined, err := confineHostPaths(roots, target)
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
		if _, err := confineHostPaths(roots, child); err != nil {
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
		Path: confined, Home: roots[0], Roots: roots,
		Crumbs: hostDirectoryCrumbs(roots, confined), Entries: directories, Truncated: truncated,
	})
}

func (s *Server) createHostDirectory(w http.ResponseWriter, r *http.Request) {
	roots, err := s.hostRoots()
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
	parent, err := confineHostPaths(roots, input.Path)
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
	if _, err := confineHostPaths(roots, target); err != nil {
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

// hostRoots resolves the roots the host directory picker may browse: the primary
// root (HostHome, or the process user's home when unset) followed by every
// configured extra root. Roots nested inside an earlier root are dropped, so the
// first entry is always the landing directory and a path has exactly one crumb
// stop. A root that does not exist yet is kept in its canonical-parent form rather
// than refused: it can be a mount that appears after the Worker started.
func (s *Server) hostRoots() ([]string, error) {
	home := strings.TrimSpace(s.HostHome)
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = userHome
	}
	primary, err := canonicalHostRoot(home)
	if err != nil {
		return nil, err
	}
	roots := []string{primary}
	for _, extra := range s.HostRoots {
		extra = strings.TrimSpace(extra)
		if extra == "" {
			continue
		}
		canonical, err := canonicalHostRoot(extra)
		if err != nil {
			return nil, fmt.Errorf("resolve host root %q: %w", extra, err)
		}
		if pathInsideAny(roots, canonical) {
			continue
		}
		roots = append(roots, canonical)
	}
	return roots, nil
}

// canonicalHostRoot resolves a root to an absolute, symlink-free directory. A
// missing directory is canonicalized through its nearest existing parent so the
// confinement check can still be evaluated for a path that has not been created.
func canonicalHostRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
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

// confineHostPaths resolves target against the allowed roots. It requires an
// absolute path inside at least one root both before and after symlink resolution,
// which is what stops `<root>/link -> /etc` from escaping the confinement.
func confineHostPaths(roots []string, target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", errHostDirectoryRelative
	}
	if !filepath.IsAbs(target) {
		return "", errHostDirectoryRelative
	}
	cleaned := filepath.Clean(target)
	if !pathInsideAny(roots, cleaned) {
		return "", errHostDirectoryForbidden
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			parent, parentErr := filepath.EvalSymlinks(filepath.Dir(cleaned))
			if parentErr != nil {
				if !pathInsideAny(roots, filepath.Dir(cleaned)) {
					return "", errHostDirectoryForbidden
				}
				return cleaned, nil
			}
			joined := filepath.Join(parent, filepath.Base(cleaned))
			if !pathInsideAny(roots, joined) {
				return "", errHostDirectoryForbidden
			}
			return joined, nil
		}
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if !pathInsideAny(roots, resolved) {
		return "", errHostDirectoryForbidden
	}
	return resolved, nil
}

func pathInsideAny(roots []string, path string) bool {
	for _, root := range roots {
		if pathInside(root, path) {
			return true
		}
	}
	return false
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// expandHomePath turns a leading "~" or "~/" into the process user's home so a
// shell-style path is confined as the directory it actually names.
func expandHomePath(path string) string {
	path = strings.TrimSpace(path)
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

var (
	errHostDirectoryRelative  = errors.New("path must be fully qualified")
	errHostDirectoryForbidden = errors.New("path must be inside the home directory or a configured host root")
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

// hostDirectoryCrumbs walks from target up to the root that contains it. With
// several roots the stop is the containing root, not necessarily Home: a path
// under an extra root never produces crumbs above that root.
func hostDirectoryCrumbs(roots []string, target string) []hostDirectoryEntry {
	current := filepath.Clean(target)
	root := current
	for _, candidate := range roots {
		if pathInside(candidate, current) {
			root = candidate
			break
		}
	}
	crumbs := make([]hostDirectoryEntry, 0, 8)
	for {
		name := filepath.Base(current)
		if current == root {
			name = root
		}
		crumbs = append(crumbs, hostDirectoryEntry{Name: name, Path: current, Hidden: false})
		if current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current || !pathInside(root, parent) {
			break
		}
		current = parent
	}
	for left, right := 0, len(crumbs)-1; left < right; left, right = left+1, right-1 {
		crumbs[left], crumbs[right] = crumbs[right], crumbs[left]
	}
	return crumbs
}
