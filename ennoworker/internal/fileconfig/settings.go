package fileconfig

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const settingsSchemaVersion = 1

type SettingsDocument struct {
	SchemaVersion int      `json:"schemaVersion"`
	DefaultModel  string   `json:"defaultModel,omitempty"`
	SkillRoots    []string `json:"skillRoots"`
}

type SettingsStore struct {
	Path string
	mu   sync.RWMutex
}

func (s *SettingsStore) Read() (SettingsDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load()
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
	document := SettingsDocument{SchemaVersion: settingsSchemaVersion, SkillRoots: []string{}}
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
	return document, nil
}
