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
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
)

const revisionSchemaVersion = 1

// maxSkillDirBytes caps the total bytes of one graph-local Skill directory
// frozen into a published revision, so a single oversized Skill cannot bloat the
// immutable revision directory.
const maxSkillDirBytes = 1 << 20 // 1 MiB

var revisionDirPattern = regexp.MustCompile(`^v([0-9]{6})$`)

type Revision struct {
	SchemaVersion int               `json:"schemaVersion"`
	ResourceID    string            `json:"resourceId"`
	SourceKind    string            `json:"sourceKind,omitempty"`
	Version       int               `json:"version"`
	Digest        string            `json:"digest"`
	HeroDigests   map[string]string `json:"heroDigests,omitempty"`
	SkillDigests  map[string]string `json:"skillDigests,omitempty"`
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
	case "", SourceKindGraph:
		return true
	default:
		return false
	}
}

func (s Store) PublishHeroRevision(id string) (*Revision, error) {
	// Publishing reads the drafts and then appends the next revision, so it is a
	// read-then-write on one object: serialise it or two concurrent publishes
	// both compute the same next version and one fails on the rename.
	unlock := lockSource(filepath.Join(s.HeroesDir(), id))
	defer unlock()
	document, digest, err := s.ReadHero(id)
	if err != nil {
		return nil, err
	}
	contents, err := heroesource.Encode(document)
	if err != nil {
		return nil, err
	}
	return publishRevision(filepath.Join(s.HeroesDir(), id), id, "",
		[]revisionFile{{Path: "hero.md", Data: contents}}, digest, nil, nil)
}

// PublishGraphRevision freezes a definition-shaped graph.yaml into one
// revision with sourceKind=graph.
func (s Store) PublishGraphRevision(id string) (*Revision, error) {
	unlock := lockSource(filepath.Join(s.GraphsDir(), id))
	defer unlock()
	definition, digest, err := s.ReadGraphDefinition(id)
	if err != nil {
		return nil, err
	}
	return s.publishDefinitionRevision(id, SourceKindGraph, graphSourceName, definition, digest)
}

// publishDefinitionRevision is the one publish writer. It freezes the definition,
// every local Hero it references, and every local Skill a hero task or a bundled
// Hero names. A check task has no skills. The caller holds the per-object lock.
func (s Store) publishDefinitionRevision(id, sourceKind, fileName string, definition *domain.FlowDefinition, digest string) (*Revision, error) {
	contents, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		return nil, err
	}
	files := []revisionFile{{Path: fileName, Data: contents}}
	handles, err := s.localHeroHandles(definition)

	if err != nil {
		return nil, err
	}
	var heroDigests map[string]string
	heroDocuments := map[string]*heroesource.Document{}
	for _, handle := range handles {
		document, heroDigest, err := s.ReadGraphHero(id, handle)
		if err != nil {
			return nil, fmt.Errorf("local Hero %q: %w", handle, err)
		}
		encoded, err := heroesource.Encode(document)
		if err != nil {
			return nil, err
		}
		files = append(files, revisionFile{Path: filepath.Join("heroes", handle, "hero.md"), Data: encoded})
		if heroDigests == nil {
			heroDigests = map[string]string{}
		}
		heroDigests[handle] = heroDigest
		heroDocuments[handle] = document
	}
	skillDigests, skillFiles, err := s.definitionSkillFiles(id, definition, heroDocuments)
	if err != nil {
		return nil, err
	}
	files = append(files, skillFiles...)
	return publishRevision(filepath.Join(s.GraphsDir(), id), id, sourceKind, files, digest, heroDigests, skillDigests)
}

