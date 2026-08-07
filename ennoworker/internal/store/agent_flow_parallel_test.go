package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ready-set parallel dispatch integration tests (design 2026-08-07): independent
// tasks with distinct goals run concurrently up to parallelism.max; writers
// hold the mutation lane unless the flow opts into disjoint writes scopes;
// a failing sibling cancels the rest of the batch; recovery re-dispatches
// every interrupted in-flight child.

func setupParallelFixture(t *testing.T) (db *sql.DB, projectID string, readerVersionID, writerVersionID string) {
	t.Helper()
	db = store.SetupDB(t)
	ctx := context.Background()
	projects := &store.ProjectRepo{DB: db}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Parallel", HostPath: t.TempDir()})
	require.NoError(t, err)
	provider, err := (&store.ProviderRepo{DB: db}).Create(ctx, store.CreateProviderInput{
		Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test", CredentialRef: "env:PAR_TEST_KEY",
	})
	require.NoError(t, err)
	model, err := (&store.ModelRepo{DB: db}).Create(ctx, store.CreateModelInput{
		ProviderID: provider.ID, ModelName: "par-model", ContextWindow: 32000, MaxOutputTokens: 2048,
		SupportsToolUse: true, SupportsThinking: true,
		ThinkingDialect:          domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium},
	})
	require.NoError(t, err)
	roles := &store.RoleRepo{DB: db, KnownTools: map[string]bool{"read": true, "grep": true, "write": true}}

	newRole := func(handle, authority string, tools []string) string {
		def := validRoleDefinition(model.ID)
		def.DelegationPolicy.Admission = domain.DelegationAutoWithinBudget
		def.DelegationPolicy.AllowedCallerKinds = []string{"host"}
		def.DelegationPolicy.MaxInvocationsPerParentRun = 64
		def.DelegationPolicy.MaxConcurrentInstances = 64
		def.DelegationPolicy.BudgetCeiling.MaxTotalTokens = 200000
		def.DelegationPolicy.BudgetCeiling.MaxWallTimeMS = 1_800_000
		def.Authority = domain.RoleAuthority(authority)
		if authority == string(domain.RoleAuthorityReadOnly) {
			def.PermissionCeiling = domain.PermissionDiscuss
		} else {
			def.PermissionCeiling = domain.PermissionAuto
		}
		def.AllowedTools = tools
		role, err := roles.Create(ctx, store.CreateRoleInput{
			Handle: handle, Name: handle, Description: handle, Positioning: handle,
			Icon: "bot", Color: "neutral", Scope: domain.RoleScopeProject, ProjectID: &project.ID, Definition: def,
		})
		require.NoError(t, err)
		version, err := roles.Publish(ctx, role.ID, 0)
		require.NoError(t, err)
		return version.ID
	}
	readerID := newRole("par-reader", string(domain.RoleAuthorityReadOnly), []string{"read", "grep"})
	writerID := newRole("par-writer", string(domain.RoleAuthorityMutation), []string{"read", "grep", "write"})
	return db, project.ID, readerID, writerID
}

// parallelFlowYAML builds the A1-A3 design graph: a0 dispatcher (reader) ->
// three independent explore tasks -> terminal. roleRef selects the task class
// (reader/writer), writesBlock adds per-task writes declarations, and
// parallelismBlock configures the flow-level parallelism section.
func parallelFlowYAML(id, roleRef, parallelismBlock, writesBlock string) string {
	return `schemaVersion: 1
id: ` + id + `
inputs:
  target: {type: path, required: true}
outputs:
  final: {type: string}
budget:
  max_total_tokens: 200000
` + parallelismBlock + `
tasks:
  a0:
    role: par-reader@1
    goal: "Dispatch {inputs.target}"
  a1:
    role: ` + roleRef + `@1
    goal: "Explore option A1 for {inputs.target}"
    depends: [a0]
    ` + writesBlock + `
  a2:
    role: ` + roleRef + `@1
    goal: "Explore option A2 for {inputs.target}"
    depends: [a0]
  a3:
    role: ` + roleRef + `@1
    goal: "Explore option A3 for {inputs.target}"
    depends: [a0]
  accept:
    terminal: {status: success, output: final}
    output: final
    depends: [a1, a2, a3]
`
}

