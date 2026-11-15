package domain

import (
	"encoding/json"
	"time"
)

// Agent Flow (roadmap item 7, governing design 2026-08-05-agent-flow-design-v2.md).
//
// An Agent Flow is a declarative task DAG: each task = hero@version + skills +
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
	FlowNodePending     FlowNodeState = "pending"
	FlowNodeRunning     FlowNodeState = "running"
	FlowNodeCompleted   FlowNodeState = "completed"
	FlowNodeFailed      FlowNodeState = "failed"
	FlowNodeBlocked     FlowNodeState = "blocked"
	FlowNodeCancelled   FlowNodeState = "cancelled"
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

// FlowTaskType distinguishes ordinary hero tasks from deterministic check gates.
const (
	FlowTaskHero  = "hero"
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

// FlowFanOut declares a read-only parallel expansion. The orchestrator starts
// Min instances and aggregates their outputs in order.
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
// The orchestrator counts rounds and terminalizes as convergence_exceeded.
type ConvergenceRule struct {
	From      string `json:"from" yaml:"from"`
	To        string `json:"to" yaml:"to"`
	MaxRounds int    `json:"maxRounds" yaml:"max_rounds"`
}

// AcceptanceResult is the three-valued verdict vocabulary of a machine-checked
// acceptance condition. These three strings are the only source of truth for
// the vocabulary: Go constants, SQL values, the OpenAPI enum, and the frontend
// types all reference them. There is deliberately no "verified" value and no
// boolean pass: a condition that cannot be decided is unverified, never
// silently true.
type AcceptanceResult string

const (
	// AcceptanceHolds means every declared leaf was decided true.
	AcceptanceHolds AcceptanceResult = "holds"
	// AcceptanceDoesNotHold means at least one declared leaf was decided false.
	AcceptanceDoesNotHold AcceptanceResult = "does_not_hold"
	// AcceptanceUnverified means no leaf was decided false, but at least one
	// could not be decided (evidence unavailable, truncated, or timed out).
	AcceptanceUnverified AcceptanceResult = "unverified"
)

// AcceptanceResultFromVerdict reads the aggregate result out of a stored
// verdict document. It returns the empty result when the document is absent or
// unreadable, which the summary counts in Total only: a verdict that cannot be
// read is never folded into a bucket, and never into holds.
func AcceptanceResultFromVerdict(verdict json.RawMessage) AcceptanceResult {
	if len(verdict) == 0 {
		return ""
	}
	var payload struct {
		Result AcceptanceResult `json:"result"`
	}
	if err := json.Unmarshal(verdict, &payload); err != nil {
		return ""
	}
	return payload.Result
}

// AcceptanceSummary is the run-level projection of the per-node acceptance
// verdicts. It exists because FlowState has no completed_unverified state: a
// run whose tasks all read `completed` can still hold tasks that were merely
// never disproved.
//
// The counts cover ONLY nodes that declared an acceptance clause, so a run
// without acceptance has no summary at all instead of one that reads as
// "everything holds". Total is the number of declared nodes; a declared node
// without a verdict yet (never folded, or reset by a resume) counts in Total
// only.
type AcceptanceSummary struct {
	Total       int `json:"total"`
	Holds       int `json:"holds"`
	DoesNotHold int `json:"doesNotHold"`
	Unverified  int `json:"unverified"`
}

// AddDeclared records one node that declared an acceptance clause. result is
// that node's verdict, or empty when it has none yet. An unrecognised result is
// counted in Total only: the summary never turns an unknown value into holds.
func (s *AcceptanceSummary) AddDeclared(result AcceptanceResult) {
	s.Total++
	switch result {
	case AcceptanceHolds:
		s.Holds++
	case AcceptanceDoesNotHold:
		s.DoesNotHold++
	case AcceptanceUnverified:
		s.Unverified++
	}
}

// FlowAcceptance is one machine-checkable acceptance condition of a task. It is
// evaluated by the kernel after the task's child Run finishes; the child's own
// prose is never consulted. File leaves are decided against the workspace, and
// their paths are workspace-relative. artifact-published is decided against the
// managed artifact store instead: it names an artifact, never a path, and it
// reads the child's recorded submit_result.artifactRefs rather than the
// workspace. There is no command field: shell-based checks are out of scope for
// this vocabulary.
type FlowAcceptance struct {
	// Artifact is the artifact NAME the task must have published. It is the
	// only operand of artifact-published and it is refused on every file leaf:
	// one clause is either about a path or about a published artifact. The name
	// is matched against the recorded reference exactly, the way column names
	// are: whitespace is significant, so nothing normalizes it behind the
	// author's back.
	Artifact string   `json:"artifact,omitempty" yaml:"artifact,omitempty"`
	File     string   `json:"file,omitempty"     yaml:"file,omitempty"`
	Check    string   `json:"check"              yaml:"check"`
	Columns  []string `json:"columns,omitempty"  yaml:"columns,omitempty"`
}

// FlowTask is one task of a flow definition.
type FlowTask struct {
	Hero     string            `json:"hero,omitempty" yaml:"hero,omitempty"`
	Skills   []string          `json:"skills,omitempty" yaml:"skills,omitempty"`
	Goal     string            `json:"goal,omitempty" yaml:"goal,omitempty"`
	Depends  []string          `json:"depends,omitempty" yaml:"depends,omitempty"`
	Type     string            `json:"type,omitempty" yaml:"type,omitempty"`
	Command  string            `json:"command,omitempty" yaml:"command,omitempty"`
	FanOut   *FlowFanOut       `json:"fanOut,omitempty" yaml:"fan_out,omitempty"`
	Budget   *FlowTaskBudget   `json:"budget,omitempty" yaml:"budget,omitempty"`
	Terminal *FlowTerminal     `json:"terminal,omitempty" yaml:"terminal,omitempty"`
	Output   string            `json:"output,omitempty" yaml:"output,omitempty"`
	Next     map[string]string `json:"next,omitempty" yaml:"next,omitempty"`
	// Writes declares the workspace paths this task mutates (globs relative to
	// /workspace). Used to grant controlled parallelism between writers when
	// the flow enables parallelism.allow_disjoint_writers; an unset/empty
	// value means "whole workspace" (exclusive lane). Scheduling-only today;
	// the sandbox does not enforce the declared scope.
	Writes []string `json:"writes,omitempty" yaml:"writes,omitempty"`
	// Acceptance declares machine-checkable conditions on this task's
	// artifacts. Empty means the task has no acceptance clause and runs exactly
	// as before.
	Acceptance []FlowAcceptance `json:"acceptance,omitempty" yaml:"acceptance,omitempty"`
	// ContextMode selects what this task's child assignment carries beyond the
	// resolved goal. Empty and "isolated" are the same thing: today's handoff,
	// the goal plus the acceptance block and nothing about the upstream tasks.
	// "snapshot" appends a bounded, deterministic digest of the `depends`
	// tasks' recorded output_ref, framed as result data rather than
	// instructions. It selects a payload; it is not a context vocabulary of its
	// own (a child Run is always task_only).
	ContextMode string `json:"contextMode,omitempty" yaml:"context_mode,omitempty"`
}

// FlowTaskContextMode values. There are exactly two: the handing-off task
// either shows nothing of its upstream results, or a bounded digest of them.
const (
	FlowTaskContextIsolated = "isolated"
	FlowTaskContextSnapshot = "snapshot"
)

// DefaultFlowParallelismMax is the ready-set dispatch concurrency ceiling when
// the flow does not specify parallelism.max (decision 2026-08-07).
const DefaultFlowParallelismMax = 10

// MaxFlowParallelismMax bounds parallelism.max at publish time.
const MaxFlowParallelismMax = 16

// FlowParallelism controls ready-set parallel dispatch of independent tasks.
// Max defaults to 10; AllowDisjointWriters is opt-in and gates parallel
// mutation between tasks that declare pairwise-disjoint Writes scopes.
type FlowParallelism struct {
	Max                  int  `json:"max,omitempty" yaml:"max,omitempty"`
	AllowDisjointWriters bool `json:"allowDisjointWriters,omitempty" yaml:"allow_disjoint_writers,omitempty"`
}

// FlowDefinition is the parsed flow contract. It is the authoring unit; the
// store publishes immutable versions from it and computes a config digest over
// its canonical JSON form.
type FlowDefinition struct {
	SchemaVersion int    `json:"schemaVersion" yaml:"schemaVersion"`
	ID            string `json:"id" yaml:"id"`
	// Name is the catalog label. Optional; a Graph must carry one before it
	// publishes.
	Name        string              `json:"name,omitempty" yaml:"name,omitempty"`
	Version     int                 `json:"version,omitempty" yaml:"version,omitempty"`
	Description string              `json:"description,omitempty" yaml:"description,omitempty"`
	Inputs      map[string]FlowPort `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Outputs     map[string]FlowPort `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Budget      FlowBudget          `json:"budget" yaml:"budget"`
	Tasks       map[string]FlowTask `json:"tasks" yaml:"tasks"`
	Convergence []ConvergenceRule   `json:"convergence,omitempty" yaml:"convergence,omitempty"`
	// Parallelism controls ready-set parallel dispatch (defaults: max 10,
	// allowDisjointWriters false). Optional; nil means defaults.
	Parallelism *FlowParallelism `json:"parallelism,omitempty" yaml:"parallelism,omitempty"`
	// UnknownTaskFields records the task-node YAML keys that the parser dropped
	// because the task struct does not know them (task -> keys, sorted).
	//
	// Parsing stays lenient on the read path so that revision loading and editor
	// loads keep working, including for definitions written by a newer Worker.
	// The publish validator rejects a definition with any recorded key, which is
	// where a typo such as `acceptence:` must fail instead of silently disabling
	// verification. It is json:"-"/yaml:"-" on purpose: it is derived from the
	// text, so it must never change the config digest or round-trip into an
	// exported definition.
	UnknownTaskFields map[string][]string `json:"-" yaml:"-"`
}

// EffectiveParallelismMax returns the ready-set dispatch concurrency ceiling:
// the declared flow value when parallelism is present and positive, else the
// default (10).
func (d *FlowDefinition) EffectiveParallelismMax() int {
	if d.Parallelism != nil && d.Parallelism.Max > 0 {
		return d.Parallelism.Max
	}
	return DefaultFlowParallelismMax
}

// DisjointWritersEnabled reports whether the flow opts into parallel mutation
// between tasks that declare pairwise-disjoint writes scopes.
func (d *FlowDefinition) DisjointWritersEnabled() bool {
	return d.Parallelism != nil && d.Parallelism.AllowDisjointWriters
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
	RunID           string          `json:"runId"`
	SessionID       string          `json:"sessionId"`
	ProjectID       string          `json:"projectId"`
	FlowVersionID   string          `json:"flowVersionId"`
	ManifestDigest  string          `json:"manifestDigest"`
	State           FlowState       `json:"state"`
	TotalTokensUsed int64           `json:"totalTokensUsed"`
	TerminalReason  string          `json:"terminalReason,omitempty"`
	InputsJSON      json.RawMessage `json:"inputs,omitempty"`
	// Acceptance is the run-level projection of the task verdicts. It is nil
	// when no task of the run declared an acceptance clause, so a run without
	// acceptance is indistinguishable from before the feature existed.
	Acceptance *AcceptanceSummary `json:"acceptance,omitempty"`
	CreatedAt  time.Time          `json:"createdAt"`
	UpdatedAt  time.Time          `json:"updatedAt"`
	FinishedAt *time.Time         `json:"finishedAt,omitempty"`
}

// RunAgentFlowNode is one task checkpoint. terminal_state + output_ref is the
// flow checkpoint: completed tasks are never replayed on resume.
type RunAgentFlowNode struct {
	RunID         string `json:"runId"`
	TaskIndex     int    `json:"taskIndex"`
	Handle        string `json:"handle"`
	HeroVersionID string `json:"heroVersionId,omitempty"`
	// SkillDigests is the bare task Skill ID list. It is kept for the public
	// Workers API shape (skillDigests: [id]); the digest-pinned view lives in
	// FrozenSkills, decoded from the same column.
	SkillDigests []string        `json:"skillDigests,omitempty"`
	GoalDigest   string          `json:"goalDigest,omitempty"`
	GoalText     string          `json:"goalText,omitempty"`
	BudgetJSON   json.RawMessage `json:"budget,omitempty"`
	// Acceptance is the acceptance specification frozen with the node at Run
	// start. It is frozen rather than re-read from the version at fold time so
	// a failed read can never silently skip verification (fail-open).
	Acceptance []FlowAcceptance `json:"acceptance,omitempty"`
	// AcceptanceVerdict is the machine-checked verdict of the frozen
	// specification, written in the same CAS as the checkpoint state. Empty
	// means "not declared, or cleared by a reset".
	AcceptanceVerdict json.RawMessage `json:"acceptanceVerdict,omitempty"`
	TerminalState     FlowNodeState   `json:"terminalState"`
	OutputRef         json.RawMessage `json:"outputRef,omitempty"`
	ChildRunID        string          `json:"childRunId,omitempty"`
	// ChildRunIDs is the full set for fan_out tasks (parallel instances).
	ChildRunIDs []string   `json:"childRunIds,omitempty"`
	ErrorCode   string     `json:"errorCode,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	// ReadOnly is the frozen concurrency class: true = hero cannot mutate the
	// workspace (may run in parallel with other readers); false = writer
	// (mutation lane, or disjoint-writes lane when the flow opts in).
	ReadOnly bool `json:"readOnly,omitempty"`
	// Writes are the frozen declared writes scopes (globs relative to
	// /workspace); empty means "whole workspace" (exclusive lane).
	Writes []string `json:"writes,omitempty"`
	// HeroDefinitionJSON is the full resolved HeroDefinition frozen at Run
	// start (global revision, graph-local hero, or inline task hero). It is
	// used to materialize child Runs without re-reading mutable files.
	HeroDefinitionJSON json.RawMessage `json:"heroDefinition,omitempty"`
	// FrozenSkills carries the task-level additive Skills with the content
	// digest frozen at Run start. It is internal (json:"-"): the public API
	// keeps exposing the Skill ID list through SkillDigests.
	FrozenSkills FrozenSkillRefs `json:"-"`
}

// FlowRunOutput is the folded flow output: the outputs port values plus the
// per-task checkpoint map (output_ref for each completed task).
type FlowRunOutput struct {
	Outputs    map[string]any             `json:"outputs"`
	TaskOutput map[string]json.RawMessage `json:"taskOutput"`
}
