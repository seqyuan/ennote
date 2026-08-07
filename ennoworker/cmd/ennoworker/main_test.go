package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRuntimeProviderClassifiesPreflightFailures(t *testing.T) {
	executor := &agentExecutor{}
	_, err := executor.resolveRuntimeProvider(domain.ModelRuntimeSnapshot{
		ProviderProfileID: "provider", ModelProfileID: "model", APIModel: "test-model",
		BaseURL: "https://provider.test", CredentialRef: "env:MISSING_PROVIDER_KEY",
	})
	require.Error(t, err)
	assert.Equal(t, domain.ErrorProviderCredentialUnavailable, domain.ErrorCodeOf(err))

	t.Setenv("PROVIDER_KEY", "secret")
	_, err = executor.resolveRuntimeProvider(domain.ModelRuntimeSnapshot{
		ProviderProfileID: "provider", ModelProfileID: "model", APIModel: "",
		BaseURL: "https://provider.test", CredentialRef: "env:PROVIDER_KEY",
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
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	ctx := context.Background()
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "admission", HostPath: t.TempDir()})
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
	definition, err := json.Marshal(domain.RoleDefinition{DelegationPolicy: domain.RoleDelegationPolicy{
		Admission: domain.DelegationApprovalRequired, AllowedCallerKinds: []string{"host"},
		AllowedStrategies: []string{"single"}, MaxInvocationsPerParentRun: 1, MaxConcurrentInstances: 1,
	}})
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_profiles(id,name,object_kind,handle,scope,project_id,draft_json,created_at,updated_at)
		VALUES('approval-role','Approval','role','approval-role','project',?,'{}','2026-08-04T00:00:00Z','2026-08-04T00:00:00Z')`, project.ID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_profile_versions(id,agent_profile_id,version,definition_json,config_digest,status,created_at)
		VALUES('approval-role-v1','approval-role',1,?,'sha256:0000000000000000000000000000000000000000000000000000000000000000','published','2026-08-04T00:00:00Z')`, string(definition))
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE agent_profiles SET current_version_id='approval-role-v1' WHERE id='approval-role'`)
	require.NoError(t, err)
	policy := &delegationAdmissionToolPolicy{Base: allowToolPolicy{},
		Delegations: &store.DelegationRepo{DB: db}, SessionID: session.ID,
		KnownSkills: map[string]bool{"review-guard": true}}
	call := domain.ToolCall{ID: "delegate", Name: "delegate_tasks", Arguments: json.RawMessage(
		`{"tasks":[{"name":"review","role":"approval-role","goal":"review","skills":["review-guard"],"budget":{"maxModelCalls":1,"maxToolCalls":1}}]}`)}
	decisions, err := policy.BeforeToolBatch(ctx, agent.ToolBatchContext{}, []domain.ToolCall{call})
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, agent.ToolRequireApproval, decisions[0].Action)
	assert.Contains(t, string(decisions[0].Arguments), `"roleVersionId":"approval-role-v1"`)
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

	_, err = db.Exec(`UPDATE agent_profiles SET delegation_enabled=0 WHERE id='approval-role'`)
	require.NoError(t, err)
	decisions, err = policy.BeforeToolBatch(ctx, agent.ToolBatchContext{}, []domain.ToolCall{call})
	require.NoError(t, err)
	assert.Equal(t, agent.ToolDeny, decisions[0].Action)
	assert.Equal(t, string(domain.ErrorDelegationNotAuthorized), decisions[0].Code)
}

func TestDelegationStrategyMatchesChildCount(t *testing.T) {
	assert.Equal(t, domain.DelegationStrategySingle, delegationStrategy(1))
	assert.Equal(t, domain.DelegationStrategyParallel, delegationStrategy(2))
}

func TestEnqueueQueuedChildrenClosesSQLiteRowsBeforeCallback(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	ctx := context.Background()
	project, _, err := (&store.ProjectRepo{DB: db}).CreateWithWorkspace(ctx, domain.CreateProjectInput{
		Name: "enqueue children", HostPath: t.TempDir(),
	})
	require.NoError(t, err)
	session, err := (&store.SessionRepo{DB: db}).Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
	require.NoError(t, err)
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
