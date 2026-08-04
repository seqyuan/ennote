package agent

import (
	"context"
	"encoding/json"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
)

type ToolAction string

const (
	ToolAllow           ToolAction = "allow"
	ToolDeny            ToolAction = "deny"
	ToolRequireApproval ToolAction = "require_approval"
	ToolTerminateBatch  ToolAction = "terminate_batch"
)

type ToolBatchContext struct {
	RunID       string
	Iteration   int
	Policy      domain.PolicySnapshot
	WorkspaceID string
}

type ToolCallContext struct {
	RunID       string
	Iteration   int
	CallIndex   int
	Policy      domain.PolicySnapshot
	WorkspaceID string
}

type ToolDecision struct {
	Action               ToolAction
	Arguments            json.RawMessage
	Code                 string
	Reason               string
	RiskClass            domain.RiskClass
	RuleID               string `json:"ruleId,omitempty"`               // standing rule ID that authorized this call
	StandingScopeKind    string `json:"standingScopeKind,omitempty"`    // for snapshot replay
	StandingScopeVersion int    `json:"standingScopeVersion,omitempty"` // for snapshot replay
	StandingScopeKey     string `json:"standingScopeKey,omitempty"`     // for snapshot replay
}

type AfterToolDecision struct {
	Result         domain.ToolResult
	StopAfterBatch bool
	Code           string
	Reason         string
}

type ToolPolicy interface {
	BeforeToolBatch(context.Context, ToolBatchContext, []domain.ToolCall) ([]ToolDecision, error)
	AfterToolCall(context.Context, ToolCallContext, domain.ToolCall, domain.ToolResult) (AfterToolDecision, error)
}

type TurnContext struct {
	RunID              string
	Iteration          int
	Messages           []domain.ChatMessage
	Completion         domain.Completion
	ToolResults        []domain.ToolResult
	Current            domain.ModelRuntimeSnapshot
	Routing            domain.FrozenRoutingConfig
	Constraint         RoutingConstraint
	EstimatedTokens    int
	StopAfterToolBatch bool
}

type TurnPlan struct {
	ModelProfileID string
	Reason         string
}

type MidRunCompactionReason string

const (
	MidRunCompactionThreshold MidRunCompactionReason = "threshold"
	MidRunCompactionOverflow  MidRunCompactionReason = "overflow"
)

type MidRunCompactionState struct {
	ID                    string `json:"id"`
	Summary               string `json:"summary"`
	SourceDigest          string `json:"sourceDigest"`
	SummaryContractDigest string `json:"summaryContractDigest"`
	Count                 int    `json:"count"`
	Attempts              int    `json:"attempts"`
	CoveredGenerated      int    `json:"coveredGenerated"`
}

type MidRunCompactionRequest struct {
	RunID             string
	Iteration         int
	RequestGeneration int
	Reason            MidRunCompactionReason
	SystemPrompt      string
	Messages          []domain.ChatMessage
	Generated         []domain.ChatMessage
	Current           domain.ModelRuntimeSnapshot
	Tools             []domain.ToolDefinition
	Previous          MidRunCompactionState
}

type MidRunCompactionResult struct {
	Messages  []domain.ChatMessage
	State     MidRunCompactionState
	Compacted bool
}

type MidRunCompactor interface {
	CompactRunContext(context.Context, MidRunCompactionRequest) (MidRunCompactionResult, error)
}

type StopDecision struct {
	Stop   bool
	Code   string
	Reason string
}

type TurnPlanner interface {
	PrepareNextTurn(context.Context, TurnContext) (TurnPlan, error)
	ShouldStopAfterTurn(context.Context, TurnContext) (StopDecision, error)
}

type RoutingConstraint struct {
	RequiresVision bool
}

type TurnRuntime struct {
	Provider        llm.Provider
	Snapshot        domain.ModelRuntimeSnapshot
	RouteReason     string
	EffectiveConfig json.RawMessage
}

type ModelRouter interface {
	ResolveTurn(context.Context, domain.FrozenRoutingConfig, TurnPlan, RoutingConstraint) (TurnRuntime, error)
}

type ImageDescriptionRequest struct {
	Image          domain.ImageRef
	ModelProfileID string
	PromptVersion  string
}

type VisionContext struct {
	RunID     string
	Iteration int
	Messages  []domain.ChatMessage
	Current   domain.ModelRuntimeSnapshot
	Policy    domain.PolicySnapshot
}

type VisionResolution struct {
	Constraint         RoutingConstraint
	RewrittenMessages  []domain.ChatMessage
	DescriptorRequests []ImageDescriptionRequest
}

type VisionResolver interface {
	ResolveImages(context.Context, VisionContext) (VisionResolution, error)
}

type ImageDescriptionCache interface {
	Get(context.Context, string, string, string) (string, bool, error)
	Put(context.Context, domain.ImageRef, string, string, string, string, string) error
}
