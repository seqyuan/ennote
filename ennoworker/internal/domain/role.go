package domain

import (
	"encoding/json"
	"time"
)

type AgentProfileObjectKind string

const (
	AgentProfileHost AgentProfileObjectKind = "host_profile"
	AgentProfileRole AgentProfileObjectKind = "role"
)

const (
	RoleSourceManaged     = "managed"
	RoleSourceProjectFile = "project_file"
)

type RoleScope string

const (
	RoleScopeBuiltin RoleScope = "builtin"
	RoleScopeGlobal  RoleScope = "global"
	RoleScopeProject RoleScope = "project"
	// RoleScopeFlow is a Role owned by exactly one Agent Flow profile. It is
	// only resolvable from task references inside that flow (flow -> project ->
	// global/builtin precedence) and never from delegate_tasks.
	RoleScopeFlow RoleScope = "flow"
)

type RoleIdentity struct {
	ID                        string          `json:"id"`
	Handle                    string          `json:"handle"`
	Name                      string          `json:"name"`
	Description               string          `json:"description"`
	Positioning               string          `json:"positioning"`
	Icon                      string          `json:"icon"`
	Color                     string          `json:"color"`
	Scope                     RoleScope       `json:"scope"`
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

type RoleSummary struct {
	ID               string    `json:"id"`
	Handle           string    `json:"handle"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Positioning      string    `json:"positioning"`
	Icon             string    `json:"icon"`
	Color            string    `json:"color"`
	Scope            RoleScope `json:"scope"`
	ProjectID        *string   `json:"projectId,omitempty"`
	FlowID           *string   `json:"flowId,omitempty"`
	Status           string    `json:"status"`
	SourceKind       string    `json:"sourceKind"`
	SourceLocator    string    `json:"sourceLocator,omitempty"`
	CurrentVersionID *string   `json:"currentVersionId,omitempty"`
	CurrentVersion   int       `json:"currentVersion,omitempty"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type RoleVersion struct {
	ID           string         `json:"id"`
	RoleID       string         `json:"roleId"`
	Version      int            `json:"version"`
	Definition   RoleDefinition `json:"definition"`
	ConfigDigest string         `json:"configDigest"`
	Status       string         `json:"status"`
	CreatedAt    time.Time      `json:"createdAt"`
}

type RoleModelBindingMode string

const (
	RoleModelFixed       RoleModelBindingMode = "fixed"
	RoleModelOverridable RoleModelBindingMode = "overridable"
	RoleModelInherit     RoleModelBindingMode = "inherit"
)

type RoleModelBinding struct {
	Mode                    RoleModelBindingMode `json:"mode"`
	ModelProfileID          string               `json:"modelProfileId,omitempty"`
	ThinkingEffort          ThinkingEffort       `json:"thinkingEffort"`
	FallbackModelProfileIDs []string             `json:"fallbackModelProfileIds"`
	OverridableFields       []string             `json:"overridableFields"`
}

type RoleSkillMode string

const (
	RoleSkillPreload   RoleSkillMode = "preload"
	RoleSkillAvailable RoleSkillMode = "available"
)

type RoleSkillEntry struct {
	SkillID string        `json:"skillId"`
	Mode    RoleSkillMode `json:"mode"`
}

type RoleSkills struct {
	Entries []RoleSkillEntry `json:"entries"`
}

type RoleAuthority string

const (
	RoleAuthorityReadOnly RoleAuthority = "read_only"
	RoleAuthorityMutation RoleAuthority = "mutation"
)

type RoleContextMode string

const (
	RoleContextRoom  RoleContextMode = "room"
	RoleContextReply RoleContextMode = "reply"
	RoleContextFresh RoleContextMode = "fresh"
	RoleContextTask  RoleContextMode = "task_only"
)

type RolePrivateContinuity string

const (
	RoleContinuityNone RolePrivateContinuity = "none"
)

type RoleContextPolicy struct {
	DefaultMode            RoleContextMode       `json:"defaultMode"`
	AllowedModes           []RoleContextMode     `json:"allowedModes"`
	OwnExecutionContinuity RolePrivateContinuity `json:"ownExecutionContinuity"`
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

type RoleDelegationPolicy struct {
	Admission                  DelegationAdmission     `json:"admission"`
	AllowedCallerKinds         []string                `json:"allowedCallerKinds"`
	AllowedStrategies          []string                `json:"allowedStrategies"`
	MaxInvocationsPerParentRun int                     `json:"maxInvocationsPerParentRun"`
	MaxConcurrentInstances     int                     `json:"maxConcurrentInstances"`
	BudgetCeiling              DelegationBudgetCeiling `json:"budgetCeiling"`
}

type RoleDefinition struct {
	SchemaVersion     int                  `json:"schemaVersion"`
	RolePrompt        string               `json:"rolePrompt"`
	ModelBinding      RoleModelBinding     `json:"modelBinding"`
	Skills            RoleSkills           `json:"skills"`
	Authority         RoleAuthority        `json:"authority"`
	PermissionCeiling PermissionMode       `json:"permissionCeiling"`
	AllowedTools      []string             `json:"allowedTools"`
	ContextPolicy     RoleContextPolicy    `json:"contextPolicy"`
	DelegationPolicy  RoleDelegationPolicy `json:"delegationPolicy"`
	OutputContract    string               `json:"outputContract"`
	MaxLoopIterations int                  `json:"maxLoopIterations"`
}

type RoleValidationDiagnostic struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type RoleValidationResult struct {
	Valid        bool                       `json:"valid"`
	Diagnostics  []RoleValidationDiagnostic `json:"diagnostics"`
	ConfigDigest string                     `json:"configDigest,omitempty"`
}
