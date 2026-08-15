package domain

import "encoding/json"

type ModelCallPurpose string

const (
	ModelCallAgentTurn         ModelCallPurpose = "agent_turn"
	ModelCallImageDescription  ModelCallPurpose = "image_description"
	ModelCallContextCompaction ModelCallPurpose = "context_compaction"
)

type ModelCallStart struct {
	ID                string
	RunID             string
	Iteration         int
	Attempt           int
	RequestGeneration int
	Purpose           ModelCallPurpose
	CompactionID      string
	ProviderProfileID string
	ModelProfileID    string
	RouteReason       string
	ParentIteration   int
	SourceArtifactID  string
	RequestedConfig   json.RawMessage
	EffectiveConfig   json.RawMessage
}

type ModelCallFinish struct {
	ID                string
	RunID             string
	Iteration         int
	Attempt           int
	RequestGeneration int
	Purpose           ModelCallPurpose
	CompactionID      string
	SourceArtifactID  string
	ActualModel       string
	StopReason        StopReason
	Usage             Usage
	ErrorCode         string
	Error             string
	HTTPStatus        int
	Retryable         bool
	Final             bool
}

type ToolPolicyMetadata struct {
	PolicyID       string
	PolicyVersion  int
	Action         string
	Code           string
	RiskClass      RiskClass
	StopAfterBatch bool
	StandingRuleID string
}

type ToolCallStart struct {
	ID                 string
	RunID              string
	Iteration          int
	CallIndex          int
	Call               ToolCall
	OriginalArguments  json.RawMessage
	EffectiveArguments json.RawMessage
	Policy             ToolPolicyMetadata
}

type ToolCallFinish struct {
	ID           string
	RunID        string
	Iteration    int
	CallIndex    int
	Call         ToolCall
	RawResult    ToolResult
	Result       ToolResult
	Status       string
	Reason       string
	Policy       ToolPolicyMetadata
	AttemptCount int
}
