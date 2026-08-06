package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Verification matrix 7/8/16 integration: a published flow freezes its
// manifest and task snapshots, the orchestrator runs it through the
// delegation substrate with typed handoff, and every flow event is durably
// persisted through the EventWriter (commit-before-publish) so the timeline
// can consume it after the fact.

func TestAgentFlowMatrixRunAndEventsPersisted(t *testing.T) {
	db, _, _, profiles, bindings, _, projectID, _, roleVersionID := setupFlowFixture(t)
	ctx := context.Background()

	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "Review", Slug: "review", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	def, err := agentflow.ParseDefinition([]byte(flowYAML("review", "")))
	require.NoError(t, err)
	version, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)

	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: projectID, Title: "matrix session",
	})
	require.NoError(t, err)
	binding, err := bindings.EnsureBindingExists(ctx, projectID, version.ID)
	require.NoError(t, err)
	_, err = bindings.Update(ctx, binding.ID, true)
	require.NoError(t, err)

	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "src/main.go"}, nil)
	require.NoError(t, err)
	// Matrix 7: freeze before the first child — exact role version, skill id,
	// goal digest, and per-task budget are captured in the node snapshots.
	freeze, diagnostics, err := (&store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}).
		FreezeFlowDefinition(ctx, projectID, def, inputs)
	require.NoError(t, err, diagnostics)
	require.Len(t, freeze, 3)
	assert.Equal(t, roleVersionID, freeze[0].RoleVersionID)
	assert.Equal(t, "skill-go-dev", freeze[0].SkillIDs[0])
	assert.NotEmpty(t, freeze[0].GoalDigest)

	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)

	// Drive the orchestrator with a stub child provider: producer returns a
	// typed payload, reviewer consumes it (typed handoff), terminal completes.
	hub := events.NewHub()
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	children := &stubMatrixChildren{db: db}
	orch := &agentflow.Orchestrator{
		Store:        &store.OrchestratorStore{Runs: flowRuns, Profiles: profiles},
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
	assert.Equal(t, domain.FlowStateCompleted, finalRun.State)

	// Matrix 8: typed handoff — reviewer's child assignment used producer's
	// output.
	assert.Contains(t, children.assignments[1], "\"a.go\"")

	// Matrix 16: the Phase 1 event set is durably persisted in order and
	// consumable (the timeline's After() source).
	eventsRepo := &store.EventRepo{DB: db}
	committed, err := eventsRepo.After(ctx, run.RunID, 0, 100)
	require.NoError(t, err)
	types := make([]string, 0, len(committed))
	for _, event := range committed {
		types = append(types, event.EventType)
	}
	require.Contains(t, types, "flow_started")
	require.Contains(t, types, "flow_task_started")
	require.Contains(t, types, "flow_task_completed")
	require.Contains(t, types, "flow_completed")
	assert.Equal(t, types[len(types)-1], "flow_completed")
	// Events carry flow_run_id + task/child refs.
	started := findEvent(committed, "flow_started")
	assert.Contains(t, string(started.Payload), run.RunID)
	completed := findEvent(committed, "flow_task_completed")
	assert.Contains(t, string(completed.Payload), "producer")
	assert.Contains(t, string(completed.Payload), "childRunId")

	// Checkpoints: producer + reviewer completed with outputs; terminal gate
	// produced the flow outputs.
	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodes {
		byHandle[node.Handle] = node
	}
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["producer"].TerminalState)
	assert.NotEmpty(t, byHandle["producer"].OutputRef)
	assert.Equal(t, domain.FlowNodeCompleted, byHandle["reviewer"].TerminalState)
}

// stubMatrixChildren materializes real delegation rows (so the child finalizer
// paths run) but settles children immediately with a typed payload.
type stubMatrixChildren struct {
	db          *sql.DB
	assignments []string
}

func (c *stubMatrixChildren) CreateTaskChild(ctx context.Context, parentRunID, sessionID string,
	spec agentflow.ChildSpec) (agentflow.ChildInfo, error) {
	info, err := (&store.OrchestratorChildren{DB: c.db, Delegations: &store.DelegationRepo{DB: c.db}}).
		CreateTaskChild(ctx, parentRunID, sessionID, spec)
	if err != nil {
		return info, err
	}
	c.assignments = append(c.assignments, spec.Assignment)
	// Settle the child as succeeded with a typed payload immediately.
	resultJSON, _ := json.Marshal(domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done",
		Payload: json.RawMessage(`{"changed_files":["a.go"]}`),
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

func (c *stubMatrixChildren) ChildRunStatus(ctx context.Context, runID string) (domain.RunStatus, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildRunStatus(ctx, runID)
}

func (c *stubMatrixChildren) ChildTerminalResult(ctx context.Context, runID string) (*domain.SubmitResult, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildTerminalResult(ctx, runID)
}

func (c *stubMatrixChildren) ChildUsage(ctx context.Context, runID string) (domain.RunBudgetUsage, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildUsage(ctx, runID)
}

func (c *stubMatrixChildren) CancelChildRun(ctx context.Context, runID string) error {
	return (&store.OrchestratorChildren{DB: c.db}).CancelChildRun(ctx, runID)
}

func roleTimeNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func findEvent(events []domain.RunEvent, eventType string) *domain.RunEvent {
	for i := range events {
		if events[i].EventType == eventType {
			return &events[i]
		}
	}
	return nil
}