// controllableChildren is a child provider whose settlement is gated by the
// test: handles in hold stay running until Settle(handle); every other child
// settles immediately with {"proposal":"<counter>"}. It records dispatch
// order and the max number of concurrently in-flight children.
type controllableChildren struct {
	db          *sql.DB
	mu          sync.Mutex
	created     []string
	assignments []string
	runIDs      map[string]string
	itemIDs     map[string]string
	hold        map[string]bool
	fail        map[string]bool
	concurrent  int
	maxConcurr  int
	counter     int
}

func newControllableChildren(db *sql.DB, hold []string) *controllableChildren {
	holds := map[string]bool{}
	for _, h := range hold {
		holds[h] = true
	}
	return &controllableChildren{
		db: db, runIDs: map[string]string{}, itemIDs: map[string]string{}, hold: holds, fail: map[string]bool{},
	}
}

func (c *controllableChildren) CreateTaskChild(ctx context.Context, parentRunID, sessionID string,
	spec agentflow.ChildSpec) (agentflow.ChildInfo, error) {
	info, err := (&store.OrchestratorChildren{DB: c.db, Delegations: &store.DelegationRepo{DB: c.db}}).
		CreateTaskChild(ctx, parentRunID, sessionID, spec)
	if err != nil {
		return info, err
	}
	// Record a deterministic usage ledger so budget folding sees 1000 tokens
	// per child (mirrors a real Run's run_budgets row).
	if _, err := c.db.ExecContext(ctx, `INSERT INTO run_budgets
		(run_id, consumed_tokens, reserved_at) VALUES (?, 1000, ?)
		ON CONFLICT(run_id) DO UPDATE SET consumed_tokens=1000`, info.RunID, roleTimeNow()); err != nil {
		return info, err
	}
	c.mu.Lock()
	c.created = append(c.created, spec.Handle)
	c.assignments = append(c.assignments, spec.Assignment)
	c.runIDs[spec.Handle] = info.RunID
	c.itemIDs[spec.Handle] = info.ItemID
	c.concurrent++
	if c.concurrent > c.maxConcurr {
		c.maxConcurr = c.concurrent
	}
	hold := c.hold[spec.Handle]
	shouldFail := c.fail[spec.Handle]
	c.mu.Unlock()
	if hold {
		return info, nil // stays running until Settle(handle)
	}
	if shouldFail {
		return info, c.settle(ctx, info, domain.RunFailed, `{"error":"boom"}`)
	}
	value := c.counter
	c.counter++
	return info, c.settle(ctx, info, domain.RunSucceeded, `{"proposal":"`+string(rune('0'+value))+`"}`)
}

func (c *controllableChildren) settle(ctx context.Context, info agentflow.ChildInfo,
	status domain.RunStatus, payload string) error {
	resultJSON, _ := json.Marshal(domain.SubmitResult{
		Status: domain.SubmitCompleted, Summary: "done", Payload: json.RawMessage(payload),
	})
	itemStatus := "succeeded"
	runStatus := "succeeded"
	if status != domain.RunSucceeded {
		itemStatus = "failed"
		runStatus = "failed"
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE delegation_items SET status=?, result_json=?
		WHERE id=? AND status='running'`, itemStatus, string(resultJSON), info.ItemID); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE delegation_item_attempts SET status=?, finished_at=?
		WHERE child_run_id=? AND status='queued'`, itemStatus, roleTimeNow(), info.RunID); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `UPDATE agent_runs SET status=?, finished_at=?
		WHERE id=? AND status IN ('queued','running')`, runStatus, roleTimeNow(), info.RunID); err != nil {
		return err
	}
	c.mu.Lock()
	c.concurrent--
	c.mu.Unlock()
	return nil
}

