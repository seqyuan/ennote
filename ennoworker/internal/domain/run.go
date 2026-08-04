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
	RunKindDelegatedAgent    RunKind = "delegated_agent"
)

type RunStatus string

const (
	RunQueued                 RunStatus = "queued"
	RunRunning                RunStatus = "running"
	RunWaitingForApproval     RunStatus = "waiting_for_approval"
	RunWaitingDelegationAdmit RunStatus = "waiting_delegation_admission"
	RunWaitingChildren        RunStatus = "waiting_children"
	RunSucceeded              RunStatus = "succeeded"
	RunFailed                 RunStatus = "failed"
	RunCancelled              RunStatus = "cancelled"
	RunInterrupted            RunStatus = "interrupted"
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
		return to == RunWaitingForApproval || to == RunWaitingDelegationAdmit ||
			to == RunWaitingChildren || to == RunSucceeded || to == RunFailed ||
			to == RunCancelled || to == RunInterrupted
	case RunWaitingForApproval:
		return to == RunQueued || to == RunCancelled || to == RunInterrupted
	case RunWaitingDelegationAdmit:
		return to == RunQueued || to == RunWaitingChildren || to == RunCancelled || to == RunInterrupted
	case RunWaitingChildren:
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
	ProviderProfileID        string           `json:"providerProfileId"`
	ModelProfileID           string           `json:"modelProfileId"`
	APIModel                 string           `json:"apiModel"`
	BaseURL                  string           `json:"baseUrl"`
	CredentialRef            string           `json:"credentialRef"`
	Proxy                    string           `json:"proxy,omitempty"`
	ContextTokens            int              `json:"contextTokens"`
	MaxOutputTokens          int              `json:"maxOutputTokens"`
	SupportsVision           bool             `json:"supportsVision"`
	SupportsToolUse          bool             `json:"supportsToolUse"`
	SupportsThinking         bool             `json:"supportsThinking"`
	ThinkingDialect          ThinkingDialect  `json:"thinkingDialect"`
	SupportedThinkingEfforts []ThinkingEffort `json:"supportedThinkingEfforts"`
}

type FrozenRoutingConfig struct {
	Candidates     []ModelRuntimeSnapshot `json:"candidates"`
	Threshold      float64                `json:"threshold"`
	Pinned         bool                   `json:"pinned"`
	AllowAutoRoute bool                   `json:"allowAutoRoute"`
}

type FrozenRoleExecution struct {
	ObjectID          string         `json:"objectId"`
	VersionID         string         `json:"versionId"`
	Version           int            `json:"version"`
	Handle            string         `json:"handle"`
	DisplayName       string         `json:"displayName"`
	ConfigDigest      string         `json:"configDigest"`
	Authority         RoleAuthority  `json:"authority"`
	PermissionCeiling PermissionMode `json:"permissionCeiling"`
	AllowedTools      []string       `json:"allowedTools"`
	Skills            RoleSkills     `json:"skills"`
	OutputContract    string         `json:"outputContract"`
}

type EffectiveRunConfig struct {
	ProviderProfileID string                     `json:"providerProfileId"`
	ModelProfileID    string                     `json:"modelProfileId"`
	APIModel          string                     `json:"apiModel"`
	ContextTokens     int                        `json:"contextTokens"`
	MaxOutputTokens   int                        `json:"maxOutputTokens"`
	MaxIterations     int                        `json:"maxIterations"`
	ThinkingEffort    ThinkingEffort             `json:"thinkingEffort"`
	ToolExecution     ToolExecutionConfig        `json:"toolExecution"`
	InitialRuntime    ModelRuntimeSnapshot       `json:"initialRuntime"`
	Routing           FrozenRoutingConfig        `json:"routing"`
	ToolPolicy        PolicySnapshot             `json:"toolPolicy"`
	TurnPolicy        PolicySnapshot             `json:"turnPolicy"`
	VisionPolicy      PolicySnapshot             `json:"visionPolicy"`
	CompactionPolicy  PolicySnapshot             `json:"compactionPolicy"`
	CompactionRuntime ModelRuntimeSnapshot       `json:"compactionRuntime"`
	HookConfig        EffectiveHookConfig        `json:"hookConfig"`
	WorkspaceSecurity *WorkspaceSecuritySnapshot `json:"workspaceSecurity,omitempty"`
	Role              *FrozenRoleExecution       `json:"role,omitempty"`
	// Delegation is the frozen root budget policy snapshot for a top-level Host
	// Run. Children inherit the root policy fact but cannot replace it.
	Delegation *DelegationPolicySnapshot `json:"delegation,omitempty"`
}

