package mcpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ProjectFile is Ennote's own `.ennote/mcp.json` shape. It is a pure
// side-effect-free discovery source: parsing never starts processes, never
// opens network connections, and never auto-enables anything. Secret-like
// environment values must be declared as credential references; literal
// secret values are rejected (fail closed).
type ProjectFile struct {
	MCPServers map[string]ProjectServer `json:"mcpServers"`
}

// ProjectServer is one candidate server declaration from a project file.
type ProjectServer struct {
	// Type is one of domain.MCPTransport*.
	Type string `json:"type"`
	// Stdio fields.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	CWD     string   `json:"cwd,omitempty"`
	// HTTP fields.
	URL string `json:"url,omitempty"`
	// Env carries non-secret literals; secret-like names are rejected here.
	Env map[string]string `json:"env,omitempty"`
	// EnvCredentials maps env name -> credential ref (env:/file:/keyring:).
	EnvCredentials map[string]string `json:"envCredentials,omitempty"`
	// Headers for HTTP transports (literals only; secret headers rejected).
	Headers map[string]string `json:"headers,omitempty"`
	// HeaderCredentials maps header name -> credential ref.
	HeaderCredentials map[string]string `json:"headerCredentials,omitempty"`

	TimeoutMS     int    `json:"timeoutMs,omitempty"`
	NetworkPolicy string `json:"networkPolicy,omitempty"`
}

// ProjectCandidate is a parsed, validated candidate that can be bound. It is
// never enabled implicitly.
type ProjectCandidate struct {
	Slug        string
	DisplayName string
	Version     domain.MCPServerProfileVersion
	// AlreadyBoundVersionID is set when the candidate matches an existing
	// project_file profile version in the store (same config digest).
	AlreadyBoundVersionID string
}

// ParseProjectFile reads and validates a `.ennote/mcp.json` file. It performs
// no I/O beyond reading the file. A missing file yields (nil, nil).
func ParseProjectFile(path string) (*ProjectFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read project mcp file: %w", err)
	}
	var file ProjectFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse project mcp file: %w", err)
	}
	if err := file.Validate(); err != nil {
		return nil, err
	}
	return &file, nil
}

// Validate enforces transport-specific invariants and the secret-literal
// ban for the whole file.
func (f *ProjectFile) Validate() error {
	if len(f.MCPServers) == 0 {
		return fmt.Errorf("mcpServers must declare at least one server")
	}
	for name, s := range f.MCPServers {
		if err := validateProjectServer(name, s); err != nil {
			return err
		}
	}
	return nil
}

func validateProjectServer(name string, s ProjectServer) error {
	if err := validateMCPSlugLocal(name); err != nil {
		return fmt.Errorf("server %q: %w", name, err)
	}
	switch s.Type {
	case domain.MCPTransportStdio:
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("server %q: stdio requires command", name)
		}
		if len(s.Args) > MaxStdioArgv {
			return fmt.Errorf("server %q: args exceed %d entries", name, MaxStdioArgv)
		}
	case domain.MCPTransportStreamableHTTP, domain.MCPTransportLegacySSE:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("server %q: HTTP requires url", name)
		}
	default:
		return fmt.Errorf("server %q: unsupported type %q", name, s.Type)
	}
	for envName := range s.Env {
		if IsSecretLikeEnvName(envName) {
			return fmt.Errorf("server %q: environment variable %s must use envCredentials, not a literal", name, envName)
		}
	}
	for envName := range s.EnvCredentials {
		if !validCredentialRefLocal(s.EnvCredentials[envName]) {
			return fmt.Errorf("server %q: invalid credential ref for %s", name, envName)
		}
	}
	for hdrName := range s.Headers {
		upper := strings.ToUpper(hdrName)
		if upper == "AUTHORIZATION" || upper == "COOKIE" || strings.Contains(upper, "API-KEY") || strings.Contains(upper, "TOKEN") {
			return fmt.Errorf("server %q: header %s must use headerCredentials, not a literal", name, hdrName)
		}
	}
	for hdrName, ref := range s.HeaderCredentials {
		if !validCredentialRefLocal(ref) {
			return fmt.Errorf("server %q: invalid credential ref for header %s", name, hdrName)
		}
		_ = hdrName
	}
	if s.Type == domain.MCPTransportStreamableHTTP || s.Type == domain.MCPTransportLegacySSE {
		if err := validateEndpointNoUserinfo(s.URL); err != nil {
			return fmt.Errorf("server %q: %w", name, err)
		}
	}
	return nil
}

