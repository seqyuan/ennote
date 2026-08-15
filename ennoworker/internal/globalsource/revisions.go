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

	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
)

const revisionSchemaVersion = 1

var revisionDirPattern = regexp.MustCompile(`^v([0-9]{6})$`)

type Revision struct {
	SchemaVersion int       `json:"schemaVersion"`
	ResourceID    string    `json:"resourceId"`
	Version       int       `json:"version"`
	Digest        string    `json:"digest"`
	PublishedAt   time.Time `json:"publishedAt"`
}

func (r Revision) ID() string {
	return fmt.Sprintf("v%06d", r.Version)
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
	return publishRevision(filepath.Join(s.RolesDir(), id), id, "role.md", contents, digest)
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
	return publishRevision(filepath.Join(s.GraphsDir(), id), id, "graph.yaml", contents, digest)
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
	contents, err := readRegular(filepath.Join(s.GraphsDir(), id, "revisions", revisionID, "graph.yaml"))
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

func publishRevision(objectDir, resourceID, sourceName string, contents []byte, digest string) (*Revision, error) {
	revisionsDir := filepath.Join(objectDir, "revisions")
	if err := os.MkdirAll(revisionsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create revisions directory: %w", err)
	}
	revisions, err := listRevisions(objectDir, resourceID)
	if err != nil {
		return nil, err
	}
	for index := range revisions {
		if revisions[index].Digest == digest {
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
		Version:       version,
		Digest:        digest,
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
	if err := writeNewSynced(filepath.Join(temporaryDir, sourceName), contents, 0o600); err != nil {
		return nil, err
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
			revision.Version != version || revision.PublishedAt.IsZero() || !validDigest(revision.Digest) {
			return nil, fmt.Errorf("revision %s metadata is invalid", entry.Name())
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
