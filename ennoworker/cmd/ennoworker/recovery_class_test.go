package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two RunClass spellings are a hand-maintained mirror across the
// agentflow -> runs boundary. This package is the only one that imports both, so
// this is the only place a drift can be caught.
func TestAgentflowRunClassMirrorMatchesRunsRunClass(t *testing.T) {
	assert.Equal(t, runs.ClassBatch, runs.RunClass(agentflow.RunClassBatch))
	assert.Equal(t, runs.ClassInteractive, runs.RunClass(agentflow.RunClassInteractive))
}

// recoveryExplorerDefinition is the builtin Workspace Explorer's frozen
// definition, which is all the delegation fixture needs to materialize a child.
const recoveryExplorerDefinition = `{"schemaVersion":1,"heroPrompt":"You are the Workspace Explorer.","modelBinding":{"mode":"inherit","modelProfileId":"provider/model","thinkingEffort":"default","fallbackModelProfileIds":[],"overridableFields":[]},"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss","allowedTools":["read","ls","grep","find","git_readonly"],"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},"delegationPolicy":{"admission":"auto_within_budget","allowedCallerKinds":["host"],"allowedStrategies":["single","parallel"],"maxInvocationsPerParentRun":16,"maxConcurrentInstances":16,"budgetCeiling":{"maxModelCalls":6,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}},"outputContract":"text-v1","maxLoopIterations":8}`

// recoverV2Sessions used to enqueue every recovered queued Run as batch. A Run
// stays `queued` while its goroutine is parked on the semaphore, so that
// re-labelled a person's own queued turn as batch work after a restart. The class
// must come from each Run's own persisted facts: the delegated children are
// batch (they park behind each other on the batch permit) and the person's turn
// is interactive (it takes the global slot the batch reservation leaves free).
func TestRecoveredQueuedRunsKeepTheirOwnClass(t *testing.T) {
	ctx := context.Background()
	projects := &projectstore.Store{Root: t.TempDir()}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "recovery", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })

	// Session A: a delegated group with two entry tasks, both left `queued` by the
	// restart. The group is background-mode because that is the shape whose
	// children survive a restart: a blocking group's parent is interrupted with
	// the Worker and ReapOrphans reaps its children, so only background children
	// reach the recovery enqueue this test pins.
	delegationSession, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "graph"})
	require.NoError(t, err)
	delegationDB, err := sessions.OpenSession(ctx, delegationSession.ID)
	require.NoError(t, err)
	delegationRuns := &store.RunRepo{DB: delegationDB}
	parent, err := delegationRuns.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: delegationSession.ID, ClientRequestID: "recovery-parent", Text: "delegate",
	})
	require.NoError(t, err)
	_, err = delegationRuns.Claim(ctx, parent.Run.ID)
	require.NoError(t, err)
	delegations := &store.DelegationRepo{DB: delegationDB, HeroSources: &globalsource.Store{HomeDir: t.TempDir()},
		Policies: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}
	meta, err := store.NewDelegationHeroMeta("builtin-workspace-explorer-v3", []byte(recoveryExplorerDefinition))
	require.NoError(t, err)
	item := func(name string) store.CreateDelegationItemInput {
		return store.CreateDelegationItemInput{
			Name: name, HeroVersionID: "builtin-workspace-explorer-v3",
			AssignmentJSON: json.RawMessage(`{"task":"inspect"}`), OutputContract: "text-v1",
			Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxWallTimeMS: 120000},
			HeroMeta: meta,
		}
	}
	_, _, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: parent.Run.ID, ParentToolCallID: "recovery-delegate",
		Strategy: domain.DelegationStrategyParallel, ExecutionMode: domain.DelegationExecutionBackground,
		Items: []store.CreateDelegationItemInput{item("first"), item("second")},
	}, delegationSession.ID)
	require.NoError(t, err)
	require.Len(t, children, 2)

	// Session B: the person's own next turn, queued at the moment of the restart.
	// It is a separate Session because a Session admits one active Run at a time.
	personSession, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "person"})
	require.NoError(t, err)
	personDB, err := sessions.OpenSession(ctx, personSession.ID)
	require.NoError(t, err)
	person, err := (&store.RunRepo{DB: personDB}).SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: personSession.ID, ClientRequestID: "recovery-person", Text: "and now this",
	})
	require.NoError(t, err)

	// The coordinator blocks every started Run so "which class got a permit" is
	// observable without a sleep-until-hopefully.
	started := make(chan string, 8)
	release := make(chan struct{})
	coordinator := runs.NewCoordinator(&store.RoutedRunRepo{Sessions: sessions, Template: &store.RunRepo{}},
		runs.ExecutorFunc(func(ctx context.Context, run *domain.AgentRun) error {
			started <- run.ID
			select {
			case <-release:
			case <-ctx.Done():
			}
			return ctx.Err()
		}), 2)

	require.NoError(t, recoverV2Sessions(ctx, sessions, &store.RunRepo{}, coordinator, nil,
		&globalsource.Store{HomeDir: t.TempDir()}, &store.ModelRepo{}, &fileconfig.PolicyStore{
			Path: filepath.Join(t.TempDir(), "config", "policies.json")}))

	// The person's own turn must take the reserved slot instead of parking behind
	// the recovered batch work, whatever order the Sessions were recovered in.
	running := map[string]bool{}
	childrenRunning := func() int {
		count := 0
		for _, child := range children {
			if running[child.ID] {
				count++
			}
		}
		return count
	}
	deadline := time.After(2 * time.Second)
	for !running[person.Run.ID] || childrenRunning() != 1 {
		select {
		case id := <-started:
			running[id] = true
		case <-deadline:
			t.Fatalf("recovery did not start the person turn and one child (running: %v): the person turn is batch",
				running)
		}
	}

	// Only one child may hold the batch permit, so the second one stays parked.
	select {
	case id := <-started:
		t.Fatalf("a third Run started while batch held its only permit: %s (running %v)", id, running)
	case <-time.After(300 * time.Millisecond):
	}

	// Let every parked Run finish before the per-Session databases are closed by
	// the fixture cleanup; their settlement is not what this test pins.
	close(release)
	require.Eventually(t, func() bool { return coordinator.ActiveCount() == 0 }, 10*time.Second, 20*time.Millisecond)
}
