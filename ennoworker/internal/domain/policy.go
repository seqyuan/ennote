package domain

import (
	"encoding/json"
	"time"
)

type PolicyProfile struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      PolicyKind      `json:"kind"`
	Version   int             `json:"version"`
	Config    json.RawMessage `json:"config"`
	Status    string          `json:"status"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type PermissionMode string

const (
	PermissionDiscuss PermissionMode = "discuss"
	PermissionAsk     PermissionMode = "ask"
	PermissionAuto    PermissionMode = "auto"
)

type RiskClass string

const (
	RiskReadOnly   RiskClass = "read_only"
	RiskLocalWrite RiskClass = "local_write"
	RiskShell      RiskClass = "shell"
	RiskExternal   RiskClass = "external"
	RiskDelegation RiskClass = "delegation"
	RiskSensitive  RiskClass = "sensitive"
)

// IsValidRiskClass reports whether value is one of the supported RiskClass
// values. Empty and unknown values are invalid; tools declaring them fail
// Registry registration (fail closed).
func IsValidRiskClass(value RiskClass) bool {
	switch value {
	case RiskReadOnly, RiskLocalWrite, RiskShell,
		RiskExternal, RiskDelegation, RiskSensitive:
		return true
	default:
		return false
	}
}

type ToolPolicyConfig struct {
	Mode                     string              `json:"mode"`
	AllowedTools             []string            `json:"allowedTools,omitempty"`
	AllowedExecutables       []string            `json:"allowedExecutables,omitempty"`
	DeniedSubcommands        map[string][]string `json:"deniedSubcommands,omitempty"`
	AllowPipes               bool                `json:"allowPipes,omitempty"`
	AllowCommandSubstitution bool                `json:"allowCommandSubstitution,omitempty"`
	AllowedWriteRoots        []string            `json:"allowedWriteRoots,omitempty"`
	MaxTimeoutSeconds        int                 `json:"maxTimeoutSeconds,omitempty"`
	RedactPatterns           []string            `json:"redactPatterns,omitempty"`
	MaxInlineToolResultBytes int64               `json:"maxInlineToolResultBytes,omitempty"` // 0 = spill disabled (design 二 P2)
}

type TurnPolicyConfig struct {
	Mode                     string   `json:"mode"`
	CandidateModelProfileIDs []string `json:"candidateModelProfileIds,omitempty"`
	Threshold                float64  `json:"threshold,omitempty"`
}

type VisionPolicyConfig struct {
	Mode                     string `json:"mode"`
	DescriptorModelProfileID string `json:"descriptorModelProfileId,omitempty"`
	PromptVersion            string `json:"promptVersion,omitempty"`
	MaxImageBytes            int64  `json:"maxImageBytes,omitempty"`
	MaxPixels                int64  `json:"maxPixels,omitempty"`
}
