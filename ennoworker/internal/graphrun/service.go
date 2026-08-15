// Package graphrun starts file-native Graph Runs. At Run start it resolves the
// immutable Graph revision, resolves every Task's Role/Model/Skill, and
// freezes the complete execution plan (FlowDefinition + per-Task Role
// definitions) into the owning Session database before any Provider call.
// Execution never re-reads mutable Role/Graph files or the removed global
// agent_profile_versions / agent_flow_versions tables.
package graphrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// Service starts file-native Graph Runs against immutable file revisions.
type Service struct {
	Sources *globalsource.Store
	Models  *store.ModelRepo
	// Sessions opens the owning Session database.
	Sessions *sessionstore.Manager
	// GlobalSkills maps global Skill id -> available. nil-safe: an unresolved
	// global Skill fails the Run freeze loudly.
	GlobalSkills map[string]bool
	// OnRunStarted wires the per-Session orchestrator for a freshly frozen
	// flow run (enqueue child runs, events, recovery).
	OnRunStarted func(ctx context.Context, db *sql.DB, sessionPath, runID string) error
}

// FlowVersionID returns the portable immutable version ref for a Graph
// revision: "<graphID>@vNNNNNN".
func FlowVersionID(graphID, revisionID string) string {
	return graphID + "@" + revisionID
}

// Start freezes and starts one Graph Run. version 0 selects the latest
// published revision.
func (s *Service) Start(ctx context.Context, projectID, graphID string, version int, sessionID string,
	inputs, vars map[string]any) (*domain.RunAgentFlow, error) {
	if s.Sources == nil || s.Models == nil || s.Sessions == nil {
		return nil, fmt.Errorf("Graph runner is not configured")
	}
	session, err := s.Sessions.FindByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.ProjectID != projectID {
		return nil, fmt.Errorf("Session %s does not belong to Project %s", sessionID, projectID)
	}
	revisionID, err := s.resolveRevision(ctx, graphID, version)
	if err != nil {
		return nil, err
	}
	document, revision, err := s.Sources.ReadGraphRevision(graphID, revisionID)
	if err != nil {
		return nil, err
	}
	definition := definitionFromDocument(document)
	inputsJSON, err := freezeInputsJSON(inputs, vars)
	if err != nil {
		return nil, err
	}
	freeze, err := s.freezeTasks(ctx, document)
	if err != nil {
		return nil, err
	}
	definitionJSON, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	db, err := s.Sessions.OpenSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	runRepo := &store.AgentFlowRunRepo{DB: db}
	run, err := runRepo.CreateFlowRun(ctx, store.CreateFlowRunInput{
		SessionID: sessionID, ProjectID: projectID,
		FlowVersionID:  FlowVersionID(graphID, revisionID),
		DefinitionJSON: definitionJSON, ConfigDigest: revision.Digest,
		InputsJSON: inputsJSON,
	}, freeze)
	if err != nil {
		return nil, err
	}
	if s.OnRunStarted != nil {
		sessionPath, pathErr := s.Sessions.SessionPath(sessionID)
		if pathErr != nil {
			return nil, pathErr
		}
		if err := s.OnRunStarted(ctx, db, sessionPath, run.RunID); err != nil {
			return nil, err
		}
	}
	return run, nil
}

