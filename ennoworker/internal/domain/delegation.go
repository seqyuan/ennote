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

// DelegationGenerationKind is why a generation exists.
type DelegationGenerationKind string

const (
	DelegationGenerationInitial  DelegationGenerationKind = "initial"
	DelegationGenerationRetry    DelegationGenerationKind = "retry"
	DelegationGenerationInput    DelegationGenerationKind = "input"
	DelegationGenerationFollowUp DelegationGenerationKind = "follow_up"
)

// DelegationGenerationStatus tracks one generation lifecycle.
type DelegationGenerationStatus string

const (
	DelegationGenerationAwaitingAuthorization DelegationGenerationStatus = "awaiting_authorization"
	DelegationGenerationQueued                DelegationGenerationStatus = "queued"
	DelegationGenerationRunning               DelegationGenerationStatus = "running"
	DelegationGenerationSettled               DelegationGenerationStatus = "settled"
	DelegationGenerationFailed                DelegationGenerationStatus = "failed"
	DelegationGenerationCancelled             DelegationGenerationStatus = "cancelled"
)

func (s DelegationGenerationStatus) Terminal() bool {
	switch s {
	case DelegationGenerationSettled, DelegationGenerationFailed, DelegationGenerationCancelled:
		return true
	default:
		return false
	}
}

// CanTransitionGeneration reports legal generation transitions. Settled is
// terminal; failed/cancelled are terminal for later generations, but a
// generation-0 initial failure may be superseded by an explicit retry.
func CanTransitionGeneration(from, to DelegationGenerationStatus) bool {
	if from == to {
		return true
	}
	if from.Terminal() {
		return false
	}
	switch from {
	case DelegationGenerationAwaitingAuthorization:
		return to == DelegationGenerationQueued || to == DelegationGenerationCancelled || to == DelegationGenerationFailed
	case DelegationGenerationQueued:
		return to == DelegationGenerationRunning || to == DelegationGenerationCancelled || to == DelegationGenerationFailed
	case DelegationGenerationRunning:
		return to == DelegationGenerationSettled || to == DelegationGenerationFailed || to == DelegationGenerationCancelled
	default:
		return false
	}
}

// DelegationAttemptStatus tracks one child Run execution.
type DelegationAttemptStatus string

const (
	DelegationAttemptQueued        DelegationAttemptStatus = "queued"
	DelegationAttemptRunning       DelegationAttemptStatus = "running"
	DelegationAttemptSucceeded     DelegationAttemptStatus = "succeeded"
	DelegationAttemptBlocked       DelegationAttemptStatus = "blocked"
	DelegationAttemptNeedsInput    DelegationAttemptStatus = "needs_input"
	DelegationAttemptNotAuthorized DelegationAttemptStatus = "not_authorized"
	DelegationAttemptFailed        DelegationAttemptStatus = "failed"
	DelegationAttemptCancelled     DelegationAttemptStatus = "cancelled"
	DelegationAttemptInterrupted   DelegationAttemptStatus = "interrupted"
)

func (s DelegationAttemptStatus) Terminal() bool {
	switch s {
	case DelegationAttemptSucceeded, DelegationAttemptBlocked, DelegationAttemptNeedsInput,
		DelegationAttemptNotAuthorized, DelegationAttemptFailed, DelegationAttemptCancelled,
		DelegationAttemptInterrupted:
		return true
	default:
		return false
	}
}

// CanTransitionAttempt reports legal attempt transitions. Terminal attempts
// are immutable.
func CanTransitionAttempt(from, to DelegationAttemptStatus) bool {
	if from == to {
		return true
	}
	if from.Terminal() {
		return false
	}
	switch from {
	case DelegationAttemptQueued:
		return to == DelegationAttemptRunning || to == DelegationAttemptCancelled || to == DelegationAttemptInterrupted
	case DelegationAttemptRunning:
		return to == DelegationAttemptSucceeded || to == DelegationAttemptBlocked ||
			to == DelegationAttemptNeedsInput || to == DelegationAttemptNotAuthorized ||
			to == DelegationAttemptFailed || to == DelegationAttemptCancelled || to == DelegationAttemptInterrupted
	default:
		return false
	}
}

// AttemptRetryEligible reports whether a terminal attempt may be selected by a
// retry generation. Succeeded/blocked/needs_input use the continuation
// commands instead; not_authorized is an admission outcome, not a retry target.
func AttemptRetryEligible(status DelegationAttemptStatus) bool {
	switch status {
	case DelegationAttemptFailed, DelegationAttemptCancelled, DelegationAttemptInterrupted:
		return true
	default:
		return false
	}
}

// RunBudgetUsage is the folded actual usage of one child Run.
type RunBudgetUsage struct {
	ModelCalls   int   `json:"modelCalls"`
	ToolCalls    int   `json:"toolCalls"`
	Tokens       int64 `json:"tokens"`
	OutputTokens int64 `json:"outputTokens"`
	CostMicros   int64 `json:"costMicros"`
}

