// Package globalsource owns file-authored Role and Graph drafts under ENNOTE_HOME.
package globalsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
)

var objectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

var ErrConflict = errors.New("source digest conflict")

type Store struct {
	HomeDir string
}

type GraphEntry struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Path     string                `json:"path"`
	Digest   string                `json:"digest,omitempty"`
	Document *graphsource.Document `json:"document,omitempty"`
	Error    string                `json:"error,omitempty"`
}

type RoleEntry struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Path     string               `json:"path"`
	Digest   string               `json:"digest,omitempty"`
	Document *rolesource.Document `json:"document,omitempty"`
	Error    string               `json:"error,omitempty"`
}

func (s Store) AgentsDir() string { return filepath.Join(s.HomeDir, "agents") }
func (s Store) GraphsDir() string { return filepath.Join(s.AgentsDir(), "graphs") }
func (s Store) RolesDir() string  { return filepath.Join(s.AgentsDir(), "roles") }

func (s Store) ListGraphs() ([]GraphEntry, error) {
	return listObjectDirs(s.GraphsDir(), "graph.yaml", func(id, path string, data []byte) GraphEntry {
		entry := GraphEntry{ID: id, Name: id, Path: path}
		document, err := graphsource.Parse(data)
		if err != nil {
			entry.Error = err.Error()
			return entry
		}
		if document.ID != id {
			entry.Error = fmt.Sprintf("Graph id %q must match directory %q", document.ID, id)
			return entry
		}
		entry.Name = document.Name
		entry.Document = document
		entry.Digest, err = graphsource.SourceDigest(document)
		if err != nil {
			entry.Error = err.Error()
		}
		return entry
	})
}

func (s Store) ListRoles() ([]RoleEntry, error) {
	return listObjectDirs(s.RolesDir(), "role.md", func(id, path string, data []byte) RoleEntry {
		entry := RoleEntry{ID: id, Name: id, Path: path}
		document, err := rolesource.Parse(data)
		if err != nil {
			entry.Error = err.Error()
			return entry
		}
		if document.Handle != id {
			entry.Error = fmt.Sprintf("Role handle %q must match directory %q", document.Handle, id)
			return entry
		}
		entry.Name = document.Name
		entry.Document = document
		entry.Digest, err = rolesource.SourceDigest(document)
		if err != nil {
			entry.Error = err.Error()
		}
		return entry
	})
}

func listObjectDirs[T any](root, filename string, parse func(id, path string, data []byte) T) ([]T, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []T{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read source root: %w", err)
	}
	result := make([]T, 0, len(entries))
	for _, entry := range entries {
		id := entry.Name()
		if !objectIDPattern.MatchString(id) {
			continue
		}
		directory := filepath.Join(root, id)
		info, statErr := os.Lstat(directory)
		path := filepath.Join(directory, filename)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			result = append(result, parse(id, path, nil))
			continue
		}
		fileInfo, statErr := os.Lstat(path)
		if statErr != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
			result = append(result, parse(id, path, nil))
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			result = append(result, parse(id, path, nil))
			continue
		}
		result = append(result, parse(id, path, data))
	}
	return result, nil
}

func (s Store) ReadGraph(id string) (*graphsource.Document, string, error) {
	path, err := objectPath(s.GraphsDir(), id, "graph.yaml")
	if err != nil {
		return nil, "", err
	}
	data, err := readRegular(path)
	if err != nil {
		return nil, "", err
	}
	document, err := graphsource.Parse(data)
	if err != nil {
		return nil, "", err
	}
	if document.ID != id {
		return nil, "", fmt.Errorf("Graph id %q must match directory %q", document.ID, id)
	}
	digest, err := graphsource.SourceDigest(document)
	return document, digest, err
}

func (s Store) CreateGraph(id, name string) (*graphsource.Document, string, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	document := &graphsource.Document{SchemaVersion: 1, ID: id, Name: name, Tasks: map[string]graphsource.Task{}, Graph: map[string][]string{}}
	encoded, err := graphsource.Encode(document)
	if err != nil {
		return nil, "", err
	}
	path, err := createObjectDir(s.GraphsDir(), id, "graph.yaml")
	if err != nil {
		return nil, "", err
	}
	if err := atomicWrite(path, encoded, true); err != nil {
		_ = os.Remove(filepath.Dir(path))
		return nil, "", err
	}
	digest, err := graphsource.SourceDigest(document)
	return document, digest, err
}

func (s Store) UpdateGraph(id, expectedDigest string, mutate func(*graphsource.Document) error) (*graphsource.Document, string, error) {
	document, digest, err := s.ReadGraph(id)
	if err != nil {
		return nil, "", err
	}
	if expectedDigest == "" || expectedDigest != digest {
		return nil, digest, ErrConflict
	}
	if mutate == nil {
		return nil, digest, fmt.Errorf("Graph mutation is required")
	}
	if err := mutate(document); err != nil {
		return nil, digest, err
	}
	encoded, err := graphsource.Encode(document)
	if err != nil {
		return nil, digest, err
	}
	path, _ := objectPath(s.GraphsDir(), id, "graph.yaml")
	if err := atomicWrite(path, encoded, false); err != nil {
		return nil, digest, err
	}
	newDigest, err := graphsource.SourceDigest(document)
	return document, newDigest, err
}