// RecoverSession resumes every non-terminal flow Run of a Session after a
// Worker restart. It is idempotent; the orchestrator reconciles crashed nodes.
func (s *Service) RecoverSession(ctx context.Context, sessionID string) error {
	if s.Sessions == nil || s.OnRunStarted == nil {
		return nil
	}
	db, err := s.Sessions.OpenSession(ctx, sessionID)
	if err != nil {
		return err
	}
	ids, err := (&store.AgentFlowRunRepo{DB: db}).ListRecoverableRuns(ctx)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	sessionPath, err := s.Sessions.SessionPath(sessionID)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		if err := s.OnRunStarted(ctx, db, sessionPath, runID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resolveRevision(ctx context.Context, graphID string, version int) (string, error) {
	revisions, err := s.Sources.ListGraphRevisions(graphID)
	if err != nil {
		return "", err
	}
	if len(revisions) == 0 {
		return "", fmt.Errorf("Graph %q has no published revision", graphID)
	}
	if version > 0 {
		for _, revision := range revisions {
			if revision.Version == version {
				return revision.ID(), nil
			}
		}
		return "", fmt.Errorf("Graph %q has no revision v%06d", graphID, version)
	}
	return revisions[len(revisions)-1].ID(), nil
}

// definitionFromDocument converts the canonical graphsource Document to the
// flow execution contract. Every Task is a role task: model-backed Tasks get
// a synthetic inline Role resolved at freeze time.
func definitionFromDocument(document *graphsource.Document) *domain.FlowDefinition {
	definition := &domain.FlowDefinition{
		SchemaVersion: domain.FlowSchemaVersion, ID: document.ID,
		Description: document.Description,
		Budget:      domain.FlowBudget{MaxTotalTokens: int64(len(document.Tasks)) * 20000},
		Tasks:       make(map[string]domain.FlowTask, len(document.Tasks)),
	}
	taskIDs := make([]string, 0, len(document.Tasks))
	for taskID := range document.Tasks {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	for _, taskID := range taskIDs {
		task := document.Tasks[taskID]
		definition.Tasks[taskID] = domain.FlowTask{
			Type:    domain.FlowTaskRole,
			Role:    task.Role,
			Goal:    task.Goal,
			Depends: append([]string(nil), document.Graph[taskID]...),
			Writes:  append([]string(nil), task.Writes...),
			Budget:  flowTaskBudget(task.Budget),
		}
	}
	return definition
}

func flowTaskBudget(budget *graphsource.TaskBudget) *domain.FlowTaskBudget {
	if budget == nil || budget.Tokens < 1 {
		return nil
	}
	return &domain.FlowTaskBudget{Tokens: budget.Tokens}
}

// freezeInputsJSON wraps run inputs + vars into the frozen payload. Graph
// documents declare no typed input ports (the graphsource format has none), so
// every supplied input is accepted and validated only at goal-template
// resolution time.
func freezeInputsJSON(inputs, vars map[string]any) (json.RawMessage, error) {
	normalized := struct {
		Inputs map[string]any `json:"inputs"`
		Vars   map[string]any `json:"vars"`
	}{}
	if inputs != nil {
		normalized.Inputs = inputs
	} else {
		normalized.Inputs = map[string]any{}
	}
	if vars != nil {
		normalized.Vars = vars
	} else {
		normalized.Vars = map[string]any{}
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// frozenTaskRole is the per-Task frozen Role identity + definition captured at
// Run start.
type frozenTaskRole struct {
	ObjectID     string
	VersionID    string
	Handle       string
	DisplayName  string
	ConfigDigest string
	Definition   domain.RoleDefinition
}

// freezeTasks resolves every Task into its frozen node snapshot in topological
// order. Any failure aborts the whole Run (fail-closed; nothing is persisted).
func (s *Service) freezeTasks(ctx context.Context, document *graphsource.Document) ([]store.FlowNodeFreeze, error) {
	order, err := agentflow.TopologicalOrder(definitionFromDocument(document).Tasks)
	if err != nil {
		return nil, err
	}
	indexOf := make(map[string]int, len(order))
	for i, name := range order {
		indexOf[name] = i
	}
	freeze := make([]store.FlowNodeFreeze, 0, len(order))
	for _, name := range order {
		task := document.Tasks[name]
		role, globalSkills, err := s.resolveTaskRole(ctx, document, name, task)
		if err != nil {
			return nil, fmt.Errorf("Task %q: %w", name, err)
		}
		switch role.Definition.DelegationPolicy.Admission {
		case domain.DelegationAutoWithinBudget:
		case domain.DelegationDenied:
			return nil, fmt.Errorf("Task %q: Role %s denies Host delegation", name, role.VersionID)
		case domain.DelegationApprovalRequired:
			return nil, fmt.Errorf("Task %q: Role %s requires explicit delegation approval; Graph Runs only admit auto_within_budget Roles", name, role.VersionID)
		default:
			return nil, fmt.Errorf("Task %q: Role %s has an invalid admission policy", name, role.VersionID)
		}
		ceiling := role.Definition.DelegationPolicy.BudgetCeiling
		if task.Budget != nil && task.Budget.Tokens > 0 {
			if task.Budget.Tokens > ceiling.MaxTotalTokens {
				return nil, fmt.Errorf("Task %q: budget tokens %d exceed the Role ceiling %d", name, task.Budget.Tokens, ceiling.MaxTotalTokens)
			}
			ceiling.MaxTotalTokens = task.Budget.Tokens
		}
		budgetJSON, _ := json.Marshal(domain.BudgetCeilingJSON{
			MaxModelCalls: ceiling.MaxModelCalls, MaxToolCalls: ceiling.MaxToolCalls,
			MaxTotalTokens: ceiling.MaxTotalTokens, MaxOutputTokens: ceiling.MaxOutputTokens,
			MaxCostMicros: ceiling.MaxCostUSDMicros, MaxWallTimeMS: ceiling.MaxWallTimeMS,
		})
		roleDefJSON, _ := json.Marshal(role.Definition)
		freeze = append(freeze, store.FlowNodeFreeze{
			TaskIndex: indexOf[name], Handle: name,
			RoleVersionID: role.VersionID, SkillIDs: globalSkills,
			GoalDigest: agentflow.TaskGoalDigest(task.Goal), GoalText: task.Goal,
			BudgetJSON: budgetJSON, ReadOnly: roleDefinitionIsReadOnly(role.Definition),
			Writes:             append([]string(nil), task.Writes...),
			RoleDefinitionJSON: roleDefJSON,
		})
	}
	return freeze, nil
}

func roleDefinitionIsReadOnly(roleDef domain.RoleDefinition) bool {
	if roleDef.Authority != domain.RoleAuthorityReadOnly {
		return false
	}
	for _, tool := range roleDef.AllowedTools {
		if !toolReadOnlyKind(tool) {
			return false
		}
	}
	return true
}

var readOnlyTools = map[string]bool{
	"read": true, "ls": true, "grep": true, "find": true,
	"bash_read": true, "sql_read": true,
}

func toolReadOnlyKind(tool string) bool {
	if readOnlyTools[tool] {
		return true
	}
	if strings.HasPrefix(tool, "mcp__") && !strings.Contains(tool, "__write__") {
		return true
	}
	return false
}

// resolveTaskRole resolves one Task's Role facts at Run start:
//   - "global/<id>"  -> the latest published immutable Role revision;
//   - "local/<id>"   -> the Graph-private Role document (digest-versioned);
//   - ""             -> a synthetic inline Role bound to the Task's model.
//
// Global Task Skills are validated and returned as additive preload ids;
// local Task Skills are inlined into the Role prompt and tool allowlist.
func (s *Service) resolveTaskRole(ctx context.Context, document *graphsource.Document,
	taskID string, task graphsource.Task) (*frozenTaskRole, []string, error) {
	if task.Role != "" {
		scope, id, _ := strings.Cut(task.Role, "/")
		switch scope {
		case "global":
			roleDoc, revision, err := s.Sources.LatestRoleRevision(id)
			if err != nil {
				return nil, nil, fmt.Errorf("global Role %q: %w", id, err)
			}
			definition, diagnostics := (&store.RoleDiscovery{Models: s.Models}).ResolveDocument(ctx, roleDoc)
			if definition == nil {
				return nil, nil, fmt.Errorf("global Role %q: %s", id, roleDiagnostic(diagnostics))
			}
			return &frozenTaskRole{
				ObjectID: id, VersionID: id + "@" + revision.ID(),
				Handle: roleDoc.Handle, DisplayName: roleDoc.Name,
				ConfigDigest: revision.Digest, Definition: *definition,
			}, nil, nil
		case "local":
			roleDoc, digest, err := s.Sources.ReadGraphRole(document.ID, id)
			if err != nil {
				return nil, nil, fmt.Errorf("local Role %q: %w", id, err)
			}
			definition, err := s.resolveLocalRole(ctx, document.ID, roleDoc)
			if err != nil {
				return nil, nil, fmt.Errorf("local Role %q: %w", id, err)
			}
			objectID := "graph:" + document.ID + "/" + id
			return &frozenTaskRole{
				ObjectID: objectID, VersionID: objectID + "@" + digest,
				Handle: id, DisplayName: roleDoc.Name,
				ConfigDigest: digest, Definition: *definition,
			}, nil, nil
		default:
			return nil, nil, fmt.Errorf("unsupported Role reference %q", task.Role)
		}
	}
	// Inline model-backed Task.
	model, err := s.Models.ResolvePortableRef(ctx, task.Model)
	if err != nil {
		return nil, nil, fmt.Errorf("model %q: %w", task.Model, err)
	}
	thinking := task.Thinking
	if thinking == "" {
		thinking = domain.ThinkingDefault
	}
	prompt := inlineTaskPrompt
	allowedTools := append([]string(nil), inlineBaseTools...)
	globalSkills := make([]string, 0, len(task.Skills))
	for _, ref := range task.Skills {
		scope, id, _ := strings.Cut(ref, "/")
		switch scope {
		case "global":
			if s.GlobalSkills == nil || !s.GlobalSkills[id] {
				return nil, nil, fmt.Errorf("global Skill %q is not available", id)
			}
			globalSkills = append(globalSkills, id)
		case "local":
			loaded, loadErr := s.loadLocalSkill(document.ID, id)
			if loadErr != nil {
				return nil, nil, fmt.Errorf("local Skill %q: %w", id, loadErr)
			}
			prompt += "\n\n<graph_local_skill id=\"" + id + "\" digest=\"" + loaded.ContentHash + "\">\n" + loaded.PromptText + "\n</graph_local_skill>"
			allowedTools = append(allowedTools, loaded.Manifest.AllowedTools...)
		default:
			return nil, nil, fmt.Errorf("unsupported Skill reference %q", ref)
		}
	}
	definition := inlineRole(model.ID, thinking, prompt, allowedTools)
	definitionJSON, _ := json.Marshal(definition)
	digest, _ := store.DigestJSON(definitionJSON)
	objectID := "inline:" + taskID
	handle := inlineHandle(taskID)
	return &frozenTaskRole{
		ObjectID: objectID, VersionID: objectID + "@" + digest,
		Handle: handle, DisplayName: task.Name,
		ConfigDigest: digest, Definition: definition,
	}, globalSkills, nil
}

// resolveLocalRole resolves a Graph-private Role document, inlining local
// Skills into the prompt and tool allowlist (global Skills keep their catalog
// binding).
func (s *Service) resolveLocalRole(ctx context.Context, graphID string, document *rolesource.Document) (*domain.RoleDefinition, error) {
	encoded, _ := json.Marshal(document)
	var copy rolesource.Document
	_ = json.Unmarshal(encoded, &copy)
	copy.Skills = nil
	for _, binding := range document.Skills {
		scope, id, scoped := strings.Cut(binding.ID, "/")
		if !scoped || scope == "global" {
			if scoped {
				binding.ID = id
			}
			copy.Skills = append(copy.Skills, binding)
			continue
		}
		if scope != "local" {
			return nil, fmt.Errorf("Skill %q must use local/ or global/", binding.ID)
		}
		loaded, err := s.loadLocalSkill(graphID, id)
		if err != nil {
			return nil, err
		}
		copy.Prompt += "\n\n<graph_local_skill id=\"" + id + "\" digest=\"" + loaded.ContentHash + "\">\n" + loaded.PromptText + "\n</graph_local_skill>"
		copy.AllowedTools = append(copy.AllowedTools, loaded.Manifest.AllowedTools...)
	}
	definition, diagnostics := (&store.RoleDiscovery{Models: s.Models}).ResolveDocument(ctx, &copy)
	if definition == nil {
		return nil, fmt.Errorf("private Role is invalid: %s", roleDiagnostic(diagnostics))
	}
	return definition, nil
}

func (s *Service) loadLocalSkill(graphID, id string) (*skills.LoadedSkill, error) {
	directory, err := s.Sources.GraphResourceDir(graphID, "skills", id)
	if err != nil {
		return nil, err
	}
	return skills.Load(directory)
}

func roleDiagnostic(diagnostics []domain.RoleValidationDiagnostic) string {
	if len(diagnostics) == 0 {
		return "invalid Role"
	}
	return diagnostics[0].Message
}

const inlineTaskPrompt = "Execute the Task goal independently. Use the provided inputs and dependency outputs, distinguish evidence from assumptions, and submit a concise structured result."

var inlineBaseTools = []string{"read", "write", "edit", "ls", "grep", "find", "bash"}

func inlineHandle(taskID string) string {
	if len(taskID) > 23 {
		taskID = taskID[:23]
	}
	return "inline_" + taskID
}

// inlineRole synthesizes the default Role for a model-backed Task. Unlike the
// legacy publish path it allows task_only context so delegated children can
// execute it.
func inlineRole(modelID string, thinking domain.ThinkingEffort, prompt string, tools []string) domain.RoleDefinition {
	if thinking == "" {
		thinking = domain.ThinkingDefault
	}
	return domain.RoleDefinition{
		SchemaVersion: 1, RolePrompt: prompt,
		ModelBinding: domain.RoleModelBinding{Mode: domain.RoleModelFixed, ModelProfileID: modelID, ThinkingEffort: thinking, FallbackModelProfileIDs: []string{}, OverridableFields: []string{}},
		Skills:       domain.RoleSkills{Entries: []domain.RoleSkillEntry{}}, Authority: domain.RoleAuthorityMutation,
		PermissionCeiling: domain.PermissionAsk, AllowedTools: unique(tools),
		ContextPolicy:    domain.RoleContextPolicy{DefaultMode: domain.RoleContextTask, AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}},
		DelegationPolicy: domain.RoleDelegationPolicy{Admission: domain.DelegationAutoWithinBudget, AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"}, MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16, BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 16, MaxTotalTokens: 20000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
		OutputContract:   "text-v1", MaxLoopIterations: 8,
	}
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
