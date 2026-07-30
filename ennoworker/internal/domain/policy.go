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
	RiskSensitive  RiskClass = "sensitive"
)

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
