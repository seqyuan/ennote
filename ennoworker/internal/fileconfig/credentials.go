package fileconfig

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/seqyuan/ennote/ennoworker/internal/llm"
)

const credentialSchemaVersion = 1

var credentialIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

type Credential struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type credentialDocument struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Credentials   map[string]Credential `json:"credentials"`
}

type CredentialStore struct {
	Path string
	mu   sync.RWMutex
}

func (s *CredentialStore) Resolve(id string) (string, error) {
	id = strings.TrimSpace(id)
	s.mu.RLock()
	defer s.mu.RUnlock()
	document, err := s.load()
	if err != nil {
		return "", err
	}
	if credential, ok := document.Credentials[id]; ok && strings.TrimSpace(credential.Value) != "" {
		value := strings.TrimSpace(credential.Value)
		if isCredentialRef(value) {
			secret, resolveErr := (llm.CredentialResolver{}).Resolve(value)
			if resolveErr != nil {
				return "", fmt.Errorf("credential %q is unavailable: %w", id, resolveErr)
			}
			return secret.Reveal(), nil
		}
		return value, nil
	}
	// Fall back to a bare environment variable named exactly like the id.
	if secret, envErr := (llm.CredentialResolver{}).Resolve("env:" + id); envErr == nil {
		return secret.Reveal(), nil
	}
	return "", fmt.Errorf("credential %q is unavailable", id)
}

func (s *CredentialStore) Has(id string) (bool, error) {
	id = strings.TrimSpace(id)
	s.mu.RLock()
	defer s.mu.RUnlock()
	document, err := s.load()
	if err != nil {
		return false, err
	}
	if credential, ok := document.Credentials[id]; ok && strings.TrimSpace(credential.Value) != "" {
		return true, nil
	}
	_, envErr := (llm.CredentialResolver{}).Resolve("env:" + id)
	return envErr == nil, nil
}

func (s *CredentialStore) Put(id, value string) error {
	id = strings.TrimSpace(id)
	value = strings.TrimSpace(value)
	if !credentialIDPattern.MatchString(id) {
		return fmt.Errorf("credential id must match %s", credentialIDPattern)
	}
	if value == "" {
		return fmt.Errorf("credential value is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.load()
	if err != nil {
		return err
	}
	document.Credentials[id] = Credential{Type: "api_key", Value: value}
	return writeJSONAtomic(s.Path, document, 0o600)
}

func (s *CredentialStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, err := s.load()
	if err != nil {
		return err
	}
	delete(document.Credentials, strings.TrimSpace(id))
	return writeJSONAtomic(s.Path, document, 0o600)
}

func (s *CredentialStore) load() (credentialDocument, error) {
	document := credentialDocument{SchemaVersion: credentialSchemaVersion, Credentials: map[string]Credential{}}
	found, err := readStrictJSON(s.Path, &document)
	if err != nil {
		return credentialDocument{}, fmt.Errorf("read provider credentials: %w", err)
	}
	if !found {
		return document, nil
	}
	info, err := os.Lstat(s.Path)
	if err != nil {
		return credentialDocument{}, fmt.Errorf("stat provider credentials: %w", err)
	}
	if !info.Mode().IsRegular() {
		return credentialDocument{}, fmt.Errorf("provider credentials must be a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return credentialDocument{}, fmt.Errorf("provider credentials permissions must be 0600, got %04o", info.Mode().Perm())
	}
	if document.SchemaVersion != credentialSchemaVersion {
		return credentialDocument{}, fmt.Errorf("unsupported provider credential schemaVersion %d", document.SchemaVersion)
	}
	if document.Credentials == nil {
		document.Credentials = map[string]Credential{}
	}
	for id, credential := range document.Credentials {
		if !credentialIDPattern.MatchString(id) {
			return credentialDocument{}, fmt.Errorf("credential id %q is invalid", id)
		}
		if credential.Type != "api_key" {
			return credentialDocument{}, fmt.Errorf("credential %q has unsupported type %q", id, credential.Type)
		}
		if strings.TrimSpace(credential.Value) == "" {
			return credentialDocument{}, fmt.Errorf("credential %q value is required", id)
		}
	}
	return document, nil
}

func IsCredentialUnavailable(err error) bool {
	return err != nil && !errors.Is(err, os.ErrPermission) && strings.Contains(err.Error(), "is unavailable")
}

// isCredentialRef reports whether a stored credential value is a resolver
// reference (env:/file:/keyring:) rather than a plaintext secret. Plaintext
// values that happen to contain a colon but not a known scheme stay plaintext.
func isCredentialRef(value string) bool {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok || rest == "" {
		return false
	}
	switch scheme {
	case "env", "file", "keyring":
		return true
	default:
		return false
	}
}
