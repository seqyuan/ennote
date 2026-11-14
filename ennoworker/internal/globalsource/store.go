// Package globalsource owns file-authored Hero and Graph drafts under ENNOTE_HOME.
package globalsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
)

var objectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

var ErrConflict = errors.New("source digest conflict")

// sourceLocks serialises mutations per source object directory. It is package
// level rather than a Store field because Store is a value type with value
// receivers: sources.UpdateGraph copies Store into its receiver, so a mutex
// field would be copied per call and each call would lock its own copy,
// serialising nothing. A keyed registry keeps distinct objects concurrent
// while forcing mutations of the same object through one mutex.
//
// The lock only serialises within this Worker process. An external editor that
// rewrites a source file concurrently is still governed by the digest
// comparison, not this registry.
var sourceLocks sync.Map // map[string]*sync.Mutex

// lockSource acquires the per-key mutex for the source object directory and
// returns the function that releases it. The returned function must be deferred
// before a mutate callback runs: a mutate callback must never re-enter a locked
// method on the same key or it will deadlock.
func lockSource(key string) func() {
	value, _ := sourceLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

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

// Legacy file labels. A current graph.yaml omits the field. These name the
// file the directory still holds, not a second catalog.
const (
	LegacyFileFlow    = "flow"
	LegacyFileGraphV1 = "graph-v1"
)

type Store struct {
	HomeDir string
}

type GraphEntry struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	SourceKind string                 `json:"sourceKind"`
	LegacyFile string                 `json:"legacyFile,omitempty"`
	Path       string                 `json:"path"`
	Digest     string                 `json:"digest,omitempty"`
	Document   *graphsource.Document  `json:"document,omitempty"`
	Flow       *domain.FlowDefinition `json:"flow,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

type HeroEntry struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Path     string                `json:"path"`
	Digest   string                `json:"digest,omitempty"`
	Document *heroesource.Document `json:"document,omitempty"`
	Error    string                `json:"error,omitempty"`
}

func (s Store) AgentsDir() string { return filepath.Join(s.HomeDir, "agents") }
func (s Store) GraphsDir() string { return filepath.Join(s.AgentsDir(), "graphs") }
func (s Store) HeroesDir() string { return filepath.Join(s.AgentsDir(), "heroes") }

// ListGraphs returns every object under agents/graphs. A current graph.yaml
// and a legacy flow.yaml are both rows. LegacyFile names the file that is not
// a current definition: "flow" for flow.yaml, "graph-v1" for a legacy
// graphsource.Document. A directory holding both files is a row with an error,
// not a silent preference for one of them.
func (s Store) ListGraphs() ([]GraphEntry, error) {
	entries, err := os.ReadDir(s.GraphsDir())
	if errors.Is(err, os.ErrNotExist) {
		return []GraphEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read source root: %w", err)
	}
	result := make([]GraphEntry, 0, len(entries))
	for _, entry := range entries {
		id := entry.Name()
		if !objectIDPattern.MatchString(id) {
			continue
		}
		directory := filepath.Join(s.GraphsDir(), id)
		info, statErr := os.Lstat(directory)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			result = append(result, GraphEntry{ID: id, Name: id, SourceKind: SourceKindGraph,
				Path: filepath.Join(directory, graphSourceName), Error: "source object must be a regular directory"})
			continue
		}
		graphExists, graphErr := regularFileExists(filepath.Join(directory, graphSourceName))
		flowExists, flowErr := regularFileExists(filepath.Join(directory, flowSourceName))
		switch {
		case graphErr != nil || flowErr != nil:
			result = append(result, GraphEntry{ID: id, Name: id, SourceKind: SourceKindGraph,
				Path: filepath.Join(directory, graphSourceName), Error: "read source files"})
		case graphExists && flowExists:
			result = append(result, GraphEntry{ID: id, Name: id, SourceKind: SourceKindGraph,
				Path:  filepath.Join(directory, graphSourceName),
				Error: fmt.Sprintf("source object %q contains both %s and %s", id, graphSourceName, flowSourceName)})
		case flowExists:
			result = append(result, s.graphEntryFromFile(id, flowSourceName, LegacyFileFlow))
		case graphExists:
			result = append(result, s.graphEntryFromFile(id, graphSourceName, ""))
		default:
			result = append(result, GraphEntry{ID: id, Name: id, SourceKind: SourceKindGraph,
				Path: filepath.Join(directory, graphSourceName), Error: "source object has no graph.yaml or flow.yaml"})
		}
	}
	return result, nil
}

// graphEntryFromFile parses one source file into a catalog row. A flow.yaml is
// always a definition. A graph.yaml is classified first: a definition carries
// Flow, a legacy document carries Document and legacyFile graph-v1.
func (s Store) graphEntryFromFile(id, filename, legacyFile string) GraphEntry {
	path := filepath.Join(s.GraphsDir(), id, filename)
	entry := GraphEntry{ID: id, Name: id, SourceKind: SourceKindGraph, LegacyFile: legacyFile, Path: path}
	data, err := readRegular(path)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	if filename == graphSourceName {
		kind, classErr := ClassifyGraphSource(data)
		if classErr != nil {
			entry.Error = classErr.Error()
			return entry
		}
		if kind == GraphFileLegacy {
			entry.LegacyFile = LegacyFileGraphV1
			return s.legacyGraphEntry(entry, id, data)
		}
	}
	definition, err := agentflow.ParseDefinition(data)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	if definition.ID != id {
		entry.Error = fmt.Sprintf("Graph id %q must match directory %q", definition.ID, id)
		return entry
	}
	entry.Name = definition.Name
	if entry.Name == "" {
		entry.Name = definition.ID
	}
	entry.Flow = definition
	entry.Digest, err = AgentFlowDigest(definition)
	if err != nil {
		entry.Error = err.Error()
	}
	return entry
}

func (s Store) legacyGraphEntry(entry GraphEntry, id string, data []byte) GraphEntry {
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

func (s Store) ListHeroes() ([]HeroEntry, error) {
	return listObjectDirs(s.HeroesDir(), "hero.md", nil, func(id, path string, data []byte) HeroEntry {
		entry := HeroEntry{ID: id, Name: id, Path: path}
		document, err := heroesource.Parse(data)
		if err != nil {
			entry.Error = err.Error()
			return entry
		}
		if document.Handle != id {
			entry.Error = fmt.Sprintf("Hero handle %q must match directory %q", document.Handle, id)
			return entry
		}
		entry.Name = document.Name
		entry.Document = document
		entry.Digest, err = heroesource.SourceDigest(document)
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
	fileKind, err := ClassifyGraphSource(data)
	if err != nil || fileKind != GraphFileLegacy {
		return nil, "", fmt.Errorf("graph_legacy_source: %s is a FlowDefinition, not a legacy graph document", id)
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

// ReadGraphDefinition reads agents/graphs/<id>/graph.yaml when it is a
// FlowDefinition. A legacy graphsource.Document is an error, not a fallback,
// and a flow.yaml is never consulted; ReadFlow keeps that job.
func (s Store) ReadGraphDefinition(id string) (*domain.FlowDefinition, string, error) {
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
	fileKind, err := ClassifyGraphSource(data)
	if err != nil || fileKind != GraphFileDefinition {
		return nil, "", fmt.Errorf("graph_legacy_source: %s is a legacy graph document, not a FlowDefinition", id)
	}
	definition, err := agentflow.ParseDefinition(data)
	if err != nil {
		return nil, "", err
	}
	if definition.ID != id {
		return nil, "", fmt.Errorf("Graph id %q must match directory %q", definition.ID, id)
	}
	digest, err := AgentFlowDigest(definition)
	return definition, digest, err
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
	// Lock after trimming so every caller that names the same object takes the
	// same key, and before the existence check that the write depends on.
	unlock := lockSource(filepath.Join(s.GraphsDir(), id))
	defer unlock()
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

// CreateGraphDefinition writes a new graph.yaml as a FlowDefinition: schemaVersion,
// a name, no tasks, and an explicit budget. A Graph created this way classifies as
// GraphFileDefinition, so publish can accept it. It never writes the legacy
// schema_version/graph document.
func (s Store) CreateGraphDefinition(id, name string) (*domain.FlowDefinition, string, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if !objectIDPattern.MatchString(id) {
		return nil, "", fmt.Errorf("invalid graph id %q", id)
	}
	if name == "" {
		return nil, "", fmt.Errorf("graph name is required")
	}
	unlock := lockSource(filepath.Join(s.GraphsDir(), id))
	defer unlock()
	definition := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: id, Name: name,
		Budget: domain.FlowBudget{MaxTotalTokens: 200000},
		Tasks:  map[string]domain.FlowTask{},
	}
	encoded, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		return nil, "", err
	}
	path, err := createObjectDir(s.GraphsDir(), id, graphSourceName)
	if err != nil {
		return nil, "", err
	}
	if err := atomicWrite(path, encoded, true); err != nil {
		_ = os.Remove(filepath.Dir(path))
		return nil, "", err
	}
	digest, err := AgentFlowDigest(definition)
	return definition, digest, err
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
	unlock := lockSource(filepath.Join(s.GraphsDir(), document.ID))
	defer unlock()
	encoded, err := graphsource.Encode(document)
	if err != nil {
		return "", err
	}
	// The Graph directory may already exist (an installer writes Graph-private
	// skills and heroes first), so only graph.yaml itself must be new.
	graphDir, err := ensureSourceDir(s.GraphsDir(), document.ID)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(graphDir, "graph.yaml"), encoded, true); err != nil {
		return "", err
	}
	return graphsource.SourceDigest(document)
}

// CreateGraphFromDefinition writes a fully authored FlowDefinition as
// agents/graphs/<id>/graph.yaml. It is what install uses for both pack formats.
// An existing graph.yaml or flow.yaml is never overwritten: the caller inspects
// first and decides create versus identical.
func (s Store) CreateGraphFromDefinition(definition *domain.FlowDefinition) (string, error) {
	if definition == nil {
		return "", fmt.Errorf("graph definition is required")
	}
	definition.ID = strings.TrimSpace(definition.ID)
	if !objectIDPattern.MatchString(definition.ID) {
		return "", fmt.Errorf("invalid graph id %q", definition.ID)
	}
	unlock := lockSource(filepath.Join(s.GraphsDir(), definition.ID))
	defer unlock()
	if _, err := s.SourceKind(definition.ID); err == nil {
		return "", fmt.Errorf("source object %q already exists", definition.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	encoded, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		return "", err
	}
	// Local heroes and skills may already exist below the object directory.
	graphDir, err := ensureSourceDir(s.GraphsDir(), definition.ID)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(graphDir, graphSourceName), encoded, true); err != nil {
		return "", err
	}
	return AgentFlowDigest(definition)
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
	unlock := lockSource(filepath.Join(s.GraphsDir(), definition.ID))
	defer unlock()
	if _, err := s.SourceKind(definition.ID); err == nil {
		return "", fmt.Errorf("source object %q already exists", definition.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	encoded, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		return "", err
	}
	// Local heroes and skills may already exist below the object directory.
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
	unlock := lockSource(filepath.Join(s.GraphsDir(), id))
	defer unlock()
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

// UpdateGraphDefinition applies one whole-document CAS update to a
// definition-shaped graph.yaml draft. The ID is the directory identity and can
// never change in place. It is the unified Graph editor's write path: a legacy
// graphsource.Document never passes through it. Editing a legacy flow.yaml draft
// through the Graph editor renames it to graph.yaml in the same atomic replace.
func (s Store) UpdateGraphDefinition(id, expectedDigest string, mutate func(*domain.FlowDefinition) error) (*domain.FlowDefinition, string, error) {
	unlock := lockSource(filepath.Join(s.GraphsDir(), id))
	defer unlock()
	kind, err := s.SourceKind(id)
	if err != nil {
		return nil, "", err
	}
	var definition *domain.FlowDefinition
	var digest string
	switch kind {
	case SourceKindGraph:
		definition, digest, err = s.ReadGraphDefinition(id)
	case SourceKindFlow:
		definition, digest, err = s.ReadFlow(id)
	default:
		return nil, "", fmt.Errorf("unsupported source kind %q", kind)
	}
	if err != nil {
		return nil, "", err
	}
	if expectedDigest == "" || expectedDigest != digest {
		return nil, digest, ErrConflict
	}
	if mutate == nil {
		return nil, digest, fmt.Errorf("Graph mutation is required")
	}
	if err := mutate(definition); err != nil {
		return nil, digest, err
	}
	if definition.ID != id {
		return nil, digest, fmt.Errorf("Graph id cannot be changed")
	}
	canonicalizeTaskTypes(definition)
	encoded, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		return nil, digest, err
	}
	graphPath, _ := objectPath(s.GraphsDir(), id, graphSourceName)
	switch kind {
	case SourceKindFlow:
		// Write graph.yaml fresh, then drop the legacy flow.yaml so the directory
		// holds exactly one source again. The immutable revisions/ are untouched.
		if err := atomicWrite(graphPath, encoded, true); err != nil {
			return nil, digest, err
		}
		flowPath, _ := objectPath(s.GraphsDir(), id, flowSourceName)
		if err := os.Remove(flowPath); err != nil {
			return nil, digest, fmt.Errorf("remove legacy flow.yaml after rename: %w", err)
		}
	case SourceKindGraph:
		if err := atomicWrite(graphPath, encoded, false); err != nil {
			return nil, digest, err
		}
	}
	newDigest, err := AgentFlowDigest(definition)
	return definition, newDigest, err
}

// canonicalizeTaskTypes applies the same task-type defaulting the parser does,
// so a caller-supplied task whose type is omitted (the editor spells the default
// hero task without it) writes and hashes identically to its re-parsed form. The
// unsupported-type rejection stays a publish-time concern: a draft may carry a
// type the validator later refuses.
func canonicalizeTaskTypes(definition *domain.FlowDefinition) {
	for name, task := range definition.Tasks {
		task.Type = strings.TrimSpace(task.Type)
		if task.Type == "" {
			task.Type = domain.FlowTaskHero
		}
		switch task.Type {
		case domain.FlowTaskCheck:
			task.Hero = ""
			task.Skills = nil
			task.Budget = nil
		default:
			task.Command = ""
		}
		definition.Tasks[name] = task
	}
}

func (s Store) UpdateGraph(id, expectedDigest string, mutate func(*graphsource.Document) error) (*graphsource.Document, string, error) {
	unlock := lockSource(filepath.Join(s.GraphsDir(), id))
	defer unlock()
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
	if kind != "heroes" && kind != "skills" {
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

func (s Store) ReadGraphHero(graphID, heroID string) (*heroesource.Document, string, error) {
	directory, err := s.GraphResourceDir(graphID, "heroes", heroID)
	if err != nil {
		return nil, "", err
	}
	data, err := readRegular(filepath.Join(directory, "hero.md"))
	if err != nil {
		return nil, "", err
	}
	document, err := heroesource.Parse(data)
	if err != nil {
		return nil, "", err
	}
	if document.Handle != heroID {
		return nil, "", fmt.Errorf("Hero handle %q must match directory %q", document.Handle, heroID)
	}
	digest, err := heroesource.SourceDigest(document)
	return document, digest, err
}

func (s Store) ReadHero(id string) (*heroesource.Document, string, error) {
	path, err := objectPath(s.HeroesDir(), id, "hero.md")
	if err != nil {
		return nil, "", err
	}
	data, err := readRegular(path)
	if err != nil {
		return nil, "", err
	}
	document, err := heroesource.Parse(data)
	if err != nil {
		return nil, "", err
	}
	if document.Handle != id {
		return nil, "", fmt.Errorf("Hero handle %q must match directory %q", document.Handle, id)
	}
	digest, err := heroesource.SourceDigest(document)
	return document, digest, err
}

func (s Store) CreateHero(document *heroesource.Document) (*heroesource.Document, string, error) {
	if document == nil {
		return nil, "", fmt.Errorf("Hero source document is required")
	}
	unlock := lockSource(filepath.Join(s.HeroesDir(), document.Handle))
	defer unlock()
	encoded, err := heroesource.Encode(document)
	if err != nil {
		return nil, "", err
	}
	path, err := createObjectDir(s.HeroesDir(), document.Handle, "hero.md")
	if err != nil {
		return nil, "", err
	}
	if err := atomicWrite(path, encoded, true); err != nil {
		_ = os.Remove(filepath.Dir(path))
		return nil, "", err
	}
	digest, err := heroesource.SourceDigest(document)
	return document, digest, err
}

// CreateGraphHero writes a Graph-private Hero draft at
// agents/graphs/<graphID>/heroes/<handle>/hero.md. The handle in the document is
// the directory name, so a caller that installs under a renamed handle must
// rewrite document.Handle first; a mismatch is rejected rather than silently
// writing a Hero a Graph can never resolve. Missing Graph/heroes directories are
// created 0700; an existing Hero is never overwritten.
func (s Store) CreateGraphHero(graphID string, document *heroesource.Document) (string, error) {
	if document == nil {
		return "", fmt.Errorf("Hero source document is required")
	}
	handle := strings.TrimSpace(document.Handle)
	if handle == "" || handle != document.Handle {
		return "", fmt.Errorf("Hero handle %q must match directory %q", document.Handle, handle)
	}
	unlock := lockSource(filepath.Join(s.GraphsDir(), graphID, "heroes", handle))
	defer unlock()
	encoded, err := heroesource.Encode(document)
	if err != nil {
		return "", err
	}
	graphDir, err := ensureSourceDir(s.GraphsDir(), graphID)
	if err != nil {
		return "", err
	}
	heroesDir, err := ensureSourceDir(graphDir, "heroes")
	if err != nil {
		return "", err
	}
	heroDir, err := createSourceDir(heroesDir, handle)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(filepath.Join(heroDir, "hero.md"), encoded, true); err != nil {
		_ = os.Remove(heroDir)
		return "", err
	}
	return heroesource.SourceDigest(document)
}

func (s Store) UpdateHero(id, expectedDigest string, mutate func(*heroesource.Document) error) (*heroesource.Document, string, error) {
	unlock := lockSource(filepath.Join(s.HeroesDir(), id))
	defer unlock()
	document, digest, err := s.ReadHero(id)
	if err != nil {
		return nil, "", err
	}
	if expectedDigest == "" || expectedDigest != digest {
		return nil, digest, ErrConflict
	}
	if mutate == nil {
		return nil, digest, fmt.Errorf("Hero mutation is required")
	}
	if err := mutate(document); err != nil {
		return nil, digest, err
	}
	if document.Handle != id {
		return nil, digest, fmt.Errorf("Hero handle cannot be changed")
	}
	encoded, err := heroesource.Encode(document)
	if err != nil {
		return nil, digest, err
	}
	path, _ := objectPath(s.HeroesDir(), id, "hero.md")
	if err := atomicWrite(path, encoded, false); err != nil {
		return nil, digest, err
	}
	newDigest, err := heroesource.SourceDigest(document)
	return document, newDigest, err
}

func objectPath(root, id, filename string) (string, error) {
	if !objectIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid object id %q", id)
	}
	return filepath.Join(root, id, filename), nil
}

// DeleteGraph removes the whole Graph directory, including its revisions and its
// Graph-private heroes and skills. Deletion is whole-object because a revision
// sequence must stay contiguous: dropping one revision directory would corrupt
// the object for every later read. The same call removes a flow-source object
// because both kinds share one object directory.
func (s Store) DeleteGraph(graphID string) error {
	unlock := lockSource(filepath.Join(s.GraphsDir(), graphID))
	defer unlock()
	return deleteObjectDir(s.GraphsDir(), graphID)
}

// DeleteGraphHero removes one Graph-private Hero directory.
func (s Store) DeleteGraphHero(graphID, handle string) error {
	if !objectIDPattern.MatchString(graphID) {
		return fmt.Errorf("invalid object id %q", graphID)
	}
	unlock := lockSource(filepath.Join(s.GraphsDir(), graphID, "heroes", handle))
	defer unlock()
	return deleteObjectDir(filepath.Join(s.GraphsDir(), graphID, "heroes"), handle)
}

// DeleteHero removes the whole Worker-global Hero directory, including every
// published revision.
func (s Store) DeleteHero(handle string) error {
	unlock := lockSource(filepath.Join(s.HeroesDir(), handle))
	defer unlock()
	return deleteObjectDir(s.HeroesDir(), handle)
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

func SortHeroEntries(entries []HeroEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
}
