package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// OrchestratorStore implements agentflow.FlowStore over the flow run repo.
type OrchestratorStore struct {
	Runs     *AgentFlowRunRepo
	Profiles *AgentFlowProfileRepo
}

func (s *OrchestratorStore) GetRun(ctx context.Context, runID string) (*domain.RunAgentFlow, error) {
	return s.Runs.GetRun(ctx, runID)
}

func (s *OrchestratorStore) GetVersion(ctx context.Context, versionID string) (*domain.AgentFlowVersion, error) {
	return s.Profiles.GetVersion(ctx, versionID)
}

func (s *OrchestratorStore) ListNodes(ctx context.Context, runID string) ([]*domain.RunAgentFlowNode, error) {
	return s.Runs.ListNodes(ctx, runID)
}

func (s *OrchestratorStore) UpdateNode(ctx context.Context, runID string, upd agentflow.NodeUpdate) error {
	_, err := s.Runs.UpdateNode(ctx, runID, NodeUpdate{
		TaskIndex: upd.TaskIndex, ExpectedStates: upd.ExpectedStates, SetState: upd.SetState,
		ChildRunID: upd.ChildRunID, OutputRef: upd.OutputRef, GoalText: upd.GoalText,
		ErrorCode: upd.ErrorCode,
	})
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
		WHERE run_id=?`, roleTime(time.Now().UTC()), runID)
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
}

// CreateTaskChild materializes a single-item delegation group + child Run for
// one task. Background mode keeps the flow anchor 'running' so the standard
// coordinator parent-wake machinery stays inert and the orchestrator owns the
// child lifecycle. The synthetic parent tool call id is unique per attempt.
func (c *OrchestratorChildren) CreateTaskChild(ctx context.Context, parentRunID, sessionID string,
	spec agentflow.ChildSpec) (agentflow.ChildInfo, error) {
	item := CreateDelegationItemInput{
		Name:           spec.Handle,
		RoleVersionID:  spec.RoleVersionID,
		AssignmentJSON: json.RawMessage(fmt.Sprintf(`{"task":%q}`, spec.Assignment)),
		// OutputContract empty: the delegation admission resolves the frozen
		// Role's contract; the typed payload still flows through submit_result.
		Budget: spec.Budget,
	}
	toolCallID := "flow:" + parentRunID + ":" + spec.Handle + ":" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	group, err := c.Delegations.CreateGroup(ctx, CreateDelegationGroupInput{
		ParentRunID:      parentRunID,
		ParentToolCallID: toolCallID,
		Strategy:         domain.DelegationStrategySingle,
		Items:            []CreateDelegationItemInput{item},
	})
	if err != nil {
		return agentflow.ChildInfo{}, fmt.Errorf("create task delegation group: %w", err)
	}
	items, err := c.Delegations.ListItems(ctx, group.ID)
	if err != nil || len(items) == 0 {
		return agentflow.ChildInfo{}, fmt.Errorf("list task items: %w", err)
	}
	child, err := c.Delegations.CreateChildRun(ctx, CreateChildRunInput{
		ParentRunID: parentRunID, ItemID: items[0].ID, SessionID: sessionID, Background: true,
	})
	if err != nil {
		return agentflow.ChildInfo{}, fmt.Errorf("create task child run: %w", err)
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
