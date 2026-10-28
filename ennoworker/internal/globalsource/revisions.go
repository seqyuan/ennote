package globalsource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
)

const revisionSchemaVersion = 1

var revisionDirPattern = regexp.MustCompile(`^v([0-9]{6})$`)

type Revision struct {
	SchemaVersion int               `json:"schemaVersion"`
	ResourceID    string            `json:"resourceId"`
	SourceKind    string            `json:"sourceKind,omitempty"`
	Version       int               `json:"version"`
	Digest        string            `json:"digest"`
	RoleDigests   map[string]string `json:"roleDigests,omitempty"`
	PublishedAt   time.Time         `json:"publishedAt"`
}

func (r Revision) ID() string {
	return fmt.Sprintf("v%06d", r.Version)
}

// normalizedSourceKind treats a legacy revision without sourceKind as graph.
func normalizedSourceKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return SourceKindGraph
	}
	return kind
}

func validSourceKind(kind string) bool {
	switch kind {
	case "", SourceKindGraph, SourceKindFlow:
		return true
	default:
		return false
	}
}

func (s Store) PublishRoleRevision(id string) (*Revision, error) {
	document, digest, err := s.ReadRole(id)
	if err != nil {
		return nil, err
	}
	contents, err := rolesource.Encode(document)
	if err != nil {
		return nil, err
	}
	return publishRevision(filepath.Join(s.RolesDir(), id), id, "",
		[]revisionFile{{Path: "role.md", Data: contents}}, digest, nil)
}

func (s Store) PublishGraphRevision(id string) (*Revision, error) {
	document, digest, err := s.ReadGraph(id)
	if err != nil {
		return nil, err
	}
	contents, err := graphsource.Encode(document)
	if err != nil {
		return nil, err
	}
	return publishRevision(filepath.Join(s.GraphsDir(), id), id, SourceKindGraph,
		[]revisionFile{{Path: graphSourceName, Data: contents}}, digest, nil)
}

// PublishFlowRevision freezes the FlowDefinition AND every flow-local Role it
// references into one immutable revision directory. Editing role.md after a
// publish therefore cannot change that revision; the next publish writes a new
// revision (even when the definition digest is unchanged).
func (s Store) PublishFlowRevision(id string) (*Revision, error) {
	definition, digest, err := s.ReadFlow(id)
	if err != nil {
		return nil, err
	}
	contents, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		return nil, err
	}
	files := []revisionFile{{Path: flowSourceName, Data: contents}}
	roleDigests := map[string]string{}
	handles, err := s.flowLocalRoleHandles(definition)
	if err != nil {
		return nil, err
	}
	for _, handle := range handles {
		document, roleDigest, err := s.ReadGraphRole(id, handle)
		if err != nil {
			return nil, fmt.Errorf("flow-local Role %q: %w", handle, err)
		}
		encoded, err := rolesource.Encode(document)
		if err != nil {
			return nil, err
		}
		files = append(files, revisionFile{Path: filepath.Join("roles", handle, "role.md"), Data: encoded})
		roleDigests[handle] = roleDigest
	}
	return publishRevision(filepath.Join(s.GraphsDir(), id), id, SourceKindFlow, files, digest, roleDigests)
}

// flowLocalRoleHandles returns every flow-local Role a definition references: an
// explicit local/<handle>, or a bare handle that exists below the flow's roles/
// directory. Version-pinned and global/ references are never bundled.
func (s Store) flowLocalRoleHandles(definition *domain.FlowDefinition) ([]string, error) {
	seen := map[string]bool{}
	for _, task := range definition.Tasks {
		if task.Type != domain.FlowTaskRole {
			continue
		}
		ref := strings.TrimSpace(task.Role)
		if ref == "" || strings.Contains(ref, "@") {
			continue
		}
		handle := ""
		switch {
		case strings.HasPrefix(ref, "local/"):
			handle = strings.TrimPrefix(ref, "local/")
		case strings.Contains(ref, "/"):
			continue
		default:
			if _, _, err := s.ReadGraphRole(definition.ID, ref); err == nil {
				handle = ref
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if handle != "" && objectIDPattern.MatchString(handle) {
			seen[handle] = true
		}
	}
	handles := make([]string, 0, len(seen))
	for handle := range seen {
		handles = append(handles, handle)
	}
	sort.Strings(handles)
	return handles, nil
}

func (s Store) ListRoleRevisions(id string) ([]Revision, error) {
	if !objectIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid object id %q", id)
	}
	return listRevisions(filepath.Join(s.RolesDir(), id), id)
}

func (s Store) ListGraphRevisions(id string) ([]Revision, error) {
	if !objectIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid object id %q", id)
	}
	return listRevisions(filepath.Join(s.GraphsDir(), id), id)
}

