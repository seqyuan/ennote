package mcpclient

import (
	"strings"
)

// Hard limits for MCP catalog normalization and result projection. All limits
// fail closed: exceeding any of them rejects the whole catalog or truncates
// the result into an error rather than letting unbounded server data into the
// model context.
const (
	// MaxCatalogTools is the maximum number of tools a single server may
	// expose. High-cardinality servers must be selected down before a Run.
	MaxCatalogTools = 128
	// MaxToolDescriptionBytes bounds each tool description.
	MaxToolDescriptionBytes = 4096
	// MaxToolSchemaBytes bounds each tool input schema.
	MaxToolSchemaBytes = 32 * 1024
	// MaxCatalogTotalBytes bounds the sum of all tool definitions.
	MaxCatalogTotalBytes = 1 * 1024 * 1024
	// MaxPaginationPages bounds tools/list cursor iteration.
	MaxPaginationPages = 16
	// MaxResultTextBytes bounds projected text content from one tool call.
	MaxResultTextBytes = 128 * 1024
	// MaxStderrBytes bounds captured stdio stderr output.
	MaxStderrBytes = 64 * 1024
	// MaxServerCountPerRun bounds MCP servers in one Run.
	MaxServerCountPerRun = 16
	// MaxToolsPerRun bounds total MCP tools exposed in one Run.
	MaxToolsPerRun = 64
	// MaxStdioArgv bounds argv entries for stdio servers.
	MaxStdioArgv = 64
	// MaxHeaderBytes bounds literal/credential header value size.
	MaxHeaderBytes = 8192
	// MaxEndpointBytes bounds configured HTTP endpoint length.
	MaxEndpointBytes = 4096
)

// SecretLikeEnvKeyPatterns are substrings (uppercased) that mark an environment
// variable as secret-like: it must never receive a literal value and requires an
// explicit credential reference (fail closed). Substring matching catches common
// names such as GITHUB_TOKEN, OPENAI_API_KEY, PUBMED_API_KEY, MY_PASSWORD and
// SERVICE_CREDENTIAL that an exact-name map would miss.
var SecretLikeEnvKeyPatterns = []string{
	"API_KEY", "APITOKEN", "ACCESS_TOKEN", "TOKEN", "SECRET", "PASSWORD",
	"PASSWD", "PRIVATE_KEY", "SIGNING_KEY", "AUTHORIZATION", "CREDENTIAL",
	"BOOTSTRAP_TOKEN",
}

// SecretLikeEnvKeys is kept as an exact-set for callers that need O(1) exact
// checks; prefer IsSecretLikeEnvName for all validation paths.
var SecretLikeEnvKeys = map[string]bool{
	"API_KEY": true, "API_TOKEN": true, "TOKEN": true, "ACCESS_TOKEN": true,
	"SECRET": true, "PASSWORD": true, "PASS": true, "PRIVATE_KEY": true,
	"AUTHORIZATION": true, "CREDENTIAL": true, "CREDENTIALS": true,
	"ENNOTE_BOOTSTRAP_TOKEN": true,
}

// IsSecretLikeEnvName reports whether an environment variable name must be
// treated as secret-like. Substring patterns cover Worker keys such as
// ENNOTE_BOOTSTRAP_TOKEN as well as common third-party credential names.
func IsSecretLikeEnvName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	for _, pat := range SecretLikeEnvKeyPatterns {
		if strings.Contains(upper, pat) {
			return true
		}
	}
	return false
}
