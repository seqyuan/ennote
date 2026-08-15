package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRuntimeProviderClassifiesPreflightFailures(t *testing.T) {
	executor := &agentExecutor{}
	// Missing plaintext API key -> credential unavailable.
	_, err := executor.resolveRuntimeProvider(domain.ModelRuntimeSnapshot{
		ProviderProfileID: "provider", ModelProfileID: "model", APIModel: "test-model",
		BaseURL: "https://provider.test", APIKey: "",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderCredentialUnavailable, domain.ErrorCodeOf(err))

	// Present key but invalid model configuration -> configuration invalid.
	_, err = executor.resolveRuntimeProvider(domain.ModelRuntimeSnapshot{
		ProviderProfileID: "provider", ModelProfileID: "model", APIModel: "",
		BaseURL: "https://provider.test", APIKey: "sk-provider-key",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderConfigurationInvalid, domain.ErrorCodeOf(err))
}

type allowToolPolicy struct{}

func (allowToolPolicy) BeforeToolBatch(_ context.Context, _ agent.ToolBatchContext,
	calls []domain.ToolCall) ([]agent.ToolDecision, error) {
	decisions := make([]agent.ToolDecision, len(calls))
	for index := range decisions {
		decisions[index] = agent.ToolDecision{Action: agent.ToolAllow, RiskClass: domain.RiskDelegation}
	}
	return decisions, nil
}
func (allowToolPolicy) AfterToolCall(context.Context, agent.ToolCallContext, domain.ToolCall,
	domain.ToolResult) (agent.AfterToolDecision, error) {
	return agent.AfterToolDecision{}, nil
}

func TestDelegationAdmissionPolicyPromotesRoleApprovalAndHonorsKillSwitch(t *testing.T) {
	db, _, _ := newSessionDB(t)
	ctx := context.Background()
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "admission", HostPath: t.TempDir()})
	require.NoError(t, err)
	session := sqlCreateSession(t, db, project.ID)

	// V2: the approval Role is a file revision resolved through the file-native
	// DelegationRepo; the legacy global role SQL is removed.
	sources, models, modelID, home := setupExecutorFileRoleDelegation(t)
	document := &rolesource.Document{
		SchemaVersion: 1, Handle: "approval-role", Name: "Approval",
		Description: "Requires delegation approval.", Positioning: "Independent", Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: modelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextRoom,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationApprovalRequired,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 1, MaxToolCalls: 1,
				MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxWallTimeMS: 120000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Approve the delegation.",
	}
	_, _, err = sources.CreateRole(document)
	require.NoError(t, err)
	_, err = sources.PublishRoleRevision(document.Handle)
	require.NoError(t, err)
	policy := &delegationAdmissionToolPolicy{Base: allowToolPolicy{},
		Delegations: &store.DelegationRepo{DB: db, RoleSources: sources, Models: models},
		SessionID:   session.ID, KnownSkills: map[string]bool{"review-guard": true}}
	call := domain.ToolCall{ID: "delegate", Name: "delegate_tasks", Arguments: json.RawMessage(
		`{"tasks":[{"name":"review","role":"approval-role","goal":"review","skills":["review-guard"],"budget":{"maxModelCalls":1,"maxToolCalls":1}}]}`)}
	decisions, err := policy.BeforeToolBatch(ctx, agent.ToolBatchContext{}, []domain.ToolCall{call})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, agent.ToolRequireApproval, decisions[0].Action)
	assert.Contains(t, string(decisions[0].Arguments), `"roleVersionId":"approval-role@v000001"`)
	assert.Contains(t, string(decisions[0].Arguments), `"skills":["review-guard"]`)

	missingSkillCall := call
	missingSkillCall.Arguments = json.RawMessage(
		`{"tasks":[{"name":"review","role":"approval-role","goal":"review","skills":["missing"],"budget":{"maxModelCalls":1,"maxToolCalls":1}}]}`)
	decisions, err = policy.BeforeToolBatch(ctx, agent.ToolBatchContext{}, []domain.ToolCall{missingSkillCall})
	require.NoError(t, err)
	assert.Equal(t, agent.ToolDeny, decisions[0].Action)
	assert.Equal(t, "skill_not_found", decisions[0].Code)

	invalidDagCall := call
	invalidDagCall.Arguments = json.RawMessage(
		`{"tasks":[{"name":"review","role":"approval-role","goal":"review","depends":["missing"],"budget":{"maxModelCalls":1,"maxToolCalls":1}}]}`)
	decisions, err = policy.BeforeToolBatch(ctx, agent.ToolBatchContext{}, []domain.ToolCall{invalidDagCall})
	require.NoError(t, err)
	assert.Equal(t, agent.ToolDeny, decisions[0].Action)
	assert.Equal(t, string(domain.ErrorDelegationDagInvalid), decisions[0].Code)

	// V2 kill switch: revoke the Role by removing its file revision; the
	// admission gate must deny with no child materialization path.
	require.NoError(t, os.RemoveAll(filepath.Join(sources.RolesDir(), "approval-role")))
	decisions, err = policy.BeforeToolBatch(ctx, agent.ToolBatchContext{}, []domain.ToolCall{call})
	require.NoError(t, err)
	assert.Equal(t, agent.ToolDeny, decisions[0].Action)
	assert.Equal(t, string(domain.ErrorDelegationNotAuthorized), decisions[0].Code)
	_ = home
}

