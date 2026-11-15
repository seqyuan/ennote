package store_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

// acceptanceSummaryFixture freezes one Session with three Graph Runs:
//
//	run 1: producer and reviewer both declare a clause
//	run 2: no task declares anything (the pre-feature shape)
//	run 3: producer declares a clause
//
// Every run is created through CreateFlowRun, and its anchor is terminated
// between runs because a Session allows one active top-level Run at a time.
func acceptanceSummaryFixture(t *testing.T) (*store.AgentFlowRunRepo, string, []string) {
	t.Helper()
	db, flowRuns, projectID, _, _ := setupFlowFixture(t)
	ctx := context.Background()
	session := sqlCreateSession(t, db, projectID)
	declared, err := agentflow.ParseDefinition([]byte(flowYAML("acceptance-summary", "")))
	require.NoError(t, err)
	producer := declared.Tasks["producer"]
	producer.Acceptance = []domain.FlowAcceptance{
		{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads"}},
	}
	declared.Tasks["producer"] = producer
	reviewer := declared.Tasks["reviewer"]
	reviewer.Acceptance = []domain.FlowAcceptance{{File: "outputs/review.md", Check: "non-empty"}}
	declared.Tasks["reviewer"] = reviewer
	undeclared, err := agentflow.ParseDefinition([]byte(flowYAML("acceptance-summary-plain", "")))
	require.NoError(t, err)

	create := func(def *domain.FlowDefinition) string {
		version := flowVersionFromDef(def)
		inputs, err := store.NormalizeFlowInputs(def, map[string]any{"target": "src/main.go"}, nil)
		require.NoError(t, err)
		freeze, diagnostics, err := freezeFlowForTest(t, db, flowRuns, projectID, "", def, inputs)
		require.NoError(t, err, diagnostics)
		run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
			SessionID: session.ID, ProjectID: projectID, FlowVersionID: version.ID,
			DefinitionJSON: version.DefinitionJSON, ConfigDigest: version.ConfigDigest, InputsJSON: inputs,
		}, freeze)
		require.NoError(t, err)
		require.NoError(t, flowRuns.TerminateAnchor(ctx, run.RunID, domain.RunSucceeded, "", ""))
		return run.RunID
	}
	runIDs := []string{create(declared), create(undeclared), create(declared)}
	return flowRuns, session.ID, runIDs
}

// writeVerdict records a task verdict in the same CAS the fold uses.
func writeVerdict(t *testing.T, repo *store.AgentFlowRunRepo, runID, handle string,
	result domain.AcceptanceResult) {
	t.Helper()
	node := nodeByHandle(t, repo, runID, handle)
	verdict := json.RawMessage(`{"result":"` + string(result) + `","checks":[{"check":"non-empty","verdict":"` +
		string(result) + `"}]}`)
	_, err := repo.UpdateNode(context.Background(), runID, store.NodeUpdate{
		TaskIndex: node.TaskIndex, ExpectedStates: []domain.FlowNodeState{domain.FlowNodePending},
		SetState: domain.FlowNodeCompleted, AcceptanceVerdict: &verdict,
	})
	require.NoError(t, err)
}

// The summary counts the nodes that declared a clause and nothing else: an
// undeclared node never enters `total`, and a declared node with no verdict yet
// counts in `total` alone — "declared but not evaluated" is not a pass, and it
// is not a failure either.
func TestAcceptanceSummaryCountsOnlyDeclaredNodes(t *testing.T) {
	repo, _, runIDs := acceptanceSummaryFixture(t)
	ctx := context.Background()
	declaredRun := runIDs[0]

	writeVerdict(t, repo, declaredRun, "producer", domain.AcceptanceHolds)

	summary, err := repo.AcceptanceSummary(ctx, declaredRun)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, domain.AcceptanceSummary{Total: 2, Holds: 1}, *summary,
		"the undeclared terminal task is never counted; the declared reviewer has no verdict yet")

	writeVerdict(t, repo, declaredRun, "reviewer", domain.AcceptanceUnverified)
	summary, err = repo.AcceptanceSummary(ctx, declaredRun)
	require.NoError(t, err)
	assert.Equal(t, domain.AcceptanceSummary{Total: 2, Holds: 1, Unverified: 1}, *summary)

	// A run where nothing declared acceptance has no summary at all, which is what
	// keeps it indistinguishable from a run written before the feature existed.
	summary, err = repo.AcceptanceSummary(ctx, runIDs[1])
	require.NoError(t, err)
	assert.Nil(t, summary)
}