// DelegationAttemptReference identifies a reused attempt inside a generation.
type DelegationAttemptReference struct {
	ItemID       string `json:"itemId"`
	AttemptID    string `json:"attemptId"`
	Generation   int    `json:"generation"`
	ChildRunID   string `json:"childRunId"`
	ResultDigest string `json:"resultDigest,omitempty"`
}

// DelegationGeneration is one explicit execution round of a group.
type DelegationGeneration struct {
	ID                    string                       `json:"id"`
	GroupID               string                       `json:"groupId"`
	Generation            int                          `json:"generation"`
	Kind                  DelegationGenerationKind     `json:"kind"`
	Status                DelegationGenerationStatus   `json:"status"`
	RetrySelection        []string                     `json:"retrySelection"`
	ReusedAttempts        []DelegationAttemptReference `json:"reusedAttempts"`
	AuthorizationSnapshot json.RawMessage              `json:"authorizationSnapshot"`
	BudgetSnapshot        json.RawMessage              `json:"budgetSnapshot"`
	ClientRequestID       string                       `json:"clientRequestId"`
	CreatedAt             time.Time                    `json:"createdAt"`
	CompletedAt           *time.Time                   `json:"completedAt,omitempty"`
}

// DelegationAttempt is one child Run execution record.
type DelegationAttempt struct {
	ID               string                  `json:"id"`
	ItemID           string                  `json:"itemId"`
	Generation       int                     `json:"generation"`
	RetryOfAttemptID string                  `json:"retryOfAttemptId,omitempty"`
	ChildRunID       string                  `json:"childRunId"`
	Status           DelegationAttemptStatus `json:"status"`
	Terminal         *SubmitResult           `json:"terminal,omitempty"`
	ResultDigest     string                  `json:"resultDigest,omitempty"`
	ActualUsage      RunBudgetUsage          `json:"actualUsage"`
	ErrorCode        string                  `json:"errorCode,omitempty"`
	ErrorMessage     string                  `json:"errorMessage,omitempty"`
	CreatedAt        time.Time               `json:"createdAt"`
	FinishedAt       *time.Time              `json:"finishedAt,omitempty"`
}

// RetryDelegationInput is the explicit retry request. ExpectedGeneration must
// match the current group generation; selection is explicit; a budget increase
// requires a new visible authorization snapshot.
type RetryDelegationInput struct {
	ExpectedGeneration int                          `json:"expectedGeneration"`
	ItemIDs            []string                     `json:"itemIds"`
	BudgetOverrides    map[string]BudgetCeilingJSON `json:"budgetOverrides,omitempty"`
	ClientRequestID    string                       `json:"clientRequestId"`
}

// DelegationApprovalRequest is a durable, parent-independent authorization for
// a delegation retry budget increase. It is separate from tool-approval
// checkpoints because the original parent may already be terminal.
type DelegationApprovalRequest struct {
	ID          string          `json:"id"`
	GroupID     string          `json:"groupId"`
	Generation  int             `json:"generation"`
	Kind        string          `json:"kind"`
	ParentRunID string          `json:"parentRunId"`
	SessionID   string          `json:"sessionId"`
	Status      string          `json:"status"`
	ItemsJSON   json.RawMessage `json:"items"`
	RequestedAt time.Time       `json:"requestedAt"`
	ResolvedAt  *time.Time      `json:"resolvedAt,omitempty"`
}

// DelegationAttemptSummary is the parent-visible projection of one attempt.
// It intentionally excludes the private transcript and credentials.
type DelegationAttemptSummary struct {
	AttemptID    string                  `json:"attemptId"`
	Generation   int                     `json:"generation"`
	ChildRunID   string                  `json:"childRunId"`
	Status       DelegationAttemptStatus `json:"status"`
	Result       *SubmitResult           `json:"result,omitempty"`
	ResultDigest string                  `json:"resultDigest,omitempty"`
	Usage        RunBudgetUsage          `json:"usage"`
	ErrorCode    string                  `json:"errorCode,omitempty"`
}

// DelegationInspectionItem is one logical item with its full attempt history.
type DelegationInspectionItem struct {
	ItemID   string                     `json:"itemId"`
	Name     string                     `json:"name"`
	Status   DelegationItemStatus       `json:"status"`
	Attempts []DelegationAttemptSummary `json:"attempts"`
}

// DelegationInspection is the parent-visible, read-only group projection used
// by the nested activity UI and the generation inspection API.
type DelegationInspection struct {
	Group             DelegationGroup            `json:"group"`
	CurrentGeneration int                        `json:"currentGeneration"`
	Items             []DelegationInspectionItem `json:"items"`
	Generations       []DelegationGeneration     `json:"generations"`
	PendingApproval   *DelegationApprovalRequest `json:"pendingApproval,omitempty"`
	ValidActions      []string                   `json:"validActions"`
}