func (s Store) GraphResourceDir(graphID, kind, resourceID string) (string, error) {
	if !objectIDPattern.MatchString(graphID) || !objectIDPattern.MatchString(resourceID) {
		return "", fmt.Errorf("invalid Graph resource id")
	}
	if kind != "roles" && kind != "skills" {
		return "", fmt.Errorf("unsupported Graph resource kind %q", kind)
	}
	graphDir := filepath.Join(s.GraphsDir(), graphID)
	for _, path := range []string{graphDir, filepath.Join(graphDir, kind), filepath.Join(graphDir, kind, resourceID)} {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("Graph resource directories must be regular directories")
		}
	}
	return filepath.Join(graphDir, kind, resourceID), nil
}

func (s Store) ReadGraphRole(graphID, roleID string) (*rolesource.Document, string, error) {
	directory, err := s.GraphResourceDir(graphID, "roles", roleID)
	if err != nil {
		return nil, "", err
	}
	data, err := readRegular(filepath.Join(directory, "role.md"))
	if err != nil {
		return nil, "", err
	}
	document, err := rolesource.Parse(data)
	if err != nil {
		return nil, "", err
	}
	if document.Handle != roleID {
		return nil, "", fmt.Errorf("Role handle %q must match directory %q", document.Handle, roleID)
	}
	digest, err := rolesource.SourceDigest(document)
	return document, digest, err
}

func (s Store) ReadRole(id string) (*rolesource.Document, string, error) {
	path, err := objectPath(s.RolesDir(), id, "role.md")
	if err != nil {
		return nil, "", err
	}
	data, err := readRegular(path)
	if err != nil {
		return nil, "", err
	}
	document, err := rolesource.Parse(data)
	if err != nil {
		return nil, "", err
	}
	if document.Handle != id {
		return nil, "", fmt.Errorf("Role handle %q must match directory %q", document.Handle, id)
	}
	digest, err := rolesource.SourceDigest(document)
	return document, digest, err
}

func (s Store) CreateRole(document *rolesource.Document) (*rolesource.Document, string, error) {
	encoded, err := rolesource.Encode(document)
	if err != nil {
		return nil, "", err
	}
	path, err := createObjectDir(s.RolesDir(), document.Handle, "role.md")
	if err != nil {
		return nil, "", err
	}
	if err := atomicWrite(path, encoded, true); err != nil {
		_ = os.Remove(filepath.Dir(path))
		return nil, "", err
	}
	digest, err := rolesource.SourceDigest(document)
	return document, digest, err
}

func (s Store) UpdateRole(id, expectedDigest string, mutate func(*rolesource.Document) error) (*rolesource.Document, string, error) {
	document, digest, err := s.ReadRole(id)
	if err != nil {
		return nil, "", err
	}
	if expectedDigest == "" || expectedDigest != digest {
		return nil, digest, ErrConflict
	}
	if mutate == nil {
		return nil, digest, fmt.Errorf("Role mutation is required")
	}
	if err := mutate(document); err != nil {
		return nil, digest, err
	}
	if document.Handle != id {
		return nil, digest, fmt.Errorf("Role handle cannot be changed")
	}
	encoded, err := rolesource.Encode(document)
	if err != nil {
		return nil, digest, err
	}
	path, _ := objectPath(s.RolesDir(), id, "role.md")
	if err := atomicWrite(path, encoded, false); err != nil {
		return nil, digest, err
	}
	newDigest, err := rolesource.SourceDigest(document)
	return document, newDigest, err
}

func objectPath(root, id, filename string) (string, error) {
	if !objectIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid object id %q", id)
	}
	return filepath.Join(root, id, filename), nil
}

func createObjectDir(root, id, filename string) (string, error) {
	path, err := objectPath(root, id, filename)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create source root: %w", err)
	}
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("object %q already exists", id)
		}
		return "", fmt.Errorf("create object directory: %w", err)
	}
	return path, nil
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source must be a regular file")
	}
	return os.ReadFile(path)
}

func atomicWrite(path string, data []byte, exclusive bool) error {
	if !exclusive {
		if _, err := readRegular(path); err != nil {
			return err
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ennote-source-*")
	if err != nil {
		return fmt.Errorf("create source temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if exclusive {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("source already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace source: %w", err)
	}
	// Persist the rename so a crash after write cannot lose the file.
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync source directory: %w", err)
	}
	return nil
}

func SortGraphEntries(entries []GraphEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
}

func SortRoleEntries(entries []RoleEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
}
