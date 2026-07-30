package llm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrCredentialNotFound = errors.New("credential not found")

type Keyring interface {
	Get(service, account string) (string, error)
}

type CredentialResolver struct {
	LookupEnv func(string) (string, bool)
	ReadFile  func(string) ([]byte, error)
	Stat      func(string) (os.FileInfo, error)
	Keyring   Keyring
}

type Secret struct {
	value string
}

func NewSecret(value string) Secret { return Secret{value: value} }
func (s Secret) Reveal() string     { return s.value }
func (s Secret) String() string     { return "[REDACTED]" }
func (s Secret) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

func (r CredentialResolver) Resolve(ref string) (Secret, error) {
	scheme, value, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || value == "" {
		return Secret{}, fmt.Errorf("invalid credential reference")
	}

	switch scheme {
	case "env":
		lookup := r.LookupEnv
		if lookup == nil {
			lookup = os.LookupEnv
		}
		secret, found := lookup(value)
		if !found || strings.TrimSpace(secret) == "" {
			return Secret{}, fmt.Errorf("%w: environment variable %s", ErrCredentialNotFound, value)
		}
		return NewSecret(strings.TrimSpace(secret)), nil

	case "file":
		path, err := filepath.Abs(value)
		if err != nil {
			return Secret{}, fmt.Errorf("resolve credential file: %w", err)
		}
		stat := r.Stat
		if stat == nil {
			stat = os.Stat
		}
		info, err := stat(path)
		if err != nil {
			return Secret{}, fmt.Errorf("%w: credential file", ErrCredentialNotFound)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return Secret{}, fmt.Errorf("credential file permissions must not grant group or other access")
		}
		readFile := r.ReadFile
		if readFile == nil {
			readFile = os.ReadFile
		}
		contents, err := readFile(path)
		if err != nil {
			return Secret{}, fmt.Errorf("read credential file: %w", err)
		}
		secret := strings.TrimSpace(string(contents))
		if secret == "" {
			return Secret{}, fmt.Errorf("%w: credential file is empty", ErrCredentialNotFound)
		}
		return NewSecret(secret), nil

	case "keyring":
		if r.Keyring == nil {
			return Secret{}, fmt.Errorf("keyring resolver is not configured")
		}
		service, account, ok := strings.Cut(value, "/")
		if !ok || service == "" || account == "" {
			return Secret{}, fmt.Errorf("keyring reference must be keyring:<service>/<account>")
		}
		secret, err := r.Keyring.Get(service, account)
		if err != nil || strings.TrimSpace(secret) == "" {
			return Secret{}, fmt.Errorf("%w: keyring entry", ErrCredentialNotFound)
		}
		return NewSecret(strings.TrimSpace(secret)), nil

	default:
		return Secret{}, fmt.Errorf("unsupported credential reference scheme: %s", scheme)
	}
}
