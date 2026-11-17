package store_test

// Durable scheduling state of a Graph task (retry attempts and the frozen
// `optional` declaration). The orchestrator's in-memory attempt bookkeeping is
// worthless if these columns do not survive a restart, so they are pinned here
// against a real Session database rather than against a fake store.

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schedulingRunFixture freezes the standard fixture with `optional: true` on the
// producer task. The freeze list built here deliberately does NOT set
// FlowNodeFreeze.Optional: CreateFlowRun takes it from the frozen definition, so
// a caller that cannot know the declaration cannot drop it.
func schedulingRunFixture(t *testing.T) (*store.AgentFlowRunRepo, string) {
	t.Helper()
	db, flowRuns, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(flowYAML("scheduling-store", "")))
	require.NoError(t, err)
	producer := def.Tasks["producer"]
	producer.Optional = true
	def.Tasks["producer"] = producer

	version := flowVersionFromDef(def)
	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "src/main.go"}, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := freezeFlowForTest(t, db, flowRuns, projectID, "", def, inputs)
	require.NoError(t, err, diagnostics)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: sqlCreateSession(t, db, projectID).ID, ProjectID: projectID,
		FlowVersionID: version.ID, DefinitionJSON: version.DefinitionJSON,
		ConfigDigest: version.ConfigDigest, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)
	return flowRuns, run.RunID
}

// The anchor Run is born running, so it must record when. Without started_at a
// finished flow row has an end and no beginning, and any duration derived from
// the pair silently reads zero.
func TestFlowAnchorRunRecordsItsStart(t *testing.T) {
	flowRuns, runID := schedulingRunFixture(t)

	var startedAt sql.NullString
	require.NoError(t, flowRuns.DB.QueryRow(`SELECT started_at FROM agent_runs WHERE id = ?`, runID).Scan(&startedAt))
	require.True(t, startedAt.Valid, "a flow anchor starts when it is created")

	var createdAt string
	require.NoError(t, flowRuns.DB.QueryRow(`SELECT created_at FROM agent_runs WHERE id = ?`, runID).Scan(&createdAt))
	assert.Equal(t, createdAt, startedAt.String, "the anchor starts when it is created, not when it is claimed")
	_, err := time.Parse(time.RFC3339Nano, startedAt.String)
	require.NoError(t, err, "started_at uses the same stamp family as every other Run")
}

// `optional` is frozen onto the node row, and a fresh node has spent no attempt.
func TestFlowRunFreezesOptionalAndAttempts(t *testing.T) {
	flowRuns, runID := schedulingRunFixture(t)

	producer := nodeByHandle(t, flowRuns, runID, "producer")
	assert.True(t, producer.Optional, "the declaration is frozen with the node")
	assert.Equal(t, 0, producer.Attempts, "a fresh node has spent no retry attempt")

	reviewer := nodeByHandle(t, flowRuns, runID, "reviewer")
	assert.False(t, reviewer.Optional, "a task without the flag freezes false")
}

// The retry reset is one write: state back to pending, the previous attempt's
// error/output/verdict cleared, and one attempt spent.
func TestFlowNodeRetryResetSpendsOneAttempt(t *testing.T) {
	flowRuns, runID := schedulingRunFixture(t)
	ctx := context.Background()
	producer := nodeByHandle(t, flowRuns, runID, "producer")
	verdict := json.RawMessage(`{"result":"unverified","checks":[]}`)

	_, err := flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: producer.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, ChildRunID: "child-1",
		OutputRef: json.RawMessage(`{"changedFiles":["a.go"]}`), ErrorCode: "task_failed",
		AcceptanceVerdict: &verdict,
	})
	require.NoError(t, err)

	// The re-dispatch CAS: running -> pending, reset + spend in one write.
	_, err = flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: producer.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodePending, ClearErrorCode: true, ClearOutputRef: true,
		AcceptanceVerdict: &json.RawMessage{}, IncrementAttempts: true,
	})
	require.NoError(t, err)

	reset := nodeByHandle(t, flowRuns, runID, "producer")
	assert.Equal(t, domain.FlowNodePending, reset.TerminalState)
	assert.Equal(t, 1, reset.Attempts)
	assert.Empty(t, reset.ErrorCode, "a re-dispatched node must not look failed")
	assert.Empty(t, reset.OutputRef, "the previous attempt's result is not a checkpoint")
	assert.Empty(t, reset.AcceptanceVerdict, "the previous attempt's verdict is cleared")
	assert.True(t, reset.Optional, "the frozen declaration is untouched by a reset")

	// A plain dispatch/state write leaves the counter alone: only the explicit
	// IncrementAttempts spends an attempt.
	_, err = flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: producer.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, nodeByHandle(t, flowRuns, runID, "producer").Attempts)
}

