package mcpclient

import (
	"fmt"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// BundledDescriptor is Ennote's local trust declaration for an MCP server it
// ships metadata for. It is metadata ONLY: the payload is delivered separately
// (embedded / on-demand download / external command) and the server is never
// auto-installed, auto-bound, or auto-enabled.
type BundledDescriptor struct {
	// Slug is the immutable server slug (matches profile slug rules).
	Slug string `json:"slug"`
	// DisplayName is the human-readable name.
	DisplayName string `json:"displayName"`
	// Version is the descriptor version (not the MCP protocol version).
	Version string `json:"version"`
	// Publisher identifies the metadata publisher.
	Publisher string `json:"publisher"`
	// License is SPDX expression or "unknown".
	License string `json:"license"`
	// Description is a short summary shown in the settings UI.
	Description string `json:"description"`
	// Transport is one of domain.MCPTransport*.
	Transport string `json:"transport"`
	// Command/Args define the stdio launch command when the payload is
	// available locally (e.g. an installed CLI).
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Endpoint is the URL for HTTP transports.
	Endpoint string `json:"endpoint,omitempty"`
	// CredentialRequirements lists env names the server needs, and whether a
	// credential reference is required (vs optional).
	CredentialRequirements []BundledCredentialRequirement `json:"credentialRequirements,omitempty"`
	// PayloadDelivery is embedded | on_demand_download | external_command.
	PayloadDelivery string `json:"payloadDelivery"`
	// PayloadDigest is the SHA-256 of the on-demand payload artifact (empty
	// for external_command / embedded).
	PayloadDigest string `json:"payloadDigest,omitempty"`
	// RiskManifest declares the reviewed local RiskClass per tool. This is
	// Ennote's own code-reviewed declaration; it is the ONLY place a bundled
	// server may claim a RiskClass lower than external.
	RiskManifest []BundledToolRisk `json:"riskManifest,omitempty"`
	// ReadOnlyOnly must be true for any descriptor claiming RiskReadOnly.
	ReadOnlyOnly bool `json:"readOnlyOnly"`
}

// BundledCredentialRequirement describes one environment variable a bundled
// server expects.
type BundledCredentialRequirement struct {
	EnvName  string `json:"envName"`
	Required bool   `json:"required"`
}

// BundledToolRisk is the reviewed risk declaration for one tool.
type BundledToolRisk struct {
	RemoteName string `json:"remoteName"`
	RiskClass  string `json:"riskClass"` // one of the domain.Risk* strings
}

// BundledRegistry is the embedded, code-reviewed set of bundled descriptors.
// Phase 1/2 ship with an empty registry: bundling scientific servers is a
// Phase 3 ecosystem decision and must never gate the generic MCP tools loop.
// Descriptors are added via Add, which validates every field before the
// descriptor becomes discoverable.
type BundledRegistry struct {
	descriptors []BundledDescriptor
}

// NewBundledRegistry returns the embedded registry. The P1/P2 catalog is
// intentionally empty; adding descriptors requires a code review of the
// risk manifest plus a separate supply-chain decision.
func NewBundledRegistry() *BundledRegistry {
	return &BundledRegistry{descriptors: []BundledDescriptor{}}
}

// Add validates and registers a bundled descriptor. It is the ONLY way to
// register descriptors: unvalidated metadata can never reach discovery.
func (r *BundledRegistry) Add(descriptors ...BundledDescriptor) error {
	for _, d := range descriptors {
		if err := ValidateBundledDescriptor(d); err != nil {
			return fmt.Errorf("bundled descriptor %q: %w", d.Slug, err)
		}
	}
	r.descriptors = append(r.descriptors, descriptors...)
	return nil
}

// MaxBundledDescriptors bounds how many servers Ennote may ship metadata for.
// Bundling is a reviewed ecosystem decision, never an unbounded catalog.
const MaxBundledDescriptors = 3

// ValidateBundledDescriptor enforces the full local trust declaration:
// transport consistency, payload delivery mode, digest presence for downloads,
// a code-reviewed risk manifest with legal RiskClass values, and the invariant
// that any descriptor claiming a read-only risk must be ReadOnlyOnly.
func ValidateBundledDescriptor(d BundledDescriptor) error {
	if d.Slug == "" {
		return fmt.Errorf("slug is required")
	}
	if err := validateMCPSlugLocal(d.Slug); err != nil {
		return err
	}
	if d.DisplayName == "" || d.Publisher == "" || d.Version == "" {
		return fmt.Errorf("displayName, publisher and version are required")
	}
	switch d.Transport {
	case domain.MCPTransportStdio:
		if strings.TrimSpace(d.Command) == "" {
			return fmt.Errorf("stdio descriptor requires a command")
		}
		if len(d.Args) > MaxStdioArgv {
			return fmt.Errorf("args exceed %d entries", MaxStdioArgv)
		}
	case domain.MCPTransportStreamableHTTP, domain.MCPTransportLegacySSE:
		if strings.TrimSpace(d.Endpoint) == "" {
			return fmt.Errorf("http descriptor requires an endpoint")
		}
	default:
		return fmt.Errorf("unsupported transport %q", d.Transport)
	}
	switch d.PayloadDelivery {
	case "embedded", "on_demand_download", "external_command":
	default:
		return fmt.Errorf("unsupported payload delivery mode %q", d.PayloadDelivery)
	}
	if d.PayloadDelivery == "on_demand_download" && len(d.PayloadDigest) != 64 {
		return fmt.Errorf("on-demand download requires a SHA-256 payload digest")
	}
	if len(d.RiskManifest) == 0 {
		return fmt.Errorf("risk manifest must declare at least one tool")
	}
	for _, risk := range d.RiskManifest {
		if risk.RemoteName == "" {
			return fmt.Errorf("risk manifest entry requires a remote name")
		}
		if !domain.IsValidRiskClass(domain.RiskClass(risk.RiskClass)) {
			return fmt.Errorf("risk manifest entry %q has invalid risk class %q", risk.RemoteName, risk.RiskClass)
		}
		if domain.RiskClass(risk.RiskClass) == domain.RiskReadOnly && !d.ReadOnlyOnly {
			return fmt.Errorf("tool %q claims read_only but the descriptor is not readOnlyOnly", risk.RemoteName)
		}
	}
	return nil
}

// List returns a copy of the bundled descriptors.
func (r *BundledRegistry) List() []BundledDescriptor {
	out := make([]BundledDescriptor, len(r.descriptors))
	copy(out, r.descriptors)
	return out
}
