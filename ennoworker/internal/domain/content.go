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
}

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

type Usage struct {
	InputTokens     int64 `json:"inputTokens"`
	OutputTokens    int64 `json:"outputTokens"`
	CachedTokens    int64 `json:"cachedTokens"`
	ReasoningTokens int64 `json:"reasoningTokens"`
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
