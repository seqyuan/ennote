package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

// CheckTaskRunner executes flow check tasks through the ToolPolicy gate,
// durable Ask-mode approvals, the workspace sandbox, and bounded output
// capture. It is the CheckRunner implementation wired by the Worker.
type CheckTaskRunner struct {
	DB *sql.DB
	// ManagerBuilder builds a sandboxed workspace manager for a session's
	// workspace root. Nil means checks are unavailable.
	ManagerBuilder func(ctx context.Context, sessionID string) (*workspace.Manager, error)
	// MaxOutputBytes bounds captured stdout/stderr per check.
	MaxOutputBytes int
	// DefaultTimeoutSeconds bounds a check command without an explicit timeout.
	DefaultTimeoutSeconds int
	// PolicyResolver overrides the session tool-policy resolution (tests).
	PolicyResolver func(ctx context.Context, db *sql.DB, sessionID string) (*agentflow.CheckPolicy, error)
}

// CheckPolicyForSession resolves the session's frozen tool policy. The flow
// anchor runs in a Host session, so the session's default tool policy applies
// (settings.default_tool_policy_profile_id, falling back to the builtin
// allow-existing policy).
func (c *CheckTaskRunner) CheckPolicyForSession(ctx context.Context, sessionID string) (*agentflow.CheckPolicy, error) {
	if c.PolicyResolver != nil {
		return c.PolicyResolver(ctx, c.DB, sessionID)
	}
	var policyID string
	if err := c.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM settings WHERE key='default_tool_policy_profile_id'), '')`).
		Scan(&policyID); err != nil {
		return nil, err
	}
	if policyID == "" {
		policyID = "builtin-tool-allow-existing-v1"
	}
	var configJSON string
	if err := c.DB.QueryRowContext(ctx, `SELECT config_json FROM policy_profiles
		WHERE id=? AND kind='tool' AND status='active'`, policyID).Scan(&configJSON); err != nil {
		return nil, fmt.Errorf("tool policy %s is not active: %w", policyID, err)
	}
	var config domain.ToolPolicyConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, err
	}
	return &agentflow.CheckPolicy{
		Mode: config.Mode, AllowedTools: config.AllowedTools,
		AllowedExecutables: config.AllowedExecutables, MaxTimeoutSeconds: config.MaxTimeoutSeconds,
	}, nil
}

// CreateCheckApproval persists the durable Ask-mode approval for one check
// task. A second request for the same (run, task) is idempotent while pending.
func (c *CheckTaskRunner) CreateCheckApproval(ctx context.Context, runID string, taskIndex int, command string) error {
	var exists int
	if err := c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_agent_flow_check_approvals
		WHERE run_id=? AND task_index=?`, runID, taskIndex).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	now := roleTime(time.Now().UTC())
	_, err := c.DB.ExecContext(ctx, `INSERT INTO run_agent_flow_check_approvals
		(run_id, task_index, command, status, requested_at)
		VALUES (?, ?, ?, 'pending', ?)`, runID, taskIndex, command, now)
	return err
}

// CheckApprovalStatus reads the durable approval state of one check task.
func (c *CheckTaskRunner) CheckApprovalStatus(ctx context.Context, runID string, taskIndex int) (agentflow.CheckApprovalStatus, error) {
	var status string
	err := c.DB.QueryRowContext(ctx, `SELECT status FROM run_agent_flow_check_approvals
		WHERE run_id=? AND task_index=?`, runID, taskIndex).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return agentflow.CheckApprovalNone, nil
	}
	if err != nil {
		return "", err
	}
	return agentflow.CheckApprovalStatus(status), nil
}

// DecideCheckApproval resolves a pending check approval (first decision wins).
func (c *CheckTaskRunner) DecideCheckApproval(ctx context.Context, runID string, taskIndex int,
	approved bool, clientRequestID string) (agentflow.CheckApprovalStatus, error) {
	status := agentflow.CheckApprovalRejected
	if approved {
		status = agentflow.CheckApprovalApproved
	}
	now := roleTime(time.Now().UTC())
	res, err := c.DB.ExecContext(ctx, `UPDATE run_agent_flow_check_approvals
		SET status=?, decision_client_request_id=?, resolved_at=?
		WHERE run_id=? AND task_index=? AND status='pending'`,
		status, clientRequestID, now, runID, taskIndex)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return c.CheckApprovalStatus(ctx, runID, taskIndex)
	}
	return status, nil
}