// ListFlowRevisions returns the immutable revisions of a flow object. A graph
// revision below the same object is a corruption, not a candidate version.
func (s Store) ListFlowRevisions(id string) ([]Revision, error) {
	revisions, err := s.ListGraphRevisions(id)
	if err != nil {
		return nil, err
	}
	for _, revision := range revisions {
		if revision.SourceKind != SourceKindFlow {
			return nil, fmt.Errorf("revision %s of flow %s has sourceKind %q", revision.ID(), id, revision.SourceKind)
		}
	}
	return revisions, nil
}

// ReadRoleRevision loads and verifies one immutable Role revision.
func (s Store) ReadRoleRevision(id, revisionID string) (*rolesource.Document, *Revision, error) {
	if !objectIDPattern.MatchString(id) || !revisionDirPattern.MatchString(revisionID) {
		return nil, nil, fmt.Errorf("invalid Role revision %q/%q", id, revisionID)
	}
	revisions, err := s.ListRoleRevisions(id)
	if err != nil {
		return nil, nil, err
	}
	var revision *Revision
	for index := range revisions {
		if revisions[index].ID() == revisionID {
			copy := revisions[index]
			revision = &copy
			break
		}
	}
	if revision == nil {
		return nil, nil, os.ErrNotExist
	}
	contents, err := readRegular(filepath.Join(s.RolesDir(), id, "revisions", revisionID, "role.md"))
	if err != nil {
		return nil, nil, err
	}
	document, err := rolesource.Parse(contents)
	if err != nil {
		return nil, nil, err
	}
	digest, err := rolesource.SourceDigest(document)
	if err != nil {
		return nil, nil, err
	}
	if document.Handle != id || digest != revision.Digest {
		return nil, nil, fmt.Errorf("Role revision %s failed integrity validation", revisionID)
	}
	return document, revision, nil
}

// ReadGraphRevision loads and verifies one immutable Graph revision. The
// returned document is the canonical graphsource form of the published
// graph.yaml; callers convert it to a FlowDefinition at Run start and freeze
// that definition into the owning Session database.
func (s Store) ReadGraphRevision(id, revisionID string) (*graphsource.Document, *Revision, error) {
	if !objectIDPattern.MatchString(id) || !revisionDirPattern.MatchString(revisionID) {
		return nil, nil, fmt.Errorf("invalid Graph revision %q/%q", id, revisionID)
	}
	revisions, err := s.ListGraphRevisions(id)
	if err != nil {
		return nil, nil, err
	}
	var revision *Revision
	for index := range revisions {
		if revisions[index].ID() == revisionID {
			copy := revisions[index]
			revision = &copy
			break
		}
	}
	if revision == nil {
		return nil, nil, os.ErrNotExist
	}
	if revision.SourceKind == SourceKindFlow {
		return nil, nil, fmt.Errorf("revision %s is a flow revision, not a Graph revision", revisionID)
	}
	contents, err := readRegular(filepath.Join(s.GraphsDir(), id, "revisions", revisionID, graphSourceName))
	if err != nil {
		return nil, nil, err
	}
	document, err := graphsource.Parse(contents)
	if err != nil {
		return nil, nil, err
	}
	digest, err := graphsource.SourceDigest(document)
	if err != nil {
		return nil, nil, err
	}
	if document.ID != id || digest != revision.Digest {
		return nil, nil, fmt.Errorf("Graph revision %s failed integrity validation", revisionID)
	}
	return document, revision, nil
}

