package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

// OrchestratorStore implements agentflow.FlowStore over the flow run repo.
// GetVersion resolves the frozen execution plan from the owning Session
// database (run_agent_flow.definition_json), never from mutable files or the
// removed global agent_flow_version tables.
type OrchestratorStore struct {
	Runs *AgentFlowRunRepo
}

func (s *OrchestratorStore) GetRun(ctx context.Context, runID string) (*domain.RunAgentFlow, error) {
	return s.Runs.GetRun(ctx, runID)
}

func (s *OrchestratorStore) GetVersion(ctx context.Context, versionID string) (*domain.AgentFlowVersion, error) {
	var definitionJSON, configDigest string
	err := s.Runs.DB.QueryRowContext(ctx, `SELECT definition_json, config_digest
		FROM run_agent_flow WHERE flow_version_id=? LIMIT 1`, versionID).
		Scan(&definitionJSON, &configDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("frozen flow definition is unavailable for %s", versionID)
	}
	if err != nil {
		return nil, err
	}
	var version int
	if _, revision, ok := strings.Cut(versionID, "@"); ok {
		_, _ = fmt.Sscanf(strings.TrimPrefix(revision, "v"), "%d", &version)
	}
	return &domain.AgentFlowVersion{
		ID: versionID, Version: version, ConfigDigest: configDigest,
		DefinitionJSON: json.RawMessage(definitionJSON),
	}, nil
}

func (s *OrchestratorStore) ListNodes(ctx context.Context, runID string) ([]*domain.RunAgentFlowNode, error) {
	return s.Runs.ListNodes(ctx, runID)
}

func (s *OrchestratorStore) UpdateNode(ctx context.Context, runID string, upd agentflow.NodeUpdate) error {
	_, err := s.Runs.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: upd.TaskIndex, ExpectedStates: upd.ExpectedStates, SetState: upd.SetState,
		ChildRunID: upd.ChildRunID, ChildRunIDs: upd.ChildRunIDs, OutputRef: upd.OutputRef,
		GoalText: upd.GoalText, ErrorCode: upd.ErrorCode, AcceptanceVerdict: upd.AcceptanceVerdict,
		ClearErrorCode: upd.ClearErrorCode, ClearOutputRef: upd.ClearOutputRef,
		IncrementAttempts: upd.IncrementAttempts,
	})
	if errors.Is(err, ErrFlowNodeStateConflict) {
		return agentflow.ErrCheckpointConflict
	}
	return err
}

func (s *OrchestratorStore) UpdateFlowState(ctx context.Context, runID string, state domain.FlowState,
	totalTokens int64, reason string) error {
	_, err := s.Runs.UpdateFlowState(ctx, runID, state, totalTokens, reason)
	return err
}

func (s *OrchestratorStore) AddTokenUsage(ctx context.Context, runID string, tokens int64) (int64, error) {
	return s.Runs.AddTokenUsage(ctx, runID, tokens)
}

func (s *OrchestratorStore) SetCancelRequested(ctx context.Context, runID string) error {
	_, err := s.Runs.DB.ExecContext(ctx, `UPDATE run_agent_flow SET cancel_requested=1, updated_at=?
		WHERE run_id=?`, heroTime(time.Now().UTC()), runID)
	return err
}