func validateMCPSlugLocal(slug string) error {
	if len(slug) > 64 {
		return fmt.Errorf("slug too long (max 64)")
	}
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("slug may only contain lowercase letters, digits, '_' and '-'")
	}
	return nil
}

func validCredentialRefLocal(ref string) bool {
	scheme, value, ok := strings.Cut(strings.TrimSpace(ref), ":")
	return ok && value != "" && (scheme == "env" || scheme == "file" || scheme == "keyring")
}

// Candidates converts a parsed project file into bound-able candidates. The
// config digest is computed without secret values (digests only reference
// names, never values).
func (f *ProjectFile) Candidates() []ProjectCandidate {
	names := make([]string, 0, len(f.MCPServers))
	for name := range f.MCPServers {
		names = append(names, name)
	}
	candidates := make([]ProjectCandidate, 0, len(names))
	for _, name := range names {
		s := f.MCPServers[name]
		version := domain.MCPServerProfileVersion{
			Transport:      s.Type,
			Executable:     s.Command,
			Argv:           s.Args,
			Endpoint:       s.URL,
			EnvLiterals:    s.Env,
			EnvCredentials: s.EnvCredentials,
			HeaderLiterals: s.Headers,
			HeaderCreds:    s.HeaderCredentials,
			CWD:            s.CWD,
			TimeoutMS:      s.TimeoutMS,
			NetworkPolicy:  s.NetworkPolicy,
		}
		if version.TimeoutMS <= 0 {
			version.TimeoutMS = 15000
		}
		if version.NetworkPolicy == "" {
			version.NetworkPolicy = "default"
		}
		version.ConfigDigest = ConfigDigest(&version)
		candidates = append(candidates, ProjectCandidate{
			Slug:        name,
			DisplayName: name,
			Version:     version,
		})
	}
	return candidates
}

// FindProjectFile locates `.ennote/mcp.json` under a project root. The file
// must sit at the workspace root (not nested) so repository-scoped content
// cannot surprise hidden subdirectories.
func FindProjectFile(projectRoot string) string {
	if projectRoot == "" {
		return ""
	}
	return filepath.Join(projectRoot, ".ennote", "mcp.json")
}

// ConfigDigest produces a stable digest of a version's non-secret connection
// config. Secret values are never part of the digest — only reference names
// and non-secret fields. Both the store and discovery share this function so
// candidate matching and persistence agree.
func ConfigDigest(v *domain.MCPServerProfileVersion) string {
	h := sha256.New()
	fmt.Fprintf(h, "transport=%s\n", v.Transport)
	fmt.Fprintf(h, "executable=%s\n", v.Executable)
	fmt.Fprintf(h, "argv=%v\n", v.Argv)
	fmt.Fprintf(h, "endpoint=%s\n", v.Endpoint)
	fmt.Fprintf(h, "cwd=%s\n", v.CWD)
	fmt.Fprintf(h, "timeout=%d\n", v.TimeoutMS)
	fmt.Fprintf(h, "network=%s\n", v.NetworkPolicy)
	for _, k := range sortedKeys(v.EnvLiterals) {
		fmt.Fprintf(h, "envlit:%s=%s\n", k, v.EnvLiterals[k])
	}
	for _, k := range sortedKeys(v.EnvCredentials) {
		fmt.Fprintf(h, "envcred:%s=%s\n", k, v.EnvCredentials[k])
	}
	for _, k := range sortedKeys(v.HeaderLiterals) {
		fmt.Fprintf(h, "hdrlit:%s=%s\n", k, v.HeaderLiterals[k])
	}
	for _, k := range sortedKeys(v.HeaderCreds) {
		fmt.Fprintf(h, "hdrcred:%s=%s\n", k, v.HeaderCreds[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
