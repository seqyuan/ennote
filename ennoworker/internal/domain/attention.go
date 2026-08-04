package domain

import (
	"encoding/json"
	"time"
)

// AttentionSourceKind identifies the authoritative row an attention item
// projects from.
type AttentionSourceKind string

const (
	AttentionSourceToolApproval         AttentionSourceKind = "tool_approval"
	AttentionSourceDelegationApproval   AttentionSourceKind = "delegation_approval"
	AttentionSourceDelegationItem       AttentionSourceKind = "delegation_item"
	AttentionSourceDelegationCompletion AttentionSourceKind = "delegation_completion"
)

// AttentionKind is the user-facing category of an attention item.
type AttentionKind string

const (
	AttentionApprovalRequired    AttentionKind = "approval_required"
	AttentionNeedsInput          AttentionKind = "needs_input"
	AttentionDelegationCompleted AttentionKind = "delegation_completed"
	AttentionDelegationFailed    AttentionKind = "delegation_failed"
)

// AttentionAction is the typed command an attention item offers. Approval and
// input items carry their authoritative target; notification items carry none.
// There is no generic decision action.
type AttentionAction struct {
	Kind               string `json:"kind"` // tool_approval | delegation_approval | delegation_input | none
	ApprovalID         string `json:"approvalId,omitempty"`
	ItemID             string `json:"itemId,omitempty"`
	AttemptID          string `json:"attemptId,omitempty"`
	ExpectedGeneration int    `json:"expectedGeneration,omitempty"`
}

// AttentionItem is a reconstructible cross-session projection. It is derived
// from terminal Run/attempt/approval facts and never acts as a second state
// machine.
type AttentionItem struct {
	ID               string              `json:"id"`
	ProjectID        string              `json:"projectId"`
	SessionID        string              `json:"sessionId"`
	SourceKind       AttentionSourceKind `json:"sourceKind"`
	SourceID         string              `json:"sourceId"`
	SourceGeneration int                 `json:"sourceGeneration"`
	Kind             AttentionKind       `json:"kind"`
	RequiresAction   bool                `json:"requiresAction"`
	Status           string              `json:"status"` // pending | resolved | dismissed
	Display          json.RawMessage     `json:"display"`
	Action           *AttentionAction    `json:"action,omitempty"`
	CreatedAt        time.Time           `json:"createdAt"`
	ResolvedAt       *time.Time          `json:"resolvedAt,omitempty"`
	DismissedAt      *time.Time          `json:"dismissedAt,omitempty"`
}
