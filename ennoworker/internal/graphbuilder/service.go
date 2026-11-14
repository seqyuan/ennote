package graphbuilder

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

//go:embed skill/SKILL.md
var skillPrompt string

type CompleteFunc func(ctx context.Context, modelProfileID, systemPrompt, userPrompt string) (string, error)

func (f CompleteFunc) Complete(ctx context.Context, modelProfileID, systemPrompt, userPrompt string) (string, error) {
	return f(ctx, modelProfileID, systemPrompt, userPrompt)
}

type Completer interface {
	Complete(context.Context, string, string, string) (string, error)
}

type Service struct {
	DB        *sql.DB
	Sources   *globalsource.Store
	Completer Completer
}

type Message struct {
	ID        string    `json:"id"`
	GraphID   string    `json:"graphId"`
	Ordinal   int       `json:"ordinal"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

type Operation struct {
	Kind        string            `json:"kind"`
	TaskID      string            `json:"taskId,omitempty"`
	Task        *domain.FlowTask `json:"task,omitempty"`
	Depends     []string          `json:"depends,omitempty"`
	Name        *string           `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
}

type Proposal struct {
	ID          string      `json:"id"`
	GraphID     string      `json:"graphId"`
	BaseDigest  string      `json:"baseDigest"`
	Operations  []Operation `json:"operations"`
	Summary     string      `json:"summary"`
	Status      string      `json:"status"`
	Diagnostics []string    `json:"diagnostics"`
	CreatedAt   time.Time   `json:"createdAt"`
}

type Thread struct {
	GraphID        string    `json:"graphId"`
	ModelProfileID string    `json:"modelProfileId,omitempty"`
	Messages       []Message `json:"messages"`
	Proposal       *Proposal `json:"proposal,omitempty"`
}

func (s *Service) GetThread(ctx context.Context, graphID string) (*Thread, error) {
	if s.DB == nil {
		return s.getFileThread(graphID)
	}
	thread := &Thread{GraphID: graphID, Messages: []Message{}}
	_ = s.DB.QueryRowContext(ctx, `SELECT model_profile_id FROM graph_builder_threads WHERE graph_id=?`, graphID).Scan(&thread.ModelProfileID)
	rows, err := s.DB.QueryContext(ctx, `SELECT id,ordinal,hero,content,created_at FROM graph_builder_messages WHERE graph_id=? ORDER BY ordinal`, graphID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var message Message
		var created string
		message.GraphID = graphID
		if err := rows.Scan(&message.ID, &message.Ordinal, &message.Role, &message.Content, &created); err != nil {
			return nil, err
		}
		message.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		thread.Messages = append(thread.Messages, message)
	}
	proposal, err := s.pendingProposal(ctx, graphID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	thread.Proposal = proposal
	return thread, rows.Err()
}

func (s *Service) Send(ctx context.Context, graphID, modelProfileID, instruction string) (*Thread, error) {
	if s.DB == nil {
		return s.sendFile(ctx, graphID, modelProfileID, instruction)
	}
	instruction = strings.TrimSpace(instruction)
	if instruction == "" || modelProfileID == "" {
		return nil, fmt.Errorf("modelProfileId and instruction are required")
	}
	if s.Completer == nil {
		return nil, fmt.Errorf("Graph Builder model service is unavailable")
	}
	definition, digest, err := s.Sources.ReadGraphDefinition(graphID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO graph_builder_threads(graph_id,model_profile_id,created_at,updated_at)
		VALUES(?,?,?,?) ON CONFLICT(graph_id) DO UPDATE SET model_profile_id=excluded.model_profile_id,updated_at=excluded.updated_at`,
		graphID, modelProfileID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return nil, err
	}
	if err := s.appendMessage(ctx, graphID, "user", instruction, now); err != nil {
		return nil, err
	}
	graphJSON, _ := json.MarshalIndent(definition, "", "  ")
	response, err := s.Completer.Complete(ctx, modelProfileID, skillPrompt, "Current Graph:\n"+string(graphJSON)+"\n\nUser instruction:\n"+instruction)
	if err != nil {
		return nil, err
	}
	var output struct {
		Message    string      `json:"message"`
		Operations []Operation `json:"operations"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(response)), &output); err != nil {
		return nil, fmt.Errorf("Graph Builder returned invalid JSON: %w", err)
	}
	if strings.TrimSpace(output.Message) == "" {
		return nil, fmt.Errorf("Graph Builder response requires a message")
	}
	if err := s.appendMessage(ctx, graphID, "assistant", output.Message, time.Now().UTC()); err != nil {
		return nil, err
	}

	diagnostics := validateOperations(definition, output.Operations)
	operationsJSON, _ := json.Marshal(output.Operations)
	diagnosticsJSON, _ := json.Marshal(diagnostics)
	if _, err := s.DB.ExecContext(ctx, `UPDATE graph_builder_proposals SET status='superseded' WHERE graph_id=? AND status='pending'`, graphID); err != nil {
		return nil, err
	}
	if len(output.Operations) > 0 {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO graph_builder_proposals
			(id,graph_id,base_digest,operations_json,summary,status,diagnostics_json,created_at)
			VALUES(?,?,?,?,?,'pending',?,?)`, uuid.NewString(), graphID, digest, string(operationsJSON), output.Message, string(diagnosticsJSON), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
	}
	return s.GetThread(ctx, graphID)
}

func (s *Service) Apply(ctx context.Context, graphID, proposalID string) (*domain.FlowDefinition, string, error) {
	if s.DB == nil {
		return s.applyFile(ctx, graphID, proposalID)
	}
	proposal, err := s.pendingProposal(ctx, graphID)
	if err != nil {
		return nil, "", err
	}
	if proposal.ID != proposalID {
		return nil, "", fmt.Errorf("proposal is no longer pending")
	}
	if len(proposal.Diagnostics) > 0 {
		return nil, "", fmt.Errorf("proposal is invalid: %s", proposal.Diagnostics[0])
	}
	definition, digest, err := s.Sources.UpdateGraphDefinition(graphID, proposal.BaseDigest, func(definition *domain.FlowDefinition) error {
		applyOperations(definition, proposal.Operations)
		return nil
	})
	if err != nil {
		return nil, digest, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE graph_builder_proposals SET status='applied',applied_at=? WHERE id=? AND status='pending'`, time.Now().UTC().Format(time.RFC3339Nano), proposalID); err != nil {
		return nil, digest, err
	}
	return definition, digest, nil
}