// DelegationPolicySnapshot is the frozen ceiling applied to every delegation
// reservation of one top-level Host Run. It is resolved from the Session's
// delegation policy profile once, when the Run's effective config freezes.
type DelegationPolicySnapshot struct {
	ID                    string            `json:"id"`
	Version               int               `json:"version"`
	MaxConcurrentChildren int               `json:"maxConcurrentChildren"`
	Budget                BudgetCeilingJSON `json:"budget"`
	Digest                string            `json:"digest"`
}

type WorkspaceSecuritySnapshot struct {
	WorkspaceID   string    `json:"workspaceId"`
	CanonicalRoot string    `json:"canonicalRoot"`
	Trusted       bool      `json:"trusted"`
	TrustedAt     time.Time `json:"trustedAt"`
}

// EffectiveHookConfig is the frozen hooks configuration for a single run.
// It captures the resolved hook set, workspace trust snapshot, and any
// additional context injected by the RunStart hook.
type EffectiveHookConfig struct {
	ResolvedHookSet json.RawMessage `json:"resolvedHookSet"`
	HookSetDigest   string          `json:"hookSetDigest"`
	WorkspaceID     string          `json:"workspaceId"`
	WorkspaceRoot   string          `json:"workspaceRoot"`
	TrustedAt       time.Time       `json:"trustedAt"`
	RunStartContext string          `json:"runStartContext,omitempty"`
}

// RunTelemetryPayload is the structured run summary emitted as the durable
// run_telemetry event immediately before any terminal run event, so SSE
// consumers always receive it before the stream closes.
type RunTelemetryPayload struct {
	Iterations            int                   `json:"iterations"`
	ModelCalls            int                   `json:"modelCalls"`
	InputTokens           int                   `json:"inputTokens"`
	OutputTokens          int                   `json:"outputTokens"`
	CachedTokens          int                   `json:"cachedTokens"`
	MaxContextUtilization float64               `json:"maxContextUtilization"`
	ToolTimings           map[string]ToolTiming `json:"toolTimings"`
	DurationMS            int64                 `json:"durationMs"`
	Partial               bool                  `json:"partial,omitempty"`
}

// ToolTiming aggregates per-tool execution statistics for telemetry.
type ToolTiming struct {
	Count      int   `json:"count"`
	ErrorCount int   `json:"errorCount"`
	Attempts   int   `json:"attempts"`
	TotalMS    int64 `json:"totalMs"`
}

// HookOutcome records the result of a decision-style hook execution.
type HookOutcome struct {
	HookID            string
	Blocked           bool
	Reason            string
	AdditionalContext string
}

// IsEmpty reports whether the hook config has no configured hooks.
func (c EffectiveHookConfig) IsEmpty() bool {
	return len(c.ResolvedHookSet) == 0 || string(c.ResolvedHookSet) == "null" || string(c.ResolvedHookSet) == "{}"
}

type RunOutput struct {
	Messages  []ChatMessage
	Suspended bool
	// Adding RunOutput
	Waiting bool
	// Terminal carries the child's submit_result contract. Only set for
	// delegated_agent Runs; the child finalizer stores it instead of publishing.
	Terminal *SubmitResult
}

type ErrorCode string