func (s *OrchestratorStore) IsCancelRequested(ctx context.Context, runID string) (bool, error) {
	var value int
	err := s.Runs.DB.QueryRowContext(ctx, `SELECT cancel_requested FROM run_agent_flow WHERE run_id=?`, runID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value == 1, err
}

func (s *OrchestratorStore) SetAnchorRunning(ctx context.Context, runID string) error {
	return s.Runs.SetAnchorRunning(ctx, runID)
}

func (s *OrchestratorStore) TerminateAnchor(ctx context.Context, runID string, status domain.RunStatus, code, message string) error {
	return s.Runs.TerminateAnchor(ctx, runID, status, code, message)
}

// OrchestratorChildren implements agentflow.ChildProvider over the delegation
// substrate: one child Run per task via a single-item delegation group.
type OrchestratorChildren struct {
	DB          *sql.DB
	Delegations *DelegationRepo
	// Policies resolves the file-backed delegation policy for the root budget
	// ledger (V2); nil keeps the legacy global policy SQL path.
	Policies *fileconfig.PolicyStore
}

// CreateTaskChild atomically materializes one task's single-item delegation
// group + child Run + budget reservation in ONE transaction, so a failure
// never leaks an orphaned group/item. Background mode keeps the flow anchor
// 'running' so the standard coordinator parent-wake machinery stays inert and
// the orchestrator owns the child lifecycle. The synthetic parent tool call
// id is unique per attempt. HeroMeta carries the frozen hero identity and
// definition from the flow node so delegation never reads global hero SQL.
func (c *OrchestratorChildren) CreateTaskChild(ctx context.Context, parentRunID, sessionID string,
	spec agentflow.ChildSpec) (agentflow.ChildInfo, error) {
	var heroMeta *DelegationHeroMeta
	if len(spec.HeroDefinitionJSON) > 0 {
		meta, err := delegationHeroMetaFromDefinition(spec.HeroVersionID, spec.HeroDefinitionJSON)
		if err != nil {
			return agentflow.ChildInfo{}, fmt.Errorf("freeze task hero meta: %w", err)
		}
		meta.Handle = spec.Handle
		if meta.DisplayName == "" {
			meta.DisplayName = spec.Handle
		}
		heroMeta = meta
	}
	item := CreateDelegationItemInput{
		Name:           spec.Handle,
		HeroVersionID:  spec.HeroVersionID,
		FrozenSkills:   append(domain.FrozenSkillRefs(nil), spec.Skills...),
		AssignmentJSON: json.RawMessage(fmt.Sprintf(`{"task":%q}`, spec.Assignment)),
		// OutputContract empty: the delegation admission resolves the frozen
		// Hero's contract; the typed payload still flows through submit_result.
		Budget:   spec.Budget,
		HeroMeta: heroMeta,
	}
	toolCallID := "flow:" + parentRunID + ":" + spec.Handle + ":" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return agentflow.ChildInfo{}, err
	}
	defer tx.Rollback()
	group, items, err := createDelegationGroupTx(ctx, tx, CreateDelegationGroupInput{
		ParentRunID:      parentRunID,
		ParentToolCallID: toolCallID,
		Strategy:         domain.DelegationStrategySingle,
		Items:            []CreateDelegationItemInput{item},
	})
	if err != nil {
		return agentflow.ChildInfo{}, fmt.Errorf("create task delegation group: %w", err)
	}
	child, err := createChildRunTx(ctx, tx, CreateChildRunInput{
		ParentRunID: parentRunID, ItemID: items[0].ID, SessionID: sessionID, Background: true,
		Policies: c.Policies,
	})
	if err != nil {
		return agentflow.ChildInfo{}, fmt.Errorf("create task child run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return agentflow.ChildInfo{}, fmt.Errorf("commit task child: %w", err)
	}
	return agentflow.ChildInfo{RunID: child.ID, ItemID: items[0].ID, GroupID: group.ID}, nil
}

func (c *OrchestratorChildren) ChildRunStatus(ctx context.Context, runID string) (domain.RunStatus, error) {
	var status string
	err := c.DB.QueryRowContext(ctx, `SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRunNotFound
	}
	return domain.RunStatus(status), err
}

// ChildTerminalResult reads the folded submit_result from the delegation item.
func (c *OrchestratorChildren) ChildTerminalResult(ctx context.Context, runID string) (*domain.SubmitResult, error) {
	var resultJSON sql.NullString
	err := c.DB.QueryRowContext(ctx, `SELECT i.result_json FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id AND a.child_run_id=?
		WHERE i.child_run_id=?`, runID, runID).Scan(&resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !resultJSON.Valid || resultJSON.String == "" {
		return nil, nil
	}
	var result domain.SubmitResult
	if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ChildUsage reads the child's consumed tokens from its run_budgets ledger.
func (c *OrchestratorChildren) ChildUsage(ctx context.Context, runID string) (domain.RunBudgetUsage, error) {
	var usage domain.RunBudgetUsage
	err := c.DB.QueryRowContext(ctx, `SELECT consumed_model_calls, consumed_tool_calls, consumed_tokens,
		consumed_output_tokens, consumed_cost_usd_micros FROM run_budgets WHERE run_id=?`, runID).
		Scan(&usage.ModelCalls, &usage.ToolCalls, &usage.Tokens, &usage.OutputTokens, &usage.CostMicros)
	if errors.Is(err, sql.ErrNoRows) {
		return usage, nil
	}
	return usage, err
}

// CancelChildRun hard-cancels the child and settles its attempt (RunRepo.Cancel).
func (c *OrchestratorChildren) CancelChildRun(ctx context.Context, runID string) error {
	return (&RunRepo{DB: c.DB}).Cancel(ctx, runID)
}

// ListRecoverableRuns returns every non-terminal meta-Run id for startup
// recovery.
func (r *AgentFlowRunRepo) ListRecoverableRuns(ctx context.Context) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT run_id FROM run_agent_flow
		WHERE state IN ('pending','running') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *OrchestratorStore) GetConvergenceRounds(ctx context.Context, runID string) (map[string]int, error) {
	return s.Runs.GetConvergenceRounds(ctx, runID)
}

func (s *OrchestratorStore) SetConvergenceRounds(ctx context.Context, runID string, rounds map[string]int) error {
	return s.Runs.SetConvergenceRounds(ctx, runID, rounds)
}

// Checkpoint CAS: the durable write of one task node, next to the FlowStore
// adapter that exposes it to the orchestrator. It lives here rather than in
// agent_flow_runs.go so the write, its NodeUpdate modes and the adapter that
// translates agentflow.NodeUpdate stay in one file.

// NodeUpdate is a CAS checkpoint mutation: the node moves from expectedState
// (or any state when empty) to the provided terminal/running state.
type NodeUpdate struct {
	TaskIndex      int
	ExpectedStates []domain.FlowNodeState
	SetState       domain.FlowNodeState
	ChildRunID     string
	ChildRunIDs    []string
	OutputRef      json.RawMessage
	GoalText       string
	ErrorCode      string
	// AcceptanceVerdict is tri-state, because one SQL write cannot express
	// "leave it", "clear it" and "set it" at once:
	//   nil            -> leave the column alone (dispatch, cancel, re-dispatch)
	//   non-nil, empty -> clear it (a node reset to pending must not keep a
	//                     stale verdict)
	//   non-nil, value -> write it (the acceptance fold, in the checkpoint CAS)
	// A plain COALESCE cannot express "clear", and an unconditional write would
	// clear verdicts on every unrelated update.
	AcceptanceVerdict *json.RawMessage
	// ClearErrorCode un-records error_code: COALESCE cannot clear, and a reset node must not look failed.
	ClearErrorCode bool
	// ClearOutputRef un-records output_ref for the same reason: a re-dispatched
	// node must not still carry the previous attempt's result as its checkpoint,
	// or a dependent would resolve {task.X.output} from a failed attempt.
	ClearOutputRef bool
	// IncrementAttempts spends one retry attempt in the SAME CAS that resets the
	// node. One write is what makes the budget durable: a crash after the reset
	// can neither lose the increment (the attempt would be re-granted on resume)
	// nor apply it twice.
	IncrementAttempts bool
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
	now := heroTime(time.Now().UTC())
	outputRef := ""
	if len(upd.OutputRef) > 0 {
		outputRef = string(upd.OutputRef)
	}
	childRunIDsJSON := ""
	if len(upd.ChildRunIDs) > 0 {
		if encoded, err := json.Marshal(upd.ChildRunIDs); err == nil {
			childRunIDsJSON = string(encoded)
		}
	}
	// Tri-state verdict: the column is NOT NULL, so "clear" writes '' and "leave" keeps it.
	setVerdict := 0
	verdict := ""
	if upd.AcceptanceVerdict != nil {
		setVerdict = 1
		verdict = string(*upd.AcceptanceVerdict)
	}
	// error_code has two ways to be left alone and one to be cleared:
	// ClearErrorCode writes '', a non-empty ErrorCode writes that code, and
	// neither keeps the current value.
	clearErrorCode := 0
	if upd.ClearErrorCode {
		clearErrorCode = 1
	}
	clearOutputRef := 0
	if upd.ClearOutputRef {
		clearOutputRef = 1
	}
	spendAttempt := 0
	if upd.IncrementAttempts {
		spendAttempt = 1
	}
	res, err := r.DB.ExecContext(ctx, `UPDATE run_agent_flow_nodes SET
		terminal_state=?, child_run_id=COALESCE(?, child_run_id),
		child_run_ids_json=CASE WHEN ?='' THEN child_run_ids_json ELSE ? END,
		output_ref=CASE WHEN ?=1 THEN NULL ELSE COALESCE(?, output_ref) END,
		goal_text=COALESCE(?, goal_text),
		error_code=CASE WHEN ?=1 THEN NULL ELSE COALESCE(?, error_code) END,
		acceptance_verdict=CASE WHEN ?=1 THEN ? ELSE acceptance_verdict END,
		attempts=attempts+?,
		finished_at=CASE WHEN ? IS NULL THEN finished_at ELSE ? END
		WHERE run_id=? AND task_index=?`+expected,
		upd.SetState, nullableOrNil(upd.ChildRunID), childRunIDsJSON, childRunIDsJSON,
		clearOutputRef, nullableOrNil(outputRef), nullableOrNil(upd.GoalText),
		clearErrorCode, nullableOrNil(upd.ErrorCode),
		setVerdict, verdict, spendAttempt,
		nullableOrNil(now), nullableOrNil(now), runID, upd.TaskIndex)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrFlowNodeStateConflict
	}
	return r.GetNode(ctx, runID, upd.TaskIndex)
}

var (
	ErrFlowNodeStateConflict = errors.New("flow node state conflict")
)
