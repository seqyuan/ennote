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

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
)

var objectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

var ErrConflict = errors.New("source digest conflict")

// Source kinds share one agent object directory. Exactly one of graph.yaml and
// flow.yaml exists below an object; revisions record the kind so a graph
// revision can never be read as a flow revision (or vice versa).
const (
	SourceKindGraph = "graph"
	SourceKindFlow  = "flow"
)

const (
	graphSourceName = "graph.yaml"
	flowSourceName  = "flow.yaml"
)

type Store struct {
	HomeDir string
}

type GraphEntry struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	SourceKind string                 `json:"sourceKind"`
	Path       string                 `json:"path"`
	Digest     string                 `json:"digest,omitempty"`
	Document   *graphsource.Document  `json:"document,omitempty"`
	Flow       *domain.FlowDefinition `json:"flow,omitempty"`
	Error      string                 `json:"error,omitempty"`
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

// ListGraphs returns graph-source objects only. Flow objects share the same
// parent directory but are reported by ListFlows, so the live Graph UI is never
// handed a document it cannot render.
func (s Store) ListGraphs() ([]GraphEntry, error) {
	return listObjectDirs(s.GraphsDir(), graphSourceName, func(id string) bool {
		return s.hasOnlySource(id, graphSourceName, flowSourceName)
	}, func(id, path string, data []byte) GraphEntry {
		entry := GraphEntry{ID: id, Name: id, SourceKind: SourceKindGraph, Path: path}
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

// ListFlows returns flow-source objects only. The FlowDefinition is the same
// canonical document run/publish consume, not a graph projection.
func (s Store) ListFlows() ([]GraphEntry, error) {
	return listObjectDirs(s.GraphsDir(), flowSourceName, func(id string) bool {
		return s.hasOnlySource(id, flowSourceName, graphSourceName)
	}, func(id, path string, data []byte) GraphEntry {
		entry := GraphEntry{ID: id, Name: id, SourceKind: SourceKindFlow, Path: path}
		definition, err := agentflow.ParseDefinition(data)
		if err != nil {
			entry.Error = err.Error()
			return entry
		}
		if definition.ID != id {
			entry.Error = fmt.Sprintf("Flow id %q must match directory %q", definition.ID, id)
			return entry
		}
		entry.Name = definition.ID
		entry.Flow = definition
		entry.Digest, err = AgentFlowDigest(definition)
		if err != nil {
			entry.Error = err.Error()
		}
		return entry
	})
}

// hasOnlySource reports whether an object directory contains the other source
// kind but not filename, so each catalog skips objects owned by the other kind.
// A directory holding both is included in both lists, where SourceKind fails
// closed instead of silently preferring one document.
func (s Store) hasOnlySource(id, filename, other string) bool {
	self, selfErr := regularFileExists(filepath.Join(s.GraphsDir(), id, filename))
	otherExists, otherErr := regularFileExists(filepath.Join(s.GraphsDir(), id, other))
	if selfErr != nil || otherErr != nil {
		return false
	}
	return otherExists && !self
}

func (s Store) ListRoles() ([]RoleEntry, error) {
	return listObjectDirs(s.RolesDir(), "role.md", nil, func(id, path string, data []byte) RoleEntry {
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

func listObjectDirs[T any](root, filename string, skip func(id string) bool, parse func(id, path string, data []byte) T) ([]T, error) {
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
		if skip != nil && skip(id) {
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

// SourceKind reports the kind of the object directory and fails closed when an
// object contains both source files.
func (s Store) SourceKind(id string) (string, error) {
	if !objectIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid object id %q", id)
	}
	directory := filepath.Join(s.GraphsDir(), id)
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return "", os.ErrNotExist
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("source object %q must be a regular directory", id)
	}
	graphExists, err := regularFileExists(filepath.Join(directory, graphSourceName))
	if err != nil {
		return "", err
	}
	flowExists, err := regularFileExists(filepath.Join(directory, flowSourceName))
	if err != nil {
		return "", err
	}
	switch {
	case graphExists && flowExists:
		return "", fmt.Errorf("source object %q contains both %s and %s", id, graphSourceName, flowSourceName)
	case flowExists:
		return SourceKindFlow, nil
	case graphExists:
		return SourceKindGraph, nil
	default:
		return "", os.ErrNotExist
	}
}

// ReadSource returns the document for either source kind plus its canonical
// digest. Exactly one of graphDocument/flowDefinition is non-nil.
func (s Store) ReadSource(id string) (string, *graphsource.Document, *domain.FlowDefinition, string, error) {
	kind, err := s.SourceKind(id)
	if err != nil {
		return "", nil, nil, "", err
	}
	switch kind {
	case SourceKindGraph:
		document, digest, err := s.ReadGraph(id)
		return kind, document, nil, digest, err
	case SourceKindFlow:
		definition, digest, err := s.ReadFlow(id)
		return kind, nil, definition, digest, err
	default:
		return "", nil, nil, "", fmt.Errorf("unsupported source kind %q", kind)
	}
}

// AgentFlowDigest is the source digest of a FlowDefinition with the same
// sha256: prefix as every other file-native source digest. It is the value a
// flow revision stores and the value run freeze uses as ConfigDigest.
func AgentFlowDigest(definition *domain.FlowDefinition) (string, error) {
	digest, err := agentflow.ConfigDigest(definition)
	if err != nil {
		return "", err
	}
	return "sha256:" + digest, nil
}

func (s Store) ReadGraph(id string) (*graphsource.Document, string, error) {
	kind, err := s.SourceKind(id)
	if err != nil {
		return nil, "", err
	}
	if kind != SourceKindGraph {
		return nil, "", fmt.Errorf("source %q is a flow, not a Graph", id)
	}
	path, err := objectPath(s.GraphsDir(), id, graphSourceName)
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

// ReadFlow reads the editable FlowDefinition draft at
// agents/graphs/<id>/flow.yaml. It never accepts a graph source.
func (s Store) ReadFlow(id string) (*domain.FlowDefinition, string, error) {
	kind, err := s.SourceKind(id)
	if err != nil {
		return nil, "", err
	}
	if kind != SourceKindFlow {
		return nil, "", fmt.Errorf("source %q is a Graph, not a flow", id)
	}
	path, err := objectPath(s.GraphsDir(), id, flowSourceName)
	if err != nil {
		return nil, "", err
	}
	data, err := readRegular(path)
	if err != nil {
		return nil, "", err
	}
	definition, err := agentflow.ParseDefinition(data)
	if err != nil {
		return nil, "", err
	}
	if definition.ID != id {
		return nil, "", fmt.Errorf("Flow id %q must match directory %q", definition.ID, id)
	}
	digest, err := AgentFlowDigest(definition)
	return definition, digest, err
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

// CreateGraphFromDocument publishes a fully authored Graph source document as a
// new Graph draft. It is the whole-document analogue of CreateGraph and is used
// by the Agent Pack installer so a pack Flow becomes an ordinary file-native
// Graph. The document id is the directory name; an existing Graph is never
// overwritten (the caller inspects first and decides create vs. identical).
func (s Store) CreateGraphFromDocument(document *graphsource.Document) (string, error) {
	if document == nil {
		return "", fmt.Errorf("Graph source document is required")
	}
	encoded, err := graphsource.Encode(document)
	if err != nil {
		return "", err
	}
	// The Graph directory may already exist (an installer writes Graph-private
	// skills and roles first), so only graph.yaml itself must be new.
	graphDir, err := ensureSourceDir(s.GraphsDir(), document.ID)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(graphDir, "graph.yaml"), encoded, true); err != nil {
		return "", err
	}
	return graphsource.SourceDigest(document)
}

// CreateFlowFromDefinition publishes a fully authored Agent Flow draft as
// agents/graphs/<id>/flow.yaml. It never overwrites an existing Graph or Flow,
// and it never publishes a revision (installs stay disabled by default).
func (s Store) CreateFlowFromDefinition(definition *domain.FlowDefinition) (string, error) {
	if definition == nil {
		return "", fmt.Errorf("flow definition is required")
	}
	definition.ID = strings.TrimSpace(definition.ID)
	if !objectIDPattern.MatchString(definition.ID) {
		return "", fmt.Errorf("invalid flow id %q", definition.ID)
	}
	if _, err := s.SourceKind(definition.ID); err == nil {
		return "", fmt.Errorf("source object %q already exists", definition.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	encoded, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		return "", err
	}
	// Local roles and skills may already exist below the object directory.
	flowDir, err := ensureSourceDir(s.GraphsDir(), definition.ID)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(flowDir, flowSourceName), encoded, true); err != nil {
		return "", err
	}
	return AgentFlowDigest(definition)
}

// UpdateFlow applies one whole-document CAS update to a Flow draft. The ID is
// the directory identity and can never change in place.
func (s Store) UpdateFlow(id, expectedDigest string, mutate func(*domain.FlowDefinition) error) (*domain.FlowDefinition, string, error) {
	definition, digest, err := s.ReadFlow(id)
	if err != nil {
		return nil, "", err
	}
	if expectedDigest == "" || expectedDigest != digest {
		return nil, digest, ErrConflict
	}
	if mutate == nil {
		return nil, digest, fmt.Errorf("flow mutation is required")
	}
	if err := mutate(definition); err != nil {
		return nil, digest, err
	}
	if definition.ID != id {
		return nil, digest, fmt.Errorf("Flow id cannot be changed")
	}
	encoded, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		return nil, digest, err
	}
	path, _ := objectPath(s.GraphsDir(), id, flowSourceName)
	if err := atomicWrite(path, encoded, false); err != nil {
		return nil, digest, err
	}
	newDigest, err := AgentFlowDigest(definition)
	return definition, newDigest, err
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

// CreateGraphRole writes a Graph-private Role draft at
// agents/graphs/<graphID>/roles/<handle>/role.md. The handle in the document is
// the directory name, so a caller that installs under a renamed handle must
// rewrite document.Handle first; a mismatch is rejected rather than silently
// writing a Role a Graph can never resolve. Missing Graph/roles directories are
// created 0700; an existing Role is never overwritten.
func (s Store) CreateGraphRole(graphID string, document *rolesource.Document) (string, error) {
	if document == nil {
		return "", fmt.Errorf("Role source document is required")
	}
	handle := strings.TrimSpace(document.Handle)
	if handle == "" || handle != document.Handle {
		return "", fmt.Errorf("Role handle %q must match directory %q", document.Handle, handle)
	}
	encoded, err := rolesource.Encode(document)
	if err != nil {
		return "", err
	}
	graphDir, err := ensureSourceDir(s.GraphsDir(), graphID)
	if err != nil {
		return "", err
	}
	rolesDir, err := ensureSourceDir(graphDir, "roles")
	if err != nil {
		return "", err
	}
	roleDir, err := createSourceDir(rolesDir, handle)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(roleDir, "role.md"), encoded, true); err != nil {
		_ = os.Remove(roleDir)
		return "", err
	}
	return rolesource.SourceDigest(document)
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

// DeleteGraph removes the whole Graph directory, including its revisions and its
// Graph-private roles and skills. Deletion is whole-object because a revision
// sequence must stay contiguous: dropping one revision directory would corrupt
// the object for every later read. The same call removes a flow-source object
// because both kinds share one object directory.
func (s Store) DeleteGraph(graphID string) error {
	return deleteObjectDir(s.GraphsDir(), graphID)
}

// DeleteGraphRole removes one Graph-private Role directory.
func (s Store) DeleteGraphRole(graphID, handle string) error {
	if !objectIDPattern.MatchString(graphID) {
		return fmt.Errorf("invalid object id %q", graphID)
	}
	return deleteObjectDir(filepath.Join(s.GraphsDir(), graphID, "roles"), handle)
}

// DeleteRole removes the whole Worker-global Role directory, including every
// published revision.
func (s Store) DeleteRole(handle string) error {
	return deleteObjectDir(s.RolesDir(), handle)
}

func deleteObjectDir(root, id string) error {
	if !objectIDPattern.MatchString(id) {
		return fmt.Errorf("invalid object id %q", id)
	}
	path := filepath.Join(root, id)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("source object %q must be a regular directory", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("delete source object: %w", err)
	}
	return syncDirectory(root)
}

// ensureSourceDir creates every missing level of segments below root with 0700
// permissions and rejects a symlinked or non-directory level, so a source write
// can never be redirected outside ENNOTE_HOME.
func ensureSourceDir(root string, segments ...string) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create source root: %w", err)
	}
	current := root
	for _, segment := range segments {
		if !objectIDPattern.MatchString(segment) {
			return "", fmt.Errorf("invalid object id %q", segment)
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", fmt.Errorf("source directory %q must be a regular directory", current)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.Mkdir(current, 0o700); err != nil {
			if errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("source directory %q already exists", current)
			}
			return "", fmt.Errorf("create source directory: %w", err)
		}
	}
	return current, nil
}

func createSourceDir(root, id string) (string, error) {
	if !objectIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid object id %q", id)
	}
	path := filepath.Join(root, id)
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("object %q already exists", id)
		}
		return "", fmt.Errorf("create object directory: %w", err)
	}
	return path, nil
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

// regularFileExists reports whether path is a regular file. A symlink or any
// non-regular type is rejected so a source kind cannot be redirected.
func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("source must be a regular file")
	}
	return true, nil
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