// ListPendingCheckApprovals lists pending check approvals of a project for the
// approval UI.
func (c *CheckTaskRunner) ListPendingCheckApprovals(ctx context.Context, projectID string) ([]CheckApprovalRow, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT a.run_id, a.task_index, a.command, a.requested_at,
		f.session_id, f.flow_version_id
		FROM run_agent_flow_check_approvals a
		JOIN run_agent_flow f ON f.run_id=a.run_id
		WHERE a.status='pending' AND f.project_id=? ORDER BY a.requested_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var approvals []CheckApprovalRow
	for rows.Next() {
		var row CheckApprovalRow
		var requestedAt string
		if err := rows.Scan(&row.RunID, &row.TaskIndex, &row.Command, &requestedAt,
			&row.SessionID, &row.FlowVersionID); err != nil {
			return nil, err
		}
		row.RequestedAt, _ = time.Parse(time.RFC3339Nano, requestedAt)
		approvals = append(approvals, row)
	}
	return approvals, rows.Err()
}

// CheckApprovalRow is the UI-visible pending approval projection.
type CheckApprovalRow struct {
	RunID         string    `json:"runId"`
	TaskIndex     int       `json:"taskIndex"`
	Command       string    `json:"command"`
	SessionID     string    `json:"sessionId"`
	FlowVersionID string    `json:"flowVersionId"`
	RequestedAt   time.Time `json:"requestedAt"`
}

// ExecuteCheck runs the check command in the workspace sandbox with bounded
// output and a bounded wall timeout. The command is passed as a structured
// argv vector (never a shell string).
func (c *CheckTaskRunner) ExecuteCheck(ctx context.Context, command string, timeoutSeconds int) (*agentflow.CheckOutcome, error) {
	if c.ManagerBuilder == nil {
		return nil, fmt.Errorf("workspace sandbox is unavailable")
	}
	argv := agentflow.ParseCheckCommand(command)
	if len(argv) == 0 {
		return nil, fmt.Errorf("check command is empty")
	}
	// Resolve the workspace for the calling flow run's session. The builder
	// is injected with the session id by the orchestrator's caller.
	sessionID, ok := ctx.Value(agentflow.CheckSessionKey{}).(string)
	if !ok {
		return nil, fmt.Errorf("check session context is missing")
	}
	wManager, err := c.ManagerBuilder(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("build workspace sandbox: %w", err)
	}
	timeout := time.Duration(c.DefaultTimeoutSeconds) * time.Second
	if c.DefaultTimeoutSeconds <= 0 {
		timeout = 2 * time.Minute
	}
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	maxBytes := c.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = 32 * 1024
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := wManager.CommandArgs(argv[0], argv[1:]...)
	if err != nil {
		return nil, err
	}
	cmd.Env = safeCheckEnvironment(wManager.RuntimeVisibleDir)
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	stdout.limit = maxBytes
	stderr.limit = maxBytes
	runErr := runProcessGroup(runCtx, cmd)
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	summary := strings.TrimSpace(stdout.String())
	if summary == "" {
		summary = strings.TrimSpace(stderr.String())
	}
	if runErr != nil && summary == "" {
		summary = runErr.Error()
	}
	if len(summary) > 4096 {
		summary = summary[:4096]
	}
	outcome := &agentflow.CheckOutcome{
		Pass: runErr == nil, ExitCode: exitCode, Summary: summary, Command: command,
	}
	return outcome, nil
}

// boundedBuffer captures command output up to a byte limit.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
	full  bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.full {
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.full = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.full = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) String() string { return b.buf.String() }

// safeCheckEnvironment returns a minimal environment for sandboxed checks.
func safeCheckEnvironment(visibleDir string) []string {
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "LANG=C"}
}
