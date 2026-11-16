package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type AgentProfileObjectKind string

const (
	AgentProfileHost AgentProfileObjectKind = "host_profile"
	AgentProfileHero AgentProfileObjectKind = "hero"
)

const (
	HeroSourceManaged     = "managed"
	HeroSourceProjectFile = "project_file"
)

type HeroScope string

const (
	HeroScopeBuiltin HeroScope = "builtin"
	HeroScopeGlobal  HeroScope = "global"
	HeroScopeProject HeroScope = "project"
	// HeroScopeFlow is a Hero owned by exactly one Agent Flow profile. It is
	// only resolvable from task references inside that flow (flow -> project ->
	// global/builtin precedence) and never from delegate_tasks.
	HeroScopeFlow HeroScope = "flow"
)

type HeroIdentity struct {
	ID                        string          `json:"id"`
	Handle                    string          `json:"handle"`
	Name                      string          `json:"name"`
	Description               string          `json:"description"`
	Positioning               string          `json:"positioning"`
	Icon                      string          `json:"icon"`
	Color                     string          `json:"color"`
	Scope                     HeroScope       `json:"scope"`
	ProjectID                 *string         `json:"projectId,omitempty"`
	FlowID                    *string         `json:"flowId,omitempty"`
	Status                    string          `json:"status"`
	SourceKind                string          `json:"sourceKind"`
	SourceLocator             string          `json:"sourceLocator,omitempty"`
	SourceDigest              string          `json:"sourceDigest,omitempty"`
	Draft                     json.RawMessage `json:"draft"`
	DraftRevision             int             `json:"draftRevision"`
	CurrentVersionID          *string         `json:"currentVersionId,omitempty"`
	CurrentVersion            int             `json:"currentVersion,omitempty"`
	DelegationEnabled         bool            `json:"delegationEnabled"`
	DelegationRevocationEpoch int             `json:"delegationRevocationEpoch"`
	DelegationDisabledAt      *time.Time      `json:"delegationDisabledAt,omitempty"`
	CreatedAt                 time.Time       `json:"createdAt"`
	UpdatedAt                 time.Time       `json:"updatedAt"`
}

type HeroSummary struct {
	ID               string    `json:"id"`
	Handle           string    `json:"handle"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Positioning      string    `json:"positioning"`
	Icon             string    `json:"icon"`
	Color            string    `json:"color"`
	Scope            HeroScope `json:"scope"`
	ProjectID        *string   `json:"projectId,omitempty"`
	FlowID           *string   `json:"flowId,omitempty"`
	Status           string    `json:"status"`
	SourceKind       string    `json:"sourceKind"`
	SourceLocator    string    `json:"sourceLocator,omitempty"`
	CurrentVersionID *string   `json:"currentVersionId,omitempty"`
	CurrentVersion   int       `json:"currentVersion,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type HeroVersion struct {
	ID           string         `json:"id"`
	HeroID       string         `json:"heroId"`
	Version      int            `json:"version"`
	Definition   HeroDefinition `json:"definition"`
	ConfigDigest string         `json:"configDigest"`
	Status       string         `json:"status"`
	CreatedAt    time.Time      `json:"createdAt"`
}

type HeroModelBindingMode string

const (
	HeroModelFixed       HeroModelBindingMode = "fixed"
	HeroModelOverridable HeroModelBindingMode = "overridable"
	HeroModelInherit     HeroModelBindingMode = "inherit"
)

type HeroModelBinding struct {
	Mode                    HeroModelBindingMode `json:"mode"`
	ModelProfileID          string               `json:"modelProfileId,omitempty"`
	ThinkingEffort          ThinkingEffort       `json:"thinkingEffort"`
	FallbackModelProfileIDs []string             `json:"fallbackModelProfileIds"`
	OverridableFields       []string             `json:"overridableFields"`
}

type HeroSkillMode string

const (
	HeroSkillPreload   HeroSkillMode = "preload"
	HeroSkillAvailable HeroSkillMode = "available"
)

type HeroSkillEntry struct {
	SkillID string        `json:"skillId"`
	Mode    HeroSkillMode `json:"mode"`
}

type HeroSkills struct {
	Entries []HeroSkillEntry `json:"entries"`
}

type HeroAuthority string

