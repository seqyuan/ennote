package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkApprovalFixture(t *testing.T) (*store.CheckTaskRunner, *sql.DB, *store.AgentFlowRunRepo, string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Checks", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Checks"})
	require.NoError(t, err)
	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	runID := "flow-run-check"
	_, err = db.Exec(`INSERT INTO run_agent_flow
		(run_id,session_id,project_id,flow_version_id,manifest_digest,inputs_json,state,total_tokens_used,created_at,updated_at,definition_json,config_digest)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		runID, session.ID, project.ID, "flow@v000001", "manifest", "{}", "running", 0, now, now, "{}", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO run_agent_flow_nodes (run_id,task_index,handle,terminal_state,created_at)
		VALUES (?,0,'gate','running',?)`, runID, now)
	require.NoError(t, err)
	return &store.CheckTaskRunner{DB: db}, db, &store.AgentFlowRunRepo{DB: db}, runID
}

func TestCheckApprovalCreateAndDecideFirstWins(t *testing.T) {
	ctx := context.Background()
	runner, _, _, runID := checkApprovalFixture(t)
	require.NoError(t, runner.CreateCheckApproval(ctx, runID, 0, "go test ./..."))
	require.NoError(t, runner.CreateCheckApproval(ctx, runID, 0, "go test ./..."), "create is idempotent")

	status, err := runner.CheckApprovalStatus(ctx, runID, 0)
	require.NoError(t, err)
	assert.Equal(t, agentflow.CheckApprovalPending, status)

	decided, err := runner.DecideCheckApproval(ctx, runID, 0, true, "req-1")
	require.NoError(t, err)
	assert.Equal(t, agentflow.CheckApprovalApproved, decided)
	again, err := runner.DecideCheckApproval(ctx, runID, 0, false, "req-2")
	require.NoError(t, err)
	assert.Equal(t, agentflow.CheckApprovalApproved, again, "first decision wins")

	row, err := runner.GetCheckApproval(ctx, runID, 0)
	require.NoError(t, err)
	assert.Equal(t, agentflow.CheckApprovalApproved, row.Status)
	assert.Equal(t, "gate", row.TaskHandle)
	assert.NotNil(t, row.ResolvedAt)
	list, err := runner.ListCheckApprovals(ctx, runID)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestResumeFlowRunClearsResetCheckApprovals(t *testing.T) {
	ctx := context.Background()
	runner, db, runs, runID := checkApprovalFixture(t)
	require.NoError(t, runner.CreateCheckApproval(ctx, runID, 0, "go test ./..."))
	_, err := runner.DecideCheckApproval(ctx, runID, 0, false, "reject")
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE run_agent_flow SET state='failed' WHERE run_id=?`, runID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE run_agent_flow_nodes SET terminal_state='failed' WHERE run_id=? AND task_index=0`, runID)
	require.NoError(t, err)

	_, err = runs.ResumeFlowRun(ctx, runID)
	require.NoError(t, err)
	status, err := runner.CheckApprovalStatus(ctx, runID, 0)
	require.NoError(t, err)
	assert.Equal(t, agentflow.CheckApprovalNone, status, "resume must clear the rejected approval so the task can ask again")
}

func TestFlowCheckPolicyAndAllowlistComeFromFileStore(t *testing.T) {
	policies := &fileconfig.PolicyStore{Path: filepath.Join(t.TempDir(), "policies.json")}
	resolver := store.FlowCheckPolicyResolver(policies)
	policy, err := resolver(context.Background(), nil, "session")
	require.NoError(t, err)
	assert.NotEmpty(t, policy.Mode)

	allowlist, unrestricted, err := store.CheckAllowlistFromPolicy(policies)
	require.NoError(t, err)
	assert.True(t, unrestricted, "the default builtin policy is allow_existing_behavior")
	assert.Empty(t, allowlist)
}