// Settle(handle) completes a held child so the flow can advance.
func (c *controllableChildren) Settle(ctx context.Context, t *testing.T, handle string) {
	t.Helper()
	c.mu.Lock()
	info := agentflow.ChildInfo{RunID: c.runIDs[handle], ItemID: c.itemIDs[handle]}
	c.mu.Unlock()
	value := c.counter
	c.counter++
	require.NoError(t, c.settle(ctx, info, domain.RunSucceeded, `{"proposal":"`+string(rune('0'+value))+`"}`))
}

func (c *controllableChildren) ChildRunStatus(ctx context.Context, runID string) (domain.RunStatus, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildRunStatus(ctx, runID)
}

func (c *controllableChildren) ChildTerminalResult(ctx context.Context, runID string) (*domain.SubmitResult, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildTerminalResult(ctx, runID)
}

func (c *controllableChildren) ChildUsage(ctx context.Context, runID string) (domain.RunBudgetUsage, error) {
	return (&store.OrchestratorChildren{DB: c.db}).ChildUsage(ctx, runID)
}

func (c *controllableChildren) CancelChildRun(ctx context.Context, runID string) error {
	return (&store.OrchestratorChildren{DB: c.db}).CancelChildRun(ctx, runID)
}

func (c *controllableChildren) createdCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.created)
}

func (c *controllableChildren) maxConcurrency() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxConcurr
}

// runParallelFlow drives a published flow to terminal and returns the final run.
func runParallelFlow(t *testing.T, db *sql.DB, profiles *store.AgentFlowProfileRepo, projectID string,
	def *domain.FlowDefinition, children *controllableChildren) *domain.RunAgentFlow {
	t.Helper()
	ctx := context.Background()
	profile, err := profiles.CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: def.ID, Slug: def.ID, SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	version, err := profiles.CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: projectID, Title: "parallel session",
	})
	require.NoError(t, err)
	bindings := &store.AgentFlowBindingRepo{DB: db}
	binding, err := bindings.EnsureBindingExists(ctx, projectID, version.ID)
	require.NoError(t, err)
	_, err = bindings.Update(ctx, binding.ID, true)
	require.NoError(t, err)
	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "pipeline"}, nil)
	require.NoError(t, err)
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}
	freeze, diagnostics, err := flowRuns.FreezeFlowDefinition(ctx, projectID, "", def, inputs)
	require.NoError(t, err, diagnostics)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)

	hub := events.NewHub()
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	orch := &agentflow.Orchestrator{
		Store:        &store.OrchestratorStore{Runs: flowRuns, Profiles: profiles},
		Children:     children,
		Events:       &store.FlowEventSink{Writer: writer},
		PollInterval: 3 * time.Millisecond,
	}
	orch.Start(ctx, run.RunID)
	return run
}

func waitFlowTerminal(t *testing.T, flowRuns *store.AgentFlowRunRepo, runID string) *domain.RunAgentFlow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current, err := flowRuns.GetRun(context.Background(), runID)
		require.NoError(t, err)
		if current.State.Terminal() {
			return current
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Fail(t, "flow run did not terminalize")
	return nil
}

// Three independent reader tasks with distinct goals dispatch concurrently
// (max in-flight reaches 3) and all complete; the flow finishes.
func TestParallelReadySetDispatchReaders(t *testing.T) {
	db, projectID, readerID, _ := setupParallelFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(parallelFlowYAML("par-readers",
		"par-reader", "parallelism:\n  max: 4\n", "")))
	require.NoError(t, err)
	children := newControllableChildren(db, []string{"a1", "a2", "a3"})
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}
	run := runParallelFlow(t, db, &store.AgentFlowProfileRepo{DB: db}, projectID, def, children)

	// Wait until all three explores are dispatched (or the flow mis-terminates).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && children.createdCount() < 4 {
		current, _ := flowRuns.GetRun(ctx, run.RunID)
		require.False(t, current.State.Terminal(), "flow terminated before all explores dispatched: %s", current.State)
		time.Sleep(3 * time.Millisecond)
	}
	require.Equal(t, 4, children.createdCount(), "a0 + a1,a2,a3 must all be dispatched while in flight")
	assert.Equal(t, 3, children.maxConcurrency(), "three readers must run concurrently")

	// Settle them one by one; the flow completes.
	children.Settle(ctx, t, "a1")
	children.Settle(ctx, t, "a2")
	children.Settle(ctx, t, "a3")
	finalRun := waitFlowTerminal(t, flowRuns, run.RunID)
	t.Logf("READERS final state=%s reason=%s", finalRun.State, finalRun.TerminalReason)
	assert.Equal(t, domain.FlowStateCompleted, finalRun.State)
	// Roles are frozen to the reader version for every role node.
	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	for _, n := range nodes {
		if n.RoleVersionID != "" {
			assert.Equal(t, readerID, n.RoleVersionID)
			assert.True(t, n.ReadOnly, n.Handle)
		}
	}
}

