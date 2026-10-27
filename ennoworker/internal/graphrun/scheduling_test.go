package graphrun_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schedulingToolMatrix is the representative classification matrix shared with
// the domain and store tests: every entry must resolve to the same answer on
// every scheduling path.
var schedulingToolMatrix = []struct {
	tool     string
	readOnly bool
}{
	{"read", true},
	{"ls", true},
	{"grep", true},
	{"find", true},
	{"git_readonly", true},
	{"search_compacted_history", true},
	{"todo", true},
	{"ask_user", true},
	{"bash", false},
	{"exec", false},
	{"write", false},
	{"edit", false},
	{"publish_artifact", false},
	{"web_fetch", false},
	{"http", false},
	{"delegate_tasks", false},
	{"mcp__server__read", false},
	{"mcp__server__write", false},
	{"unknown_tool", false},
}

// TestServiceFreezeMatchesSharedReadOnlyClassifier proves the Graph Run freeze
// class -- the ReadOnly flag the scheduler uses for concurrency and writer
// scope -- is computed from the same conservative domain allowlist as the Agent
// Flow publish resolver. Before unification graphrun silently treated any
// mcp__* name without "__write__" as read-only, so a mutating MCP tool could be
// granted fan-out parallelism.
func TestServiceFreezeMatchesSharedReadOnlyClassifier(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	models := fileconfig.NewModelStore(
		filepath.Join(home, "config", "models.json"),
		filepath.Join(home, "config", "provider-auth.json"),
		filepath.Join(home, "config", "settings.json"),
	)
	_, err := models.CreateProvider(ctx, fileconfig.CreateProviderInput{
		Key: "provider", Name: "Provider", ProviderType: domain.ProviderOpenAICompatible,
		BaseURL: "https://provider.test/v1", APIKey: "sk-sched-secret",
	})
	require.NoError(t, err)
	model, err := models.CreateModel(ctx, fileconfig.CreateModelInput{
		ProviderID: "provider", ModelName: "sched-model", ContextWindow: 32768, MaxOutputTokens: 4096,
		SupportsToolUse: true, ThinkingDialect: domain.ThinkingDialectNone,
		SupportedThinkingEfforts: []domain.ThinkingEffort{domain.ThinkingDefault}, IsDefault: true,
	})
	require.NoError(t, err)

	sources := &globalsource.Store{HomeDir: home}
	_, _, err = sources.CreateGraph("pipeline", "Pipeline")
	require.NoError(t, err)

	// One read_only Role per matrix row, each allowed exactly {read, <tool>}.
	tasks := make(map[string]graphsource.Task, len(schedulingToolMatrix))
	deps := make(map[string][]string, len(schedulingToolMatrix))
	for index, tc := range schedulingToolMatrix {
		handle := fmt.Sprintf("sched_%02d", index)
		allowed := []string{"read"}
		if tc.tool != "read" {
			allowed = append(allowed, tc.tool)
		}
		_, _, err = sources.CreateRole(&rolesource.Document{
			SchemaVersion: 1, Handle: handle, Name: handle,
			Description: "Scheduling matrix role", Positioning: "Scheduling matrix.",
			Icon: "bot", Color: "neutral",
			Model:  rolesource.ModelBinding{Ref: model.ID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
			Skills: []rolesource.SkillBinding{}, Authority: rolesource.AuthorityReadOnly,
			PermissionCeiling: domain.PermissionDiscuss, AllowedTools: allowed,
			Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
				AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
			Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
				AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
				MaxInvocationsPerParentRun: 2, MaxConcurrentInstances: 1,
				BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
					MaxTotalTokens: 200000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000}},
			OutputContract: "text-v1", MaxLoopIterations: 8, Prompt: "Classify one tool.",
		})
		require.NoError(t, err)
		_, err = sources.PublishRoleRevision(handle)
		require.NoError(t, err)

		taskName := fmt.Sprintf("task_%02d", index)
		tasks[taskName] = graphsource.Task{
			Name: taskName, Role: "global/" + handle, Goal: "Classify " + tc.tool,
		}
		deps[taskName] = []string{}
	}

	current, _, err := sources.ReadGraph("pipeline")
	require.NoError(t, err)
	digest, err := graphsource.SourceDigest(current)
	require.NoError(t, err)
	_, _, err = sources.UpdateGraph("pipeline", digest, func(document *graphsource.Document) error {
		document.Tasks = tasks
		document.Graph = deps
		return nil
	})
	require.NoError(t, err)
	_, err = sources.PublishGraphRevision("pipeline")
	require.NoError(t, err)

	projects := &projectstore.Store{Root: filepath.Join(home, "projects")}
	project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Project", HostPath: t.TempDir()})
	require.NoError(t, err)
	sessions := sessionstore.NewManager(projects.Root, projects)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID, Title: "Session"})
	require.NoError(t, err)

	service := &graphrun.Service{Sources: sources, Models: &store.ModelRepo{Files: models}, Sessions: sessions}
	run, err := service.Start(ctx, project.ID, "pipeline", 0, session.ID, nil, nil)
	require.NoError(t, err)

	db, err := sessions.OpenSession(ctx, session.ID)
	require.NoError(t, err)
	rows, err := db.Query(`SELECT handle, read_only FROM run_agent_flow_nodes WHERE run_id=?`, run.RunID)
	require.NoError(t, err)
	defer rows.Close()
	frozen := map[string]bool{}
	for rows.Next() {
		var handle string
		var readOnly int
		require.NoError(t, rows.Scan(&handle, &readOnly))
		frozen[handle] = readOnly != 0
	}
	require.NoError(t, rows.Err())
	require.Len(t, frozen, len(schedulingToolMatrix))

	resolver := store.NewFlowValidator(store.FlowPublishOptions{}).Resolver
	for index, tc := range schedulingToolMatrix {
		taskName := fmt.Sprintf("task_%02d", index)
		frozenReadOnly, ok := frozen[taskName]
		require.True(t, ok, "freeze is missing task %q (%s)", taskName, tc.tool)
		assert.Equal(t, tc.readOnly, frozenReadOnly, "graphrun freeze classification for %q", tc.tool)
		// The other two scheduling paths must agree with the freeze.
		assert.Equal(t, frozenReadOnly, domain.IsReadOnlySchedulingTool(tc.tool), "domain classifier for %q", tc.tool)
		assert.Equal(t, frozenReadOnly, resolver.ToolReadOnly(ctx, tc.tool), "publish resolver for %q", tc.tool)
	}
}
