package mcpclient

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBundledRegistryDefaultsToEmpty(t *testing.T) {
	reg := NewBundledRegistry()
	require.NotNil(t, reg)
	// P1/P2 ships an empty registry: bundling servers is a Phase 3 decision and
	// must never gate the generic MCP tools loop.
	assert.Empty(t, reg.List())
}

func TestBundledDescriptorShape(t *testing.T) {
	// A code-reviewed descriptor must carry the full local trust declaration:
	// publisher, license, transport, credential requirements, and the reviewed
	// risk manifest. This test pins the shape so future descriptors cannot
	// silently drop a required field.
	descriptor := BundledDescriptor{
		Slug:                   "pubmed-search",
		DisplayName:            "PubMed Search",
		Version:                "1.0.0",
		Publisher:              "Ennote",
		License:                "MIT",
		Description:            "Read-only literature search",
		Transport:              "stdio",
		Command:                "uvx",
		Args:                   []string{"mcp-server-pubmed"},
		PayloadDelivery:        "external_command",
		ReadOnlyOnly:           true,
		CredentialRequirements: []BundledCredentialRequirement{{EnvName: "PUBMED_API_KEY", Required: false}},
		RiskManifest:           []BundledToolRisk{{RemoteName: "search_articles", RiskClass: "read_only"}},
	}
	assert.Equal(t, "pubmed-search", descriptor.Slug)
	assert.Equal(t, "stdio", descriptor.Transport)
	assert.True(t, descriptor.ReadOnlyOnly)
	require.Len(t, descriptor.RiskManifest, 1)
	assert.Equal(t, "read_only", descriptor.RiskManifest[0].RiskClass)
}

func TestBundledRegistryListReturnsCopy(t *testing.T) {
	reg := NewBundledRegistry()
	list := reg.List()
	// Mutating the returned slice must not affect the registry.
	list = append(list, BundledDescriptor{Slug: "x"})
	assert.Empty(t, reg.List())
}

func validBundled() BundledDescriptor {
	return BundledDescriptor{
		Slug: "pubmed-search", DisplayName: "PubMed Search", Version: "1.0.0",
		Publisher: "Ennote", License: "MIT", Description: "read-only search",
		Transport: "stdio", Command: "uvx", Args: []string{"mcp-server-pubmed"},
		PayloadDelivery: "external_command", ReadOnlyOnly: true,
		RiskManifest: []BundledToolRisk{{RemoteName: "search_articles", RiskClass: "read_only"}},
	}
}

func TestValidateBundledDescriptor(t *testing.T) {
	require.NoError(t, ValidateBundledDescriptor(validBundled()))

	// Bad transport.
	bad := validBundled()
	bad.Transport = "magic"
	require.Error(t, ValidateBundledDescriptor(bad))

	// stdio without a command.
	bad = validBundled()
	bad.Command = ""
	require.Error(t, ValidateBundledDescriptor(bad))

	// on_demand_download without a digest.
	bad = validBundled()
	bad.PayloadDelivery = "on_demand_download"
	require.Error(t, ValidateBundledDescriptor(bad))

	// read_only claim without readOnlyOnly.
	bad = validBundled()
	bad.ReadOnlyOnly = false
	require.Error(t, ValidateBundledDescriptor(bad))

	// Invalid risk class.
	bad = validBundled()
	bad.RiskManifest[0].RiskClass = "bogus"
	require.Error(t, ValidateBundledDescriptor(bad))

	// Empty risk manifest.
	bad = validBundled()
	bad.RiskManifest = nil
	require.Error(t, ValidateBundledDescriptor(bad))
}

func TestBundledRegistryAddValidates(t *testing.T) {
	reg := NewBundledRegistry()
	require.NoError(t, reg.Add(validBundled()))
	require.Len(t, reg.List(), 1)

	// A descriptor that tries to claim read_only without readOnlyOnly must be
	// rejected; the registry stays unchanged (fail closed).
	bad := validBundled()
	bad.ReadOnlyOnly = false
	require.Error(t, reg.Add(bad))
	assert.Len(t, reg.List(), 1)
}
