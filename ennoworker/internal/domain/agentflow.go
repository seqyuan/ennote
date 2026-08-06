package domain

import (
	"encoding/json"
	"time"
)

// Agent Flow (roadmap item 7, governing design 2026-08-05-agent-flow-design-v2.md).
//
// An Agent Flow is a declarative task DAG: each task = role@version + skills +
// goal + depends (+ optional budget/check/fan-out/terminal). The meta-Run is a
// pure orchestration state machine that coordinates one child Run per task and
// never calls a Provider itself.

// Agent Flow profile source kinds.
const (
	FlowSourceManaged     = "managed"
	FlowSourceProjectFile = "project_file"
)

// FlowSchemaVersion is the only supported flow definition schema version.
const FlowSchemaVersion = 1

// FlowState is the meta-Run state machine.
type FlowState string

const (
	FlowStatePending             FlowState = "pending"
	FlowStateRunning             FlowState = "running"
	FlowStateCompleted           FlowState = "completed"
	FlowStateFailed              FlowState = "failed"
	FlowStateCancelled           FlowState = "cancelled"
	FlowStateConvergenceExceeded FlowState = "convergence_exceeded"
	FlowStateBudgetExceeded      FlowState = "budget_exceeded"
)

func (s FlowState) Terminal() bool {
	switch s {
	case FlowStateCompleted, FlowStateFailed, FlowStateCancelled,
		FlowStateConvergenceExceeded, FlowStateBudgetExceeded:
		return true
	default:
		return false
	}
}

// FlowNodeState is one task checkpoint state.
type FlowNodeState string

const (
	FlowNodePending    FlowNodeState = "pending"
	FlowNodeRunning    FlowNodeState = "running"
	FlowNodeCompleted  FlowNodeState = "completed"
	FlowNodeFailed     FlowNodeState = "failed"
	FlowNodeBlocked    FlowNodeState = "blocked"
	FlowNodeCancelled  FlowNodeState = "cancelled"
	FlowNodeInterrupted FlowNodeState = "interrupted"
)

func (s FlowNodeState) Terminal() bool {
	switch s {
	case FlowNodeCompleted, FlowNodeFailed, FlowNodeBlocked,
		FlowNodeCancelled, FlowNodeInterrupted:
		return true
	default:
		return false
	}
}

// FlowTaskType distinguishes ordinary role tasks from deterministic check gates.
const (
	FlowTaskRole  = "role"
	FlowTaskCheck = "check"
)

// FlowPortType is the typed port vocabulary of flow inputs/outputs and task
// handoff values.
const (
	PortTypePath     = "path"
	PortTypeString   = "string"
	PortTypeInt      = "int"
	PortTypeFile     = "file"
	PortTypeArtifact = "artifact"
)

// FlowPort declares a typed input/output port.
type FlowPort struct {
	Type     string `json:"type" yaml:"type"`
	Required bool   `json:"required,omitempty" yaml:"required,omitempty"`
}

// FlowBudget is the flow-level total budget. max_total_tokens is mandatory
// (publish rejects a missing value).
type FlowBudget struct {
	MaxTotalTokens int64 `json:"maxTotalTokens" yaml:"max_total_tokens"`
}

// FlowTaskBudget overrides the per-task budget ceiling (tokens only in v1).
type FlowTaskBudget struct {
	Tokens int64 `json:"tokens,omitempty" yaml:"tokens,omitempty"`
}

// FlowFanOut declares a read-only parallel expansion (validation-only in Phase 1).
type FlowFanOut struct {
	Min int `json:"min" yaml:"min"`
	Max int `json:"max" yaml:"max"`
}

// FlowTerminal declares a terminal task (no child Run; flow completes).
type FlowTerminal struct {
	Status string `json:"status" yaml:"status"` // "success"
	Output string `json:"output,omitempty" yaml:"output,omitempty"`
}

// ConvergenceRule binds one convergence declaration to an actual back-edge.
// Phase 1 validates the binding; execution is Phase 2.
type ConvergenceRule struct {
	From      string `json:"from" yaml:"from"`
	To        string `json:"to" yaml:"to"`
	MaxRounds int    `json:"maxRounds" yaml:"max_rounds"`
}

// FlowTask is one task of a flow definition.
type FlowTask struct {
	Role     string            `json:"role,omitempty" yaml:"role,omitempty"`
	Skills   []string          `json:"skills,omitempty" yaml:"skills,omitempty"`
	Goal     string            `json:"goal,omitempty" yaml:"goal,omitempty"`
	Depends  []string          `json:"depends,omitempty" yaml:"depends,omitempty"`
	Type     string            `json:"type,omitempty" yaml:"type,omitempty"`
	Command  string            `json:"command,omitempty" yaml:"command,omitempty"`
	FanOut   *FlowFanOut       `json:"fanOut,omitempty" yaml:"fan_out,omitempty"`
	Budget   *FlowTaskBudget   `json:"budget,omitempty" yaml:"budget,omitempty"`
	Terminal *FlowTerminal     `json:"terminal,omitempty" yaml:"terminal,omitempty"`
	Output   string            `json:"output,omitempty" yaml:"output,omitempty"`
	Next     map[string]string `json:"next,omitempty" yaml:"next,omitempty"` // reserved for Phase 2
}

