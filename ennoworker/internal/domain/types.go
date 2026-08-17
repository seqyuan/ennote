package domain

import (
	"encoding/json"
	"time"
)

// Project represents a logical research project.
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ProjectWorkspace binds a project to a host directory.
type ProjectWorkspace struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Kind            string    `json:"kind"`
	HostPath        string    `json:"hostPath"`
	VirtualPath     string    `json:"virtualPath"`
	Status          string    `json:"status"`
	PathFingerprint string    `json:"pathFingerprint"`
	CreatedAt       time.Time `json:"createdAt"`
}

type CreateProjectInput struct {
	Name        string
	Description string
	HostPath    string
}

type SessionMode string

const (
	SessionModeHosted SessionMode = "hosted"
	SessionModeRoom   SessionMode = "room"
)

type SpeakerKind string

const (
	SpeakerUser     SpeakerKind = "user"
	SpeakerHost     SpeakerKind = "host"
	SpeakerRole     SpeakerKind = "role"
	SpeakerWorkflow SpeakerKind = "workflow"
	SpeakerRoom     SpeakerKind = "room"
	SpeakerSystem   SpeakerKind = "system"
)

type MessageVisibility string

const (
	VisibilityPublic          MessageVisibility = "public"
	VisibilityPrivate         MessageVisibility = "private"
	VisibilityRoomControl     MessageVisibility = "room_control"
	VisibilityLegacyExecution MessageVisibility = "legacy_execution"
)

type Session struct {
	ID                        string      `json:"id"`
	ProjectID                 string      `json:"projectId"`
	Title                     string      `json:"title"`
	Status                    string      `json:"status"`
	Mode                      SessionMode `json:"mode"`
	ActiveLeafMessageID       *string     `json:"activeLeafMessageId,omitempty"`
	ActiveBranchID            *string     `json:"activeBranchId,omitempty"`
	DefaultAgentProfileID     *string     `json:"defaultAgentProfileId,omitempty"`
	DefaultModelProfileID     *string     `json:"defaultModelProfileId,omitempty"`
	CompactionPolicyProfileID *string     `json:"compactionPolicyProfileId,omitempty"`
	SourceSessionID           *string     `json:"sourceSessionId,omitempty"`
	SourceMessageID           *string     `json:"sourceMessageId,omitempty"`
	CreatedAt                 time.Time   `json:"createdAt"`
	UpdatedAt                 time.Time   `json:"updatedAt"`
}

type CreateSessionInput struct {
	ProjectID                 string
	Title                     string
	DefaultAgentProfileID     *string
	DefaultModelProfileID     *string
	CompactionPolicyProfileID *string
	SourceSessionID           *string
	SourceMessageID           *string
}

type Message struct {
	ID                    string            `json:"id"`
	SessionID             string            `json:"sessionId"`
	ParentMessageID       *string           `json:"parentMessageId,omitempty"`
	Role                  string            `json:"role"`
	Status                string            `json:"status"`
	RunID                 *string           `json:"runId,omitempty"`
	// ModelProfileID / APIModel are denormalized from the run's frozen
	// effective config at read time so the UI can attribute a reply to a
	// model without a separate run fetch.
	ModelProfileID string `json:"modelProfileId,omitempty"`
	APIModel       string `json:"apiModel,omitempty"`
	SpeakerKind           SpeakerKind       `json:"speakerKind"`
	SpeakerObjectID       *string           `json:"speakerObjectId,omitempty"`
	SpeakerVersionID      *string           `json:"speakerVersionId,omitempty"`
	ParticipantInstanceID *string           `json:"participantInstanceId,omitempty"`
	SpeakerSnapshot       json.RawMessage   `json:"speakerSnapshot"`
	AddresseeKind         *string           `json:"addresseeKind,omitempty"`
	AddresseeObjectID     *string           `json:"addresseeObjectId,omitempty"`
	AddresseeVersionID    *string           `json:"addresseeVersionId,omitempty"`
	ReplyToMessageID      *string           `json:"replyToMessageId,omitempty"`
	Visibility            MessageVisibility `json:"visibility"`
	OriginatedAt          *time.Time        `json:"originatedAt,omitempty"`
	Parts                 []ContentBlock    `json:"parts"`
	CreatedAt             time.Time         `json:"createdAt"`
	// Seq is the session-monotonic message sequence, assigned at insert time
	// (MAX(seq)+1 within the session) and used for gap detection / consecutive
	// assertions on the client. Legacy rows backfilled once read Seq = 0.
	Seq int64 `json:"seq"`
}

// SessionContextUsage is the per-session context-occupancy projection reported
// by the Worker. It mirrors deepseek-harness's token-meter contextPressure +
// contextBreakdown projections: the ring reads projectedTokens as the used
// numerator and contextWindow as the capacity denominator, and the panel
// proportions system/tools/messages onto a stacked bar.
type SessionContextUsage struct {
	ContextWindow   int `json:"contextWindow"`
	ProjectedTokens int `json:"projectedTokens"`
	SystemTokens    int `json:"systemTokens"`
	ToolsTokens     int `json:"toolsTokens"`
	MessageTokens   int `json:"messageTokens"`
}

// SessionStats is the per-session aggregate the composer's StatsLine renders:
// turn/step counts, model/tool wall time, first-token/decode timing, and the
// cumulative token billing. Computed by the Worker from the durable model_calls
// and tool_calls tables so paging and compaction cannot change it.
type SessionStats struct {
	Turns               int   `json:"turns"`
	Steps               int   `json:"steps"`
	LLMMs               int64 `json:"llmMs"`
	ToolMs              int64 `json:"toolMs"`
	TTFTMs              int64 `json:"ttftMs"`
	TTFTSteps           int   `json:"ttftSteps"`
	DecodeMs            int64 `json:"decodeMs"`
	DecodeTokens        int64 `json:"decodeTokens"`
	UncachedInputTokens int64 `json:"uncachedInputTokens"`
	CacheReadTokens     int64 `json:"cacheReadTokens"`
	CacheWriteTokens    int64 `json:"cacheWriteTokens"`
	OutputTokens        int64 `json:"outputTokens"`
}

// TurnMetric is the per-turn footer reading the message chrome renders beside
// each completed turn's copy/branch actions: wall time, first-step TTFT, and
// decode throughput. Computed by the Worker from the run and its model calls.
// Zero readings are omitted so an unrecorded figure stays undefined on the
// wire, matching deepseek-harness's optional TurnMetrics (the chrome hides
// absent readings rather than rendering "0s / 0 tok/s").
type TurnMetric struct {
	RunID           string  `json:"runId"`
	RunMs           int64   `json:"runMs,omitempty"`
	TTFTMs          int64   `json:"ttftMs,omitempty"`
	TokensPerSecond float64 `json:"tokensPerSecond,omitempty"`
}

type SessionBranch struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"sessionId"`
	ParentBranchID *string   `json:"parentBranchId,omitempty"`
	ForkMessageID  *string   `json:"forkMessageId,omitempty"`
	LeafMessageID  *string   `json:"leafMessageId,omitempty"`
	Label          string    `json:"label"`
	MessageCount   int       `json:"messageCount"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type BranchNavigation struct {
	Session  Session         `json:"session"`
	Branches []SessionBranch `json:"branches"`
}

type SessionLineage struct {
	Messages []Message `json:"messages"`
}

type MessagePage struct {
	Messages            []Message
	HasMore             bool
	NextBeforeMessageID string
}