// setupExecutorFileRoleDelegation wires a file-backed Role source + model
// catalog for the admission policy tests.
func setupExecutorFileRoleDelegation(t *testing.T) (*globalsource.Store, *store.ModelRepo, string, string) {
	t.Helper()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(context.Background(), fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-role-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(context.Background(), fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "role-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)
	return &globalsource.Store{HomeDir: home}, &store.ModelRepo{Files: models}, model.ID, home
}

func TestDelegationStrategyMatchesChildCount(t *testing.T) {
	assert.Equal(t, domain.DelegationStrategySingle, delegationStrategy(1))
	assert.Equal(t, domain.DelegationStrategyParallel, delegationStrategy(2))
}

func TestEnqueueQueuedChildrenClosesSQLiteRowsBeforeCallback(t *testing.T) {
	db, _, _ := newSessionDB(t)
	ctx := context.Background()
	project, _, err := newFileProjects(t).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "enqueue children", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session := sqlCreateSession(t, db, project.ID)
	runs := &store.RunRepo{DB: db}
	submission, err := runs.SubmitTurn(ctx, domain.SubmitTurnInput{
		SessionID: session.ID, ClientRequestID: "parent", Text: "delegate",
	})
	require.NoError(t, err)
	_, err = runs.Claim(ctx, submission.Run.ID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_runs
		(id,session_id,run_kind,status,parent_run_id,root_run_id,execution_depth,publish_mode,commit_format_version,created_at)
		VALUES('queued-child',?,'delegated_agent','queued',?,?,1,'private_to_parent',2,?)`,
		session.ID, submission.Run.ID, submission.Run.ID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	callbackErr := make(chan error, 1)
	executor := &agentExecutor{db: db, OnChildRunsCreated: func(callbackCtx context.Context, ids []string) {
		if !assert.Equal(t, []string{"queued-child"}, ids) {
			callbackErr <- assert.AnError
			return
		}
		var status string
		callbackErr <- db.QueryRowContext(callbackCtx, `SELECT status FROM agent_runs WHERE id=?`, ids[0]).Scan(&status)
	}}
	done := make(chan struct{})
	go func() {
		executor.enqueueQueuedChildren(ctx, submission.Run.ID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue callback blocked behind open SQLite rows")
	}
	require.NoError(t, <-callbackErr)
}

func TestWorkspaceCanonicalizationGuard(t *testing.T) {
	// Regression guard: CheckPrompt (line ~588) and queueRunEndObserver
	// (line ~553) must use workspace.CanonicalWorkspaceRoot, not
	// filepath.Abs, so symlink workspace paths resolve correctly for both
	// prompt hooks and run-end outbox delivery.
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)
	text := string(src)

	// Must not contain the old pattern.
	assert.NotContainsf(t, text, "filepath.Abs(wSpace.HostPath)",
		"regression: found filepath.Abs — must use workspace.CanonicalWorkspaceRoot")

	// Must contain at least 2 usages (CheckPrompt + queueRunEndObserver).
	count := strings.Count(text, "workspace.CanonicalWorkspaceRoot(wSpace.HostPath)")
	assert.GreaterOrEqual(t, count, 2,
		"expected >=2 CanonicalWorkspaceRoot(wSpace.HostPath) calls, got %d", count)
}

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

// sqlCreateSessionWithModel is sqlCreateSession with an optional default model.
func sqlCreateSessionWithModel(t *testing.T, db *sql.DB, projectID string, defaultModel *string) domain.Session {
	t.Helper()
	now := time.Now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	id, branchID := uuid.NewString(), uuid.NewString()
	_, err := db.Exec(`INSERT INTO sessions (id, project_id, title, status, mode, default_model_profile_id, active_branch_id, created_at, updated_at)
		VALUES (?,?,?, 'active','hosted',?,NULL,?,?)`, id, projectID, "session", defaultModel, timestamp, timestamp)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO session_branches (id,session_id,label,created_at,updated_at) VALUES(?,?,'Main',?,?)`,
		branchID, id, timestamp, timestamp)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sessions SET active_branch_id=? WHERE id=?`, branchID, id)
	require.NoError(t, err)
	return domain.Session{ID: id, ProjectID: projectID, Title: "session", Status: "active",
		Mode: domain.SessionModeHosted, DefaultModelProfileID: defaultModel,
		ActiveBranchID: &branchID, CreatedAt: now, UpdatedAt: now}
}
