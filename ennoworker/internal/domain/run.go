package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type RunKind string

const (
	RunKindAgent             RunKind = "agent"
	RunKindContextCompaction RunKind = "context_compaction"
)

type RunStatus string

const (
	RunQueued             RunStatus = "queued"
	RunRunning            RunStatus = "running"
	RunWaitingForApproval RunStatus = "waiting_for_approval"
	RunSucceeded          RunStatus = "succeeded"
	RunFailed             RunStatus = "failed"
	RunCancelled          RunStatus = "cancelled"
	RunInterrupted        RunStatus = "interrupted"
)

func (s RunStatus) Terminal() bool {
	switch s {
	case RunSucceeded, RunFailed, RunCancelled, RunInterrupted:
		return true
	default:
		return false
	}
}

func CanTransitionRun(from, to RunStatus) bool {
	switch from {
	case RunQueued:
		return to == RunRunning || to == RunCancelled || to == RunInterrupted
	case RunRunning:
		return to == RunWaitingForApproval || to == RunSucceeded || to == RunFailed || to == RunCancelled || to == RunInterrupted
	case RunWaitingForApproval:
		return to == RunQueued || to == RunCancelled || to == RunInterrupted
	default:
		return false
	}
}

type TurnStatus string

const (
	TurnPending            TurnStatus = "pending"
	TurnRunning            TurnStatus = "running"
	TurnWaitingForApproval TurnStatus = "waiting_for_approval"
	TurnSucceeded          TurnStatus = "succeeded"
	TurnFailed             TurnStatus = "failed"
	TurnCancelled          TurnStatus = "cancelled"
	TurnInterrupted        TurnStatus = "interrupted"
)

type PolicyKind string

const (
	PolicyKindTool       PolicyKind = "tool"
	PolicyKindTurn       PolicyKind = "turn"
	PolicyKindVision     PolicyKind = "vision"
	PolicyKindCompaction PolicyKind = "compaction"
)

type PolicySnapshot struct {
	ID      string          `json:"id"`
	Kind    PolicyKind      `json:"kind"`
	Version int             `json:"version"`
	Config  json.RawMessage `json:"config"`
}

type ModelRuntimeSnapshot struct {
	ProviderProfileID string `json:"providerProfileId"`
	ModelProfileID    string `json:"modelProfileId"`
	APIModel          string `json:"apiModel"`
	BaseURL           string `json:"baseUrl"`
	CredentialRef     string `json:"credentialRef"`
	Proxy             string `json:"proxy,omitempty"`
	ContextTokens     int    `json:"contextTokens"`
	MaxOutputTokens   int    `json:"maxOutputTokens"`
	SupportsVision    bool   `json:"supportsVision"`
	SupportsToolUse   bool   `json:"supportsToolUse"`
	SupportsThinking  bool   `json:"supportsThinking"`
}

type FrozenRoutingConfig struct {
	Candidates     []ModelRuntimeSnapshot `json:"candidates"`
	Threshold      float64                `json:"threshold"`
	Pinned         bool                   `json:"pinned"`
	AllowAutoRoute bool                   `json:"allowAutoRoute"`
}

type EffectiveRunConfig struct {
	ProviderProfileID string               `json:"providerProfileId"`
	ModelProfileID    string               `json:"modelProfileId"`
	APIModel          string               `json:"apiModel"`
	ContextTokens     int                  `json:"contextTokens"`
	MaxOutputTokens   int                  `json:"maxOutputTokens"`
	MaxIterations     int                  `json:"maxIterations"`
	ToolExecution     ToolExecutionConfig  `json:"toolExecution"`
	InitialRuntime    ModelRuntimeSnapshot `json:"initialRuntime"`
	Routing           FrozenRoutingConfig  `json:"routing"`
	ToolPolicy        PolicySnapshot       `json:"toolPolicy"`
	TurnPolicy        PolicySnapshot       `json:"turnPolicy"`
	VisionPolicy      PolicySnapshot       `json:"visionPolicy"`
	CompactionPolicy  PolicySnapshot       `json:"compactionPolicy"`
	CompactionRuntime ModelRuntimeSnapshot `json:"compactionRuntime"`
}

type RunOutput struct {
	Messages  []ChatMessage
	Suspended bool
}

type ErrorCode string

