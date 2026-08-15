package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Design-synthesis graph verification (user request 2026-08-07):
//
//	a0 (entry dispatcher)
//	 -> a1,a2,a3 (3 explore tasks, distinct goals, write .docs/plan/*.md)
//	    -> b1,b2 (2 synthesis tasks, each depends on all 3 explores)
//	       -> c1 (1 consolidation task)
//	          -> d1 (1 review+modify task)
//	             -> accept (terminal)
//
// Engine invariants exercised: single entry requirement, strict serial
// topological dispatch, multi-upstream fan-in, and typed {task.X.output}
// handoff through the convergence-free DAG.
func designGraphYAML(id string) string {
	return `schemaVersion: 1
id: ` + id + `
description: multi-tier design synthesis
inputs:
  target: {type: path, required: true}
outputs:
  final: {type: string}
budget:
  max_total_tokens: 200000
tasks:
  a0:
    role: flow-worker@1
    goal: "Define exploration brief for {inputs.target}"
  a1:
    role: flow-worker@1
    goal: "Explore design option A1 for {inputs.target}; write .docs/plan/` + id + `-a1.md"
    depends: [a0]
  a2:
    role: flow-worker@1
    goal: "Explore design option A2 for {inputs.target}; write .docs/plan/` + id + `-a2.md"
    depends: [a0]
  a3:
    role: flow-worker@1
    goal: "Explore design option A3 for {inputs.target}; write .docs/plan/` + id + `-a3.md"
    depends: [a0]
  b1:
    role: flow-worker@1
    goal: "Synthesize {task.a1.output.proposal}, {task.a2.output.proposal}, {task.a3.output.proposal} into design B1; write .docs/plan/` + id + `-b1.md"
    depends: [a1, a2, a3]
  b2:
    role: flow-worker@1
    goal: "Synthesize {task.a1.output.proposal}, {task.a2.output.proposal}, {task.a3.output.proposal} into design B2; write .docs/plan/` + id + `-b2.md"
    depends: [a1, a2, a3]
  c1:
    role: flow-worker@1
    goal: "Consolidate {task.b1.output.proposal} and {task.b2.output.proposal} into one design C1; write .docs/plan/` + id + `-c1.md"
    depends: [b1, b2]
  d1:
    role: flow-worker@1
    goal: "Review C1 {task.c1.output.proposal}; produce final modified plan; write .docs/plan/` + id + `-final.md"
    depends: [c1]
  accept:
    terminal: {status: success, output: final}
    output: final
    depends: [d1]
`
}

