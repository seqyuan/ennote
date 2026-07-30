package llm

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeKeyring map[string]string

func (f fakeKeyring) Get(service, account string) (string, error) {
	value, ok := f[service+"/"+account]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

func TestResolveEnvironmentCredential(t *testing.T) {
	resolver := CredentialResolver{LookupEnv: func(name string) (string, bool) {
		assert.Equal(t, "DEEPSEEK_API_KEY", name)
		return "  sk-env-secret  ", true
	}}
	secret, err := resolver.Resolve("env:DEEPSEEK_API_KEY")
	require.NoError(t, err)
	assert.Equal(t, "sk-env-secret", secret.Reveal())
	assert.Equal(t, "[REDACTED]", secret.String())
}

func TestResolveFileCredentialRequiresPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("sk-file-secret\n"), 0o600))

	secret, err := (CredentialResolver{}).Resolve("file:" + path)
	require.NoError(t, err)
	assert.Equal(t, "sk-file-secret", secret.Reveal())

	require.NoError(t, os.Chmod(path, 0o644))
	_, err = (CredentialResolver{}).Resolve("file:" + path)
	assert.ErrorContains(t, err, "permissions")
}

func TestResolveKeyringCredential(t *testing.T) {
	resolver := CredentialResolver{Keyring: fakeKeyring{"ennote/provider-1": "sk-keyring"}}
	secret, err := resolver.Resolve("keyring:ennote/provider-1")
	require.NoError(t, err)
	assert.Equal(t, "sk-keyring", secret.Reveal())
}

func TestCredentialErrorsDoNotContainSecrets(t *testing.T) {
	for _, ref := range []string{"plain:sk-secret", "env:", "file:/missing"} {
		_, err := (CredentialResolver{}).Resolve(ref)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "sk-secret")
	}
}

func TestSecretJSONIsRedacted(t *testing.T) {
	encoded, err := json.Marshal(struct {
		Secret Secret `json:"secret"`
	}{Secret: NewSecret("sk-never-serialize")})
	require.NoError(t, err)
	assert.JSONEq(t, `{"secret":"[REDACTED]"}`, string(encoded))
	assert.False(t, strings.Contains(string(encoded), "sk-never-serialize"))
}