const (
	HeroAuthorityReadOnly HeroAuthority = "read_only"
	HeroAuthorityMutation HeroAuthority = "mutation"
)

type HeroContextMode string

const (
	HeroContextRoom  HeroContextMode = "room"
	HeroContextReply HeroContextMode = "reply"
	HeroContextFresh HeroContextMode = "fresh"
	HeroContextTask  HeroContextMode = "task_only"
)

type HeroPrivateContinuity string

const (
	HeroContinuityNone HeroPrivateContinuity = "none"
)

type HeroContextPolicy struct {
	DefaultMode            HeroContextMode       `json:"defaultMode"`
	AllowedModes           []HeroContextMode     `json:"allowedModes"`
	OwnExecutionContinuity HeroPrivateContinuity `json:"ownExecutionContinuity"`
}

type DelegationAdmission string

const (
	DelegationDenied           DelegationAdmission = "denied"
	DelegationApprovalRequired DelegationAdmission = "approval_required"
	DelegationAutoWithinBudget DelegationAdmission = "auto_within_budget"
)

type DelegationBudgetCeiling struct {
	MaxModelCalls    int   `json:"maxModelCalls"`
	MaxToolCalls     int   `json:"maxToolCalls"`
	MaxTotalTokens   int64 `json:"maxTotalTokens"`
	MaxOutputTokens  int64 `json:"maxOutputTokens"`
	MaxCostUSDMicros int64 `json:"maxCostUsdMicros"`
	MaxWallTimeMS    int64 `json:"maxWallTimeMs"`
}

type HeroDelegationPolicy struct {
	Admission                  DelegationAdmission     `json:"admission"`
	AllowedCallerKinds         []string                `json:"allowedCallerKinds"`
	AllowedStrategies          []string                `json:"allowedStrategies"`
	MaxInvocationsPerParentRun int                     `json:"maxInvocationsPerParentRun"`
	MaxConcurrentInstances     int                     `json:"maxConcurrentInstances"`
	BudgetCeiling              DelegationBudgetCeiling `json:"budgetCeiling"`
}

// HeroReportContract is the versioned shape a Hero promises for the terminal
// submit_result of one of its Runs. OutputContract stays the bare label the
// delegation admission compares for string equality; this is the part of the
// promise a machine can check, and ValidateReportAgainstContract is the single
// implementation of it (the Graph fold and the delegation completion both call
// it, so a child cannot pass one path and fail the other).
//
// The yaml tags exist because the Portable Hero document is authored as YAML
// frontmatter and encodes through yaml.Marshal; without them a Hero file would
// round-trip through the lowercased Go field names.
type HeroReportContract struct {
	// Version is the contract revision. Only HeroReportContractV1 is supported;
	// an unsupported version is refused at write time and fails closed at
	// enforcement time, never silently ignored.
	Version int `json:"version" yaml:"version"`
	// SummaryMaxBytes bounds the submit_result summary. The Hero source validator
	// requires an explicit 1..4096 value, so a written contract always states its
	// cap; enforcement reads 0 as "tighten nothing" rather than as a second,
	// implicit rule.
	SummaryMaxBytes int `json:"summaryMaxBytes,omitempty" yaml:"summaryMaxBytes,omitempty"`
	// RequireArtifacts demands at least one artifact reference in the report.
	RequireArtifacts bool `json:"requireArtifacts,omitempty" yaml:"requireArtifacts,omitempty"`
	// PayloadFields are the top-level payload keys the report must carry. When
	// non-empty the payload must also be a JSON object.
	PayloadFields []string `json:"payloadFields,omitempty" yaml:"payloadFields,omitempty"`
}

const (
	// HeroReportContractV1 is the only report contract version this Worker can
	// enforce.
	HeroReportContractV1 = 1
	// HeroReportSummaryMaxBytesCeiling is the global submit_result summary cap.
	// A contract can tighten it and never exceed it.
	HeroReportSummaryMaxBytesCeiling = 4096
	// HeroReportPayloadFieldLimit bounds the required payload keys of one
	// contract, so enforcement stays a bounded loop.
	HeroReportPayloadFieldLimit = 16
)