// Writers without disjoint-writes opt-in hold the mutation lane: only one is
// dispatched at a time even though all three are ready.
func TestParallelWritersExclusiveLane(t *testing.T) {
	db, projectID, _, _ := setupParallelFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(parallelFlowYAML("par-writers",
		"par-writer", "", "")))
	require.NoError(t, err)
	children := newControllableChildren(db, []string{"a1", "a2", "a3"})
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}
	run := runParallelFlow(t, db, &store.AgentFlowProfileRepo{DB: db}, projectID, def, children)

	// a0 dispatches, then exactly one writer; the others must stay pending.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && children.createdCount() < 2 {
		time.Sleep(3 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // generous window to catch a leak
	assert.Equal(t, 2, children.createdCount(), "only a0 + one writer may dispatch")
	assert.Equal(t, 1, children.maxConcurrency(), "writers must hold an exclusive lane")

	children.Settle(ctx, t, "a1")
	waitForCount(t, children, 3, 3*time.Second)
	assert.Equal(t, 1, children.maxConcurrency())
	children.Settle(ctx, t, "a2")
	waitForCount(t, children, 4, 3*time.Second)
	children.Settle(ctx, t, "a3")
	finalRun := waitFlowTerminal(t, flowRuns, run.RunID)
	assert.Equal(t, domain.FlowStateCompleted, finalRun.State)
}

// With allow_disjoint_writers and pairwise-disjoint declared writes scopes,
// three writers run concurrently.
func TestParallelDisjointWriters(t *testing.T) {
	db, projectID, _, writerID := setupParallelFixture(t)
	ctx := context.Background()
	writesA1 := "writes: [\".docs/plan/par-disjoint-a1.md\"]\n  "
	def, err := agentflow.ParseDefinition([]byte(parallelFlowYAML("par-disjoint",
		"par-writer", "parallelism:\n  max: 4\n  allow_disjoint_writers: true\n",
		writesA1)))
	require.NoError(t, err)
	// a2/a3 need their own scopes: patch the definition directly.
	t2 := def.Tasks["a2"]
	t2.Writes = []string{".docs/plan/par-disjoint-a2.md"}
	def.Tasks["a2"] = t2
	t3 := def.Tasks["a3"]
	t3.Writes = []string{".docs/plan/par-disjoint-a3.md"}
	def.Tasks["a3"] = t3

	// Publish validation must accept the disjoint-scoped flow.
	profiles := &store.AgentFlowProfileRepo{DB: db}
	result := store.NewFlowValidator(store.FlowPublishOptions{
		DB: db, ProjectID: projectID, Skills: map[string]bool{"go-dev": true},
	}).Validate(ctx, def)
	require.True(t, result.Valid, "disjoint writers must pass publish validation: %v", result.Diagnostics)

	children := newControllableChildren(db, []string{"a1", "a2", "a3"})
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}
	run := runParallelFlow(t, db, profiles, projectID, def, children)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && children.createdCount() < 4 {
		current, _ := flowRuns.GetRun(ctx, run.RunID)
		require.False(t, current.State.Terminal(), "flow terminated before writers dispatched: %s", current.State)
		time.Sleep(3 * time.Millisecond)
	}
	require.Equal(t, 4, children.createdCount(), "a0 + all three writers must dispatch")
	assert.Equal(t, 3, children.maxConcurrency(), "disjoint writers must run concurrently")

	children.Settle(ctx, t, "a1")
	children.Settle(ctx, t, "a2")
	children.Settle(ctx, t, "a3")
	finalRun := waitFlowTerminal(t, flowRuns, run.RunID)
	assert.Equal(t, domain.FlowStateCompleted, finalRun.State)
	// Writes scopes are frozen on the nodes.
	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	for _, n := range nodes {
		if n.Handle == "a2" {
			assert.Equal(t, writerID, n.RoleVersionID)
			assert.False(t, n.ReadOnly)
			assert.Equal(t, []string{".docs/plan/par-disjoint-a2.md"}, n.Writes)
		}
	}
}

