package domain

import (
	"encoding/json"
	"time"
)

type ApprovalStatus string

const (
	ApprovalPending   ApprovalStatus = "pending"
	ApprovalApproved  ApprovalStatus = "approved"
	ApprovalRejected  ApprovalStatus = "rejected"
	ApprovalCancelled ApprovalStatus = "cancelled"
)

type ApprovalDecision string

const (
	DecisionApproved ApprovalDecision = "approved"
	DecisionRejected ApprovalDecision = "rejected"
)

type ExecutionCheckpointStatus string

const (
	CheckpointPending     ExecutionCheckpointStatus = "pending"
	CheckpointExecuting   ExecutionCheckpointStatus = "executing"
	CheckpointConsumed    ExecutionCheckpointStatus = "consumed"
	CheckpointCancelled   ExecutionCheckpointStatus = "cancelled"
	CheckpointInterrupted ExecutionCheckpointStatus = "interrupted"
)

type ApprovalItem struct {
	CallIndex        int                         `json:"callIndex"`
	ToolCallID       string                      `json:"toolCallId"`
	ToolName         string                      `json:"toolName"`
	RiskClass        RiskClass                   `json:"riskClass"`
	ArgumentsPreview string                      `json:"argumentsPreview"`
	Delegations      []DelegationApprovalPreview `json:"delegations,omitempty"`
	StandingScope    *StandingScopeInfo          `json:"standingScope,omitempty"`
}

// DelegationApprovalPreview is the bounded, approval-visible portion of one
// requested child assignment. It contains no Role prompt or execution config.
// DelegationApprovalPreview is the admission preview of one delegated task.
// It is the parent-visible, read-only projection shown before the batch is
// admitted; the canonical request and Role snapshots remain authoritative.
type DelegationApprovalPreview struct {
	Name           string            `json:"name"`
	Role           string            `json:"role"`
	RoleHandle     string            `json:"roleHandle,omitempty"` // legacy preview compat
	GoalPreview    string            `json:"goalPreview"`
	AssignmentPreview string          `json:"assignmentPreview,omitempty"` // legacy preview compat
	Skills         []string          `json:"skills,omitempty"`
	Depends        []string          `json:"depends,omitempty"`
	OutputContract string            `json:"outputContract"`
	Budget         BudgetCeilingJSON `json:"budget"`
}

// StandingScopeInfo is the safe, client-visible representation of a standing
// scope candidate. The canonical key is internal-only and never returned.
type StandingScopeInfo struct {
	Kind         string `json:"kind"`
	ScopeVersion int    `json:"scopeVersion"`
	Display      string `json:"display"`
}

type ApprovalAttribution struct {
	SpeakerKind       SpeakerKind    `json:"speakerKind"`
	ObjectID          string         `json:"objectId,omitempty"`
	VersionID         string         `json:"versionId,omitempty"`
	Handle            string         `json:"handle,omitempty"`
	DisplayName       string         `json:"displayName"`
	PermissionCeiling PermissionMode `json:"permissionCeiling,omitempty"`
	Authority         RoleAuthority  `json:"authority,omitempty"`
}

type ToolApprovalRequest struct {
	ID                      string               `json:"id"`
	RunID                   string               `json:"runId"`
	SessionID               string               `json:"sessionId"`
	CheckpointID            string               `json:"-"`
	Iteration               int                  `json:"iteration"`
	BatchDigest             string               `json:"batchDigest"`
	Status                  ApprovalStatus       `json:"status"`
	Items                   []ApprovalItem       `json:"items"`
	DecisionClientRequestID string               `json:"-"`
	RequestedAt             time.Time            `json:"requestedAt"`
	ResolvedAt              *time.Time           `json:"resolvedAt,omitempty"`
	Attribution             *ApprovalAttribution `json:"attribution,omitempty"`
}

type RunExecutionCheckpoint struct {
	ID            string                    `json:"id"`
	RunID         string                    `json:"runId"`
	SchemaVersion int                       `json:"schemaVersion"`
	Iteration     int                       `json:"iteration"`
	BatchDigest   string                    `json:"batchDigest"`
	State         json.RawMessage           `json:"-"`
	Status        ExecutionCheckpointStatus `json:"status"`
	CreatedAt     time.Time                 `json:"createdAt"`
	StartedAt     *time.Time                `json:"startedAt,omitempty"`
	FinishedAt    *time.Time                `json:"finishedAt,omitempty"`
}

type ApprovalResume struct {
	Approval   ToolApprovalRequest
	Checkpoint RunExecutionCheckpoint
	Decision   ApprovalDecision
}

type ActiveRunState struct {
	Run             AgentRun             `json:"run"`
	PendingApproval *ToolApprovalRequest `json:"pendingApproval,omitempty"`
}
