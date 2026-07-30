package domain

import (
	"encoding/json"
	"time"
)

type CompactionMode string

const (
	CompactionDisabled      CompactionMode = "disabled"
	CompactionManualOnly    CompactionMode = "manual_only"
	CompactionManualAndAuto CompactionMode = "manual_and_auto"
)

type CompactionReason string

const (
	CompactionReasonManual    CompactionReason = "manual"
	CompactionReasonThreshold CompactionReason = "threshold"
	CompactionReasonOverflow  CompactionReason = "overflow"
)

type CompactionStatus string

const (
	CompactionPlanned   CompactionStatus = "planned"
	CompactionRunning   CompactionStatus = "running"
	CompactionCompleted CompactionStatus = "completed"
	CompactionFailed    CompactionStatus = "failed"
	CompactionCancelled CompactionStatus = "cancelled"
)

type CompactionPolicyConfig struct {
	Mode                     CompactionMode `json:"mode"`
	TriggerRatio             float64        `json:"triggerRatio"`
	KeepRecentTurns          int            `json:"keepRecentTurns"`
	TailTokenRatio           float64        `json:"tailTokenRatio"`
	TailMinTokens            int            `json:"tailMinTokens"`
	TailMaxTokens            int            `json:"tailMaxTokens"`
	SummaryInputRatio        float64        `json:"summaryInputRatio"`
	CompactionModelProfileID *string        `json:"compactionModelProfileId"`
	SummaryMaxOutputTokens   int            `json:"summaryMaxOutputTokens"`
	IncludeReasoning         bool           `json:"includeReasoning"`
	AllowHistoryLookup       bool           `json:"allowHistoryLookup"`
	AllowOverflowRecovery    bool           `json:"allowOverflowRecovery"`
	MaxOverflowRecoveries    int            `json:"maxOverflowRecoveries"`
	IneffectiveReclaimRatio  float64        `json:"ineffectiveReclaimRatio"`
	IneffectiveLimit         int            `json:"ineffectiveLimit"`
	FailureCooldownSeconds   int            `json:"failureCooldownSeconds"`
	PromptVersion            string         `json:"promptVersion"`
}

func DefaultCompactionPolicy() CompactionPolicyConfig {
	return CompactionPolicyConfig{
		Mode: CompactionManualOnly, TriggerRatio: 0.75, KeepRecentTurns: 2,
		TailTokenRatio: 0.20, TailMinTokens: 8000, TailMaxTokens: 32000,
		SummaryInputRatio: 0.70, SummaryMaxOutputTokens: 4096,
		AllowHistoryLookup: true, AllowOverflowRecovery: true, MaxOverflowRecoveries: 1,
		IneffectiveReclaimRatio: 0.10, IneffectiveLimit: 3,
		FailureCooldownSeconds: 600, PromptVersion: "v1",
	}
}

type ContextCompaction struct {
	ID                     string           `json:"id"`
	RunID                  *string          `json:"runId,omitempty"`
	SessionID              string           `json:"sessionId"`
	ClientRequestID        *string          `json:"clientRequestId,omitempty"`
	Status                 CompactionStatus `json:"status"`
	Reason                 CompactionReason `json:"reason"`
	PolicyProfileID        string           `json:"policyProfileId,omitempty"`
	PolicyVersion          int              `json:"policyVersion,omitempty"`
	RequestedConfig        json.RawMessage  `json:"requestedConfig"`
	EffectiveConfig        json.RawMessage  `json:"effectiveConfig"`
	BaseLeafMessageID      string           `json:"baseLeafMessageId"`
	PreviousCompactionID   *string          `json:"previousCompactionId,omitempty"`
	SourceFromMessageID    *string          `json:"sourceFromMessageId,omitempty"`
	SourceThroughMessageID *string          `json:"sourceThroughMessageId,omitempty"`
	FirstKeptMessageID     string           `json:"firstKeptMessageId"`
	SourceDigest           string           `json:"sourceDigest"`
	SummaryContractDigest  string           `json:"summaryContractDigest"`
	Summary                string           `json:"summary"`
	SummaryDigest          string           `json:"summaryDigest"`
	PromptVersion          string           `json:"promptVersion"`
	CustomInstructions     string           `json:"-"`
	ModelCallID            *string          `json:"modelCallId,omitempty"`
	TokensBefore           int              `json:"tokensBefore"`
	EstimatedTokensAfter   int              `json:"estimatedTokensAfter"`
	ReclaimedTokens        int              `json:"reclaimedTokens"`
	ErrorCode              *string          `json:"errorCode,omitempty"`
	ErrorMessage           *string          `json:"errorMessage,omitempty"`
	StartedAt              *time.Time       `json:"startedAt,omitempty"`
	FinishedAt             *time.Time       `json:"finishedAt,omitempty"`
	CreatedAt              time.Time        `json:"createdAt"`
}

type RunContextCompaction struct {
	ID                    string           `json:"id"`
	RunID                 string           `json:"runId"`
	PreviousCompactionID  *string          `json:"previousCompactionId,omitempty"`
	Status                CompactionStatus `json:"status"`
	Reason                CompactionReason `json:"reason"`
	Iteration             int              `json:"iteration"`
	RequestGeneration     int              `json:"requestGeneration"`
	PolicyProfileID       string           `json:"policyProfileId,omitempty"`
	PolicyVersion         int              `json:"policyVersion,omitempty"`
	EffectiveConfig       json.RawMessage  `json:"effectiveConfig"`
	SourceDigest          string           `json:"sourceDigest"`
	SummaryContractDigest string           `json:"summaryContractDigest"`
	Summary               string           `json:"summary"`
	SummaryDigest         string           `json:"summaryDigest"`
	CoveredGenerated      int              `json:"coveredGenerated"`
	ModelCallID           *string          `json:"modelCallId,omitempty"`
	TokensBefore          int              `json:"tokensBefore"`
	EstimatedTokensAfter  int              `json:"estimatedTokensAfter"`
	ReclaimedTokens       int              `json:"reclaimedTokens"`
	ErrorCode             *string          `json:"errorCode,omitempty"`
	ErrorMessage          *string          `json:"errorMessage,omitempty"`
	StartedAt             *time.Time       `json:"startedAt,omitempty"`
	FinishedAt            *time.Time       `json:"finishedAt,omitempty"`
	CreatedAt             time.Time        `json:"createdAt"`
}

type ManualCompactionInput struct {
	SessionID       string
	BaseMessageID   string
	ClientRequestID string
	Instructions    string
	RequestedConfig json.RawMessage
}

type CompactionSubmission struct {
	RunID        string `json:"runId"`
	CompactionID string `json:"compactionId"`
	Status       string `json:"status"`
	Existing     bool   `json:"existing"`
}

type CompactionState struct {
	SessionID            string
	FailureCooldownUntil *time.Time
	LastFailureCode      *string
	IneffectiveCount     int
	LastReclaimRatio     *float64
	UpdatedAt            time.Time
}