// A failing sibling cancels the rest of the parallel batch and fails the flow.
func TestParallelFailureCancelsSiblings(t *testing.T) {
	db, projectID, _, _ := setupParallelFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(parallelFlowYAML("par-fail",
		"par-reader", "parallelism:\n  max: 4\n", "")))
	require.NoError(t, err)
	children := newControllableChildren(db, []string{"a1", "a3"})
	children.fail["a2"] = true
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}
	run := runParallelFlow(t, db, &store.AgentFlowProfileRepo{DB: db}, projectID, def, children)

	finalRun := waitFlowTerminal(t, flowRuns, run.RunID)
	assert.Equal(t, domain.FlowStateFailed, finalRun.State)
	// a1/a3 were cancelled by the sibling failure.
	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, n := range nodes {
		byHandle[n.Handle] = n
	}
	assert.Equal(t, domain.FlowNodeFailed, byHandle["a2"].TerminalState)
	assert.Equal(t, domain.FlowNodeCancelled, byHandle["a1"].TerminalState)
	assert.Equal(t, domain.FlowNodeCancelled, byHandle["a3"].TerminalState)
}

// A parallel batch over the flow budget terminates with budget_exceeded and
// cancels the still-running siblings.
func TestParallelBudgetOverrunCancelsSiblings(t *testing.T) {
	db, projectID, _, _ := setupParallelFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(parallelFlowYAML("par-budget",
		"par-reader", "parallelism:\n  max: 4\n", "")))
	require.NoError(t, err)
	def.Budget.MaxTotalTokens = 2500 // a0(1000) + a1(1000) + a2(1000) exceeds it
	children := newControllableChildren(db, []string{"a2", "a3"})
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}
	run := runParallelFlow(t, db, &store.AgentFlowProfileRepo{DB: db}, projectID, def, children)

	// a1 settles (2000 total), then settle a2 -> 3000 > 2500.
	waitForCount(t, children, 4, 5*time.Second)
	t.Logf("BUDGET created=%v count=%d", children.created, children.createdCount())
	children.Settle(ctx, t, "a2")
	finalRun := waitFlowTerminal(t, flowRuns, run.RunID)
	t.Logf("BUDGET final state=%s reason=%s", finalRun.State, finalRun.TerminalReason)
	assert.Equal(t, domain.FlowStateBudgetExceeded, finalRun.State)
	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	byHandle := map[string]*domain.RunAgentFlowNode{}
	for _, n := range nodes {
		byHandle[n.Handle] = n
	}
	assert.Equal(t, domain.FlowNodeCancelled, byHandle["a3"].TerminalState)
}

