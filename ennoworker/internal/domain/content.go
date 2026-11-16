package domain

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentKind string

const (
	ContentText             ContentKind = "text"
	ContentThinking         ContentKind = "thinking"
	ContentToolCall         ContentKind = "tool_call"
	ContentToolResult       ContentKind = "tool_result"
	ContentImage            ContentKind = "image"
	ContentImageDescription ContentKind = "image_description"
	ContentContextSummary   ContentKind = "context_summary"
	ContentRoomControl      ContentKind = "room_control"
)

type ImageRef struct {
	ArtifactID string `json:"artifactId"`
	MIMEType   string `json:"mimeType"`
	SHA256     string `json:"sha256"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Data       []byte `json:"-"`
}

type DerivedImageDescription struct {
	ArtifactID    string `json:"artifactId"`
	Text          string `json:"text"`
	ModelID       string `json:"modelId"`
	PromptVersion string `json:"promptVersion"`
}

type ContextSummary struct {
	CheckpointID string `json:"checkpointId"`
	SourceDigest string `json:"sourceDigest"`
	Summary      string `json:"summary"`
}

type RoomControl struct {
	Action                string          `json:"action"`
	ParticipantInstanceID string          `json:"participantInstanceId"`
	ObjectID              string          `json:"objectId"`
	ObjectVersionID       string          `json:"objectVersionId"`
	DisplaySnapshot       json.RawMessage `json:"displaySnapshot"`
}

type ContentBlock struct {
	Kind             ContentKind              `json:"type"`
	Text             string                   `json:"text,omitempty"`
	ToolCall         *ToolCall                `json:"toolCall,omitempty"`
	ToolResult       *ToolResult              `json:"toolResult,omitempty"`
	Image            *ImageRef                `json:"image,omitempty"`
	ImageDescription *DerivedImageDescription `json:"imageDescription,omitempty"`
	ContextSummary   *ContextSummary          `json:"contextSummary,omitempty"`
	RoomControl      *RoomControl             `json:"roomControl,omitempty"`
}

type ChatMessage struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

type ToolCall struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Arguments         json.RawMessage `json:"arguments"`
	ArgumentsFragment string          `json:"argumentsFragment,omitempty"`
	Partial           bool            `json:"partial,omitempty"`
}

type ToolResult struct {
	ToolCallID string              `json:"toolCallId"`
	ToolName   string              `json:"toolName"`
	Content    string              `json:"content"`
	IsError    bool                `json:"isError"`
	Artifacts  []ArtifactReference `json:"artifacts,omitempty"`
	// ExitStatus is the exit status of the process a shell tool ran, or nil when
	// this call never ran a process to completion. It is the single fact
	// tool_calls.exit_code records, and it is deliberately off the wire: the
	// Provider request, the committed message and the Web client read the tool
	// result shape that existed before this field, which is why it carries
	// json:"-". A reader who needs it reads the recorded row (see
	// domain.ToolExecution.ExitCode).
	//
	// Non-nil values follow ExitCodeUnrecorded's vocabulary: 0 for success, the
	// process's own code for a non-zero exit, 128+signal for a process killed by
	// a signal (the shell convention), and one of the negative sentinels below for
	// a process that ended without an exit status of its own. nil means "no
	// process ran", which reads back as ExitCodeUnrecorded.
	ExitStatus *int `json:"-"`
}

// Truncation markers of a bounded tool output.
//
// A tool that keeps only part of what a command printed keeps BOTH ends: the head
// is what the command started with and the tail is where every test runner puts
// its pass/fail summary. The bytes between the two are what is dropped, so the
// retained text reads head, head marker, tail marker, tail.
//
// Both markers start with the historical literal "[output truncated]", which is
// what readers (agentflow's tests-passed leaf among them) already grep for; the
// rest of each marker says which end of the retained text it describes. Every
// producer of a bounded output (the shell tools and the store's per-row read of a
// recorded preview) uses these two constants, so one vocabulary describes both
// layers and a reader never has to guess which one cut.
const (
	// OutputHeadTruncationMarker closes the retained head: everything before it
	// is the beginning of the output, and the output continues somewhere later.
	OutputHeadTruncationMarker = "[output truncated] head cut"
	// OutputTailTruncationMarker opens the retained tail: everything after it is
	// the END of the output, so a summary found there is the run's real summary.
	OutputTailTruncationMarker = "[output truncated] tail cut"
)

// Exit status sentinels for a shell process that ended without an exit status of
// its own. They are negative because a real exit status is 0..255 and a signal
// death is recorded as 128+signal; a reader that only knows "zero vs non-zero"
// still gets the right answer from them.
const (
	// ExitCodeTimedOut is the tool's own deadline killing the process: the run
	// never completed, so 0 (or any code it might have produced) would be a lie.
	ExitCodeTimedOut = -2
	// ExitCodeCancelled is the Run being cancelled while the process ran. It is a
	// different fact from the deadline, which is why it is a different value.
	ExitCodeCancelled = -3
)

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	// RiskClass is the mandatory local risk classification of the tool. The
	// Registry fails registration when it is missing or invalid; the value
	// never leaves the Worker (json:"-" keeps it out of Provider requests and
	// tool token estimates). External tools (e.g. MCP adapters) must supply a
	// conservative local RiskClass before they can be registered.
	RiskClass RiskClass `json:"-"`
}

// Usage is the provider-billing token accounting, normalized to three disjoint
// prompt-side buckets (uncached + cacheRead + cacheWrite = total input). This
// mirrors deepseek-harness's token-meter buckets so cache-hit share and total
// input are correct for every provider.
type Usage struct {
	UncachedInputTokens int64 `json:"uncachedInputTokens"`
	CacheReadTokens     int64 `json:"cacheReadTokens"`
	CacheWriteTokens    int64 `json:"cacheWriteTokens"`
	OutputTokens        int64 `json:"outputTokens"`
	ReasoningTokens     int64 `json:"reasoningTokens"`
}

// InputTokens returns the total billed prompt-side input (the three buckets).
func (u Usage) InputTokens() int64 {
	return u.UncachedInputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

type CompletionRequest struct {
	Model       string
	Messages    []ChatMessage
	Tools       []ToolDefinition
	MaxTokens   int
	Temperature *float64
	Reasoning   *ReasoningConfig
}

type StopReason = string

const (
	StopReasonStop      StopReason = "stop"
	StopReasonToolCalls StopReason = "tool_calls"
	StopReasonLength    StopReason = "length"
)

type Completion struct {
	CallID      string
	Content     []ContentBlock
	ToolCalls   []ToolCall
	StopReason  StopReason
	Usage       Usage
	ActualModel string
}
