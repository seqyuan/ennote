package mcpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var remoteToolNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

// NormalizeCatalog converts raw SDK tools into bounded, digest-bound catalog
// entries. Any collision, invalid schema, or limit breach rejects the entire
// catalog (fail closed): one misbehaving tool must not slip into a Run.
func NormalizeCatalog(serverSlug string, raw []*mcp.Tool) ([]domain.MCPCatalogEntry, error) {
	entries := make([]domain.MCPCatalogEntry, 0, len(raw))
	exposed := map[string]string{} // exposed name -> remote name
	var total int
	for _, tool := range raw {
		if tool == nil {
			return nil, fmt.Errorf("nil tool in catalog")
		}
		if !remoteToolNameRe.MatchString(tool.Name) {
			return nil, fmt.Errorf("tool name %q contains illegal characters", tool.Name)
		}
		schemaJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal input schema for %s: %w", tool.Name, err)
		}
		if len(schemaJSON) > MaxToolSchemaBytes {
			return nil, fmt.Errorf("input schema for %s exceeds %d bytes", tool.Name, MaxToolSchemaBytes)
		}
		desc := SanitizeDescription(tool.Description)
		exposedName := serverSlug + "__" + tool.Name
		if len(exposedName) > 256 {
			return nil, fmt.Errorf("exposed tool name too long for %s", tool.Name)
		}
		if prior, dup := exposed[exposedName]; dup {
			return nil, fmt.Errorf("normalized tool name collision: %s maps to both %s and %s", exposedName, prior, tool.Name)
		}
		exposed[exposedName] = tool.Name
		entry := domain.MCPCatalogEntry{
			RemoteName:  tool.Name,
			ExposedName: exposedName,
			Description: desc,
			InputSchema: schemaJSON,
		}
		if tool.OutputSchema != nil {
			outJSON, err := json.Marshal(tool.OutputSchema)
			if err == nil && len(outJSON) <= MaxToolSchemaBytes {
				entry.OutputSchema = outJSON
			}
		}
		if tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
			entry.ReadOnlyHint = true
		}
		entry.Digest = entryDigest(entry)
		entries = append(entries, entry)
		total += len(entry.RemoteName) + len(entry.ExposedName) + len(entry.Description) + len(schemaJSON)
		if total > MaxCatalogTotalBytes {
			return nil, fmt.Errorf("catalog exceeds total size limit")
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("server exposed an empty tool catalog")
	}
	return entries, nil
}

func entryDigest(e domain.MCPCatalogEntry) string {
	h := sha256.New()
	h.Write([]byte(e.RemoteName))
	h.Write([]byte{0})
	h.Write([]byte(e.ExposedName))
	h.Write([]byte{0})
	h.Write([]byte(e.Description))
	h.Write([]byte{0})
	h.Write(e.InputSchema)
	return hex.EncodeToString(h.Sum(nil))
}

// NormalizedRemoteNames returns the remote names in catalog order.
func NormalizedRemoteNames(entries []domain.MCPCatalogEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.RemoteName
	}
	return names
}

// LookupEntry finds a normalized entry by exposed name.
func LookupEntry(entries []domain.MCPCatalogEntry, exposedName string) (domain.MCPCatalogEntry, bool) {
	for _, e := range entries {
		if e.ExposedName == exposedName {
			return e, true
		}
	}
	return domain.MCPCatalogEntry{}, false
}

// BuildToolDefinition converts a normalized entry into a domain tool
// definition with the conservative local RiskClass.
func BuildToolDefinition(e domain.MCPCatalogEntry, risk domain.RiskClass) domain.ToolDefinition {
	return domain.ToolDefinition{
		Name:        e.ExposedName,
		Description: e.Description,
		Parameters:  e.InputSchema,
		RiskClass:   risk,
	}
}

// TrimDescription bounds a server-supplied description.
func TrimDescription(s string) string { return SanitizeDescription(s) }

// SanitizeDescription bounds a server-supplied description.
func SanitizeDescription(desc string) string {
	if len(desc) > MaxToolDescriptionBytes {
		return desc[:MaxToolDescriptionBytes]
	}
	return desc
}