// definitionSkillFiles freezes every local Skill a hero task declares and every
// local Skill a bundled Hero binds. Check tasks have no skills.
func (s Store) definitionSkillFiles(id string, definition *domain.FlowDefinition, heroes map[string]*heroesource.Document) (map[string]string, []revisionFile, error) {
	skillSeen := map[string]bool{}
	skillContext := map[string]string{}
	taskIDs := make([]string, 0, len(definition.Tasks))
	for taskID := range definition.Tasks {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	for _, taskID := range taskIDs {
		task := definition.Tasks[taskID]
		if task.Type == domain.FlowTaskCheck {
			continue
		}
		for _, ref := range task.Skills {
			if skillID, ok := strings.CutPrefix(ref, "local/"); ok && !skillSeen[skillID] {
				skillSeen[skillID] = true
				skillContext[skillID] = fmt.Sprintf("Task %q", taskID)
			}
		}
	}
	for handle, document := range heroes {
		for _, binding := range document.Skills {
			if skillID, ok := strings.CutPrefix(binding.ID, "local/"); ok && !skillSeen[skillID] {
				skillSeen[skillID] = true
				skillContext[skillID] = fmt.Sprintf("Hero %q", handle)
			}
		}
	}
	skillIDs := make([]string, 0, len(skillSeen))
	for skillID := range skillSeen {
		skillIDs = append(skillIDs, skillID)
	}
	sort.Strings(skillIDs)
	var skillDigests map[string]string
	var files []revisionFile
	for _, skillID := range skillIDs {
		directory, err := s.GraphResourceDir(id, "skills", skillID)
		if err != nil {
			return nil, nil, fmt.Errorf("%s references local Skill %q: %w", skillContext[skillID], skillID, err)
		}
		skillFiles, err := collectSkillFiles(skillID, directory)
		if err != nil {
			return nil, nil, err
		}
		// A Skill directory with no regular files still hashes, but publishRevision
		// only creates directories for files it writes, so recording such a digest
		// would publish a revision that cannot be read back. Refuse it here.
		if len(skillFiles) == 0 {
			return nil, nil, fmt.Errorf("%s references local Skill %q, which contains no files", skillContext[skillID], skillID)
		}
		// Freezing only copies the tree; the Run loads it with skills.Load, so a
		// directory that hashes but does not load would publish cleanly and fail
		// every Run. Refuse it at publish instead.
		if _, err := skills.Load(directory); err != nil {
			return nil, nil, fmt.Errorf("%s references local Skill %q: %w", skillContext[skillID], skillID, err)
		}
		contentDigest, err := skills.ContentDigest(directory)
		if err != nil {
			return nil, nil, fmt.Errorf("%s references local Skill %q: %w", skillContext[skillID], skillID, err)
		}
		if skillDigests == nil {
			skillDigests = map[string]string{}
		}
		skillDigests[skillID] = contentDigest
		files = append(files, skillFiles...)
	}
	return skillDigests, files, nil
}

// localHeroHandles returns every graph-local Hero a definition references: an
// explicit local/<handle>, or a bare handle that exists below the flow's heroes/
// directory. Version-pinned and global/ references are never bundled.
func (s Store) localHeroHandles(definition *domain.FlowDefinition) ([]string, error) {
	seen := map[string]bool{}
	for _, task := range definition.Tasks {
		if task.Type != domain.FlowTaskHero {
			continue
		}
		ref := strings.TrimSpace(task.Hero)
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
			if _, _, err := s.ReadGraphHero(definition.ID, ref); err == nil {
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

func (s Store) ListHeroRevisions(id string) ([]Revision, error) {
	if !objectIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid object id %q", id)
	}
	return listRevisions(filepath.Join(s.HeroesDir(), id), id)
}

func (s Store) ListGraphRevisions(id string) ([]Revision, error) {
	if !objectIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid object id %q", id)
	}
	return listRevisions(filepath.Join(s.GraphsDir(), id), id)
}

// ReadHeroRevision loads and verifies one immutable Hero revision.
func (s Store) ReadHeroRevision(id, revisionID string) (*heroesource.Document, *Revision, error) {
	if !objectIDPattern.MatchString(id) || !revisionDirPattern.MatchString(revisionID) {
		return nil, nil, fmt.Errorf("invalid Hero revision %q/%q", id, revisionID)
	}
	revisions, err := s.ListHeroRevisions(id)
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
	contents, err := readRegular(filepath.Join(s.HeroesDir(), id, "revisions", revisionID, "hero.md"))
	if err != nil {
		return nil, nil, err
	}
	document, err := heroesource.Parse(contents)
	if err != nil {
		return nil, nil, err
	}
	digest, err := heroesource.SourceDigest(document)
	if err != nil {
		return nil, nil, err
	}
	if document.Handle != id || digest != revision.Digest {
		return nil, nil, fmt.Errorf("Hero revision %s failed integrity validation", revisionID)
	}
	return document, revision, nil
}

// graphRevision locates and validates the metadata for one Graph revision.
func (s Store) graphRevision(id, revisionID string) (*Revision, error) {
	if !objectIDPattern.MatchString(id) || !revisionDirPattern.MatchString(revisionID) {
		return nil, fmt.Errorf("invalid Graph revision %q/%q", id, revisionID)
	}
	revisions, err := s.ListGraphRevisions(id)
	if err != nil {
		return nil, err
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
		return nil, os.ErrNotExist
	}
	return revision, nil
}

// GraphRevisionBundle is one immutable Graph revision plus every graph-local
// resource frozen with it.
type GraphRevisionBundle struct {
	Heroes    map[string]*heroesource.Document
	SkillDirs map[string]string
	Revision  *Revision
	// Frozen reports whether the revision recorded at least one local Hero or
	// Skill digest. A revision that referenced local resources always records
	// them, so an empty set means either a legacy revision (fall back to draft)
	// or a Graph with no local resources at all (nothing to freeze) — the two are
	// indistinguishable and behave identically.
	Frozen bool
}

// ReadGraphRevisionBundle loads and verifies one immutable Graph revision plus
// every frozen graph-local Hero and Skill. The graph digest and every bundled
// resource digest are verified; any drift fails closed.
func (s Store) ReadGraphRevisionBundle(id, revisionID string) (*GraphRevisionBundle, error) {
	revision, err := s.graphRevision(id, revisionID)
	if err != nil {
		return nil, err
	}
	revisionDir := filepath.Join(s.GraphsDir(), id, "revisions", revisionID)
	contents, err := readRegular(filepath.Join(revisionDir, graphSourceName))
	if err != nil {
		return nil, err
	}
	digest, err := definitionRevisionDigest(contents)
	if err != nil {
		return nil, err
	}
	if digest != revision.Digest {
		return nil, fmt.Errorf("Graph revision %s failed integrity validation", revisionID)
	}
	heroes, skillDirs, err := s.verifyRevisionResources(revisionID, revisionDir, revision)
	if err != nil {
		return nil, err
	}
	return &GraphRevisionBundle{
		Heroes: heroes, SkillDirs: skillDirs, Revision: revision,
		Frozen: len(revision.HeroDigests) > 0 || len(revision.SkillDigests) > 0,
	}, nil
}

func definitionRevisionDigest(contents []byte) (string, error) {
	definition, err := agentflow.ParseDefinition(contents)
	if err != nil {
		return "", err
	}
	return AgentFlowDigest(definition)
}

// verifyRevisionResources checks every frozen Hero and Skill digest. Any drift
// fails closed.
func (s Store) verifyRevisionResources(revisionID, revisionDir string, revision *Revision) (map[string]*heroesource.Document, map[string]string, error) {
	heroes := make(map[string]*heroesource.Document, len(revision.HeroDigests))
	for handle, expected := range revision.HeroDigests {
		heroContents, err := readRegular(filepath.Join(revisionDir, "heroes", handle, "hero.md"))
		if err != nil {
			return nil, nil, fmt.Errorf("Graph revision %s Hero %q: %w", revisionID, handle, err)
		}
		heroDocument, err := heroesource.Parse(heroContents)
		if err != nil {
			return nil, nil, err
		}
		heroDigest, err := heroesource.SourceDigest(heroDocument)
		if err != nil {
			return nil, nil, err
		}
		if heroDocument.Handle != handle || heroDigest != expected {
			return nil, nil, fmt.Errorf("Graph revision %s Hero %q failed integrity validation", revisionID, handle)
		}
		heroes[handle] = heroDocument
	}
	skillDirs := make(map[string]string, len(revision.SkillDigests))
	for skillID, expected := range revision.SkillDigests {
		directory := filepath.Join(revisionDir, "skills", skillID)
		info, err := os.Lstat(directory)
		if err != nil {
			return nil, nil, fmt.Errorf("Graph revision %s Skill %q: %w", revisionID, skillID, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, nil, fmt.Errorf("Graph revision %s Skill %q failed integrity validation", revisionID, skillID)
		}
		if err := validateSkillTree(skillID, directory); err != nil {
			return nil, nil, fmt.Errorf("Graph revision %s Skill %q: %w", revisionID, skillID, err)
		}
		actual, err := skills.ContentDigest(directory)
		if err != nil {
			return nil, nil, fmt.Errorf("Graph revision %s Skill %q: %w", revisionID, skillID, err)
		}
		if actual != expected {
			return nil, nil, fmt.Errorf("Graph revision %s Skill %q failed integrity validation", revisionID, skillID)
		}
		skillDirs[skillID] = directory
	}
	return heroes, skillDirs, nil
}

// ReadGraphDefinitionRevision loads one immutable definition revision: the
// FlowDefinition, plus the exact graph-local Heroes and Skills frozen with it.
func (s Store) ReadGraphDefinitionRevision(id, revisionID string) (*domain.FlowDefinition, map[string]*heroesource.Document, *Revision, error) {
	if !objectIDPattern.MatchString(id) || !revisionDirPattern.MatchString(revisionID) {
		return nil, nil, nil, fmt.Errorf("invalid Graph revision %q/%q", id, revisionID)
	}
	revisions, err := s.ListGraphRevisions(id)
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
	contents, err := readRegular(filepath.Join(revisionDir, graphSourceName))
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
		return nil, nil, nil, fmt.Errorf("Graph revision %s failed integrity validation", revisionID)
	}
	heroes := make(map[string]*heroesource.Document, len(revision.HeroDigests))
	for handle, expected := range revision.HeroDigests {
		if !objectIDPattern.MatchString(handle) {
			return nil, nil, nil, fmt.Errorf("Graph revision %s has an invalid Hero handle %q", revisionID, handle)
		}
		heroContents, err := readRegular(filepath.Join(revisionDir, "heroes", handle, "hero.md"))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("Graph revision %s Hero %q: %w", revisionID, handle, err)
		}
		document, err := heroesource.Parse(heroContents)
		if err != nil {
			return nil, nil, nil, err
		}
		heroDigest, err := heroesource.SourceDigest(document)
		if err != nil {
			return nil, nil, nil, err
		}
		if document.Handle != handle || heroDigest != expected {
			return nil, nil, nil, fmt.Errorf("Graph revision %s Hero %q failed integrity validation", revisionID, handle)
		}
		heroes[handle] = document
	}
	return definition, heroes, revision, nil
}

// ResolvedHero is the Hero a bare handle resolves to, plus its audit source
// (design 六 P1). The source records which layer won the resolution; it is
// "global" today because file-native V2 Heroes are global-only, and will become
// "project" when a project-scoped override layer is added.
type ResolvedHero struct {
	Document *heroesource.Document
	Revision Revision
	Source   string
}

// ResolveHero resolves a bare handle to its latest published revision with an
// audit source. The result is the final effective Hero for the handle — a
// whole-layer value, never a field-level merge (D6).
func (s Store) ResolveHero(handle string) (*ResolvedHero, error) {
	document, revision, err := s.LatestHeroRevision(handle)
	if err != nil {
		return nil, err
	}
	return &ResolvedHero{Document: document, Revision: *revision, Source: "global"}, nil
}

// LatestHeroRevision resolves the highest published Hero revision.
func (s Store) LatestHeroRevision(id string) (*heroesource.Document, *Revision, error) {
	revisions, err := s.ListHeroRevisions(id)
	if err != nil {
		return nil, nil, err
	}
	if len(revisions) == 0 {
		return nil, nil, os.ErrNotExist
	}
	latest := revisions[len(revisions)-1]
	return s.ReadHeroRevision(id, latest.ID())
}

type revisionFile struct {
	Path string
	Data []byte
}

func publishRevision(objectDir, resourceID, sourceKind string, files []revisionFile, digest string, heroDigests, skillDigests map[string]string) (*Revision, error) {
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
			sameHeroDigests(revisions[index].HeroDigests, heroDigests) &&
			sameHeroDigests(revisions[index].SkillDigests, skillDigests) {
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
		HeroDigests:   heroDigests,
		SkillDigests:  skillDigests,
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

func sameHeroDigests(left, right map[string]string) bool {
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

// collectSkillFiles returns the revision files for every regular file below dir,
// rooted at skills/<skillID>/<rel>. It rejects any symlink or non-regular file
// entry so a frozen Skill tree can never follow a symlink out of the source
// directory, and it aborts as soon as the running total exceeds
// maxSkillDirBytes so an oversized Skill is refused before its bytes are read
// into memory rather than after.
func collectSkillFiles(skillID, dir string) ([]revisionFile, error) {
	var files []revisionFile
	var total int64
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill %q contains a symlink at %q", skillID, path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill %q contains a non-regular file %q", skillID, path)
		}
		total += info.Size()
		if total > maxSkillDirBytes {
			return fmt.Errorf("skill %q exceeds the %d-byte freeze limit", skillID, maxSkillDirBytes)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("skill %q: read %q: %w", skillID, rel, err)
		}
		files = append(files, revisionFile{
			Path: filepath.Join("skills", skillID, filepath.ToSlash(rel)),
			Data: data,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// validateSkillTree rejects any symlink or non-regular file entry below dir. It
// is run before skills.ContentDigest, whose dirHash would otherwise follow a
// symlinked file through os.Open.
func validateSkillTree(skillID, dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill %q contains a symlink at %q", skillID, path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill %q contains a non-regular file %q", skillID, path)
		}
		return nil
	})
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
		for handle, digest := range revision.HeroDigests {
			if !objectIDPattern.MatchString(handle) || !validDigest(digest) {
				return nil, fmt.Errorf("revision %s metadata is invalid", entry.Name())
			}
		}
		for skillID, digest := range revision.SkillDigests {
			if !objectIDPattern.MatchString(skillID) || !validDigest(digest) {
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
