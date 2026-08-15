package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/require"
)

// setupFlowFixture seeds a Project + published file Role (globalsource) for
// the orchestrator tests. Flow versions are in-memory (flowVersionFromDef);
// the freeze helper resolves the Role from the file source + model catalog.
func setupFlowFixture(t *testing.T) (*sql.DB, *store.AgentFlowRunRepo, string, string, string) {
	t.Helper()
	db := store.SetupDB(t)
	ctx := context.Background()
	projects := newFileProjects(t)
	project, _, err := projects.CreateWithWorkspace(ctx,
		domain.CreateProjectInput{Name: "Flows", HostPath: t.TempDir()})
	require.NoError(t, err)
	stack := newFileConfigStack(t)
	model, err := stack.Models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "flow-model", ContextWindow: 32000, MaxOutputTokens: 2048,
		SupportsToolUse: true, SupportsThinking: true,
		ThinkingDialect:          domain.ThinkingDialectOpenAIReasoningEffort,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium},
	})
	require.NoError(t, err)
	sources := stack.Sources
	document := &rolesource.Document{
		SchemaVersion: 1, Handle: "flow-worker", Name: "Flow Worker",
		Description: "Flow task worker", Positioning: "Executes one task in a flow.",
		Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionDiscuss, AllowedTools: []string{"read", "grep"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextRoom,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextRoom}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single", "parallel"},
			MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
				MaxTotalTokens: 200000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 1_800_000}},
		OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Execute one task in a flow.",
	}
	_, _, err = sources.CreateRole(document)
	require.NoError(t, err)
	_, err = sources.PublishRoleRevision(document.Handle)
	require.NoError(t, err)
	testFlowSources, testFlowModels = sources, stack.ModelRepo
	return db,
		&store.AgentFlowRunRepo{DB: db, SkillCatalog: map[string]string{"go-dev": "skill-go-dev"}},
		project.ID, model.ID, "flow-worker@v000001"
}

// flowVersionFromDef builds an in-memory immutable flow version. The
// orchestrator never reads it after freeze: CreateFlowRun stores the full
// definition JSON and the config digest in the Session database, and
// OrchestratorStore.GetVersion resolves from the frozen row.
func flowVersionFromDef(def *domain.FlowDefinition) *domain.AgentFlowVersion {
	digest, err := agentflow.ConfigDigest(def)
	if err != nil {
		panic(err)
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		panic(err)
	}
	return &domain.AgentFlowVersion{
		ID: def.ID + "@v000001", ProfileID: def.ID, Version: 1,
		ConfigDigest: digest, DefinitionJSON: encoded,
	}
}

func flowYAML(id string, extra string) string {
	return `schemaVersion: 1
id: ` + id + `
description: test flow
inputs:
  target: {type: path, required: true}
outputs:
  report: {type: string}
budget:
  max_total_tokens: 200000
tasks:
  producer:
    role: flow-worker@1
    skills: [go-dev]
    goal: "Implement {inputs.target}"
    budget: {tokens: 50000}
  reviewer:
    role: flow-worker@1
    goal: "Review {task.producer.output.changed_files}"
    depends: [producer]
  accept:
    terminal: {status: success, output: report}
    output: report
    depends: [reviewer]
` + extra
}

