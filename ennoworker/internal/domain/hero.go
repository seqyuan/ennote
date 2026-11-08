package domain

import (
	"encoding/json"
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
	// HeroScopeFlow is a Role owned by exactly one Agent Flow profile. It is
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

type HeroDefinition struct {
	SchemaVersion     int                  `json:"schemaVersion"`
	HeroPrompt        string               `json:"heroPrompt"`
	ModelBinding      HeroModelBinding     `json:"modelBinding"`
	Skills            HeroSkills           `json:"skills"`
	Authority         HeroAuthority        `json:"authority"`
	PermissionCeiling PermissionMode       `json:"permissionCeiling"`
	AllowedTools      []string             `json:"allowedTools"`
	ContextPolicy     HeroContextPolicy    `json:"contextPolicy"`
	DelegationPolicy  HeroDelegationPolicy `json:"delegationPolicy"`
	OutputContract    string               `json:"outputContract"`
	MaxLoopIterations int                  `json:"maxLoopIterations"`
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