// FlowDefinition is the parsed flow contract. It is the authoring unit; the
// store publishes immutable versions from it and computes a config digest over
// its canonical JSON form.
type FlowDefinition struct {
	SchemaVersion int                     `json:"schemaVersion" yaml:"schemaVersion"`
	ID            string                  `json:"id" yaml:"id"`
	Version       int                     `json:"version,omitempty" yaml:"version,omitempty"`
	Description   string                  `json:"description,omitempty" yaml:"description,omitempty"`
	Inputs        map[string]FlowPort     `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Outputs       map[string]FlowPort     `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Budget        FlowBudget              `json:"budget" yaml:"budget"`
	Tasks         map[string]FlowTask     `json:"tasks" yaml:"tasks"`
	Convergence   []ConvergenceRule       `json:"convergence,omitempty" yaml:"convergence,omitempty"`
}

// AgentFlowProfile is the stable identity of a flow definition.
type AgentFlowProfile struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Slug          string          `json:"slug"`
	SourceKind    string          `json:"sourceKind"`
	ProjectScope  *string         `json:"projectScope,omitempty"`
	SourceLocator string          `json:"sourceLocator,omitempty"`
	Lifecycle     string          `json:"lifecycleStatus"`
	LatestVersion int             `json:"latestVersion"`
	DraftJSON     json.RawMessage `json:"draft,omitempty"`
	DraftYAML     string          `json:"draftYaml,omitempty"`
	DraftRevision int             `json:"draftRevision"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// AgentFlowVersion is an immutable published flow version.
type AgentFlowVersion struct {
	ID             string          `json:"id"`
	ProfileID      string          `json:"profileId"`
	Version        int             `json:"version"`
	ConfigDigest   string          `json:"configDigest"`
	DefinitionJSON json.RawMessage `json:"definition"`
	PublishedAt    time.Time       `json:"publishedAt"`
}

// ProjectAgentFlowBinding is the per-Project desired enablement of one version.
type ProjectAgentFlowBinding struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"projectId"`
	FlowVersionID  string    `json:"flowVersionId"`
	DesiredEnabled bool      `json:"desiredEnabled"`
	Revision       int       `json:"revision"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// RunAgentFlow is the meta-Run orchestration record. RunID is the anchor
// top-level agent run id so delegation children, events, and SSE all hang off
// one durable run identity.
type RunAgentFlow struct {
	RunID            string          `json:"runId"`
	SessionID        string          `json:"sessionId"`
	ProjectID        string          `json:"projectId"`
	FlowVersionID    string          `json:"flowVersionId"`
	ManifestDigest   string          `json:"manifestDigest"`
	State            FlowState       `json:"state"`
	TotalTokensUsed  int64           `json:"totalTokensUsed"`
	TerminalReason   string          `json:"terminalReason,omitempty"`
	InputsJSON       json.RawMessage `json:"inputs,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
	FinishedAt       *time.Time      `json:"finishedAt,omitempty"`
}

// RunAgentFlowNode is one task checkpoint. terminal_state + output_ref is the
// flow checkpoint: completed tasks are never replayed on resume.
type RunAgentFlowNode struct {
	RunID         string          `json:"runId"`
	TaskIndex     int             `json:"taskIndex"`
	Handle        string          `json:"handle"`
	RoleVersionID string          `json:"roleVersionId,omitempty"`
	SkillDigests  []string        `json:"skillDigests,omitempty"`
	GoalDigest    string          `json:"goalDigest,omitempty"`
	GoalText      string          `json:"goalText,omitempty"`
	BudgetJSON    json.RawMessage `json:"budget,omitempty"`
	TerminalState FlowNodeState   `json:"terminalState"`
	OutputRef     json.RawMessage `json:"outputRef,omitempty"`
	ChildRunID    string          `json:"childRunId,omitempty"`
	ErrorCode     string          `json:"errorCode,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	FinishedAt    *time.Time      `json:"finishedAt,omitempty"`
}

// FlowRunOutput is the folded flow output: the outputs port values plus the
// per-task checkpoint map (output_ref for each completed task).
type FlowRunOutput struct {
	Outputs    map[string]any            `json:"outputs"`
	TaskOutput map[string]json.RawMessage `json:"taskOutput"`
}
