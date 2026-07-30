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
	CallIndex        int       `json:"callIndex"`
	ToolCallID       string    `json:"toolCallId"`
	ToolName         string    `json:"toolName"`
	RiskClass        RiskClass `json:"riskClass"`
	ArgumentsPreview string    `json:"argumentsPreview"`
}

type ToolApprovalRequest struct {
	ID                      string         `json:"id"`
	RunID                   string         `json:"runId"`
	SessionID               string         `json:"sessionId"`
	CheckpointID            string         `json:"-"`
	Iteration               int            `json:"iteration"`
	BatchDigest             string         `json:"batchDigest"`
	Status                  ApprovalStatus `json:"status"`
	Items                   []ApprovalItem `json:"items"`
	DecisionClientRequestID string         `json:"-"`
	RequestedAt             time.Time      `json:"requestedAt"`
	ResolvedAt              *time.Time     `json:"resolvedAt,omitempty"`
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
