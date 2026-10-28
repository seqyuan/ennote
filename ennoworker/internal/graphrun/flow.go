package graphrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// StartFlow freezes and starts one full Agent Flow from the Project's pinned
// binding revision. Publishing and enabling are separate: an unpublished,
// unbound, disabled, or mismatched binding is rejected before any child Run.
func (s *Service) StartFlow(ctx context.Context, projectID, flowID string, version int, sessionID string,
	inputs, vars map[string]any) (*domain.RunAgentFlow, error) {
	if s.Sources == nil || s.Models == nil || s.Sessions == nil {
		return nil, fmt.Errorf("Flow runner is not configured")
	}
	session, err := s.Sessions.FindByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session.ProjectID != projectID {
		return nil, fmt.Errorf("Session %s does not belong to Project %s", sessionID, projectID)
	}
	revisionID, err := s.flowBindingRevision(ctx, projectID, flowID, version)
	if err != nil {
		return nil, err
	}
	definition, roles, revision, err := s.Sources.ReadFlowRevision(flowID, revisionID)
	if err != nil {
		return nil, err
	}
	inputsJSON, err := store.NormalizeFlowInputs(definition, inputs, vars)
	if err != nil {
		return nil, err
	}
	freeze, err := s.freezeFlow(ctx, flowID, definition, roles)
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
		FlowVersionID:  FlowVersionID(flowID, revisionID),
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

// flowBindingRevision resolves the exact revision a Project enabled. A missing
// or disabled binding is a hard error: flow Runs never fall back to "latest".
func (s *Service) flowBindingRevision(ctx context.Context, projectID, flowID string, version int) (string, error) {
	if s.Projects == nil {
		return "", fmt.Errorf("flow_binding_unavailable: project bindings are not configured")
	}
	binding, err := s.Projects.FindGraphBinding(ctx, projectID, projectstore.SourceKindFlow, flowID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("flow_binding_required: flow %q is not enabled in this Project", flowID)
		}
		return "", err
	}
	if !binding.Enabled {
		return "", fmt.Errorf("flow_binding_disabled: flow %q is disabled in this Project", flowID)
	}
	if binding.Revision == "" {
		return "", fmt.Errorf("flow_binding_revision_missing: flow %q has no pinned revision", flowID)
	}
	if version > 0 {
		pinned, err := revisionVersion(binding.Revision)
		if err != nil || pinned != version {
			return "", fmt.Errorf("flow_binding_revision_mismatch: binding pins %s, requested v%06d", binding.Revision, version)
		}
	}
	return binding.Revision, nil
}

func revisionVersion(revisionID string) (int, error) {
	if !strings.HasPrefix(revisionID, "v") {
		return 0, fmt.Errorf("invalid revision id %q", revisionID)
	}
	return strconv.Atoi(strings.TrimPrefix(revisionID, "v"))
}

