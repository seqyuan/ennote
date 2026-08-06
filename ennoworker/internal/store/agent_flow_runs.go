package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// AgentFlowRunRepo persists meta-Run records and task checkpoints.
type AgentFlowRunRepo struct {
	DB *sql.DB
	// SkillCatalog maps skill name -> skill id at freeze time (nil-safe:
	// unresolved skills fail the freeze loudly).
	SkillCatalog map[string]string
}

// CreateFlowRunInput freezes one flow run: the anchor run, the meta-Run row,
// and every task node snapshot in one transaction.
type CreateFlowRunInput struct {
	SessionID     string
	ProjectID     string
	FlowVersionID string
	// InputsJSON is the frozen run inputs + vars: {"inputs":{...},"vars":{...}}.
	InputsJSON json.RawMessage
}

// FlowNodeFreeze is one task's frozen snapshot resolved at run start.
type FlowNodeFreeze struct {
	TaskIndex     int
	Handle        string
	RoleVersionID string
	SkillIDs      []string
	GoalDigest    string
	GoalText      string
	BudgetJSON    json.RawMessage
}

// CreateFlowRun atomically materializes the anchor agent run, the meta-Run
// record, and all task node snapshots. Any freeze failure aborts the whole
// run: no partial flow is ever visible.
func (r *AgentFlowRunRepo) CreateFlowRun(ctx context.Context, input CreateFlowRunInput,
	freeze []FlowNodeFreeze) (*domain.RunAgentFlow, error) {
	if input.SessionID == "" || input.ProjectID == "" || input.FlowVersionID == "" {
		return nil, fmt.Errorf("session, project, and flow version are required")
	}
	version, err := (&AgentFlowProfileRepo{DB: r.DB}).GetVersion(ctx, input.FlowVersionID)
	if err != nil {
		return nil, fmt.Errorf("load flow version: %w", err)
	}
	var def domain.FlowDefinition
	if err := json.Unmarshal(version.DefinitionJSON, &def); err != nil {
		return nil, fmt.Errorf("decode flow version definition: %w", err)
	}
	manifestDigest, err := agentflow.ManifestDigest(version.ConfigDigest, input.InputsJSON)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	timestamp := roleTime(now)
	runID := uuid.NewString()
	messageID := uuid.NewString()
	turnID := uuid.NewString()
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Anchor transcript: one private system message + one host turn so the
	// anchor satisfies the agent_runs invariants (agent runs need a turn) and
	// the session busy constraint applies while the flow is running. The
	// message is private: the public transcript stays clean; the flow timeline
	// is event-driven, not transcript-driven.
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages
		(id,session_id,parent_message_id,role,status,speaker_kind,speaker_snapshot_json,visibility,created_at)
		VALUES(?,?,NULL,'user','complete','system','{"kind":"system","displayName":"System"}','private',?)`,
		messageID, input.SessionID, timestamp); err != nil {
		return nil, fmt.Errorf("create flow anchor message: %w", err)
	}
	if err := insertMessageParts(ctx, tx, messageID, []domain.ContentBlock{{
		Kind: domain.ContentText,
		Text: fmt.Sprintf("Agent Flow %s v%d started.", def.ID, version.Version),
	}}); err != nil {
		return nil, fmt.Errorf("create flow anchor message parts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO turns
		(id,session_id,client_request_id,user_message_id,base_message_id,status,
		 input_message_id,input_kind,target_kind,context_mode,reply_to_json,created_at,updated_at)
		VALUES(?,?,?,?,NULL,'pending',?,'user_message','host','task_only','[]',?,?)`,
		turnID, input.SessionID, "agent-flow:"+runID, messageID, messageID, timestamp, timestamp); err != nil {
		return nil, fmt.Errorf("create flow anchor turn: %w", err)
	}
	// Anchor: a top-level Host agent run owned entirely by the orchestrator.
	// It never goes through the agent loop; it is the durable parent identity
	// for delegation children, events, and SSE.
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_runs
		(id, turn_id, session_id, run_kind, base_message_id, attempt, status, requested_config_json,
		 effective_config_json, speaker_snapshot_json, root_run_id, execution_depth, publish_mode,
		 commit_format_version, context_snapshot_json, created_at)
		VALUES (?, ?, ?, 'agent', ?, 1, 'running', '{}', '{}', '{"kind":"host","displayName":"Host"}', ?, 0, 'private_to_parent', 2, '{}', ?)`,
		runID, turnID, input.SessionID, messageID, runID, timestamp); err != nil {
		return nil, fmt.Errorf("create flow anchor run: %w", err)
	}
	inputsJSON := input.InputsJSON
	if len(inputsJSON) == 0 {
		inputsJSON = json.RawMessage(`{}`)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_agent_flow
		(run_id, session_id, project_id, flow_version_id, manifest_digest, inputs_json, state,
		 total_tokens_used, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?)`,
		runID, input.SessionID, input.ProjectID, input.FlowVersionID, manifestDigest,
		string(inputsJSON), timestamp, timestamp); err != nil {
		return nil, fmt.Errorf("create flow run: %w", err)
	}
	// Verify the freeze covers every task in the version (exact structural match).
	if len(freeze) != len(def.Tasks) {
		return nil, fmt.Errorf("flow freeze mismatch: version declares %d tasks, freeze covers %d",
			len(def.Tasks), len(freeze))
	}
	seen := make(map[int]bool, len(freeze))
	for _, node := range freeze {
		if seen[node.TaskIndex] {
			return nil, fmt.Errorf("flow freeze covers task index %d twice", node.TaskIndex)
		}
		seen[node.TaskIndex] = true
		if node.Handle == "" || node.GoalDigest == "" {
			return nil, fmt.Errorf("flow freeze for task %d is incomplete", node.TaskIndex)
		}
		skillsJSON, _ := json.Marshal(node.SkillIDs)
		budgetJSON := node.BudgetJSON
		if len(budgetJSON) == 0 {
			budgetJSON = json.RawMessage(`{}`)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_agent_flow_nodes
			(run_id, task_index, handle, role_version_id, skill_digests_json, goal_digest, goal_text,
			 budget_json, terminal_state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
			runID, node.TaskIndex, node.Handle, node.RoleVersionID, string(skillsJSON),
			node.GoalDigest, node.GoalText, string(budgetJSON), timestamp); err != nil {
			return nil, fmt.Errorf("create flow node %d: %w", node.TaskIndex, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &domain.RunAgentFlow{
		RunID: runID, SessionID: input.SessionID, ProjectID: input.ProjectID,
		FlowVersionID: input.FlowVersionID, ManifestDigest: manifestDigest,
		State: domain.FlowStatePending, InputsJSON: inputsJSON, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// GetRun loads the meta-Run record.
func (r *AgentFlowRunRepo) GetRun(ctx context.Context, runID string) (*domain.RunAgentFlow, error) {
	var run domain.RunAgentFlow
	var inputs, createdAt, updatedAt, finishedAt sql.NullString
	err := r.DB.QueryRowContext(ctx, `SELECT run_id, session_id, project_id, flow_version_id, manifest_digest,
		inputs_json, state, total_tokens_used, terminal_reason, created_at, updated_at, finished_at
		FROM run_agent_flow WHERE run_id=?`, runID).
		Scan(&run.RunID, &run.SessionID, &run.ProjectID, &run.FlowVersionID, &run.ManifestDigest,
			&inputs, &run.State, &run.TotalTokensUsed, &run.TerminalReason,
			&createdAt, &updatedAt, &finishedAt)
	if err != nil {
		return nil, err
	}
	if inputs.Valid {
		run.InputsJSON = json.RawMessage(inputs.String)
	}
	run.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt.String)
	run.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt.String)
	if finishedAt.Valid && finishedAt.String != "" {
		if value, err := time.Parse(time.RFC3339Nano, finishedAt.String); err == nil {
			run.FinishedAt = &value
		}
	}
	return &run, nil
}

// ListProjectRuns lists meta-Run records of one project, newest first.
func (r *AgentFlowRunRepo) ListProjectRuns(ctx context.Context, projectID string, limit int) ([]*domain.RunAgentFlow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT run_id, session_id, project_id, flow_version_id, manifest_digest,
		inputs_json, state, total_tokens_used, terminal_reason, created_at, updated_at, finished_at
		FROM run_agent_flow WHERE project_id=? ORDER BY created_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []*domain.RunAgentFlow
	for rows.Next() {
		var run domain.RunAgentFlow
		var inputs, createdAt, updatedAt, finishedAt sql.NullString
		if err := rows.Scan(&run.RunID, &run.SessionID, &run.ProjectID, &run.FlowVersionID,
			&run.ManifestDigest, &inputs, &run.State, &run.TotalTokensUsed, &run.TerminalReason,
			&createdAt, &updatedAt, &finishedAt); err != nil {
			return nil, err
		}
		if inputs.Valid {
			run.InputsJSON = json.RawMessage(inputs.String)
		}
		run.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt.String)
		run.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt.String)
		if finishedAt.Valid && finishedAt.String != "" {
			if value, err := time.Parse(time.RFC3339Nano, finishedAt.String); err == nil {
				run.FinishedAt = &value
			}
		}
		runs = append(runs, &run)
	}
	return runs, rows.Err()
}

// ListNodes returns all task checkpoints of a flow run ordered by task index.
func (r *AgentFlowRunRepo) ListNodes(ctx context.Context, runID string) ([]*domain.RunAgentFlowNode, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT run_id, task_index, handle, role_version_id, skill_digests_json,
		goal_digest, goal_text, budget_json, terminal_state, output_ref, child_run_id, error_code, created_at, finished_at
		FROM run_agent_flow_nodes WHERE run_id=? ORDER BY task_index`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []*domain.RunAgentFlowNode
	for rows.Next() {
		node, err := scanFlowNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

// GetNode fetches one task checkpoint.
func (r *AgentFlowRunRepo) GetNode(ctx context.Context, runID string, taskIndex int) (*domain.RunAgentFlowNode, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT run_id, task_index, handle, role_version_id, skill_digests_json,
		goal_digest, goal_text, budget_json, terminal_state, output_ref, child_run_id, error_code, created_at, finished_at
		FROM run_agent_flow_nodes WHERE run_id=? AND task_index=?`, runID, taskIndex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	node, err := scanFlowNode(rows)
	if err != nil {
		return nil, err
	}
	return node, rows.Err()
}

// NodeUpdate is a CAS checkpoint mutation: the node moves from expectedState
// (or any state when empty) to the provided terminal/running state.
type NodeUpdate struct {
	TaskIndex      int
	ExpectedStates []domain.FlowNodeState
	SetState       domain.FlowNodeState
	ChildRunID     string
	OutputRef      json.RawMessage
	GoalText       string
	ErrorCode      string
}

// UpdateNode applies a checkpoint mutation and returns the node when the CAS
// matched. It fails with ErrFlowNodeStateConflict otherwise.
func (r *AgentFlowRunRepo) UpdateNode(ctx context.Context, runID string, upd NodeUpdate) (*domain.RunAgentFlowNode, error) {
	expected := ""
	if len(upd.ExpectedStates) > 0 {
		quoted := make([]string, 0, len(upd.ExpectedStates))
		for _, s := range upd.ExpectedStates {
			quoted = append(quoted, fmt.Sprintf("'%s'", s))
		}
		expected = " AND terminal_state IN (" + strings.Join(quoted, ",") + ")"
	}
	now := roleTime(time.Now().UTC())
	outputRef := ""
	if len(upd.OutputRef) > 0 {
		outputRef = string(upd.OutputRef)
	}
	res, err := r.DB.ExecContext(ctx, `UPDATE run_agent_flow_nodes SET
		terminal_state=?, child_run_id=COALESCE(?, child_run_id),
		output_ref=COALESCE(?, output_ref), goal_text=COALESCE(?, goal_text),
		error_code=COALESCE(?, error_code),
		finished_at=CASE WHEN ?='' THEN finished_at ELSE ? END
		WHERE run_id=? AND task_index=?`+expected,
		upd.SetState, upd.ChildRunID, outputRef, upd.GoalText, upd.ErrorCode,
		now, now, runID, upd.TaskIndex)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrFlowNodeStateConflict
	}
	return r.GetNode(ctx, runID, upd.TaskIndex)
}

