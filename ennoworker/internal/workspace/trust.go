package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TrustRecord is a single trusted workspace entry.
type TrustRecord struct {
	WorkspaceID   string    `json:"workspaceId"`
	CanonicalRoot string    `json:"canonicalRoot"`
	TrustedAt     time.Time `json:"trustedAt"`
}

// TrustStore manages the external workspace trust file at
// $ENNOTE_HOME/trusted-workspaces.json. Workspace trust is never
// self-declared inside the workspace directory; it always lives
// outside so that cloning a repo cannot grant automatic trust.
type TrustStore struct {
	path string
	mu   sync.RWMutex
}

// NewTrustStore opens (or creates) the trust file at the given home directory.
func NewTrustStore(homeDir string) (*TrustStore, error) {
	path := filepath.Join(homeDir, "trusted-workspaces.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("trust store: create directory: %w", err)
	}
	// Ensure the file exists.
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := atomicWrite(path, []byte("[]\n")); err != nil {
			return nil, err
		}
	}
	return &TrustStore{path: path}, nil
}

// IsTrusted checks whether the given workspaceId is trusted AND the
// canonicalRoot matches the stored root.
func (s *TrustStore) IsTrusted(workspaceID, canonicalRoot string) (bool, error) {
	records, err := s.readAll()
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if r.WorkspaceID == workspaceID && r.CanonicalRoot == canonicalRoot {
			return true, nil
		}
	}
	return false, nil
}

// Trust adds or updates a trust record for the workspace.
func (s *TrustStore) Trust(workspaceID, canonicalRoot string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.readAllLocked()
	if err != nil {
		return err
	}
	now := time.Now()
	found := false
	for i, r := range records {
		if r.WorkspaceID == workspaceID {
			records[i].CanonicalRoot = canonicalRoot
			records[i].TrustedAt = now
			found = true
			break
		}
	}
	if !found {
		records = append(records, TrustRecord{
			WorkspaceID:   workspaceID,
			CanonicalRoot: canonicalRoot,
			TrustedAt:     now,
		})
	}
	return s.writeAllLocked(records)
}

// Revoke removes the trust record for the given workspaceId.
func (s *TrustStore) Revoke(workspaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.readAllLocked()
	if err != nil {
		return err
	}
	filtered := records[:0]
	for _, r := range records {
		if r.WorkspaceID != workspaceID {
			filtered = append(filtered, r)
		}
	}
	return s.writeAllLocked(filtered)
}

// List returns all current trust records.
func (s *TrustStore) List() ([]TrustRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readAllLocked()
}

// Path returns the filesystem path of the trust file (for CLI display).
func (s *TrustStore) Path() string { return s.path }

func (s *TrustStore) readAll() ([]TrustRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readAllLocked()
}

func (s *TrustStore) readAllLocked() ([]TrustRecord, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("trust store: read: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var records []TrustRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("trust store: parse: %w", err)
	}
	return records, nil
}

func (s *TrustStore) writeAllLocked(records []TrustRecord) error {
	if records == nil {
		records = []TrustRecord{}
	}
	encoded, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("trust store: encode: %w", err)
	}
	encoded = append(encoded, '\n')
	return atomicWrite(s.path, encoded)
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("trust store: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("trust store: commit: %w", err)
	}
	return nil
}
