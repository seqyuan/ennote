package fileconfig

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const settingsSchemaVersion = 1

// Full-text content index modes for the catalog projection. The catalog's
// session title/name matching is always available; a FTS5 content index is
// opt-in because it costs build time and disk and is only useful once content
// search exists.
const (
	FullTextOff      = "off"
	FullTextOnDemand = "on-demand"
	FullTextStartup  = "startup"
)

type SettingsDocument struct {
	SchemaVersion        int      `json:"schemaVersion"`
	DefaultModel         string   `json:"defaultModel,omitempty"`
	SkillRoots           []string `json:"skillRoots"`
	CatalogFullTextIndex string   `json:"catalogFullTextIndex,omitempty"`
}

type SettingsStore struct {
	Path string
	mu   sync.RWMutex
	snap fileSnapshot[SettingsDocument]
}

func (s *SettingsStore) Read() (SettingsDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadForRead()
}

func (s *SettingsStore) SetDefaultModel(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref != "" {
		if _, _, err := SplitModelRef(ref); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.load()
	if err != nil {
		return err
	}
	document.DefaultModel = ref
	return writeJSONAtomic(s.Path, document, 0o600)
}

// SetCatalogFullTextIndex sets the full-text content index mode (off,
// on-demand, or startup). The value is validated before it is persisted.
func (s *SettingsStore) SetCatalogFullTextIndex(mode string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = FullTextOff
	}
	switch mode {
	case FullTextOff, FullTextOnDemand, FullTextStartup:
	default:
		return fmt.Errorf("catalogFullTextIndex must be one of off, on-demand, startup")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.load()
	if err != nil {
		return err
	}
	document.CatalogFullTextIndex = mode
	return writeJSONAtomic(s.Path, document, 0o600)
}

func (s *SettingsStore) SetSkillRoots(paths []string) error {
	roots := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve skill root %q: %w", path, err)
		}
		absolute = filepath.Clean(absolute)
		if !seen[absolute] {
			seen[absolute] = true
			roots = append(roots, absolute)
		}
	}
	sort.Strings(roots)
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.load()
	if err != nil {
		return err
	}
	document.SkillRoots = roots
	return writeJSONAtomic(s.Path, document, 0o600)
}

func (s *SettingsStore) load() (SettingsDocument, error) {
	document, err := s.loadDisk()
	if err == nil {
		s.snap.set(document, time.Now())
	}
	return document, err
}

func (s *SettingsStore) loadDisk() (SettingsDocument, error) {
	document := SettingsDocument{SchemaVersion: settingsSchemaVersion, SkillRoots: []string{}, CatalogFullTextIndex: FullTextOff}
	found, err := readStrictJSON(s.Path, &document)
	if err != nil {
		return SettingsDocument{}, fmt.Errorf("read settings: %w", err)
	}
	if !found {
		return document, nil
	}
	if document.SchemaVersion != settingsSchemaVersion {
		return SettingsDocument{}, fmt.Errorf("unsupported settings schemaVersion %d", document.SchemaVersion)
	}
	if document.DefaultModel != "" {
		if _, _, err := SplitModelRef(document.DefaultModel); err != nil {
			return SettingsDocument{}, fmt.Errorf("defaultModel: %w", err)
		}
	}
	if document.SkillRoots == nil {
		document.SkillRoots = []string{}
	}
	if document.CatalogFullTextIndex == "" {
		document.CatalogFullTextIndex = FullTextOff
	}
	switch document.CatalogFullTextIndex {
	case FullTextOff, FullTextOnDemand, FullTextStartup:
	default:
		return SettingsDocument{}, fmt.Errorf("catalogFullTextIndex must be one of off, on-demand, startup")
	}
	return document, nil
}

// loadForRead is the read-path entry: it prefers the current on-disk document
// and degrades to the latest valid snapshot when the file is unparsable.
func (s *SettingsStore) loadForRead() (SettingsDocument, error) {
	document, err := s.load()
	if err == nil {
		return document, nil
	}
	if snap, _, ok := s.snap.get(); ok {
		return snap, nil
	}
	return document, err
}

// StartWatch begins watching the settings file; changes re-load the snapshot
// on a debounce. A failed watch is a no-op. It returns a stop function.
func (s *SettingsStore) StartWatch() (stop func()) {
	return watchFile(s.Path, 100*time.Millisecond, func() {
		s.mu.Lock()
		_, _ = s.load()
		s.mu.Unlock()
	})
}
