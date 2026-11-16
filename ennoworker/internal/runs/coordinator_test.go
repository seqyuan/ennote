package runs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingExecutor is the executor shape the scheduler tests need: a started Run
// announces itself and then blocks until the test opens the gate, so "which Runs
// are executing" is observable without a sleep-until-hopefully.
func blockingExecutor(started chan<- string, release <-chan struct{}) Executor {
	return ExecutorFunc(func(ctx context.Context, run *domain.AgentRun) error {
		started <- run.ID
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	})
}

func waitForAll(t *testing.T, coordinator *Coordinator, runIDs ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, runID := range runIDs {
		require.NoError(t, coordinator.Wait(ctx, runID))
	}
}

// MaxConcurrentRuns=2: the batch reservation is one slot, so a second batch Run
// waits and a person's Run goes straight through to the slot it leaves free.
func TestCoordinatorReservesSlotForInteractiveRuns(t *testing.T) {
	repo := setupRunDB(t)
	firstBatch := setupRun(t, repo, "batch-first")
	secondBatch := setupRun(t, repo, "batch-second")
	interactive := setupRun(t, repo, "interactive")

	release := make(chan struct{})
	started := make(chan string, 4)
	coordinator := NewCoordinator(repo, blockingExecutor(started, release), 2)
	require.NoError(t, coordinator.EnqueueWithClass(context.Background(), firstBatch.Run.ID, ClassBatch))
	require.NoError(t, coordinator.EnqueueWithClass(context.Background(), secondBatch.Run.ID, ClassBatch))

	// Exactly one of the two batch Runs executes: batch may hold 2-1 slots.
	running := <-started
	var waiting *domain.TurnSubmission
	switch running {
	case firstBatch.Run.ID:
		waiting = secondBatch
	case secondBatch.Run.ID:
		waiting = firstBatch
	default:
		t.Fatalf("unexpected Run started: %s", running)
	}
	waitingRun, err := repo.Get(context.Background(), waiting.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunQueued, waitingRun.Status, "the second batch Run must still be queued")
	select {
	case second := <-started:
		t.Fatalf("a second batch Run took a slot while the reservation was in force: %s", second)
	case <-time.After(200 * time.Millisecond):
	}

	// The person's Run must not queue behind the waiting batch Run.
	require.NoError(t, coordinator.Enqueue(context.Background(), interactive.Run.ID))
	select {
	case id := <-started:
		require.Equal(t, interactive.Run.ID, id, "the interactive Run must run before the queued batch Run")
	case <-time.After(2 * time.Second):
		t.Fatal("interactive Run queued behind batch work instead of taking the reserved slot")
	}
	close(release)

	waitForAll(t, coordinator, firstBatch.Run.ID, secondBatch.Run.ID, interactive.Run.ID)
	assert.Equal(t, 0, coordinator.ActiveCount())
	for _, id := range []string{firstBatch.Run.ID, secondBatch.Run.ID, interactive.Run.ID} {
		run, err := repo.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, domain.RunSucceeded, run.Status)
	}
}

// MaxConcurrentRuns=1: there is no slot to reserve, so batch keeps the only one
// and the two classes are serialised. A zero-size batch semaphore would wedge
// batch here forever.
func TestCoordinatorSingleSlotHasNoReservation(t *testing.T) {
	repo := setupRunDB(t)
	batch := setupRun(t, repo, "single-batch")
	interactive := setupRun(t, repo, "single-interactive")

	release := make(chan struct{})
	started := make(chan string, 2)
	var current, maximum atomic.Int32
	executor := ExecutorFunc(func(ctx context.Context, run *domain.AgentRun) error {
		value := current.Add(1)
		for {
			old := maximum.Load()
			if value <= old || maximum.CompareAndSwap(old, value) {
				break
			}
		}
		started <- run.ID
		select {
		case <-release:
		case <-ctx.Done():
		}
		current.Add(-1)
		return ctx.Err()
	})
	coordinator := NewCoordinator(repo, executor, 1)

	require.NoError(t, coordinator.EnqueueWithClass(context.Background(), batch.Run.ID, ClassBatch))
	require.Equal(t, batch.Run.ID, <-started, "batch keeps the only slot: with one permit there is nothing to reserve")
	require.NoError(t, coordinator.Enqueue(context.Background(), interactive.Run.ID))
	select {
	case id := <-started:
		t.Fatalf("two Runs overlapped on a single slot: %s", id)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)

	waitForAll(t, coordinator, batch.Run.ID, interactive.Run.ID)
	assert.Equal(t, int32(1), maximum.Load(), "the two classes are serialised on one permit")
	for _, id := range []string{batch.Run.ID, interactive.Run.ID} {
		run, err := repo.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, domain.RunSucceeded, run.Status)
	}
}

