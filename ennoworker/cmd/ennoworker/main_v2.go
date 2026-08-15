package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/api"
	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/compaction"
	"github.com/seqyuan/ennote/ennoworker/internal/config"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/seqyuan/ennote/ennoworker/internal/globalsource"
	"github.com/seqyuan/ennote/ennoworker/internal/graphbuilder"
	"github.com/seqyuan/ennote/ennoworker/internal/graphrun"
	"github.com/seqyuan/ennote/ennoworker/internal/hooks"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/mcpclient"
	"github.com/seqyuan/ennote/ennoworker/internal/projection"
	"github.com/seqyuan/ennote/ennoworker/internal/projectstore"
	"github.com/seqyuan/ennote/ennoworker/internal/prompts"
	"github.com/seqyuan/ennote/ennoworker/internal/providerdoctor"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/runtimeinfo"
	"github.com/seqyuan/ennote/ennoworker/internal/sessionstore"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
	"github.com/seqyuan/ennote/ennoworker/internal/storage"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.BootstrapToken == "" {
		return fmt.Errorf("ENNOTE_BOOTSTRAP_TOKEN is required")
	}

	var projections *projection.Stores
	layout, err := storage.Bootstrap(cfg.HomeDir, func(layout storage.Layout) error {
		var openErr error
		projections, openErr = projection.Open(layout.CatalogDB, layout.UsageDB)
		return openErr
	})
	if err != nil {
		if projections != nil {
			_ = projections.Close()
		}
		return fmt.Errorf("bootstrap V2 storage: %w", err)
	}
	defer projections.Close()

	projectFiles := &projectstore.Store{Root: layout.Projects}
	projects := &store.ProjectRepo{Files: projectFiles}
	sessionManager := sessionstore.NewManager(layout.Projects, projectFiles)
	defer sessionManager.Close()
	modelFiles := fileconfig.NewModelStore(layout.Models, layout.ProviderAuth, layout.Settings)
	providers := &store.ProviderRepo{Files: modelFiles}
	models := &store.ModelRepo{Files: modelFiles}
	policyFiles := &fileconfig.PolicyStore{Path: layout.Policies}
	policies := &store.PolicyRepo{Files: policyFiles}
	sessions := &store.SessionRepo{Files: sessionManager, Models: models}
	mcpProfiles := &store.MCPProfileRepo{FilePath: layout.MCP}
	mcpBindings := &store.MCPBindingRepo{Projects: projectFiles}
	mcpCatalogs := &store.MCPCatalogRepo{CacheDir: filepath.Join(layout.Cache, "mcp")}
	mcpServer := &api.MCPServer{Profiles: mcpProfiles, Bindings: mcpBindings, Catalogs: mcpCatalogs,
		Runs: &store.MCPRunRepo{}, Bundled: mcpclient.NewBundledRegistry(), Logger: slog.Default()}

	rebuilder := &projection.Rebuilder{Stores: projections, Projects: projectFiles, Sessions: sessionManager}
	if err := rebuilder.Rebuild(context.Background()); err != nil {
		return fmt.Errorf("rebuild global projections: %w", err)
	}
	projector := &projection.Projector{Stores: projections, Sessions: sessionManager}

	trustStore, err := workspace.NewTrustStore(cfg.HomeDir)
	if err != nil {
		return fmt.Errorf("init trust store: %w", err)
	}
	hub := events.NewHub()
	globalSources := &globalsource.Store{HomeDir: cfg.HomeDir}
	globalSkillCatalog := make(map[string]bool)
	for _, skill := range skills.Discover(cfg.SkillsDir, cfg.BuiltinSkillsDir) {
		globalSkillCatalog[skill.Manifest.ID] = true
	}
	runTemplate := &store.RunRepo{Publisher: hub, Providers: providers, Models: models, Policies: policyFiles, RoleSources: globalSources}
	routedRuns := &store.RoutedRunRepo{Sessions: sessionManager, Template: runTemplate}

	executorRouter := &sessionExecutorRouter{sessions: sessionManager}
	executorRouter.build = func(db *sql.DB, sessionPath string) (*agentExecutor, error) {
		runRepo := *runTemplate
		runRepo.DB = db
		eventWriter := events.NewWriter(&store.EventRepo{DB: db}, hub)
		callRepo := &store.CallRepo{DB: db, Publisher: hub}
		compactionRepo := &store.CompactionRepo{DB: db, Publisher: hub, Policies: policyFiles}
		runCompactionRepo := &store.RunCompactionRepo{DB: db, Publisher: hub}
		approvalRepo := &store.ApprovalRepo{DB: db, Publisher: hub}
		artifactService := &artifacts.Service{DB: db, Root: filepath.Join(sessionPath, "artifacts")}
		if err := artifactService.Reconcile(context.Background()); err != nil {
			return nil, fmt.Errorf("reconcile Session artifacts: %w", err)
		}
		sessionMCP := *mcpServer
		sessionMCP.Runs = &store.MCPRunRepo{DB: db}
		executor := &agentExecutor{
			db: db, writer: eventWriter, hub: hub, homeDir: cfg.HomeDir, sessionPath: sessionPath, MCP: &sessionMCP,
			trustStore: trustStore, outboxStore: &hooks.OutboxStore{DB: db}, runs: &runRepo,
			calls: callRepo, projects: projects, sessionDB: &store.SessionRepo{DB: db},
			msgRepo: &store.MessageRepo{DB: db}, skillRepo: &store.SkillSnapshotRepo{DB: db},
			skillsDir: cfg.SkillsDir, builtinDir: cfg.BuiltinSkillsDir,
			sandbox: cfg.SandboxMode, artifacts: artifactService, approvals: approvalRepo,
			standingApprovals: &store.StandingApprovalRepo{DB: db},
		}
		executor.compaction = &compaction.Service{
			Repo: compactionRepo, RunRepo: runCompactionRepo, Calls: callRepo,
			Messages: executor.msgRepo, Events: eventWriter, Providers: executor.resolveRuntimeProvider,
		}
		return executor, nil
	}

	coordinator := runs.NewCoordinator(routedRuns, executorRouter, cfg.MaxConcurrentRuns)
	executorRouter.onChild = func(_ context.Context, ids []string) {
		for _, id := range ids {
			if err := coordinator.Enqueue(context.Background(), id); err != nil {
				slog.Warn("enqueue child run failed", "runID", id, "error", err)
			}
		}
	}
	coordinator.SetRunSettledHook(func(ctx context.Context, run *domain.AgentRun) error {
		db, err := sessionManager.OpenSession(ctx, run.SessionID)
		if err != nil {
			return err
		}
		if _, err := projector.DrainSession(ctx, run.SessionID, 100); err != nil {
			slog.Warn("project Session changes", "sessionID", run.SessionID, "error", err)
		}
		return tickSessionAutoResume(ctx, db, coordinator, run.SessionID)
	})
	graphRuns := &graphrun.Service{
		Sources: globalSources, Models: models, Sessions: sessionManager,
		GlobalSkills: globalSkillCatalog,
		OnRunStarted: func(_ context.Context, db *sql.DB, sessionPath, runID string) error {
			startFlowOrchestrator(db, runID, hub, coordinator, globalSources, models, policyFiles)
			return nil
		},
	}
	if err := recoverV2Sessions(context.Background(), sessionManager, runTemplate, coordinator, graphRuns,
		globalSources, models, policyFiles); err != nil {
		return err
	}

	builtins, err := prompts.LoadBuiltins()
	if err != nil {
		return fmt.Errorf("load builtin prompts: %w", err)
	}
	globalPrompts, err := prompts.OpenGlobalStore(cfg.HomeDir)
	if err != nil {
		return fmt.Errorf("open global prompt store: %w", err)
	}
	promptService := &prompts.Service{
		HomeDir: cfg.HomeDir, Projects: projects, TrustStore: trustStore,
		Builtins: builtins, GlobalStore: globalPrompts,
	}
	settings, err := modelFiles.Settings.Read()
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	additionalRoots := make([]skillsmgmt.Root, 0, len(settings.SkillRoots))
	for index, path := range settings.SkillRoots {
		additionalRoots = append(additionalRoots, skillsmgmt.Root{Name: filepath.Base(path), Path: path, Priority: (index + 1) * 10})
	}
	userHome, _ := os.UserHomeDir()
	builderCompleter := graphbuilder.CompleteFunc(func(ctx context.Context, modelID, systemPrompt, userPrompt string) (string, error) {
		model, err := models.FindByID(ctx, modelID)
		if err != nil || model == nil {
			return "", fmt.Errorf("resolve Graph Builder model: %w", err)
		}
		providerProfile, err := providers.FindByID(ctx, model.ProviderID)
		if err != nil || providerProfile == nil {
			return "", fmt.Errorf("resolve Graph Builder provider: %w", err)
		}
		provider, err := llm.NewOpenAIProvider(llm.OpenAIConfig{
			BaseURL: providerProfile.BaseURL, APIKey: llm.NewSecret(providerProfile.APIKey),
			Model: model.ModelName, MaxTokens: model.MaxOutputTokens,
		})
		if err != nil {
			return "", err
		}
		completion, err := provider.Stream(ctx, domain.CompletionRequest{
			Model: model.ModelName,
			Messages: []domain.ChatMessage{
				{Role: domain.RoleSystem, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: systemPrompt}}},
				{Role: domain.RoleUser, Content: []domain.ContentBlock{{Kind: domain.ContentText, Text: userPrompt}}},
			},
		}, llm.NopSink{})
		if err != nil {
			return "", err
		}
		var output strings.Builder
		for _, block := range completion.Content {
			if block.Kind == domain.ContentText {
				output.WriteString(block.Text)
			}
		}
		return output.String(), nil
	})
	instanceID, err := runtimeinfo.NewInstanceID()
	if err != nil {
		return err
	}
	doctor := &providerdoctor.Service{Providers: providers, Models: models, Timeout: 15 * time.Second}

	server := &api.Server{
		CatalogDB: projections.Catalog, UsageDB: projections.Usage, SessionStores: sessionManager,
		Token: cfg.BootstrapToken, Sandbox: cfg.SandboxMode,
		Projects: projects, Providers: providers, Models: models, Policies: policies,
		Artifacts: &artifacts.Service{}, Sessions: sessions,
		Branches: &store.BranchRepo{}, Messages: &store.MessageRepo{}, Compactions: &store.CompactionRepo{Publisher: hub, Policies: policyFiles},
		Approvals: &store.ApprovalRepo{Publisher: hub}, StandingApprovals: &store.StandingApprovalRepo{},
		Delegations: &store.DelegationRepo{}, DelegationApprovals: &store.DelegationApprovalRepo{},
		Runs: runTemplate, Queue: &store.QueueRepo{}, Events: &store.EventRepo{},
		Hub: hub, Control: api.CoordinatorController{Coordinator: coordinator}, Projection: projector, InstanceID: instanceID,
		PromptGate: executorRouter, Prompts: promptService, Doctor: doctor, MCP: mcpServer,
		Skills: &skillsmgmt.Service{
			UserRoot: cfg.SkillsDir, BuiltinRoot: cfg.BuiltinSkillsDir,
			HomeDir: userHome, AdditionalRoots: additionalRoots,
		},
		SkillRoots: &store.SkillRootRepo{Settings: modelFiles.Settings},
		Trust:      trustStore, GlobalSources: globalSources,
		GraphBuilder: &graphbuilder.Service{Sources: globalSources, Completer: builderCompleter},
		GraphRuns:    graphRuns,
		GraphRunResume: func(_ context.Context, db *sql.DB, sessionID, runID string) error {
			startFlowOrchestrator(db, runID, hub, coordinator, globalSources, models, policyFiles)
			return nil
		},
	}

	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}
	httpServer := &http.Server{
		Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 90 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- httpServer.Serve(listener) }()
	slog.Info("ennoworker ready", "address", listener.Addr().String(), "sandbox", cfg.SandboxMode, "storageLayout", storage.LayoutVersion)

	if err := runtimeinfo.WriteAtomic(layout.WorkerState, runtimeinfo.WorkerState{
		Version: runtimeinfo.StateVersion, URL: fmt.Sprintf("http://%s", listener.Addr().String()),
		PID: os.Getpid(), InstanceID: instanceID, BootstrapToken: cfg.BootstrapToken,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		_ = httpServer.Close()
		return fmt.Errorf("write worker runtime state: %w", err)
	}
	defer func() {
		if err := runtimeinfo.RemoveIfOwner(layout.WorkerState, os.Getpid(), instanceID); err != nil {
			slog.Warn("remove worker runtime state", "error", err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case received := <-signals:
		slog.Info("ennoworker shutting down", "signal", received.String())
	case err := <-serverErrors:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func recoverV2Sessions(ctx context.Context, sessions *sessionstore.Manager, template *store.RunRepo,
	coordinator *runs.Coordinator, graphRuns *graphrun.Service, sources *globalsource.Store,
	models *store.ModelRepo, policyFiles *fileconfig.PolicyStore) error {
	ids, err := sessions.AllSessionIDs()
	if err != nil {
		return err
	}
	for _, sessionID := range ids {
		db, err := sessions.OpenSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if graphRuns != nil {
			if err := graphRuns.RecoverSession(ctx, sessionID); err != nil {
				return fmt.Errorf("recover Session %s flow runs: %w", sessionID, err)
			}
		}
		runRepo := *template
		runRepo.DB = db
		queued, err := runRepo.RecoverActive(ctx)
		if err != nil {
			return fmt.Errorf("recover Session %s runs: %w", sessionID, err)
		}
		delegations := &store.DelegationRepo{DB: db, RoleSources: sources, Models: models, Policies: policyFiles}
		if _, err := delegations.ReapOrphans(ctx); err != nil {
			return err
		}
		if _, err := delegations.RecoverDelegation(ctx); err != nil {
			return err
		}
		if _, err := delegations.RebuildMissingCompletions(ctx, 100); err != nil {
			return err
		}
		if _, err := delegations.RebuildMissingDeliveryEvents(ctx, 100); err != nil {
			return err
		}
		if _, err := (&store.AttentionRepo{DB: db}).RebuildAttention(ctx, 100); err != nil {
			return err
		}
		ready, err := delegations.ReadyChildrenForEnqueue(ctx, queued)
		if err != nil {
			return err
		}
		for _, runID := range ready {
			if err := coordinator.Enqueue(context.Background(), runID); err != nil {
				return err
			}
		}
	}
	return nil
}

// startFlowOrchestrator wires a per-Session agentflow.Orchestrator for one
// frozen Graph Run and resumes it. The flow store resolves the frozen
// execution plan from the owning Session database; child Runs materialize
// from frozen delegation role meta and are enqueued through the coordinator.
// Recovery is idempotent: terminal runs are skipped and crashed nodes are
// reconciled from child Run terminal facts.
func startFlowOrchestrator(db *sql.DB, runID string,
	hub *events.Hub, coordinator *runs.Coordinator, sources *globalsource.Store,
	models *store.ModelRepo, policyFiles *fileconfig.PolicyStore) {
	// The orchestrator goroutine must outlive the HTTP request that created
	// the run: it runs on the Worker lifetime context, never the request
	// context (which is cancelled when the handler returns).
	ctx := context.Background()
	eventWriter := events.NewWriter(&store.EventRepo{DB: db}, hub)
	orchestrator := &agentflow.Orchestrator{
		Store: &store.OrchestratorStore{Runs: &store.AgentFlowRunRepo{DB: db}},
		Children: &store.OrchestratorChildren{
			DB: db, Delegations: &store.DelegationRepo{DB: db, RoleSources: sources, Models: models, Policies: policyFiles},
			Policies: policyFiles,
		},
		Events: &store.FlowEventSink{Writer: eventWriter},
		Enqueue: func(ctx context.Context, childRunID string) error {
			return coordinator.Enqueue(ctx, childRunID)
		},
	}
	orchestrator.Recover(ctx, []string{runID})
}
