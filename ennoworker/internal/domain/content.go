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

type ContentBlock struct {
	Kind             ContentKind              `json:"type"`
	Text             string                   `json:"text,omitempty"`
	ToolCall         *ToolCall                `json:"toolCall,omitempty"`
	ToolResult       *ToolResult              `json:"toolResult,omitempty"`
	Image            *ImageRef                `json:"image,omitempty"`
	ImageDescription *DerivedImageDescription `json:"imageDescription,omitempty"`
	ContextSummary   *ContextSummary          `json:"contextSummary,omitempty"`
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
}

type Usage struct {
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
}

type CompletionRequest struct {
	Model       string
	Messages    []ChatMessage
	Tools       []ToolDefinition
	MaxTokens   int
	Temperature *float64
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