// The per-run-id dedupe predates RunClass and must survive it: a second enqueue
// of a scheduled run id starts nothing, whatever class it names.
func TestCoordinatorEnqueueWithClassDeduplicatesPerRunID(t *testing.T) {
	repo := setupRunDB(t)
	submission := setupRun(t, repo, "class-dedupe")
	release := make(chan struct{})
	started := make(chan string, 4)
	var executions atomic.Int32
	executor := ExecutorFunc(func(ctx context.Context, run *domain.AgentRun) error {
		executions.Add(1)
		started <- run.ID
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	})
	coordinator := NewCoordinator(repo, executor, 2)

	require.NoError(t, coordinator.EnqueueWithClass(context.Background(), submission.Run.ID, ClassBatch))
	require.Equal(t, submission.Run.ID, <-started)
	// A Run already scheduled keeps its class: re-enqueueing cannot reclassify it.
	require.NoError(t, coordinator.EnqueueWithClass(context.Background(), submission.Run.ID, ClassInteractive))
	require.NoError(t, coordinator.EnqueueWithClass(context.Background(), submission.Run.ID, ClassBatch))
	require.NoError(t, coordinator.Enqueue(context.Background(), submission.Run.ID))
	assert.Equal(t, 1, coordinator.ActiveCount())
	close(release)

	waitForAll(t, coordinator, submission.Run.ID)
	assert.Equal(t, int32(1), executions.Load(), "the run executed exactly once")
	select {
	case id := <-started:
		t.Fatalf("the run id was scheduled twice: %s", id)
	case <-time.After(100 * time.Millisecond):
	}
}

// A Run cancelled while it is queued for a permit never starts: the cancel takes
// the same path it took when one semaphore gated both classes.
func TestCoordinatorCancelsQueuedInteractiveRunBeforeItStarts(t *testing.T) {
	repo := setupRunDB(t)
	holder := setupRun(t, repo, "slot-holder")
	queued := setupRun(t, repo, "queued-interactive")

	release := make(chan struct{})
	started := make(chan string, 4)
	coordinator := NewCoordinator(repo, blockingExecutor(started, release), 1)

	// A batch Run takes the only permit, so the interactive Run is genuinely
	// queued rather than racing for a free slot.
	require.NoError(t, coordinator.EnqueueWithClass(context.Background(), holder.Run.ID, ClassBatch))
	require.Equal(t, holder.Run.ID, <-started)
	require.NoError(t, coordinator.Enqueue(context.Background(), queued.Run.ID))
	require.NoError(t, coordinator.Cancel(context.Background(), queued.Run.ID))

	waitForAll(t, coordinator, queued.Run.ID)
	stored, err := repo.Get(context.Background(), queued.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, stored.Status)
	select {
	case id := <-started:
		t.Fatalf("a cancelled queued Run executed: %s", id)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	waitForAll(t, coordinator, holder.Run.ID)
}

func TestCoordinatorRunClassValuesAreStable(t *testing.T) {
	// cmd/ennoworker converts agentflow.RunClass into this type, so the values are
	// a cross-package contract, not an implementation detail.
	assert.Equal(t, "interactive", string(ClassInteractive))
	assert.Equal(t, "batch", string(ClassBatch))
}

func setupRunDB(t *testing.T) *store.RunRepo {
	t.Helper()
	db, _, _ := newSessionDB(t)
	return &store.RunRepo{DB: db}
}

func setupRun(t *testing.T, repo *store.RunRepo, requestID string) *domain.TurnSubmission {
	t.Helper()
	session := sqlCreateSession(t, repo.DB, "00000000-0000-4000-8000-00000000000f")
	submission, err := repo.SubmitTurn(context.Background(), domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: requestID, Text: "work",
	})
	require.NoError(t, err)
	return submission
}

func TestCoordinatorRunsDifferentSessionsInParallel(t *testing.T) {
	repo := setupRunDB(t)
	first := setupRun(t, repo, "first")
	second := setupRun(t, repo, "second")

	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var current, maximum atomic.Int32
	executor := ExecutorFunc(func(ctx context.Context, _ *domain.AgentRun) error {
		value := current.Add(1)
		for {
			old := maximum.Load()
			if value <= old || maximum.CompareAndSwap(old, value) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		current.Add(-1)
		return ctx.Err()
	})
	coordinator := NewCoordinator(repo, executor, 2)
	require.NoError(t, coordinator.Enqueue(context.Background(), first.Run.ID))
	require.NoError(t, coordinator.Enqueue(context.Background(), second.Run.ID))
	<-started
	<-started
	assert.Equal(t, int32(2), maximum.Load())
	close(release)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, first.Run.ID))
	require.NoError(t, coordinator.Wait(ctx, second.Run.ID))
	assert.Equal(t, 0, coordinator.ActiveCount())
}

