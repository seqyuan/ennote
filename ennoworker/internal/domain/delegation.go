package domain

import (
	"encoding/json"
	"time"
)

// DelegationStrategy defines how children are executed.
type DelegationStrategy string

const (
	DelegationStrategySingle   DelegationStrategy = "single"
	DelegationStrategyParallel DelegationStrategy = "parallel"
)

// DelegationGroupStatus tracks the lifecycle of a delegation group.
type DelegationGroupStatus string

const (
	DelegationGroupPending          DelegationGroupStatus = "pending"
	DelegationGroupWaitingAdmission DelegationGroupStatus = "waiting_admission"
	DelegationGroupWaitingChildren  DelegationGroupStatus = "waiting_children"
	DelegationGroupSettled          DelegationGroupStatus = "settled"
	DelegationGroupCancelled        DelegationGroupStatus = "cancelled"
)

// DelegationItemStatus tracks one child assignment.
type DelegationItemStatus string

const (
	DelegationItemPending   DelegationItemStatus = "pending"
	DelegationItemRunning   DelegationItemStatus = "running"
	DelegationItemTerminal  DelegationItemStatus = "succeeded"
	DelegationItemFailed    DelegationItemStatus = "failed"
	DelegationItemCancelled DelegationItemStatus = "cancelled"
	DelegationItemNotAuth   DelegationItemStatus = "not_authorized"
)

// DelegationGroup represents one parent tool call's set of children.
type DelegationGroup struct {
	ID               string                `json:"id"`
	ParentRunID      string                `json:"parentRunId"`
	ParentToolCallID string                `json:"parentToolCallId"`
	Strategy         DelegationStrategy    `json:"strategy"`
	Status           DelegationGroupStatus `json:"status"`
	CreatedAt        time.Time             `json:"createdAt"`
}

// DelegationItem is one child assignment within a group.
// DelegationActivityPage is the parent-visible, read-only projection used by
// nested activity UI. It intentionally excludes child prompts and transcripts.
type DelegationActivityPage struct {
	ParentRunID string                    `json:"parentRunId"`
	Groups      []DelegationActivityGroup `json:"groups"`
}

type DelegationActivityGroup struct {
	ID               string                    `json:"id"`
	ParentToolCallID string                    `json:"parentToolCallId"`
	Strategy         DelegationStrategy        `json:"strategy"`
	Status           DelegationGroupStatus     `json:"status"`
	Children         []DelegationChildActivity `json:"children"`
	CreatedAt        time.Time                 `json:"createdAt"`
}

type DelegationChildActivity struct {
	ItemID          string               `json:"itemId"`
	ChildRunID      string               `json:"childRunId,omitempty"`
	Name            string               `json:"name"`
	RoleHandle      string               `json:"roleHandle"`
	RoleDisplayName string               `json:"roleDisplayName"`
	ItemStatus      DelegationItemStatus `json:"itemStatus"`
	RunStatus       RunStatus            `json:"runStatus,omitempty"`
	Result          *SubmitResult        `json:"result,omitempty"`
	ErrorCode       string               `json:"errorCode,omitempty"`
	ErrorMessage    string               `json:"errorMessage,omitempty"`
	CreatedAt       time.Time            `json:"createdAt"`
}

type DelegationItem struct {
	ID             string               `json:"id"`
	GroupID        string               `json:"groupId"`
	ChildRunID     *string              `json:"childRunId,omitempty"`
	Name           string               `json:"name"`
	RoleVersionID  string               `json:"roleVersionId"`
	AssignmentJSON json.RawMessage      `json:"assignment"`
	OutputContract string               `json:"outputContract"`
	BudgetJSON     json.RawMessage      `json:"budget"`
	ResultJSON     json.RawMessage      `json:"result,omitempty"`
	Status         DelegationItemStatus `json:"status"`
	Ordinal        int                  `json:"ordinal"`
	CreatedAt      time.Time            `json:"createdAt"`
}

// BudgetCeilingJSON is the wire representation of a child budget ceiling.
type BudgetCeilingJSON struct {
	MaxModelCalls   int   `json:"maxModelCalls"`
	MaxToolCalls    int   `json:"maxToolCalls"`
	MaxTotalTokens  int64 `json:"maxTotalTokens"`
	MaxOutputTokens int64 `json:"maxOutputTokens"`
	MaxCostMicros   int64 `json:"maxCostUsdMicros"`
	MaxWallTimeMS   int64 `json:"maxWallTimeMs"`
}

// RunBudget is the per-Run budget ledger.
type RunBudget struct {
	RunID                 string     `json:"runId"`
	MaxModelCalls         int        `json:"maxModelCalls"`
	MaxToolCalls          int        `json:"maxToolCalls"`
	MaxTotalTokens        int64      `json:"maxTotalTokens"`
	MaxOutputTokens       int64      `json:"maxOutputTokens"`
	MaxCostUSDMicros      int64      `json:"maxCostUsdMicros"`
	MaxWallTimeMS         int64      `json:"maxWallTimeMs"`
	ConsumedModelCalls    int        `json:"consumedModelCalls"`
	ConsumedToolCalls     int        `json:"consumedToolCalls"`
	ConsumedTokens        int64      `json:"consumedTokens"`
	ConsumedOutputTokens  int64      `json:"consumedOutputTokens"`
	ConsumedCostUSDMicros int64      `json:"consumedCostUsdMicros"`
	StartedAt             *time.Time `json:"startedAt,omitempty"`
	ReservedAt            time.Time  `json:"reservedAt"`
}

// SubmitStatus is the terminal contract status returned by submit_result.
type SubmitStatus string

const (
	SubmitCompleted  SubmitStatus = "completed"
	SubmitBlocked    SubmitStatus = "blocked"
	SubmitNeedsInput SubmitStatus = "needs_input"
)

// SubmitResult is the structured terminal output of a child Run.
type SubmitResult struct {
	Status       SubmitStatus        `json:"status"`
	Summary      string              `json:"summary"`
	ArtifactRefs []ArtifactReference `json:"artifactRefs,omitempty"`
	Payload      json.RawMessage     `json:"payload,omitempty"`
}
