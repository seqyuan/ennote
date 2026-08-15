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
		Items: []store.CreateDelegationItemInput{{Name: "child", RoleVersionID: "builtin-workspace-explorer-v3",
			AssignmentJSON: json.RawMessage(`{"task":"inspect"}`), OutputContract: "text-v1",
			Budget: domain.BudgetCeilingJSON{MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 20000,
				MaxOutputTokens: 4000, MaxWallTimeMS: 120000},
			RoleMeta: coordinatorRoleMetaFromDB(t, repo.DB, "builtin-workspace-explorer-v3")}},
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

// coordinatorRoleMetaFromDB freezes the builtin Workspace Explorer RoleMeta
// without consulting global role SQL (V2).
func coordinatorRoleMetaFromDB(t *testing.T, _ *sql.DB, _ string) *store.DelegationRoleMeta {
	t.Helper()
	var definition domain.RoleDefinition
	require.NoError(t, json.Unmarshal([]byte(coordinatorExplorerRoleDefinition), &definition))
	return &store.DelegationRoleMeta{
		ObjectID: "builtin-workspace-explorer", VersionID: "builtin-workspace-explorer-v3",
		Handle: "workspace-explorer", DisplayName: "Workspace Explorer",
		ConfigDigest: "sha256:c7cf36749030bd0626c24eea7ea325c2b70be64bd2f623b3c94b5fc8b81aa38b",
		Definition:   definition,
	}
}

const coordinatorExplorerRoleDefinition = `{"schemaVersion":1,"rolePrompt":"You are the Workspace Explorer. Use read, ls, grep, and find to answer questions about workspace files.","modelBinding":{"mode":"inherit","modelProfileId":"provider/model","thinkingEffort":"default","fallbackModelProfileIds":[],"overridableFields":[]},"skills":{"entries":[]},"authority":"read_only","permissionCeiling":"discuss","allowedTools":["read","ls","grep","find","git_readonly"],"contextPolicy":{"defaultMode":"task_only","allowedModes":["task_only"],"ownExecutionContinuity":"none"},"delegationPolicy":{"admission":"auto_within_budget","allowedCallerKinds":["host"],"allowedStrategies":["single","parallel"],"maxInvocationsPerParentRun":16,"maxConcurrentInstances":16,"budgetCeiling":{"maxModelCalls":6,"maxToolCalls":8,"maxTotalTokens":20000,"maxOutputTokens":4000,"maxCostUsdMicros":100000,"maxWallTimeMs":120000}},"outputContract":"text-v1","maxLoopIterations":8}`

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