// The Session list answers every run in one call: a run with a declaration gets
// its summary, a run without one gets no entry to attach.
func TestSessionAcceptanceSummariesCoversEveryRunInOneCall(t *testing.T) {
	repo, sessionID, runIDs := acceptanceSummaryFixture(t)
	ctx := context.Background()
	writeVerdict(t, repo, runIDs[0], "producer", domain.AcceptanceHolds)
	writeVerdict(t, repo, runIDs[2], "producer", domain.AcceptanceDoesNotHold)
	writeVerdict(t, repo, runIDs[2], "reviewer", domain.AcceptanceUnverified)

	counting, queries := openCountingDB(t, repo.DB)
	counted := &store.AgentFlowRunRepo{DB: counting}
	summaries, err := counted.SessionAcceptanceSummaries(ctx, sessionID)
	require.NoError(t, err)
	assert.Equal(t, 1, queries.count(), "the list must not query once per run")
	require.Len(t, summaries, 2)
	assert.Equal(t, domain.AcceptanceSummary{Total: 2, Holds: 1}, *summaries[runIDs[0]])
	assert.Equal(t, domain.AcceptanceSummary{Total: 2, DoesNotHold: 1, Unverified: 1}, *summaries[runIDs[2]])
	assert.NotContains(t, summaries, runIDs[1])

	// The detail entry point is one query too.
	queries.reset()
	single, err := counted.AcceptanceSummary(ctx, runIDs[0])
	require.NoError(t, err)
	assert.Equal(t, 1, queries.count())
	require.NotNil(t, single)
	assert.Equal(t, 1, single.Holds)
}

// queryCounter records the statements a wrapped connection saw.
type queryCounter struct {
	mu         sync.Mutex
	statements []string
}

func (c *queryCounter) record(query string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = append(c.statements, query)
}

func (c *queryCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.statements)
}

func (c *queryCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = nil
}

// countingDriver wraps the sqlite driver so a test can assert how many
// statements one call issues. The store has no counting seam of its own, and
// "one query, not N+1" is exactly the kind of property a comment cannot keep.
type countingDriver struct {
	inner   driver.Driver
	counter *queryCounter
}

func (d *countingDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, counter: d.counter}, nil
}

type countingConn struct {
	driver.Conn
	counter *queryCounter
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.record(query)
	queryer, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.QueryContext(ctx, query, args)
}

func (c *countingConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	c.counter.record(query)
	queryer, ok := c.Conn.(driver.Queryer)
	if !ok {
		return nil, driver.ErrSkip
	}
	return queryer.Query(query, args)
}

// openCountingDB opens a second read-only handle on the fixture's database file,
// so the statements one store call issues can be counted without changing how
// the fixture itself writes.
func openCountingDB(t *testing.T, fixture *sql.DB) (*sql.DB, *queryCounter) {
	t.Helper()
	var sequence int
	var name, file string
	require.NoError(t, fixture.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &file))
	require.Equal(t, "main", name)
	require.NotEmpty(t, file)
	driverName := fmt.Sprintf("sqlite-counting-%s", filepath.Base(file))
	counter := &queryCounter{}
	sql.Register(driverName, &countingDriver{inner: &sqlite.Driver{}, counter: counter})
	db, err := sql.Open(driverName, file+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, counter
}