// Resume reopens the run but never hands back an attempt it already spent:
// otherwise `retry.max` would be unbounded across restarts.
func TestFlowNodeAttemptsSurviveResume(t *testing.T) {
	flowRuns, runID := schedulingRunFixture(t)
	ctx := context.Background()
	producer := nodeByHandle(t, flowRuns, runID, "producer")

	_, err := flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: producer.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeRunning, IncrementAttempts: true,
	})
	require.NoError(t, err)
	_, err = flowRuns.UpdateNode(ctx, runID, store.NodeUpdate{
		TaskIndex: producer.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodeRunning},
		SetState: domain.FlowNodeFailed, ErrorCode: "task_failed",
		OutputRef: json.RawMessage(`{"changedFiles":["a.go"]}`),
	})
	require.NoError(t, err)
	_, err = flowRuns.UpdateFlowState(ctx, runID, domain.FlowStateFailed, 0, "task failed")
	require.NoError(t, err)

	_, err = flowRuns.ResumeFlowRun(ctx, runID)
	require.NoError(t, err)

	resumed := nodeByHandle(t, flowRuns, runID, "producer")
	assert.Equal(t, domain.FlowNodePending, resumed.TerminalState)
	assert.Equal(t, 1, resumed.Attempts, "a resume must not re-grant a spent attempt")
	assert.True(t, resumed.Optional, "the frozen declaration survives a resume")
	assert.Empty(t, resumed.ErrorCode)
	assert.Empty(t, resumed.OutputRef)
}

// B2. The flow's cancel must reach BOTH channels: the durable row and the
// in-process execution. A DB-only cancel marks the child cancelled while its loop
// keeps executing, so a `retry` deciding on the row dispatches a second child next
// to it in the same workspace.
func TestCancelChildRunReachesExecutionAndRow(t *testing.T) {
	db, flowRuns, projectID, _, heroVersionID := setupFlowFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(flowYAML("cancel-wiring", "")))
	require.NoError(t, err)
	version := flowVersionFromDef(def)
	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "src/main.go"}, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := freezeFlowForTest(t, db, flowRuns, projectID, "", def, inputs)
	require.NoError(t, err, diagnostics)
	session := sqlCreateSession(t, db, projectID)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID,
		DefinitionJSON: version.DefinitionJSON, ConfigDigest: version.ConfigDigest, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)
	var producerNode *store.FlowNodeFreeze
	for i := range freeze {
		if freeze[i].Handle == "producer" {
			producerNode = &freeze[i]
		}
	}
	require.NotNil(t, producerNode)
	var ceiling domain.BudgetCeilingJSON
	require.NoError(t, json.Unmarshal(producerNode.BudgetJSON, &ceiling))

	var cancelledExecutions []string
	policies := &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}
	children := &store.OrchestratorChildren{
		DB:          db,
		Delegations: &store.DelegationRepo{DB: db, Policies: policies},
		Policies:    policies,
		CancelExecution: func(ctx context.Context, runID string) error {
			cancelledExecutions = append(cancelledExecutions, runID)
			return nil
		},
	}
	first, err := children.CreateTaskChild(ctx, run.RunID, session.ID, agentflow.ChildSpec{
		Handle: "producer", HeroVersionID: heroVersionID, Assignment: "a",
		HeroDefinitionJSON: producerNode.HeroDefinitionJSON, Budget: ceiling,
	})
	require.NoError(t, err)
	require.NoError(t, children.CancelChildRun(ctx, first.RunID))

	assert.Equal(t, []string{first.RunID}, cancelledExecutions,
		"the in-process execution is cancelled, not only the row")
	status, err := children.ChildRunStatus(ctx, first.RunID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, status)

	// The execution channel runs even when the row write is refused (a child that
	// already settled cannot transition to cancelled), so an error on one channel
	// never leaves a live execution behind - and the error is surfaced, not
	// swallowed.
	cancelledExecutions = nil
	second, err := children.CreateTaskChild(ctx, run.RunID, session.ID, agentflow.ChildSpec{
		Handle: "producer", HeroVersionID: heroVersionID, Assignment: "a",
		HeroDefinitionJSON: producerNode.HeroDefinitionJSON, Budget: ceiling,
	})
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE agent_runs SET status='succeeded', finished_at=? WHERE id=?`,
		heroTimeNow(), second.RunID)
	require.NoError(t, err)

	err = children.CancelChildRun(ctx, second.RunID)
	require.Error(t, err, "a settled child refuses the cancel; the error is not discarded")
	assert.Equal(t, []string{second.RunID}, cancelledExecutions)
	status, err = children.ChildRunStatus(ctx, second.RunID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, status, "the settled outcome is untouched")
}