const (
	ErrorProviderUnavailable            ErrorCode = "provider_unavailable"
	ErrorProviderConfigurationInvalid   ErrorCode = "provider_configuration_invalid"
	ErrorProviderCredentialUnavailable  ErrorCode = "provider_credential_unavailable"
	ErrorProviderAuthentication         ErrorCode = "provider_authentication_failed"
	ErrorProviderModelNotFound          ErrorCode = "provider_model_not_found"
	ErrorProviderRateLimited            ErrorCode = "provider_rate_limited"
	ErrorProviderTimeout                ErrorCode = "provider_request_timeout"
	ErrorProviderRequestRejected        ErrorCode = "provider_request_rejected"
	ErrorModelStreamInterrupted         ErrorCode = "model_stream_interrupted"
	ErrorModelOutputTruncated           ErrorCode = "model_output_truncated"
	ErrorModelProtocol                  ErrorCode = "model_protocol_error"
	ErrorEventPersistence               ErrorCode = "event_persistence_failed"
	ErrorToolBatchFailed                ErrorCode = "tool_batch_failed"
	ErrorStuckToolLoop                  ErrorCode = "stuck_tool_loop"
	ErrorMaxIterations                  ErrorCode = "max_iterations_exceeded"
	ErrorRunCancelled                   ErrorCode = "run_cancelled"
	ErrorToolPolicyFailed               ErrorCode = "tool_policy_failed"
	ErrorToolPolicyTerminated           ErrorCode = "tool_policy_terminated"
	ErrorTurnPolicyFailed               ErrorCode = "turn_policy_failed"
	ErrorModelRouteFailed               ErrorCode = "model_route_failed"
	ErrorVisionUnsupported              ErrorCode = "vision_unsupported"
	ErrorVisionFallbackFailed           ErrorCode = "vision_fallback_failed"
	ErrorImageInvalid                   ErrorCode = "image_invalid"
	ErrorSessionBusy                    ErrorCode = "session_busy"
	ErrorSessionCompacting              ErrorCode = "session_compacting"
	ErrorCompactionNotAllowed           ErrorCode = "compaction_not_allowed"
	ErrorCompactionNothingToCompact     ErrorCode = "compaction_nothing_to_compact"
	ErrorCompactionModelUnavailable     ErrorCode = "compaction_model_unavailable"
	ErrorCompactionConfigInvalid        ErrorCode = "compaction_config_invalid"
	ErrorCompactionInputTooLarge        ErrorCode = "compaction_input_too_large"
	ErrorContextTurnTooLarge            ErrorCode = "context_turn_too_large"
	ErrorCompactionProviderFailed       ErrorCode = "compaction_provider_failed"
	ErrorCompactionOutputInvalid        ErrorCode = "compaction_output_invalid"
	ErrorCompactionCheckpointInvalid    ErrorCode = "compaction_checkpoint_invalid"
	ErrorCompactionCancelled            ErrorCode = "compaction_cancelled"
	ErrorContextCompactionRequired      ErrorCode = "context_compaction_required"
	ErrorContextOverflow                ErrorCode = "context_overflow"
	ErrorContextOverflowAfterCompaction ErrorCode = "context_overflow_after_compaction"
	ErrorContextOverflowInRun           ErrorCode = "context_overflow_in_run"
	ErrorHistoryLookupForbidden         ErrorCode = "history_lookup_forbidden"
	ErrorHistoryLookupOutOfRange        ErrorCode = "history_lookup_out_of_range"
	ErrorApprovalCheckpointInvalid      ErrorCode = "approval_checkpoint_invalid"
)

type CodedError struct {
	Code  ErrorCode
	Cause error
}

func (e *CodedError) Error() string {
	if e.Cause == nil {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Cause)
}

func (e *CodedError) Unwrap() error { return e.Cause }

func NewCodedError(code ErrorCode, cause error) error {
	var existing *CodedError
	if errors.As(cause, &existing) && existing.Code == code {
		return cause
	}
	return &CodedError{Code: code, Cause: cause}
}

func ErrorCodeOf(err error) ErrorCode {
	if errors.Is(err, context.Canceled) {
		return ErrorRunCancelled
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ErrorToolBatchFailed
}

type AgentRun struct {
	ID                 string          `json:"id"`
	TurnID             string          `json:"turnId,omitempty"`
	SessionID          string          `json:"sessionId"`
	RunKind            RunKind         `json:"runKind"`
	BaseMessageID      string          `json:"baseMessageId,omitempty"`
	Attempt            int             `json:"attempt"`
	Status             RunStatus       `json:"status"`
	AssistantMessageID *string         `json:"assistantMessageId,omitempty"`
	RetryOfRunID       string          `json:"retryOfRunId,omitempty"`
	RequestedConfig    json.RawMessage `json:"requestedConfig"`
	EffectiveConfig    json.RawMessage `json:"effectiveConfig"`
	ErrorCode          *string         `json:"errorCode,omitempty"`
	ErrorMessage       *string         `json:"errorMessage,omitempty"`
	StartedAt          *time.Time      `json:"startedAt,omitempty"`
	FinishedAt         *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt          time.Time       `json:"createdAt"`
}

type RunEvent struct {
	EventID   int64
	RunID     string
	Seq       int64
	EventType string
	Payload   json.RawMessage
	CreatedAt time.Time
}

type PendingEvent struct {
	EventType string
	Payload   json.RawMessage
}

type SubmitTurnInput struct {
	SessionID       string
	ClientRequestID string
	BaseMessageID   string
	Text            string
	Parts           []ContentBlock
	RequestedConfig json.RawMessage
}

type TurnSubmission struct {
	TurnID        string   `json:"turnId"`
	UserMessageID string   `json:"userMessageId"`
	Run           AgentRun `json:"run"`
	Existing      bool     `json:"existing"`
}

type RetryBlockedReason string

const (
	RetryBlockedActiveRun       RetryBlockedReason = "active_run"
	RetryBlockedInactiveTurn    RetryBlockedReason = "inactive_turn"
	RetryBlockedNotLatest       RetryBlockedReason = "not_latest_attempt"
	RetryBlockedProjectedOutput RetryBlockedReason = "projected_output"
	RetryBlockedSideEffect      RetryBlockedReason = "side_effect_boundary"
)

type RunRecovery struct {
	Run           AgentRun           `json:"run"`
	Retryable     bool               `json:"retryable"`
	BlockedReason RetryBlockedReason `json:"blockedReason,omitempty"`
}

type RunRetrySubmission struct {
	SourceRunID string   `json:"sourceRunId"`
	Run         AgentRun `json:"run"`
	Existing    bool     `json:"existing"`
}