const (
	ErrorProviderUnavailable             ErrorCode = "provider_unavailable"
	ErrorProviderConfigurationInvalid    ErrorCode = "provider_configuration_invalid"
	ErrorProviderCredentialUnavailable   ErrorCode = "provider_credential_unavailable"
	ErrorProviderAuthentication          ErrorCode = "provider_authentication_failed"
	ErrorProviderModelNotFound           ErrorCode = "provider_model_not_found"
	ErrorProviderRateLimited             ErrorCode = "provider_rate_limited"
	ErrorProviderTimeout                 ErrorCode = "provider_request_timeout"
	ErrorProviderRequestRejected         ErrorCode = "provider_request_rejected"
	ErrorModelStreamInterrupted          ErrorCode = "model_stream_interrupted"
	ErrorModelOutputTruncated            ErrorCode = "model_output_truncated"
	ErrorModelProtocol                   ErrorCode = "model_protocol_error"
	ErrorEventPersistence                ErrorCode = "event_persistence_failed"
	ErrorToolBatchFailed                 ErrorCode = "tool_batch_failed"
	ErrorStuckToolLoop                   ErrorCode = "stuck_tool_loop"
	ErrorMaxIterations                   ErrorCode = "max_iterations_exceeded"
	ErrorRunCancelled                    ErrorCode = "run_cancelled"
	ErrorToolPolicyFailed                ErrorCode = "tool_policy_failed"
	ErrorToolPolicyTerminated            ErrorCode = "tool_policy_terminated"
	ErrorTurnPolicyFailed                ErrorCode = "turn_policy_failed"
	ErrorModelRouteFailed                ErrorCode = "model_route_failed"
	ErrorVisionUnsupported               ErrorCode = "vision_unsupported"
	ErrorVisionFallbackFailed            ErrorCode = "vision_fallback_failed"
	ErrorImageInvalid                    ErrorCode = "image_invalid"
	ErrorSessionBusy                     ErrorCode = "session_busy"
	ErrorSessionCompacting               ErrorCode = "session_compacting"
	ErrorCompactionNotAllowed            ErrorCode = "compaction_not_allowed"
	ErrorCompactionNothingToCompact      ErrorCode = "compaction_nothing_to_compact"
	ErrorCompactionModelUnavailable      ErrorCode = "compaction_model_unavailable"
	ErrorCompactionConfigInvalid         ErrorCode = "compaction_config_invalid"
	ErrorCompactionInputTooLarge         ErrorCode = "compaction_input_too_large"
	ErrorContextTurnTooLarge             ErrorCode = "context_turn_too_large"
	ErrorCompactionProviderFailed        ErrorCode = "compaction_provider_failed"
	ErrorCompactionOutputInvalid         ErrorCode = "compaction_output_invalid"
	ErrorCompactionCheckpointInvalid     ErrorCode = "compaction_checkpoint_invalid"
	ErrorCompactionCancelled             ErrorCode = "compaction_cancelled"
	ErrorContextCompactionRequired       ErrorCode = "context_compaction_required"
	ErrorContextOverflow                 ErrorCode = "context_overflow"
	ErrorContextOverflowAfterCompaction  ErrorCode = "context_overflow_after_compaction"
	ErrorContextOverflowInRun            ErrorCode = "context_overflow_in_run"
	ErrorHistoryLookupForbidden          ErrorCode = "history_lookup_forbidden"
	ErrorHistoryLookupOutOfRange         ErrorCode = "history_lookup_out_of_range"
	ErrorApprovalCheckpointInvalid       ErrorCode = "approval_checkpoint_invalid"
	ErrorCommitFormatNotEnabled          ErrorCode = "commit_format_not_enabled"
	ErrorContextProjectionNotEnabled     ErrorCode = "context_projection_not_enabled"
	ErrorInvocationTargetInvalid         ErrorCode = "invocation_target_invalid"
	ErrorIncompleteTerminalContract      ErrorCode = "incomplete_terminal_contract"
	ErrorDelegationBudgetExceeded        ErrorCode = "delegation_budget_exceeded"
	ErrorDelegationNotAuthorized         ErrorCode = "delegation_not_authorized"
	ErrorDelegationGenerationConflict    ErrorCode = "delegation_generation_conflict"
	ErrorDelegationRetryIneligible       ErrorCode = "delegation_retry_ineligible"
	ErrorDelegationRetryApprovalRequired ErrorCode = "delegation_retry_budget_approval_required"
	ErrorDelegationRetrySnapshotMismatch ErrorCode = "delegation_retry_snapshot_mismatch"
	ErrorDelegationCompletionConflict    ErrorCode = "delegation_completion_conflict"
	ErrorDelegationInputStale            ErrorCode = "delegation_input_stale"
	ErrorDelegationFollowUpForbidden     ErrorCode = "delegation_follow_up_forbidden"
	ErrorTranscriptShadowMissing         ErrorCode = "transcript_shadow_missing"
	ErrorTranscriptShadowMismatch        ErrorCode = "transcript_shadow_mismatch"
	ErrorTranscriptCorrupt               ErrorCode = "transcript_corrupt"
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

type CommitFormatVersion int

const (
	CommitFormatLegacyV1  CommitFormatVersion = 1
	CommitFormatSpeakerV2 CommitFormatVersion = 2
)

type PublishMode string

const (
	PublishPublicFinal     PublishMode = "public_final"
	PublishPrivateToParent PublishMode = "private_to_parent"
)

type SystemPromptSnapshot struct {
	Version         int    `json:"version"`
	AgentProfileID  string `json:"agentProfileId,omitempty"`
	AgentPrompt     string `json:"agentPrompt"`
	PlatformVersion string `json:"platformVersion"`
	Digest          string `json:"digest"`
}

type SystemPromptMetadata struct {
	Version         int    `json:"version"`
	AgentProfileID  string `json:"agentProfileId,omitempty"`
	PlatformVersion string `json:"platformVersion"`
	Digest          string `json:"digest"`
}

type AgentRun struct {
	ID                    string                `json:"id"`
	TurnID                string                `json:"turnId,omitempty"`
	SessionID             string                `json:"sessionId"`
	RunKind               RunKind               `json:"runKind"`
	BaseMessageID         string                `json:"baseMessageId,omitempty"`
	Attempt               int                   `json:"attempt"`
	Status                RunStatus             `json:"status"`
	AssistantMessageID    *string               `json:"assistantMessageId,omitempty"`
	RetryOfRunID          string                `json:"retryOfRunId,omitempty"`
	CommitFormatVersion   CommitFormatVersion   `json:"commitFormatVersion"`
	SystemPrompt          *SystemPromptMetadata `json:"systemPrompt,omitempty"`
	ParentRunID           string                `json:"parentRunId,omitempty"`
	RootRunID             string                `json:"rootRunId,omitempty"`
	ExecutionDepth        int                   `json:"executionDepth"`
	PublishMode           PublishMode           `json:"publishMode"`
	SpeakerSnapshot       json.RawMessage       `json:"speakerSnapshot"`
	ContextSnapshot       json.RawMessage       `json:"contextSnapshot"`
	ContextSnapshotDigest string                `json:"contextSnapshotDigest,omitempty"`
	RequestedConfig       json.RawMessage       `json:"requestedConfig"`
	EffectiveConfig       json.RawMessage       `json:"effectiveConfig"`
	ErrorCode             *string               `json:"errorCode,omitempty"`
	ErrorMessage          *string               `json:"errorMessage,omitempty"`
	StartedAt             *time.Time            `json:"startedAt,omitempty"`
	FinishedAt            *time.Time            `json:"finishedAt,omitempty"`
	CreatedAt             time.Time             `json:"createdAt"`
}

func (r AgentRun) MarshalJSON() ([]byte, error) {
	type plain AgentRun
	encoded, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}
	if effective, ok := payload["effectiveConfig"]; ok {
		redactRunConfig(effective)
	}
	return json.Marshal(payload)
}

