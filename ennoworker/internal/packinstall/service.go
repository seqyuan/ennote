package packinstall

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// Service installs, lists, and removes Agent Packs. Every entry point takes one
// internal mutex: installation is a multi-file transaction and the Worker is the
// only writer, so a process-wide lock is sufficient and a filesystem lock would
// add a crash-recovery surface S2a does not need.
type Service struct {
	HomeDir    string
	Sources    *globalsource.Store
	Models     *store.ModelRepo
	SkillsRoot string
	Sessions   *sessionstore.Manager
	Now        func() time.Time

	mu sync.Mutex
}

// PacksDir is the Agent Pack state root below ENNOTE_HOME.
func (s *Service) PacksDir() string {
	return filepath.Join(strings.TrimSpace(s.HomeDir), "packs")
}

// StagingDir holds one directory per in-flight install. Every directory below it
// is garbage once the Worker restarts without a live transaction.
func (s *Service) StagingDir() string { return filepath.Join(s.PacksDir(), "staging") }

// StateDir holds the install state files (the commit point) and the copied pack
// manifests that record provenance.
func (s *Service) StateDir() string { return filepath.Join(s.PacksDir(), "state") }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// validateState checks only what the read/remove paths need: the state root.
func (s *Service) validateState() error {
	if strings.TrimSpace(s.HomeDir) == "" {
		return fmt.Errorf("pack installer home directory is not configured")
	}
	return nil
}

func (s *Service) validate() error {
	if strings.TrimSpace(s.HomeDir) == "" {
		return fmt.Errorf("pack installer home directory is not configured")
	}
	if s.Sources == nil {
		return fmt.Errorf("pack installer source store is not configured")
	}
	if s.Models == nil {
		return fmt.Errorf("pack installer model catalog is not configured")
	}
	if strings.TrimSpace(s.SkillsRoot) == "" {
		return fmt.Errorf("pack installer skills root is not configured")
	}
	return nil
}

// GCStaging removes every leftover staging directory. Call it when the Worker
// starts: a staging directory only exists while an install holds the mutex, so
// anything found at startup belongs to a crashed transaction. The state file is
// the commit point, therefore removing staging never changes an installed pack.
func (s *Service) GCStaging() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gcStagingLocked()
}

func (s *Service) gcStagingLocked() error {
	root := s.StagingDir()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pack staging root: %w", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("remove pack staging directory: %w", err)
		}
	}
	return nil
}

func newTransactionID(now time.Time) string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand cannot fail on supported platforms; fall back to the clock
		// so a failed read still produces a unique-enough directory name.
		return fmt.Sprintf("%d", now.UTC().UnixNano())
	}
	return fmt.Sprintf("%d-%s", now.UTC().UnixNano(), hex.EncodeToString(buffer))
}

// packIDParts splits a validated manifest pack id (publisher/name) into its two
// path segments. The manifest pattern already forbids separators and dot-only
// segments; this is the belt-and-braces check for state paths built from a pack
// id read back from disk.
func packIDParts(packID string) (string, string, error) {
	publisher, name, found := strings.Cut(strings.TrimSpace(packID), "/")
	if !found || publisher == "" || name == "" ||
		strings.ContainsAny(publisher+name, `/\`) ||
		publisher == "." || publisher == ".." || name == "." || name == ".." {
		return "", "", fmt.Errorf("invalid pack id %q", packID)
	}
	return publisher, name, nil
}

func (s *Service) statePath(packID string) (string, error) {
	publisher, name, err := packIDParts(packID)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.StateDir(), publisher, name+".json"), nil
}

func (s *Service) manifestCopyPath(packID string) (string, error) {
	publisher, name, err := packIDParts(packID)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.StateDir(), publisher, name, "manifest.yaml"), nil
}

// atomicWriteFile publishes data at path with a temp file + fsync + rename, so a
// reader never sees a partial state file or manifest copy.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create pack state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".pack-*")
	if err != nil {
		return fmt.Errorf("create pack state temp file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish pack state file: %w", err)
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

// sortedKeys is a tiny helper for deterministic map iteration.
func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