type resultExecutorFunc func(context.Context, *domain.AgentRun) (domain.RunOutput, error)

func (f resultExecutorFunc) Execute(ctx context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
	return f(ctx, run)
}

func TestCoordinatorLeavesSuspendedRunWaitingAndReleasesCapacity(t *testing.T) {
	repo := setupRunDB(t)
	submission := setupRun(t, repo, "suspended")
	approvals := &store.ApprovalRepo{DB: repo.DB}
	executor := resultExecutorFunc(func(ctx context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
		_, err := approvals.Suspend(ctx, run.ID, 1, 1, "digest", []byte(`{"version":1}`),
			[]domain.ApprovalItem{{ToolCallID: "call", ToolName: "write", RiskClass: domain.RiskLocalWrite}}, nil)
		return domain.RunOutput{Suspended: true}, err
	})
	coordinator := NewCoordinator(repo, executor, 1)
	require.NoError(t, coordinator.Enqueue(context.Background(), submission.Run.ID))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, submission.Run.ID))
	run, err := repo.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunWaitingForApproval, run.Status)
	assert.Zero(t, coordinator.ActiveCount())
}

func TestCoordinatorPersistsStableFailureCode(t *testing.T) {
	repo := setupRunDB(t)
	submission := setupRun(t, repo, "typed-failure")
	executor := resultExecutorFunc(func(context.Context, *domain.AgentRun) (domain.RunOutput, error) {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorModelProtocol, assert.AnError)
	})
	coordinator := NewCoordinator(repo, executor, 1)
	require.NoError(t, coordinator.Enqueue(context.Background(), submission.Run.ID))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, submission.Run.ID))
	run, err := repo.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunFailed, run.Status)
	require.NotNil(t, run.ErrorCode)
	assert.Equal(t, string(domain.ErrorModelProtocol), *run.ErrorCode)
}

func TestCoordinatorFinalizesProjectedMessages(t *testing.T) {
	repo := setupRunDB(t)
	submission := setupRun(t, repo, "projected-success")
	executor := resultExecutorFunc(func(context.Context, *domain.AgentRun) (domain.RunOutput, error) {
		return domain.RunOutput{Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}}}, nil
	})
	coordinator := NewCoordinator(repo, executor, 1)
	require.NoError(t, coordinator.Enqueue(context.Background(), submission.Run.ID))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, submission.Run.ID))
	run, err := repo.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, run.Status)
	require.NotNil(t, run.AssistantMessageID)
}

func TestCoordinatorInvokesRunSettledHookAfterTerminalCommit(t *testing.T) {
	repo := setupRunDB(t)
	submission := setupRun(t, repo, "settled-hook")
	executor := resultExecutorFunc(func(context.Context, *domain.AgentRun) (domain.RunOutput, error) {
		return domain.RunOutput{Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}}}, nil
	})
	coordinator := NewCoordinator(repo, executor, 1)
	hooked := make(chan *domain.AgentRun, 1)
	coordinator.SetRunSettledHook(func(_ context.Context, run *domain.AgentRun) error {
		hooked <- run
		return nil
	})
	require.NoError(t, coordinator.Enqueue(context.Background(), submission.Run.ID))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, submission.Run.ID))
	select {
	case run := <-hooked:
		assert.Equal(t, domain.RunSucceeded, run.Status)
		assert.Equal(t, submission.Run.SessionID, run.SessionID)
	case <-ctx.Done():
		t.Fatal("run-settled hook was not called")
	}
}

func TestCoordinatorMarksRunFailedWhenSuccessProjectionCannotCommit(t *testing.T) {
	repo := setupRunDB(t)
	submission := setupRun(t, repo, "projection-failure")
	_, err := repo.DB.Exec(`CREATE TRIGGER fail_message_commit BEFORE INSERT ON run_events
		WHEN NEW.event_type = 'message_committed' BEGIN SELECT RAISE(ABORT, 'injected projection failure'); END`)
	require.NoError(t, err)
	executor := resultExecutorFunc(func(context.Context, *domain.AgentRun) (domain.RunOutput, error) {
		return domain.RunOutput{Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "done"}}}}}, nil
	})
	coordinator := NewCoordinator(repo, executor, 1)
	require.NoError(t, coordinator.Enqueue(context.Background(), submission.Run.ID))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, submission.Run.ID))
	run, err := repo.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunFailed, run.Status)
	require.NotNil(t, run.ErrorCode)
	assert.Equal(t, string(domain.ErrorEventPersistence), *run.ErrorCode)
}