// ReadFlowRevision loads one immutable flow revision: the FlowDefinition, plus
// the exact flow-local Role documents frozen with it. Both the definition digest
// and every bundled Role digest are verified; any drift fails closed.
func (s Store) ReadFlowRevision(id, revisionID string) (*domain.FlowDefinition, map[string]*rolesource.Document, *Revision, error) {
	if !objectIDPattern.MatchString(id) || !revisionDirPattern.MatchString(revisionID) {
		return nil, nil, nil, fmt.Errorf("invalid Flow revision %q/%q", id, revisionID)
	}
	revisions, err := s.ListFlowRevisions(id)
	if err != nil {
		return nil, nil, nil, err
	}
	var revision *Revision
	for index := range revisions {
		if revisions[index].ID() == revisionID {
			copy := revisions[index]
			revision = &copy
			break
		}
	}
	if revision == nil {
		return nil, nil, nil, os.ErrNotExist
	}
	revisionDir := filepath.Join(s.GraphsDir(), id, "revisions", revisionID)
	contents, err := readRegular(filepath.Join(revisionDir, flowSourceName))
	if err != nil {
		return nil, nil, nil, err
	}
	definition, err := agentflow.ParseDefinition(contents)
	if err != nil {
		return nil, nil, nil, err
	}
	digest, err := AgentFlowDigest(definition)
	if err != nil {
		return nil, nil, nil, err
	}
	if definition.ID != id || digest != revision.Digest {
		return nil, nil, nil, fmt.Errorf("Flow revision %s failed integrity validation", revisionID)
	}
	roles := make(map[string]*rolesource.Document, len(revision.RoleDigests))
	for handle, expected := range revision.RoleDigests {
		if !objectIDPattern.MatchString(handle) {
			return nil, nil, nil, fmt.Errorf("Flow revision %s has an invalid Role handle %q", revisionID, handle)
		}
		roleContents, err := readRegular(filepath.Join(revisionDir, "roles", handle, "role.md"))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("Flow revision %s Role %q: %w", revisionID, handle, err)
		}
		document, err := rolesource.Parse(roleContents)
		if err != nil {
			return nil, nil, nil, err
		}
		roleDigest, err := rolesource.SourceDigest(document)
		if err != nil {
			return nil, nil, nil, err
		}
		if document.Handle != handle || roleDigest != expected {
			return nil, nil, nil, fmt.Errorf("Flow revision %s Role %q failed integrity validation", revisionID, handle)
		}
		roles[handle] = document
	}
	return definition, roles, revision, nil
}

// ResolvedRole is the Role a bare handle resolves to, plus its audit source
// (design 六 P1). The source records which layer won the resolution; it is
// "global" today because file-native V2 Roles are global-only, and will become
// "project" when a project-scoped override layer is added.
type ResolvedRole struct {
	Document *rolesource.Document
	Revision Revision
	Source   string
}

// ResolveRole resolves a bare handle to its latest published revision with an
// audit source. The result is the final effective Role for the handle — a
// whole-layer value, never a field-level merge (D6).
func (s Store) ResolveRole(handle string) (*ResolvedRole, error) {
	document, revision, err := s.LatestRoleRevision(handle)
	if err != nil {
		return nil, err
	}
	return &ResolvedRole{Document: document, Revision: *revision, Source: "global"}, nil
}

// LatestRoleRevision resolves the highest published Role revision.
func (s Store) LatestRoleRevision(id string) (*rolesource.Document, *Revision, error) {
	revisions, err := s.ListRoleRevisions(id)
	if err != nil {
		return nil, nil, err
	}
	if len(revisions) == 0 {
		return nil, nil, os.ErrNotExist
	}
	latest := revisions[len(revisions)-1]
	return s.ReadRoleRevision(id, latest.ID())
}

type revisionFile struct {
	Path string
	Data []byte
}