// freezeFlowForTest freezes a flow definition into per-task node snapshots,
// mirroring the removed store.FreezeFlowDefinition (SQL flow freeze). It
// resolves fixture-published roles via the snapshot role tables and carries
// the full RoleDefinitionJSON so child materialization freezes RoleMeta. The
// fixture snapshot is test-only; production freezes via graphrun.Service.
func freezeFlowForTest(t *testing.T, db *sql.DB, flowRuns *store.AgentFlowRunRepo,
	projectID, flowID string, def *domain.FlowDefinition,
	inputsJSON json.RawMessage) ([]store.FlowNodeFreeze, []string, error) {
	t.Helper()
	_ = projectID
	_ = flowID
	_ = inputsJSON
	var diagnostics []string
	add := func(format string, args ...any) {
		diagnostics = append(diagnostics, fmt.Sprintf(format, args...))
	}
	order, err := agentflow.TopologicalOrder(def.Tasks)
	if err != nil {
		return nil, nil, err
	}
	indexOf := make(map[string]int, len(order))
	for i, name := range order {
		indexOf[name] = i
	}
	freeze := make([]store.FlowNodeFreeze, 0, len(order))
	for _, name := range order {
		task := def.Tasks[name]
		node := store.FlowNodeFreeze{TaskIndex: indexOf[name], Handle: name,
			GoalDigest: agentflow.TaskGoalDigest(task.Goal)}
		if task.Terminal != nil || task.Type == domain.FlowTaskCheck {
			// Terminal/check gates complete the flow; they freeze no Role binding.
			node.BudgetJSON = json.RawMessage(`{}`)
			freeze = append(freeze, node)
			continue
		}
		if task.Type != domain.FlowTaskRole {
			add("task %q has unsupported type %q", name, task.Type)
			continue
		}
		versionID, definitionJSON, err := resolveFlowRoleForTest(t, db, task.Role)
		if err != nil {
			add("task %q: %v", name, err)
			continue
		}
		var roleDef domain.RoleDefinition
		if err := json.Unmarshal(definitionJSON, &roleDef); err != nil {
			add("task %q: decode Role definition: %v", name, err)
			continue
		}
		if roleDef.DelegationPolicy.Admission != domain.DelegationAutoWithinBudget {
			add("task %q: Role %s is not auto_within_budget", name, task.Role)
			continue
		}
		skillIDs := make([]string, 0, len(task.Skills))
		for _, skillName := range task.Skills {
			id, ok := flowRuns.SkillCatalog[skillName]
			if !ok {
				add("task %q: skill %q is not in the catalog", name, skillName)
				continue
			}
			skillIDs = append(skillIDs, id)
		}
		ceiling := roleDef.DelegationPolicy.BudgetCeiling
		if task.Budget != nil && task.Budget.Tokens > 0 {
			if task.Budget.Tokens > ceiling.MaxTotalTokens {
				add("task %q: budget tokens %d exceed the Role ceiling %d", name, task.Budget.Tokens, ceiling.MaxTotalTokens)
				continue
			}
			ceiling.MaxTotalTokens = task.Budget.Tokens
		}
		budgetJSON, _ := json.Marshal(domain.BudgetCeilingJSON{
			MaxModelCalls: ceiling.MaxModelCalls, MaxToolCalls: ceiling.MaxToolCalls,
			MaxTotalTokens: ceiling.MaxTotalTokens, MaxOutputTokens: ceiling.MaxOutputTokens,
			MaxCostMicros: ceiling.MaxCostUSDMicros, MaxWallTimeMS: ceiling.MaxWallTimeMS,
		})
		roleDefJSON, _ := json.Marshal(roleDef)
		node.RoleVersionID = versionID
		node.SkillIDs = skillIDs
		node.BudgetJSON = budgetJSON
		node.ReadOnly = testRoleDefinitionIsReadOnly(roleDef)
		node.Writes = append([]string(nil), task.Writes...)
		node.RoleDefinitionJSON = roleDefJSON
		freeze = append(freeze, node)
	}
	if len(diagnostics) > 0 {
		return nil, diagnostics, fmt.Errorf("flow freeze failed: %s", diagnostics[0])
	}
	return freeze, nil, nil
}

// testFlowSources/testFlowModels carry the file Role source + model catalog
// for the current flow freeze (set by the flow fixtures; tests run serially).
var testFlowSources *globalsource.Store
var testFlowModels *store.ModelRepo

// resolveFlowRoleForTest resolves a file Role reference (handle@version) to
// its published file revision id + resolved definition JSON (V2).
func resolveFlowRoleForTest(t *testing.T, _ *sql.DB, roleRef string) (string, json.RawMessage, error) {
	t.Helper()
	handle, _, _ := strings.Cut(strings.TrimSpace(roleRef), "@")
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return "", nil, fmt.Errorf("role reference %q has an empty handle", roleRef)
	}
	if testFlowSources == nil || testFlowModels == nil {
		return "", nil, fmt.Errorf("flow Role source is unavailable")
	}
	document, revision, err := testFlowSources.ReadRoleRevision(handle, "v000001")
	if err != nil {
		return "", nil, fmt.Errorf("role %q is not published: %w", handle, err)
	}
	definition, diagnostics := (&store.RoleDiscovery{Models: testFlowModels}).ResolveDocument(context.Background(), document)
	if definition == nil {
		if len(diagnostics) > 0 {
			return "", nil, fmt.Errorf("resolve role %q: %s", handle, diagnostics[0].Message)
		}
		return "", nil, fmt.Errorf("resolve role %q", handle)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		return "", nil, err
	}
	return handle + "@" + revision.ID(), encoded, nil
}

// testRoleDefinitionIsReadOnly mirrors the production roleDefinitionIsReadOnly
// classification for the fixture-published roles.
func testRoleDefinitionIsReadOnly(roleDef domain.RoleDefinition) bool {
	if roleDef.Authority != domain.RoleAuthorityReadOnly {
		return false
	}
	for _, tool := range roleDef.AllowedTools {
		if !testToolReadOnly(tool) {
			return false
		}
	}
	return true
}

func testToolReadOnly(tool string) bool {
	switch tool {
	case "read", "ls", "grep", "find", "git_readonly", "search_compacted_history", "todo":
		return true
	default:
		return false
	}
}