func setupDelegatedCoordinatorTree(t *testing.T, repo *store.RunRepo, requestID string) (*domain.TurnSubmission, *domain.AgentRun) {
	t.Helper()
	submission := setupRun(t, repo, requestID)
	_, err := repo.Claim(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	delegations := &store.DelegationRepo{DB: repo.DB, Policies: &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "config", "policies.json")}}
	_, _, children, err := delegations.CreateGroupWithChildren(context.Background(), store.CreateDelegationGroupInput{
		ParentRunID: submission.Run.ID, ParentToolCallID: "delegate-" + requestID,
		Strategy: domain.DelegationStrategySingle,
		Items: []store.CreateDelegationItemInput{{Name: "child", HeroVersionID: "builtin-workspace-explorer-v3",
			AssignmentJSON: json.RawMessage(`{"task":"inspect"}`), OutputContract: "text-v1",
			Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxWallTimeMS: 120000},
			HeroMeta: coordinatorHeroMetaFromDB(t, repo.DB, "builtin-workspace-explorer-v3")}},
	}, submission.Run.SessionID)
	require.NoError(t, err)
	require.Len(t, children, 1)
	return submission, children[0]
}

func TestCoordinatorFoldsChildFailureAndResumesParent(t *testing.T) {
	repo := setupRunDB(t)
	parent, child := setupDelegatedCoordinatorTree(t, repo, "child-failure")
	executor := resultExecutorFunc(func(_ context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
		if run.RunKind == domain.RunKindDelegatedAgent {
			return domain.RunOutput{}, errors.New("provider unavailable")
		}
		return domain.RunOutput{Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "handled child failure"}}}}}, nil
	})
	coordinator := NewCoordinator(repo, executor, 2)
	require.NoError(t, coordinator.Enqueue(context.Background(), child.ID))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, child.ID))
	require.NoError(t, coordinator.Wait(ctx, parent.Run.ID))

	storedChild, err := repo.Get(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunFailed, storedChild.Status)
	storedParent, err := repo.Get(ctx, parent.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, storedParent.Status)
	var itemStatus, groupStatus string
	require.NoError(t, repo.DB.QueryRow(`SELECT status FROM delegation_items WHERE child_run_id=?`, child.ID).Scan(&itemStatus))
	require.NoError(t, repo.DB.QueryRow(`SELECT status FROM delegation_groups WHERE parent_run_id=?`, parent.Run.ID).Scan(&groupStatus))
	assert.Equal(t, "failed", itemStatus)
	assert.Equal(t, "settled", groupStatus)
}

func TestCoordinatorFoldsChildSuccessFinalizerFailure(t *testing.T) {
	repo := setupRunDB(t)
	parent, child := setupDelegatedCoordinatorTree(t, repo, "child-finalizer-failure")
	executor := resultExecutorFunc(func(_ context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
		if run.RunKind == domain.RunKindDelegatedAgent {
			return domain.RunOutput{Terminal: &domain.SubmitResult{Status: domain.SubmitCompleted, Summary: "bad artifact",
				ArtifactRefs: []domain.ArtifactReference{{ArtifactID: "missing", Name: "x", Kind: domain.ArtifactKindFile,
					MIMEType: "text/plain", SHA256: "missing"}}}}, nil
		}
		return domain.RunOutput{Messages: []domain.ChatMessage{{Role: domain.RoleAssistant,
			Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: "handled finalizer failure"}}}}}, nil
	})
	coordinator := NewCoordinator(repo, executor, 2)
	require.NoError(t, coordinator.Enqueue(context.Background(), child.ID))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, child.ID))
	require.NoError(t, coordinator.Wait(ctx, parent.Run.ID))
	storedChild, err := repo.Get(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunFailed, storedChild.Status)
	storedParent, err := repo.Get(ctx, parent.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunSucceeded, storedParent.Status)
}