// UpdateFlowState transitions the meta-Run state. Terminal transitions also
// record the finished timestamp.
func (r *AgentFlowRunRepo) UpdateFlowState(ctx context.Context, runID string, state domain.FlowState,
	totalTokens int64, reason string) (*domain.RunAgentFlow, error) {
	now := roleTime(time.Now().UTC())
	finished := ""
	if state.Terminal() {
		finished = now
	}
	res, err := r.DB.ExecContext(ctx, `UPDATE run_agent_flow SET
		state=?, total_tokens_used=?, terminal_reason=COALESCE(?, terminal_reason),
		updated_at=?, finished_at=COALESCE(?, finished_at) WHERE run_id=?`,
		state, totalTokens, reason, now, finished, runID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return r.GetRun(ctx, runID)
}

// AddTokenUsage increments the meta-Run token ledger and returns the new total.
func (r *AgentFlowRunRepo) AddTokenUsage(ctx context.Context, runID string, tokens int64) (int64, error) {
	res, err := r.DB.ExecContext(ctx, `UPDATE run_agent_flow SET
		total_tokens_used=total_tokens_used+?, updated_at=? WHERE run_id=?`,
		tokens, roleTime(time.Now().UTC()), runID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, sql.ErrNoRows
	}
	var total int64
	if err := r.DB.QueryRowContext(ctx, `SELECT total_tokens_used FROM run_agent_flow WHERE run_id=?`, runID).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// SetAnchorRunning restores the anchor run to 'running' after crash recovery
// (RecoverActive interrupts it on restart). It is a no-op when already running.
func (r *AgentFlowRunRepo) SetAnchorRunning(ctx context.Context, runID string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE agent_runs SET status='running', error_code=NULL, error_message=NULL
		WHERE id=? AND status NOT IN ('succeeded','failed','cancelled')`, runID)
	return err
}

// TerminateAnchor ends the anchor run after a terminal flow state. The anchor
// is the orchestration vehicle, not an agent loop run, so we record the
// terminal state directly and leave the delegation history intact. The anchor
// turn follows the run state.
func (r *AgentFlowRunRepo) TerminateAnchor(ctx context.Context, runID string, status domain.RunStatus, code, message string) error {
	if status != domain.RunSucceeded && status != domain.RunFailed && status != domain.RunCancelled {
		return fmt.Errorf("invalid anchor terminal status %s", status)
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status=?, error_code=?, error_message=?,
		finished_at=? WHERE id=? AND status NOT IN ('succeeded','failed','cancelled','interrupted')`,
		status, code, message, roleTime(time.Now().UTC()), runID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // already terminal
	}
	turnStatus := "succeeded"
	if status != domain.RunSucceeded {
		turnStatus = "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET status=?, updated_at=?
		WHERE id=(SELECT turn_id FROM agent_runs WHERE id=?)`, turnStatus, roleTime(time.Now().UTC()), runID); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveFlowRoleVersion resolves "handle@version" to a published immutable
// role version inside the project's scope boundary. Project-scoped roles only
// resolve for their owning project; global/builtin roles resolve for everyone.
func (r *AgentFlowRunRepo) ResolveFlowRoleVersion(ctx context.Context, roleRef, projectID string) (versionID string, definitionJSON json.RawMessage, err error) {
	handle, versionText, ok := strings.Cut(strings.TrimSpace(roleRef), "@")
	if !ok || strings.TrimSpace(handle) == "" || strings.TrimSpace(versionText) == "" {
		return "", nil, fmt.Errorf("role reference %q must be handle@version", roleRef)
	}
	var versionNumber int
	if _, err := fmt.Sscanf(versionText, "%d", &versionNumber); err != nil || versionNumber < 1 {
		return "", nil, fmt.Errorf("role reference %q has an invalid version", roleRef)
	}
	var versionIDOut, definitionJSONOut string
	var scope, roleProject sql.NullString
	err = r.DB.QueryRowContext(ctx, `SELECT v.id, p.scope, p.project_id, v.definition_json
		FROM agent_profiles p JOIN agent_profile_versions v ON v.agent_profile_id=p.id
		WHERE p.object_kind='role' AND p.handle=? AND v.version=? AND v.status='published'
		  AND p.status='active' AND (p.project_id=? OR p.project_id IS NULL)
		ORDER BY CASE WHEN p.project_id=? THEN 0 WHEN p.scope='builtin' THEN 1 ELSE 2 END LIMIT 1`,
		handle, versionNumber, projectID, projectID).
		Scan(&versionIDOut, &scope, &roleProject, &definitionJSONOut)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("role %q is not published in this project", roleRef)
	}
	if err != nil {
		return "", nil, err
	}
	return versionIDOut, json.RawMessage(definitionJSONOut), nil
}

// ResolveFlowSkills resolves skill names to catalog ids. Missing skills fail
// loudly (fail-closed: no silent empty skill set).
func (r *AgentFlowRunRepo) ResolveFlowSkills(ctx context.Context, names []string) ([]string, error) {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		if r.SkillCatalog == nil {
			return nil, fmt.Errorf("skill catalog is unavailable")
		}
		id, ok := r.SkillCatalog[name]
		if !ok {
			return nil, fmt.Errorf("skill %q is not in the catalog", name)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func scanFlowNode(scan interface{ Scan(...any) error }) (*domain.RunAgentFlowNode, error) {
	var node domain.RunAgentFlowNode
	var roleVersionID, skillsJSON, goalDigest, goalText, budgetJSON, outputRef, childRunID, errorCode, createdAt, finishedAt sql.NullString
	if err := scan.Scan(&node.RunID, &node.TaskIndex, &node.Handle, &roleVersionID,
		&skillsJSON, &goalDigest, &goalText, &budgetJSON, &node.TerminalState,
		&outputRef, &childRunID, &errorCode, &createdAt, &finishedAt); err != nil {
		return nil, err
	}
	if roleVersionID.Valid {
		node.RoleVersionID = roleVersionID.String
	}
	if goalDigest.Valid {
		node.GoalDigest = goalDigest.String
	}
	if goalText.Valid {
		node.GoalText = goalText.String
	}
	if errorCode.Valid {
		node.ErrorCode = errorCode.String
	}
	if skillsJSON.Valid {
		_ = json.Unmarshal([]byte(skillsJSON.String), &node.SkillDigests)
	}
	if budgetJSON.Valid {
		node.BudgetJSON = json.RawMessage(budgetJSON.String)
	}
	if outputRef.Valid && outputRef.String != "" {
		node.OutputRef = json.RawMessage(outputRef.String)
	}
	if childRunID.Valid {
		node.ChildRunID = childRunID.String
	}
	node.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt.String)
	if finishedAt.Valid && finishedAt.String != "" {
		if value, err := time.Parse(time.RFC3339Nano, finishedAt.String); err == nil {
			node.FinishedAt = &value
		}
	}
	return &node, nil
}

var (
	ErrFlowNodeStateConflict = errors.New("flow node state conflict")
)

// ResumeFlowRun reopens a cancelled (or failed/interrupted) meta-Run for
// checkpoint continuation: completed task checkpoints are kept, all other
// nodes are reset to pending, and the cancel flag clears. Force full replay is
// a NEW run (explicit), never a silent rewrite of a terminal run.
func (r *AgentFlowRunRepo) ResumeFlowRun(ctx context.Context, runID string) (*domain.RunAgentFlow, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE run_agent_flow SET state='pending', cancel_requested=0,
		terminal_reason=NULL, finished_at=NULL, updated_at=?
		WHERE run_id=? AND state IN ('cancelled','failed')`, roleTime(time.Now().UTC()), runID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("flow run %s is not resumable", runID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_agent_flow_nodes SET terminal_state='pending',
		error_code=NULL, output_ref=NULL, finished_at=NULL
		WHERE run_id=? AND terminal_state IN ('cancelled','interrupted','failed','blocked')`, runID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetRun(ctx, runID)
}
