//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/seqyuan/ennote/ennoworker/internal/api"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/mcpclient"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain reuses the test binary as an MCP stdio server subprocess when the
// marker environment variable is set; otherwise it runs the normal tests.
func TestMain(m *testing.M) {
	if os.Getenv("ENNOTE_MCP_TEST_SERVER") == "1" {
		runLiveMCPServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runLiveMCPServer serves one echo tool over stdio.
func runLiveMCPServer() {
	server := mcp.NewServer(&mcp.Implementation{Name: "ennote-live-mcp", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", Description: "Echo arguments back",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + args.Text}}}, nil
		})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, _ = server.Connect(ctx, &mcp.StdioTransport{}, nil)
	<-ctx.Done()
}

// TestLiveAgentFlowChildCallsMCP qualifies the Agent Flow child → MCP tool
// path against a real Provider and a real MCP stdio server: a Role-backed
// Task's child Run discovers the bound server, freezes the echo tool, calls
// it, and folds the echoed value into its submitted result.
//
// It requires ENNOTE_LIVE_BASE_URL / ENNOTE_LIVE_API_KEY / ENNOTE_LIVE_MODEL.
func TestLiveAgentFlowChildCallsMCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	stack := newLiveStack(t, "flow-mcp-live")
	db := stack.DB
	project := stack.Project
	session := stack.Session

	// ——— MCP profile (stdio, self binary) + project binding ———
	profiles := &store.MCPProfileRepo{FilePath: filepath.Join(stack.Home, "config", "mcp.json")}
	profile, err := profiles.CreateProfile(ctx, store.CreateMCPProfileInput{
		DisplayName: "Live MCP", Slug: "test", SourceKind: domain.MCPSourceManaged,
	})
	require.NoError(t, err)
	exe, err := os.Executable()
	require.NoError(t, err)
	version := &domain.MCPServerProfileVersion{
		Transport:   domain.MCPTransportStdio,
		Executable:  exe,
		EnvLiterals: map[string]string{"ENNOTE_MCP_TEST_SERVER": "1"},
		TimeoutMS:   10000,
	}
	require.NoError(t, profiles.CreateVersion(ctx, profile.ID, version))
	bindings := &store.MCPBindingRepo{Projects: stack.Projects}
	binding, err := bindings.EnsureBindingExists(ctx, project.ID, version.ID)
	require.NoError(t, err)
	enabled, required := true, true
	binding, err = bindings.Update(ctx, binding.ID, store.MCPBindingUpdate{
		DesiredEnabled: &enabled, Required: &required, SelectedRemoteToolNames: []string{"echo"},
	})
	require.NoError(t, err)
	require.True(t, binding.DesiredEnabled)

	mcpServer := &api.MCPServer{
		Profiles: profiles, Bindings: bindings,
		Catalogs: &store.MCPCatalogRepo{CacheDir: filepath.Join(stack.Home, "cache", "mcp")},
		Runs:     &store.MCPRunRepo{DB: db},
		Bundled:  mcpclient.NewBundledRegistry(), Logger: slog.Default(),
	}

	// ——— Role (auto ceiling so the MCP tool is allowed) ———
	role := &rolesource.Document{
		SchemaVersion: 1, Handle: "mcp-caller", Name: "MCP Caller",
		Description: "Calls the bound MCP echo tool.", Positioning: "Independent",
		Icon: "bot", Color: "neutral",
		Model:  rolesource.ModelBinding{Ref: stack.ModelID, ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []rolesource.SkillBinding{}, Authority: domain.RoleAuthorityReadOnly,
		PermissionCeiling: domain.PermissionAuto, AllowedTools: []string{"test__echo"},
		Context: rolesource.ContextPolicy{DefaultMode: domain.RoleContextTask,
			AllowedModes: []domain.RoleContextMode{domain.RoleContextTask}, OwnExecutionContinuity: domain.RoleContinuityNone},
		Delegation: rolesource.DelegationPolicy{Admission: domain.DelegationAutoWithinBudget,
			AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
			MaxInvocationsPerParentRun: 4, MaxConcurrentInstances: 4,
			BudgetCeiling: rolesource.DelegationBudgetCeiling{MaxModelCalls: 8, MaxToolCalls: 16,
				MaxTotalTokens: 40000, MaxOutputTokens: 4096, MaxCostUSDMicros: 0, MaxWallTimeMS: 180000}},
		OutputContract: "text-v1", MaxLoopIterations: 8,
		Prompt: "You are an MCP tool caller. Call test__echo with text \"hello\" and then call submit_result with a summary that includes the echoed value.",
	}
	_, _, err = stack.Sources.CreateRole(role)
	require.NoError(t, err)
	_, err = stack.Sources.PublishRoleRevision(role.Handle)
	require.NoError(t, err)

	// ——— Graph: one Role-backed Task ———
	_, digest, err := stack.Sources.CreateGraph("mcp-flow", "MCP Flow")
	require.NoError(t, err)
	_, _, err = stack.Sources.UpdateGraph("mcp-flow", digest, func(d *graphsource.Document) error {
		d.Tasks = map[string]graphsource.Task{
			"call": {Name: "call", Role: "global/mcp-caller",
				Goal: "Call test__echo with text \"hello\" and call submit_result with the echoed value."},
		}
		d.Graph = map[string][]string{"call": {}}
		return nil
	})
	require.NoError(t, err)
	_, err = stack.Sources.PublishGraphRevision("mcp-flow")
	require.NoError(t, err)

	// ——— coordinator + executor router with MCP wired ———
	hub := events.NewHub()
	runTemplate := &store.RunRepo{Publisher: hub, Providers: stack.Providers, Models: stack.ModelRepo,
		Policies: stack.Policies, RoleSources: stack.Sources}
	routedRuns := &store.RoutedRunRepo{Sessions: stack.Sessions, Template: runTemplate}
	executorRouter := &sessionExecutorRouter{sessions: stack.Sessions}
	executorRouter.build = func(db *sql.DB, sessionPath string) (*agentExecutor, error) {
		writer := events.NewWriter(&store.EventRepo{DB: db}, hub)
		callRepo := &store.CallRepo{DB: db, Publisher: hub}
		trustStore, err := workspace.NewTrustStore(t.TempDir())
		if err != nil {
			return nil, err
		}
		emptySkills := t.TempDir()
		sessionMCP := *mcpServer
		sessionMCP.Runs = &store.MCPRunRepo{DB: db}
		return &agentExecutor{
			db: db, writer: writer, homeDir: t.TempDir(), runs: &store.RunRepo{DB: db, Publisher: hub,
				Providers: stack.Providers, Models: stack.ModelRepo, Policies: stack.Policies, RoleSources: stack.Sources},
			calls: callRepo, sessionDB: &store.SessionRepo{DB: db}, msgRepo: &store.MessageRepo{DB: db},
			projects:  &store.ProjectRepo{Files: stack.Projects},
			skillRepo: &store.SkillSnapshotRepo{DB: db}, skillsDir: emptySkills,
			builtinDir: emptySkills, sandbox: "none",
			hub:               hub,
			approvals:         &store.ApprovalRepo{DB: db},
			standingApprovals: &store.StandingApprovalRepo{DB: db},
			trustStore:        trustStore,
			MCP:               &sessionMCP,
		}, nil
	}
	coordinator := runs.NewCoordinator(routedRuns, executorRouter, 2)
	executorRouter.onChild = func(_ context.Context, ids []string) {
		for _, id := range ids {
			_ = coordinator.Enqueue(context.Background(), id)
		}
	}

	// ——— graph runner ———
	service := &graphrun.Service{
		Sources: stack.Sources, Models: stack.ModelRepo, Sessions: stack.Sessions,
		OnRunStarted: func(_ context.Context, db *sql.DB, sessionPath, runID string) error {
			startFlowOrchestrator(db, runID, hub, coordinator, stack.Sources, stack.ModelRepo, stack.Policies)
			return nil
		},
	}
	run, err := service.Start(ctx, project.ID, "mcp-flow", 1, session.ID, nil, nil)
	require.NoError(t, err)

	flowRuns := &store.AgentFlowRunRepo{DB: db}
	final := waitLiveFlowTerminal(t, flowRuns, run.RunID, 150*time.Second)
	require.Equal(t, domain.FlowStateCompleted, final.State, "flow must complete: %s", final.TerminalReason)

	// ——— the MCP server + tool were frozen for the child Run ———
	var serverCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_mcp_servers`).Scan(&serverCount))
	assert.Positive(t, serverCount, "run_mcp_servers must have the frozen snapshot")
	var toolCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM run_mcp_tools WHERE remote_name='echo'`).Scan(&toolCount))
	assert.Positive(t, toolCount, "run_mcp_tools must freeze the echo tool")

	// ——— the echoed value reached the submitted result ———
	var resultJSON string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(i.result_json, '') FROM delegation_items i
		JOIN delegation_item_attempts a ON a.item_id=i.id
		WHERE a.child_run_id IN (SELECT child_run_id FROM run_agent_flow_nodes WHERE run_id=?) LIMIT 1`, run.RunID).Scan(&resultJSON))
	assert.Contains(t, resultJSON, "hello",
		"the child must call the MCP echo tool and fold the echoed value")

	// ——— MCP call recorded ———
	var mcpCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM mcp_requests`).Scan(&mcpCount))
	assert.Positive(t, mcpCount, "mcp_requests must record the tool call")
}