func validateOperations(definition *domain.FlowDefinition, operations []Operation) []string {
	copy := cloneDefinition(definition)
	applyOperations(copy, operations)
	if _, err := agentflow.DefinitionToYAML(copy); err != nil {
		return []string{err.Error()}
	}
	if _, err := agentflow.TaskOrder(copy); err != nil {
		return []string{err.Error()}
	}
	return []string{}
}

func applyOperations(definition *domain.FlowDefinition, operations []Operation) {
	if definition.Tasks == nil {
		definition.Tasks = map[string]domain.FlowTask{}
	}
	for _, operation := range operations {
		switch operation.Kind {
		case "upsert_task":
			if operation.Task != nil {
				task := *operation.Task
				if existing, exists := definition.Tasks[operation.TaskID]; exists && task.Depends == nil {
					task.Depends = existing.Depends
				}
				definition.Tasks[operation.TaskID] = task
			}
		case "delete_task":
			delete(definition.Tasks, operation.TaskID)
			for id, task := range definition.Tasks {
				if len(task.Depends) == 0 {
					continue
				}
				kept := make([]string, 0, len(task.Depends))
				for _, dependency := range task.Depends {
					if dependency != operation.TaskID {
						kept = append(kept, dependency)
					}
				}
				task.Depends = kept
				definition.Tasks[id] = task
			}
		case "set_dependencies":
			task := definition.Tasks[operation.TaskID]
			task.Depends = append([]string(nil), operation.Depends...)
			definition.Tasks[operation.TaskID] = task
		case "update_graph":
			if operation.Name != nil {
				definition.Name = *operation.Name
			}
			if operation.Description != nil {
				definition.Description = *operation.Description
			}
		}
	}
}

func cloneDefinition(definition *domain.FlowDefinition) *domain.FlowDefinition {
	encoded, _ := json.Marshal(definition)
	var copy domain.FlowDefinition
	_ = json.Unmarshal(encoded, &copy)
	if copy.Tasks == nil {
		copy.Tasks = map[string]domain.FlowTask{}
	}
	return &copy
}

func (s *Service) appendMessage(ctx context.Context, graphID, hero, content string, created time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO graph_builder_messages(id,graph_id,ordinal,hero,content,created_at)
		VALUES(?,?,COALESCE((SELECT MAX(ordinal)+1 FROM graph_builder_messages WHERE graph_id=?),1),?,?,?)`,
		uuid.NewString(), graphID, graphID, hero, content, created.Format(time.RFC3339Nano))
	return err
}

func (s *Service) pendingProposal(ctx context.Context, graphID string) (*Proposal, error) {
	proposal := &Proposal{GraphID: graphID}
	var operationsJSON, diagnosticsJSON, created string
	err := s.DB.QueryRowContext(ctx, `SELECT id,base_digest,operations_json,summary,status,diagnostics_json,created_at
		FROM graph_builder_proposals WHERE graph_id=? AND status='pending' ORDER BY created_at DESC LIMIT 1`, graphID).
		Scan(&proposal.ID, &proposal.BaseDigest, &operationsJSON, &proposal.Summary, &proposal.Status, &diagnosticsJSON, &created)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(operationsJSON), &proposal.Operations)
	_ = json.Unmarshal([]byte(diagnosticsJSON), &proposal.Diagnostics)
	proposal.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return proposal, nil
}