// A crashed orchestrator restarts and re-dispatches every in-flight child
// (multiple running nodes reconciled at once); completed checkpoints are
// preserved and never double-folded.
func TestParallelRecoveryRedispatchesAllInFlight(t *testing.T) {
	db, projectID, _, _ := setupParallelFixture(t)
	ctx := context.Background()
	def, err := agentflow.ParseDefinition([]byte(parallelFlowYAML("par-recover",
		"par-reader", "parallelism:\n  max: 4\n", "")))
	require.NoError(t, err)
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}}

	profile, err := (&store.AgentFlowProfileRepo{DB: db}).CreateProfile(ctx, store.CreateAgentFlowProfileInput{
		Name: "par-recover", Slug: "par-recover", SourceKind: domain.FlowSourceManaged,
	})
	require.NoError(t, err)
	version, err := (&store.AgentFlowProfileRepo{DB: db}).CreateVersion(ctx, profile.ID, def)
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{
		ProjectID: projectID, Title: "recover session",
	})
	require.NoError(t, err)
	binding, err := (&store.AgentFlowBindingRepo{DB: db}).EnsureBindingExists(ctx, projectID, version.ID)
	require.NoError(t, err)
	_, err = (&store.AgentFlowBindingRepo{DB: db}).Update(ctx, binding.ID, true)
	require.NoError(t, err)
	inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "pipeline"}, nil)
	require.NoError(t, err)
	freeze, diagnostics, err := flowRuns.FreezeFlowDefinition(ctx, projectID, "", def, inputs)
	require.NoError(t, err, diagnostics)
	run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID, InputsJSON: inputs,
	}, freeze)
	require.NoError(t, err)

	// First orchestrator: dispatch a1,a2,a3 (held), then "crash" (context done).
	children1 := newControllableChildren(db, []string{"a1", "a2", "a3"})
	hub := events.NewHub()
	writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
	orch1 := &agentflow.Orchestrator{
		Store: &store.OrchestratorStore{Runs: flowRuns, Profiles: &store.AgentFlowProfileRepo{DB: db}},
		Children: children1, Events: &store.FlowEventSink{Writer: writer}, PollInterval: 3 * time.Millisecond,
	}
	crashCtx, cancel := context.WithCancel(ctx)
	orch1.Start(crashCtx, run.RunID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && children1.createdCount() < 4 {
		time.Sleep(3 * time.Millisecond)
	}
	require.Equal(t, 4, children1.createdCount(), "all three explores must be in flight before crash")
	// Wait for a0's checkpoint to fold (deterministic crash point: a0 completed,
	// a1-a3 running with in-flight children).
	for time.Now().Before(deadline) {
		nodes, _ := flowRuns.ListNodes(ctx, run.RunID)
		a0Done := false
		for _, n := range nodes {
			if n.Handle == "a0" && n.TerminalState == domain.FlowNodeCompleted {
				a0Done = true
			}
		}
		if a0Done {
			break
		}
		time.Sleep(3 * time.Millisecond)
	}
	cancel() // crash: in-flight children stay running; nodes left 'running'

	// Second orchestrator: reconcile re-dispatches every interrupted child
	// (a0 stays folded; a1-a3 re-dispatched exactly once).
	children2 := newControllableChildren(db, []string{"a1", "a2", "a3"})
	orch2 := &agentflow.Orchestrator{
		Store: &store.OrchestratorStore{Runs: flowRuns, Profiles: &store.AgentFlowProfileRepo{DB: db}},
		Children: children2, Events: &store.FlowEventSink{Writer: writer}, PollInterval: 3 * time.Millisecond,
	}
	orch2.Start(ctx, run.RunID)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && children2.createdCount() < 3 {
		current, _ := flowRuns.GetRun(ctx, run.RunID)
		if current.State.Terminal() {
			break
		}
		time.Sleep(3 * time.Millisecond)
	}
	require.Equal(t, 3, children2.createdCount(), "recovery must re-dispatch a1,a2,a3 exactly once")
	children2.Settle(ctx, t, "a1")
	children2.Settle(ctx, t, "a2")
	children2.Settle(ctx, t, "a3")
	finalRun := waitFlowTerminal(t, flowRuns, run.RunID)
	assert.Equal(t, domain.FlowStateCompleted, finalRun.State)
	nodes, err := flowRuns.ListNodes(ctx, run.RunID)
	require.NoError(t, err)
	for _, n := range nodes {
		if n.RoleVersionID != "" {
			assert.Equal(t, domain.FlowNodeCompleted, n.TerminalState, n.Handle)
			assert.NotEmpty(t, n.OutputRef, n.Handle)
		}
	}
}

func waitForCount(t *testing.T, children *controllableChildren, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && children.createdCount() < want {
		time.Sleep(3 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, children.createdCount(), want)
}