func TestCoordinatorParentCancelStopsActiveChildContext(t *testing.T) {
	repo := setupRunDB(t)
	parent, child := setupDelegatedCoordinatorTree(t, repo, "tree-cancel")
	started := make(chan struct{})
	stopped := make(chan struct{})
	executor := resultExecutorFunc(func(ctx context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
		if run.ID != child.ID {
			return domain.RunOutput{}, nil
		}
		close(started)
		<-ctx.Done()
		close(stopped)
		return domain.RunOutput{}, ctx.Err()
	})
	coordinator := NewCoordinator(repo, executor, 2)
	require.NoError(t, coordinator.Enqueue(context.Background(), child.ID))
	<-started
	require.NoError(t, coordinator.Cancel(context.Background(), parent.Run.ID))
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("child runtime context was not cancelled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, child.ID))
	storedChild, err := repo.Get(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, storedChild.Status)
}

func TestCoordinatorCancellationIsIdempotent(t *testing.T) {
	repo := setupRunDB(t)
	submission := setupRun(t, repo, "cancel")
	started := make(chan struct{})
	executor := ExecutorFunc(func(ctx context.Context, _ *domain.AgentRun) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	coordinator := NewCoordinator(repo, executor, 1)
	require.NoError(t, coordinator.Enqueue(context.Background(), submission.Run.ID))
	<-started
	require.NoError(t, coordinator.Cancel(context.Background(), submission.Run.ID))
	require.NoError(t, coordinator.Cancel(context.Background(), submission.Run.ID))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, coordinator.Wait(ctx, submission.Run.ID))
	run, err := repo.Get(context.Background(), submission.Run.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RunCancelled, run.Status)
}

// coordinatorHeroMetaFromDB freezes the builtin Workspace Explorer HeroMeta
// without consulting global hero SQL (V2).
func coordinatorHeroMetaFromDB(t *testing.T, _ *sql.DB, _ string) *store.DelegationHeroMeta {
	t.Helper()
	var definition domain.HeroDefinition
	require.NoError(t, json.Unmarshal([]byte(coordinatorExplorerHeroDefinition), &definition))
	return &store.DelegationHeroMeta{
		ObjectID: "builtin-workspace-explorer", VersionID: "builtin-workspace-explorer-v3",
		Handle: "workspace-explorer", DisplayName: "Workspace Explorer",
		ConfigDigest: "sha256:c7cf36749030bd0626c24eea7ea325c2b70be64bd2f623b3c94b5fc8b81aa38b",
		Definition:   definition,
	}
}

const coordinatorExplorerHeroDefinition = `{"schemaVersion":1,"heroPrompt":"You are the Workspace Explorer. Use read, ls, grep, and find to answer questions about workspace files.","modelBinding":{"mode":"inherit","modelProfileId":"provider/model","thinkingEffort":"default","fallbackModelProfileIds":[],"overridableFields":[]},"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss","allowedTools":["read","ls","grep","find","git_readonly"],"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},"delegationPolicy":{"admission":"auto_within_budget","allowedCallerKinds":["host"],"allowedStrategies":["single","parallel"],"maxInvocationsPerParentRun":16,"maxConcurrentInstances":16,"budgetCeiling":{"maxModelCalls":6,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}},"outputContract":"text-v1","maxLoopIterations":8}`

// newFileProjects returns a file-native ProjectRepo (V2).
func newFileProjects(t *testing.T) *store.ProjectRepo {
	t.Helper()
	return &store.ProjectRepo{Files: &projectstore.Store{Root: t.TempDir()}}
}

// sqlCreateSession inserts a Session row + Main branch directly on the caller's
// per-Session database (V2).
func sqlCreateSession(t *testing.T, db *sql.DB, projectID string) domain.Session {
	t.Helper()
	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	id, branchID := uuid.NewString(), uuid.NewString()
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, title, status, mode, active_branch_id, created_at, updated_at)
		VALUES (?,?,?, 'active','hosted',NULL,?,?)`, id, projectID, "session", timestamp, timestamp)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO session_branches (id,session_id,label,created_at,updated_at) VALUES(?,?,'Main',?,?)`,
		branchID, id, timestamp, timestamp)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sessions SET active_branch_id=? WHERE id=?`, branchID, id)
	require.NoError(t, err)
	return domain.Session{ID: id, ProjectID: projectID, Title: "session", Status: "active",
		Mode: domain.SessionModeHosted, ActiveBranchID: &branchID, CreatedAt: now, UpdatedAt: now}
}

// newSessionDB creates a project + Session in the file-native layout and opens
// the per-Session database.
func newSessionDB(t *testing.T) (*sql.DB, *sessionstore.Manager, domain.Session) {
	t.Helper()
	ctx := context.Background()
	projects := &projectstore.Store{Root: t.TempDir()}
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "session", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "session"})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	return db, sessions, *session
}