func TestDesignGraphMultiTierSynthesis(t *testing.T) {
	db, flowRuns, projectID, _, roleVersionID := setupFlowFixture(t)
	ctx := context.Background()

	def, err := agentflow.ParseDefinition([]byte(designGraphYAML("design-synthesis")))
	require.NoError(t, err)

	// The 3-entry variant of this graph must be rejected by publish validation
	// (exactly one entry task), which is why a0 dispatches the explore tier.
	threeEntry, err := agentflow.ParseDefinition([]byte(designGraphYAML("design-synthesis")))
	require.NoError(t, err)
	withoutDispatcher := threeEntry.Tasks
	delete(withoutDispatcher, "a0")
	for _, task := range withoutDispatcher {
		task.Depends = nil // A1-A3 become 3 entry tasks
	}
	threeEntry.Tasks = withoutDispatcher
	result := store.NewFlowValidator(store.FlowPublishOptions{
		DB: db, ProjectID: projectID, Skills: map[string]bool{"go-dev": true},
		Sources: testFlowSources, Models: testFlowModels,
	}).Validate(ctx, threeEntry)
	assertEntryFailure := false
	for _, diag := range result.Diagnostics {
		if diag.Code == "entry_task_count" {
			assertEntryFailure = true
			assert.Contains(t, diag.Message, "found 3")
		}
	}
	assert.True(t, assertEntryFailure, "3 entry tasks must be rejected")

	// Freeze and run the real (single-entry) graph.
	version := flowVersionFromDef(def)

	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "pipeline"}, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := freezeFlowForTest(t, db, flowRuns, projectID, "", def, inputs)
	require.NoError(t, err, diagnostics)
	require.Len(t, freeze, 9) // a0,a1,a2,a3,b1,b2,c1,d1 role nodes + accept terminal node

	session := sqlCreateSession(t, db, projectID)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID,
		DefinitionJSON: version.DefinitionJSON, ConfigDigest: version.ConfigDigest, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)

	hub := events.NewHub()
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	children := &counterStubChildren{db: db,
		policies: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}
	orch := &agentflow.Orchestrator{
		Store:        &store.OrchestratorStore{Runs: flowRuns},
		Children:     children,
		Events:       &store.FlowEventSink{Writer: writer},
		PollInterval: 5 * time.Millisecond,
	}
	orch.Start(ctx, run.RunID)

	deadline := time.Now().Add(10 * time.Second)
	var finalRun *domain.RunAgentFlow
	for time.Now().Before(deadline) {
		current, err := flowRuns.GetRun(ctx, run.RunID)
		require.NoError(t, err)
		if current.State.Terminal() {
			finalRun = current
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotNil(t, finalRun, "flow run must terminalize")
	t.Logf("PROBE terminal state=%s reason=%q", finalRun.State, finalRun.TerminalReason)
	assert.Equal(t, domain.FlowStateCompleted, finalRun.State)

	// 1. Strict serial topological dispatch order.
	handles := []string{"a0", "a1", "a2", "a3", "b1", "b2", "c1", "d1"}
	require.Len(t, children.assignments, len(handles))

	// 2. Typed fan-in: b1's goal contains the three explore outputs (1,2,3),
	//    c1 contains b1+b2 (4,5), d1 contains c1 (6).
	//    a0=0, a1=1, a2=2, a3=3, b1=4, b2=5, c1=6, d1=7 (counter-based payloads).
	b := children.assignments
	assert.Contains(t, b[4], "1, 2, 3", "b1 must fan in all three explore outputs")
	assert.Contains(t, b[5], "1, 2, 3", "b2 must fan in all three explore outputs")
	assert.Contains(t, b[6], "4 and 5", "c1 must fan in b1+b2 outputs")
	assert.Contains(t, b[7], "6", "d1 must reference c1 output")
	// 3. Explore goals carry the file-write instructions verbatim.
	assert.Contains(t, b[1], ".docs/plan/design-synthesis-a1.md")
	assert.Contains(t, b[3], ".docs/plan/design-synthesis-a3.md")
	// 4. Roles are frozen to the fixture role version for every role node.
	for _, node := range freeze {
		if node.Handle == "accept" {
			continue // terminal node has no RoleVersionID
		}
		assert.Equal(t, roleVersionID, node.RoleVersionID)
	}

	// 5. Checkpoints: every role node completed; terminal produced flow output.
	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodes {
		byHandle[node.Handle] = node
	}
	for _, handle := range handles {
		assert.Equal(t, domain.FlowNodeCompleted, byHandle[handle].TerminalState, handle)
		assert.NotEmpty(t, byHandle[handle].OutputRef, handle)
	}
	// Terminal gate resolves flow outputs; the accept node itself is not
	// role-dispatched and stays pending.
	assert.Equal(t, domain.FlowNodePending, byHandle["accept"].TerminalState)
}

// counterStubChildren settles each child with {"proposal": "<n>"} where n is
// the creation counter, so fan-in references resolve to distinct values.
type counterStubChildren struct {
	db          *sql.DB
	policies    *fileconfig.PolicyStore
	assignments []string
	counter     int
}

func (c *counterStubChildren) CreateTaskChild(ctx context.Context, parentRunID, sessionID string,
	spec agentflow.ChildSpec) (agentflow.ChildInfo, error) {
	info, err := (&store.OrchestratorChildren{DB: c.db, Policies: c.policies,
		Delegations: &store.DelegationRepo{DB: c.db, Policies: c.policies}}).
		CreateTaskChild(ctx, parentRunID, sessionID, spec)
	if err != nil {
		return info, err
	}
	c.assignments = append(c.assignments, spec.Assignment)
	value := c.counter
	c.counter++
	resultJSON, _ := json.Marshal(domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done",
		Payload: json.RawMessage(`{"proposal":"` + string(rune('0'+value)) + `"}`),
	})
	_, err = c.db.ExecContext(ctx, `UPDATE delegation_items SET status='succeeded', result_json=?
		WHERE id=? AND status='running'`, string(resultJSON), info.ItemID)
	if err != nil {
		return info, err
	}
	_, err = c.db.ExecContext(ctx, `UPDATE delegation_item_attempts SET status='succeeded', finished_at=?
		WHERE child_run_id=? AND status='queued'`, roleTimeNow(), info.RunID)
	if err != nil {
		return info, err
	}
	_, err = c.db.ExecContext(ctx, `UPDATE agent_runs SET status='succeeded', finished_at=?
		WHERE id=? AND status IN ('queued','running')`, roleTimeNow(), info.RunID)
	return info, err
}

func (c *counterStubChildren) ChildRunStatus(ctx context.Context, runID string) (domain.RunStatus, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildRunStatus(ctx, runID)
}

func (c *counterStubChildren) ChildTerminalResult(ctx context.Context, runID string) (*domain.SubmitResult, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildTerminalResult(ctx, runID)
}

func (c *counterStubChildren) ChildUsage(ctx context.Context, runID string) (domain.RunBudgetUsage, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildUsage(ctx, runID)
}

func (c *counterStubChildren) CancelChildRun(ctx context.Context, runID string) error {
	return (&store.OrchestratorChildren{DB: c.db}).CancelChildRun(ctx, runID)
}