// CloneHeroReportContract deep-copies a contract (nil-safe) so a frozen
// definition never shares a payload-field slice with the document that produced
// it.
func CloneHeroReportContract(contract *HeroReportContract) *HeroReportContract {
	if contract == nil {
		return nil
	}
	clone := *contract
	clone.PayloadFields = append([]string(nil), contract.PayloadFields...)
	return &clone
}

// IsHeroReportPayloadField reports whether the value is a plain identifier, the
// only shape a required payload key may take (^[A-Za-z_][A-Za-z0-9_]*$).
func IsHeroReportPayloadField(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && index > 0:
		default:
			return false
		}
	}
	return true
}

// ValidateReportAgainstContract decides whether a delegated child's terminal
// report keeps the shape its Hero promised. A nil contract returns nil: a Hero
// without a report contract keeps exactly the pre-existing behaviour (the
// outputContract label equality rule is untouched). The returned error names the
// first violation in reading order, so a fold can record it verbatim.
func ValidateReportAgainstContract(result *SubmitResult, contract *HeroReportContract) error {
	if contract == nil {
		return nil
	}
	if contract.Version != HeroReportContractV1 {
		return fmt.Errorf("report contract version %d is not supported (this Worker enforces %d)",
			contract.Version, HeroReportContractV1)
	}
	if result == nil {
		return fmt.Errorf("report contract v%d requires a terminal report, but none was recorded", contract.Version)
	}
	if contract.SummaryMaxBytes > 0 && len(result.Summary) > contract.SummaryMaxBytes {
		return fmt.Errorf("summary is %d bytes, report contract v%d allows at most %d",
			len(result.Summary), contract.Version, contract.SummaryMaxBytes)
	}
	if contract.RequireArtifacts && len(result.ArtifactRefs) == 0 {
		return fmt.Errorf("report contract v%d requires at least one artifact, the report has none",
			contract.Version)
	}
	if len(contract.PayloadFields) == 0 {
		return nil
	}
	// An empty payload is not an object, so a report that omits payload entirely
	// cannot carry a required key either. The message names the required keys, so
	// the child (or its author) learns what the promise asked for even though the
	// violation is the shape.
	trimmed := bytes.TrimSpace(result.Payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("report contract v%d requires payload to be a JSON object (payloadFields: %s)",
			contract.Version, strings.Join(contract.PayloadFields, ", "))
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return fmt.Errorf("report contract v%d requires payload to be a JSON object: %w", contract.Version, err)
	}
	for _, field := range contract.PayloadFields {
		if _, present := payload[field]; !present {
			return fmt.Errorf("payload is missing required field %q (report contract v%d requires: %s)",
				field, contract.Version, strings.Join(contract.PayloadFields, ", "))
		}
	}
	return nil
}

type HeroDefinition struct {
	SchemaVersion     int              `json:"schemaVersion"`
	HeroPrompt        string           `json:"heroPrompt"`
	ModelBinding      HeroModelBinding `json:"modelBinding"`
	Skills            HeroSkills       `json:"skills"`
	Authority         HeroAuthority    `json:"authority"`
	PermissionCeiling PermissionMode   `json:"permissionCeiling"`
	AllowedTools      []string         `json:"allowedTools"`
	// AcceptanceKinds is the frozen copy of the Hero source's acceptanceKinds:
	// the acceptance leaf kinds a Graph task may declare when it runs this Hero.
	// Empty means "the kernel's whole vocabulary" (the pre-existing behaviour),
	// so a Hero written before the field existed keeps publishing unchanged.
	AcceptanceKinds  []string             `json:"acceptanceKinds,omitempty"`
	ContextPolicy    HeroContextPolicy    `json:"contextPolicy"`
	DelegationPolicy HeroDelegationPolicy `json:"delegationPolicy"`
	// OutputContract is the bare equality label the delegation admission
	// compares. ReportContract is the enforceable shape of the same promise.
	OutputContract    string              `json:"outputContract"`
	ReportContract    *HeroReportContract `json:"reportContract,omitempty"`
	MaxLoopIterations int                 `json:"maxLoopIterations"`
}

type HeroValidationDiagnostic struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type HeroValidationResult struct {
	Valid        bool                       `json:"valid"`
	Diagnostics  []HeroValidationDiagnostic `json:"diagnostics"`
	ConfigDigest string                     `json:"configDigest,omitempty"`
}