func redactRunConfig(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "credentialRef", "apiKey", "systemPrompt", "agentPrompt":
				delete(typed, key)
			default:
				redactRunConfig(child)
			}
		}
	case []any:
		for _, child := range typed {
			redactRunConfig(child)
		}
	}
}

type RunMessage struct {
	ID         string            `json:"id"`
	RunID      string            `json:"runId"`
	Ordinal    int               `json:"ordinal"`
	Role       Role              `json:"role"`
	Content    []ContentBlock    `json:"content"`
	Visibility MessageVisibility `json:"visibility"`
	CreatedAt  time.Time         `json:"createdAt"`
}

type RunEvent struct {
	EventID   int64
	RunID     string
	Seq       int64
	EventType string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// LiveRunEvent is a transient rendering delta delivered over live channels
// (SSE event: live frame, WS durability: live). It has no EventID, never
// advances the client cursor, and is never stored in the durable event log.
// Lost live events are acceptable — the final durable state (message_committed,
// tool_call_completed) always covers the gap on reconnect.
type LiveRunEvent struct {
	RunID     string          `json:"runId"`
	Type      string          `json:"type"`
	StreamID  string          `json:"streamId"` // tool call id + stream, e.g. "call-1:stdout"
	LiveSeq   int64           `json:"liveSeq"`  // connection-local sequence, not durable
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Live event type constants — these are rendering deltas, not state transitions.
const (
	LiveTextDelta              = "text_delta"
	LiveThinkingDelta          = "thinking_delta"
	LiveToolCallDelta          = "tool_call_delta"
	LiveVisionDescriptionDelta = "vision_description_delta"
	LiveToolOutputDelta        = "tool_output_delta"
)

type PendingEvent struct {
	EventType string
	Payload   json.RawMessage
}

type InvocationTargetKind string

const (
	InvocationTargetHost InvocationTargetKind = "host"
	InvocationTargetRole InvocationTargetKind = "role"
)

type InvocationContextMode string

const (
	InvocationContextRoom    InvocationContextMode = "room"
	InvocationContextFresh   InvocationContextMode = "fresh"
	InvocationContextReplyTo InvocationContextMode = "reply_to"
	InvocationContextTask    InvocationContextMode = "task_only"
)

type RoleInvocationTarget struct {
	Kind        InvocationTargetKind  `json:"kind"`
	ObjectID    string                `json:"objectId"`
	VersionID   string                `json:"versionId"`
	ContextMode InvocationContextMode `json:"contextMode"`
	ReplyTo     []string              `json:"replyTo,omitempty"`
}

type SubmitTurnInput struct {
	SessionID       string
	ClientRequestID string
	BaseMessageID   string
	Text            string
	Parts           []ContentBlock
	RequestedConfig json.RawMessage
}

type SubmitInvocationInput struct {
	SessionID       string
	ClientRequestID string
	BaseMessageID   string
	Text            string
	Parts           []ContentBlock
	Target          RoleInvocationTarget
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