func publishRevision(objectDir, resourceID, sourceKind string, files []revisionFile, digest string, roleDigests map[string]string) (*Revision, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("revision source files are required")
	}
	if !validSourceKind(sourceKind) {
		return nil, fmt.Errorf("unsupported revision sourceKind %q", sourceKind)
	}
	revisionsDir := filepath.Join(objectDir, "revisions")
	if err := os.MkdirAll(revisionsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create revisions directory: %w", err)
	}
	revisions, err := listRevisions(objectDir, resourceID)
	if err != nil {
		return nil, err
	}
	for index := range revisions {
		if revisions[index].Digest == digest &&
			normalizedSourceKind(revisions[index].SourceKind) == normalizedSourceKind(sourceKind) &&
			sameRoleDigests(revisions[index].RoleDigests, roleDigests) {
			copy := revisions[index]
			return &copy, nil
		}
	}
	version := 1
	if len(revisions) > 0 {
		version = revisions[len(revisions)-1].Version + 1
	}
	revision := Revision{
		SchemaVersion: revisionSchemaVersion,
		ResourceID:    resourceID,
		SourceKind:    sourceKind,
		Version:       version,
		Digest:        digest,
		RoleDigests:   roleDigests,
		PublishedAt:   time.Now().UTC(),
	}
	temporaryDir, err := os.MkdirTemp(objectDir, ".revision-*")
	if err != nil {
		return nil, fmt.Errorf("create revision temp directory: %w", err)
	}
	defer os.RemoveAll(temporaryDir)
	if err := os.Chmod(temporaryDir, 0o700); err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.Path == "" || filepath.IsAbs(file.Path) || strings.HasPrefix(file.Path, ".."+string(filepath.Separator)) || file.Path == ".." {
			return nil, fmt.Errorf("invalid revision file path %q", file.Path)
		}
		target := filepath.Join(temporaryDir, file.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		if err := writeNewSynced(target, file.Data, 0o600); err != nil {
			return nil, err
		}
	}
	metadata, err := json.MarshalIndent(revision, "", "  ")
	if err != nil {
		return nil, err
	}
	metadata = append(metadata, '\n')
	if err := writeNewSynced(filepath.Join(temporaryDir, "revision.json"), metadata, 0o600); err != nil {
		return nil, err
	}
	if err := syncDirectory(temporaryDir); err != nil {
		return nil, err
	}
	finalDir := filepath.Join(revisionsDir, revision.ID())
	if _, err := os.Lstat(finalDir); err == nil {
		return nil, fmt.Errorf("revision %s already exists", revision.ID())
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(temporaryDir, finalDir); err != nil {
		return nil, fmt.Errorf("publish revision: %w", err)
	}
	if err := syncDirectory(revisionsDir); err != nil {
		return nil, err
	}
	return &revision, nil
}

func sameRoleDigests(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func listRevisions(objectDir, resourceID string) ([]Revision, error) {
	revisionsDir := filepath.Join(objectDir, "revisions")
	entries, err := os.ReadDir(revisionsDir)
	if errors.Is(err, os.ErrNotExist) {
		return []Revision{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read revisions: %w", err)
	}
	revisions := make([]Revision, 0, len(entries))
	for _, entry := range entries {
		matches := revisionDirPattern.FindStringSubmatch(entry.Name())
		if len(matches) != 2 {
			continue
		}
		directory := filepath.Join(revisionsDir, entry.Name())
		info, err := os.Lstat(directory)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("revision %s must be a regular directory", entry.Name())
		}
		contents, err := readRegular(filepath.Join(directory, "revision.json"))
		if err != nil {
			return nil, fmt.Errorf("read revision %s metadata: %w", entry.Name(), err)
		}
		var revision Revision
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&revision); err != nil {
			return nil, fmt.Errorf("decode revision %s: %w", entry.Name(), err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("revision %s metadata must contain one JSON value", entry.Name())
		}
		version, _ := strconv.Atoi(matches[1])
		if revision.SchemaVersion != revisionSchemaVersion || revision.ResourceID != resourceID ||
			revision.Version != version || revision.PublishedAt.IsZero() || !validDigest(revision.Digest) ||
			!validSourceKind(revision.SourceKind) {
			return nil, fmt.Errorf("revision %s metadata is invalid", entry.Name())
		}
		for handle, digest := range revision.RoleDigests {
			if !objectIDPattern.MatchString(handle) || !validDigest(digest) {
				return nil, fmt.Errorf("revision %s metadata is invalid", entry.Name())
			}
		}
		revisions = append(revisions, revision)
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Version < revisions[j].Version })
	for index, revision := range revisions {
		if revision.Version != index+1 {
			return nil, fmt.Errorf("revision sequence has a gap before %s", revision.ID())
		}
	}
	return revisions, nil
}

func writeNewSynced(path string, contents []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return false
	}
	for _, character := range digest[len("sha256:"):] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