// freezeFlow snapshots every task, including check and terminal gates. The
// graph freeze path (freezeTasks) is deliberately unchanged; this is the same
// run_agent_flow_nodes contract with full-flow task types.
func (s *Service) freezeFlow(ctx context.Context, flowID string, definition *domain.FlowDefinition,
	bundledRoles map[string]*rolesource.Document) ([]store.FlowNodeFreeze, error) {
	order, err := agentflow.TaskOrder(definition)
	if err != nil {
		return nil, err
	}
	indexOf := make(map[string]int, len(order))
	for index, name := range order {
		indexOf[name] = index
	}
	skillCatalog := s.skillDigests()
	freeze := make([]store.FlowNodeFreeze, 0, len(order))
	emptyBudget, _ := json.Marshal(domain.BudgetCeilingJSON{})
	for _, name := range order {
		task := definition.Tasks[name]
		node := store.FlowNodeFreeze{
			TaskIndex: indexOf[name], Handle: name,
			GoalDigest: agentflow.TaskGoalDigest(task.Goal), GoalText: task.Goal,
			BudgetJSON: emptyBudget,
			Writes:     append([]string(nil), task.Writes...),
		}
		if task.Terminal != nil {
			node.ReadOnly = true
			node.Writes = nil
			freeze = append(freeze, node)
			continue
		}
		if task.Type == domain.FlowTaskCheck {
			if strings.TrimSpace(task.Command) == "" {
				return nil, fmt.Errorf("Task %q: check command is required", name)
			}
			node.ReadOnly = false
			freeze = append(freeze, node)
			continue
		}
		if task.Type != domain.FlowTaskRole {
			return nil, fmt.Errorf("Task %q: unsupported type %q", name, task.Type)
		}
		role, globalSkills, err := s.freezeFlowRole(ctx, flowID, name, task, bundledRoles, skillCatalog)
		if err != nil {
			return nil, fmt.Errorf("Task %q: %w", name, err)
		}
		readOnly := domain.RoleDefinitionIsReadOnly(role.Definition)
		if task.FanOut != nil && !readOnly {
			return nil, fmt.Errorf("flow_fan_out_not_read_only: Task %q fan_out requires a fully read-only Role", name)
		}
		frozenSkills, err := domain.NormalizeFrozenSkills(globalSkills)
		if err != nil {
			return nil, fmt.Errorf("Task %q: %w", name, err)
		}
		switch role.Definition.DelegationPolicy.Admission {
		case domain.DelegationAutoWithinBudget:
		case domain.DelegationDenied:
			return nil, fmt.Errorf("Task %q: Role %s denies Host delegation", name, role.VersionID)
		case domain.DelegationApprovalRequired:
			return nil, fmt.Errorf("Task %q: Role %s requires explicit delegation approval; Flow Runs only admit auto_within_budget Roles", name, role.VersionID)
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
		node.RoleVersionID = role.VersionID
		node.Skills = frozenSkills
		node.BudgetJSON = budgetJSON
		node.ReadOnly = readOnly
		node.RoleDefinitionJSON = roleDefJSON
		freeze = append(freeze, node)
	}
	return freeze, nil
}

func (s *Service) freezeFlowRole(ctx context.Context, flowID, taskID string, task domain.FlowTask,
	bundled map[string]*rolesource.Document, skillCatalog map[string]string) (*frozenTaskRole, domain.FrozenSkillRefs, error) {
	ref := strings.TrimSpace(task.Role)
	if ref == "" {
		return nil, nil, fmt.Errorf("role is required")
	}
	handle, versionText, hasVersion := strings.Cut(ref, "@")
	handle = strings.TrimSpace(handle)
	var document *rolesource.Document
	var versionID, digest string
	switch {
	case strings.HasPrefix(handle, "local/"):
		if hasVersion {
			return nil, nil, fmt.Errorf("flow_local_role_version_unsupported: Role %q cannot be version-pinned", ref)
		}
		id := strings.TrimSpace(strings.TrimPrefix(handle, "local/"))
		bundledDocument, ok := bundled[id]
		if !ok {
			return nil, nil, fmt.Errorf("flow-local Role %q is not present in the published revision bundle", id)
		}
		document = bundledDocument
		digest, _ = rolesource.SourceDigest(document)
		versionID = "flow:" + flowID + "/" + id + "@" + digest
	case strings.HasPrefix(handle, "global/"):
		id := strings.TrimSpace(strings.TrimPrefix(handle, "global/"))
		doc, revision, err := s.flowGlobalRole(id, versionText, hasVersion)
		if err != nil {
			return nil, nil, err
		}
		document, versionID, digest = doc, id+"@"+revision.ID(), revision.Digest
	default:
		if !hasVersion {
			if bundledDocument, ok := bundled[handle]; ok {
				document = bundledDocument
				digest, _ = rolesource.SourceDigest(document)
				versionID = "flow:" + flowID + "/" + handle + "@" + digest
				break
			}
		}
		doc, revision, err := s.flowGlobalRole(handle, versionText, hasVersion)
		if err != nil {
			return nil, nil, err
		}
		document, versionID, digest = doc, handle+"@"+revision.ID(), revision.Digest
	}
	definition, err := s.resolveLocalRole(ctx, flowID, document)
	if err != nil {
		return nil, nil, fmt.Errorf("Role %q: %w", handle, err)
	}
	globalSkills, err := s.applyFlowTaskSkills(ctx, flowID, definition, task.Skills, skillCatalog)
	if err != nil {
		return nil, nil, err
	}
	return &frozenTaskRole{
		ObjectID: versionID, VersionID: versionID,
		Handle: document.Handle, DisplayName: document.Name,
		ConfigDigest: digest, Definition: *definition,
	}, globalSkills, nil
}

func (s *Service) flowGlobalRole(handle, versionText string, hasVersion bool) (*rolesource.Document, *globalsource.Revision, error) {
	if !hasVersion {
		document, revision, err := s.Sources.LatestRoleRevision(handle)
		if err != nil {
			return nil, nil, fmt.Errorf("global Role %q: %w", handle, err)
		}
		return document, revision, nil
	}
	versionText = strings.TrimSpace(versionText)
	revisions, err := s.Sources.ListRoleRevisions(handle)
	if err != nil {
		return nil, nil, err
	}
	for _, revision := range revisions {
		if revision.ID() == versionText || strconv.Itoa(revision.Version) == versionText {
			document, read, err := s.Sources.ReadRoleRevision(handle, revision.ID())
			if err != nil {
				return nil, nil, err
			}
			return document, read, nil
		}
	}
	return nil, nil, fmt.Errorf("global Role %q has no published version %q", handle, versionText)
}

// applyFlowTaskSkills inlines local Skills (never widening the Role's tool
// allowlist) and returns the content-pinned global Skill refs.
func (s *Service) applyFlowTaskSkills(ctx context.Context, flowID string, definition *domain.RoleDefinition,
	refs []string, skillCatalog map[string]string) (domain.FrozenSkillRefs, error) {
	globalSkills := make(domain.FrozenSkillRefs, 0, len(refs))
	for _, ref := range refs {
		scope, id, scoped := strings.Cut(strings.TrimSpace(ref), "/")
		if !scoped {
			// Bare refs resolve flow-local first, exactly like task Roles.
			if loaded, err := s.loadLocalSkill(flowID, id); err == nil {
				definition.RolePrompt += "\n\n<graph_local_skill id=\"" + id + "\" digest=\"" + loaded.ContentHash + "\">\n" + loaded.PromptText + "\n</graph_local_skill>"
				continue
			}
			scope, id = "global", ref
		}
		switch scope {
		case "local":
			loaded, err := s.loadLocalSkill(flowID, id)
			if err != nil {
				return nil, fmt.Errorf("local Skill %q: %w", id, err)
			}
			definition.RolePrompt += "\n\n<graph_local_skill id=\"" + id + "\" digest=\"" + loaded.ContentHash + "\">\n" + loaded.PromptText + "\n</graph_local_skill>"
		case "global":
			digest, ok := skillCatalog[id]
			if !ok || digest == "" {
				return nil, fmt.Errorf("global Skill %q is not available", id)
			}
			globalSkills = append(globalSkills, domain.FrozenSkillRef{ID: id, Digest: digest})
		default:
			return nil, fmt.Errorf("unsupported Skill reference %q", ref)
		}
	}
	definition.AllowedTools = sortedUniqueStrings(definition.AllowedTools)
	return globalSkills, nil
}

func sortedUniqueStrings(values []string) []string {
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
