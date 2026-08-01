package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/api"
	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/compaction"
	"github.com/seqyuan/ennote/ennoworker/internal/config"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/providerdoctor"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/runtimeinfo"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/tools"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type agentExecutor struct {
	db         *sql.DB
	writer     *events.Writer
	runs       *store.RunRepo
	calls      *store.CallRepo
	sessionDB  *store.SessionRepo
	msgRepo    *store.MessageRepo
	skillRepo  *store.SkillSnapshotRepo
	skillsDir  string
	builtinDir string
	sandbox    string
	artifacts  *artifacts.Service
	compaction *compaction.Service
	approvals  *store.ApprovalRepo
}

func (e *agentExecutor) Execute(ctx context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
	if run.RunKind == domain.RunKindContextCompaction {
		return e.executeContextCompaction(ctx, run)
	}
	var resumeRecord *domain.ApprovalResume
	if e.approvals != nil {
		var err error
		resumeRecord, err = e.approvals.BeginResume(ctx, run.ID)
		if err != nil {
			return domain.RunOutput{}, err
		}
	}
	var resumeState *agent.ResumeState
	if resumeRecord != nil {
		resumeState = &agent.ResumeState{}
		if err := json.Unmarshal(resumeRecord.Checkpoint.State, resumeState); err != nil {
			return domain.RunOutput{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, err)
		}
	}

	session, err := e.sessionDB.FindByID(ctx, run.SessionID)
	if err != nil || session == nil {
		return domain.RunOutput{}, fmt.Errorf("load session: %w", err)
	}
	resolved, err := e.runs.ResolveAndFreezeConfig(ctx, run)
	if err != nil {
		return domain.RunOutput{}, err
	}
	wSpace, err := loadProjectWorkspace(ctx, e.db, run.SessionID)
	if err != nil {
		return domain.RunOutput{}, fmt.Errorf("load project: %w", err)
	}
	if run.BaseMessageID == "" {
		if session.ActiveLeafMessageID == nil {
			return domain.RunOutput{}, fmt.Errorf("session has no active message leaf")
		}
		run.BaseMessageID = *session.ActiveLeafMessageID
	}
	history, err := e.msgRepo.Lineage(ctx, run.SessionID, run.BaseMessageID)
	if err != nil {
		return domain.RunOutput{}, fmt.Errorf("load message history: %w", err)
	}

	provider, err := e.resolveProvider(resolved)
	if err != nil {
		return domain.RunOutput{}, err
	}
	toolPolicy, err := agent.NewBuiltinToolPolicy(resolved.Effective.ToolPolicy)
	if err != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorToolPolicyFailed, err)
	}
	router := &agent.SnapshotModelRouter{Factory: e.resolveRuntimeProvider}

	wDir := filepath.Join(os.Getenv("ENNOTE_HOME"), "runtime", "runs", run.ID)
	snapDir := filepath.Join(wDir, "skills")

	systemPrompt := "You are a helpful assistant."
	if resumeState != nil {
		systemPrompt = resumeState.SystemPrompt
	} else {
		var skillPrompt strings.Builder
		for _, skill := range skills.Discover(e.skillsDir, e.builtinDir) {
			snapPath, err := skill.CopyToSnapshot(snapDir)
			if err != nil {
				slog.Warn("skill snapshot failed", "skill", skill.Manifest.ID, "error", err)
				continue
			}
			if _, err := e.skillRepo.Save(ctx, run.ID, skill, snapPath); err != nil {
				slog.Warn("save skill snapshot failed", "skill", skill.Manifest.ID, "error", err)
			}
			skillPrompt.WriteString(skill.PromptText)
			skillPrompt.WriteString("\n\n")
		}
		if s := skillPrompt.String(); s != "" {
			systemPrompt = s + "\n" + systemPrompt
		}
	}

	wManager, err := workspace.NewManager(wSpace.HostPath, wDir, snapDir, workspace.SandboxMode(e.sandbox))
	if err != nil {
		return domain.RunOutput{}, fmt.Errorf("create workspace: %w", err)
	}
	artifactSink := &tools.ArtifactSink{Service: e.artifacts, ProjectID: session.ProjectID,
		SessionID: run.SessionID, RunID: run.ID}
	toolReg, err := tools.NewDefaultRegistry(wManager, artifactSink)
	if err != nil {
		return domain.RunOutput{}, fmt.Errorf("create tools: %w", err)
	}

	todoStore := domain.NewTodoStore()
	if err := toolReg.Register(&tools.TodoTool{Store: todoStore}); err != nil {
		return domain.RunOutput{}, fmt.Errorf("register todo tool: %w", err)
	}

	prepared, err := e.compaction.Prepare(ctx, run, history, resolved.Effective, systemPrompt, toolReg.Definitions())
	if err != nil {
		return domain.RunOutput{}, err
	}
	chatHistory := prepared.Messages
	var compactionConfig domain.CompactionPolicyConfig
	if err := json.Unmarshal(resolved.Effective.CompactionPolicy.Config, &compactionConfig); err != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, err)
	}
	var historyTool *tools.CompactedHistoryTool
	if compactionConfig.AllowHistoryLookup &&
		(prepared.Checkpoint != nil || compactionConfig.Mode == domain.CompactionManualAndAuto) {
		checkpoint := domain.ContextCompaction{}
		if prepared.Checkpoint != nil {
			checkpoint = *prepared.Checkpoint
		}
		historyTool = tools.NewCompactedHistoryTool(history, checkpoint)
		if resumeState != nil {
			historyTool.UseRunLocal(run.ID, resumeState.Generated, resumeState.MidRunCompaction.CoveredGenerated)
		}
		if err := toolReg.Register(historyTool); err != nil {
			return domain.RunOutput{}, fmt.Errorf("register compacted history lookup: %w", err)
		}
	}

	var runCompactor *compaction.RunCompactor
	if historyTool != nil {
		runCompactor, err = compaction.NewRunCompactor(e.compaction, run, resolved.Effective, historyTool)
	} else {
		runCompactor, err = compaction.NewRunCompactor(e.compaction, run, resolved.Effective)
	}
	if err != nil {
		return domain.RunOutput{}, err
	}
	loop := &agent.Loop{
		Provider: provider, ModelRouter: router, TurnPlanner: agent.ContextTurnPlanner{},
		MidRunCompactor:   runCompactor,
		VisionResolver:    &agent.BuiltinVisionResolver{Loader: e.artifacts},
		ImageDescriptions: &store.ImageDescriptionRepo{DB: e.db},
		Tools:             toolReg, ToolPolicy: toolPolicy, ToolPolicySnapshot: resolved.Effective.ToolPolicy,
		WorkspaceID: wSpace.ID, Events: e.writer, Recorder: e.calls,
		QueuedInputs: &queueAdapter{repo: &store.QueueRepo{DB: e.db}},
		SteeringMode: domain.QueueOneAtATime, FollowUpMode: domain.QueueOneAtATime,
		MaxIterations: resolved.Effective.MaxIterations,
		ContextTokens: resolved.Effective.ContextTokens,
		MaxOutput:     resolved.Effective.MaxOutputTokens,
		ToolExecution: resolved.Effective.ToolExecution,
		TodoStore:     todoStore,
		Reminders: agent.NewReminderRegistry(
			&agent.TodoReminderProvider{Store: todoStore},
			&agent.BudgetReminderProvider{},
		),
	}
	overflowRecovery := func(recoveryCtx context.Context) ([]domain.ChatMessage, error) {
		result, recoveryErr := e.compaction.RecoverOverflow(recoveryCtx, run, history, resolved.Effective,
			systemPrompt, toolReg.Definitions())
		if recoveryErr == nil && result.Checkpoint != nil && compactionConfig.AllowHistoryLookup {
			if historyTool == nil {
				historyTool = tools.NewCompactedHistoryTool(history, *result.Checkpoint)
				if registerErr := toolReg.Register(historyTool); registerErr != nil {
					return nil, registerErr
				}
			} else {
				historyTool.UseCheckpoint(*result.Checkpoint)
			}
		}
		return result.Messages, recoveryErr
	}
	if compactionConfig.Mode != domain.CompactionManualAndAuto || !compactionConfig.AllowOverflowRecovery {
		overflowRecovery = nil
	}
	var approvalResolution *agent.ApprovalResolution
	if resumeRecord != nil {
		approvalResolution = &agent.ApprovalResolution{Decision: resumeRecord.Decision,
			BatchDigest: resumeRecord.Approval.BatchDigest}
	}
	result, err := loop.Run(ctx, agent.RunInput{
		RunID: run.ID, Model: resolved.Effective.APIModel,
		ProviderProfileID: resolved.Effective.ProviderProfileID,
		ModelProfileID:    resolved.Effective.ModelProfileID,
		RequestedConfig:   run.RequestedConfig, EffectiveConfig: run.EffectiveConfig,
		InitialRuntime: resolved.Effective.InitialRuntime, Routing: resolved.Effective.Routing,
		VisionPolicy: resolved.Effective.VisionPolicy,
		SystemPrompt: systemPrompt, History: chatHistory, OverflowRecovery: overflowRecovery,
		Resume: resumeState, Approval: approvalResolution,
	})
	if err != nil {
		var approvalRequired *agent.ApprovalRequiredError
		if errors.As(err, &approvalRequired) && e.approvals != nil {
			encoded, encodeErr := json.Marshal(approvalRequired.State)
			if encodeErr != nil {
				return domain.RunOutput{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, encodeErr)
			}
			if _, suspendErr := e.approvals.Suspend(context.WithoutCancel(ctx), run.ID,
				agent.ResumeStateVersion, approvalRequired.State.Iteration, approvalRequired.BatchDigest,
				encoded, approvalRequired.Items); suspendErr != nil {
				return domain.RunOutput{}, domain.NewCodedError(domain.ErrorEventPersistence, suspendErr)
			}
			return domain.RunOutput{Suspended: true}, nil
		}
		return domain.RunOutput{}, err
	}
	if e.approvals != nil {
		if err := e.approvals.CompleteExecuting(context.WithoutCancel(ctx), run.ID); err != nil {
			return domain.RunOutput{}, domain.NewCodedError(domain.ErrorEventPersistence, err)
		}
	}
	return domain.RunOutput{Messages: result.Generated}, nil
}

func (e *agentExecutor) executeContextCompaction(ctx context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
	resolved, err := e.runs.ResolveAndFreezeConfig(ctx, run)
	if err != nil {
		return domain.RunOutput{}, err
	}
	workspaceRecord, err := loadProjectWorkspace(ctx, e.db, run.SessionID)
	if err != nil {
		return domain.RunOutput{}, err
	}
	workDir := filepath.Join(os.Getenv("ENNOTE_HOME"), "runtime", "runs", run.ID)
	manager, err := workspace.NewManager(workspaceRecord.HostPath, workDir, filepath.Join(workDir, "skills"), workspace.SandboxMode(e.sandbox))
	if err != nil {
		return domain.RunOutput{}, err
	}
	registry, err := tools.NewDefaultRegistry(manager)
	if err != nil {
		return domain.RunOutput{}, err
	}
	err = e.compaction.ExecuteManual(ctx, run, resolved, "You are a helpful assistant.", registry.Definitions())
	return domain.RunOutput{}, err
}

func (e *agentExecutor) resolveProvider(resolved *store.ResolvedRunConfig) (llm.Provider, error) {
	runtime := resolved.Effective.InitialRuntime
	if runtime.ModelProfileID == "" {
		runtime = domain.ModelRuntimeSnapshot{ProviderProfileID: resolved.Provider.ID,
			ModelProfileID: resolved.Model.ID, APIModel: resolved.Model.ModelName,
			BaseURL: resolved.Provider.BaseURL, CredentialRef: resolved.Provider.CredentialRef,
			Proxy: resolved.Provider.Proxy, ContextTokens: resolved.Model.ContextWindow,
			MaxOutputTokens: resolved.Model.MaxOutputTokens, SupportsVision: resolved.Model.SupportsVision,
			SupportsToolUse: resolved.Model.SupportsToolUse, SupportsThinking: resolved.Model.SupportsThinking}
	}
	return e.resolveRuntimeProvider(runtime)
}

func (e *agentExecutor) resolveRuntimeProvider(runtime domain.ModelRuntimeSnapshot) (llm.Provider, error) {
	resolver := llm.CredentialResolver{}
	secret, err := resolver.Resolve(runtime.CredentialRef)
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorProviderCredentialUnavailable,
			fmt.Errorf("resolve credentials for provider %s: %w", runtime.ProviderProfileID, err))
	}
	provider, err := llm.NewOpenAIProvider(llm.OpenAIConfig{
		BaseURL: runtime.BaseURL, APIKey: secret,
		Model: runtime.APIModel, MaxTokens: runtime.MaxOutputTokens,
	})
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, err)
	}
	return provider, nil
}

type queueAdapter struct{ repo *store.QueueRepo }

func (a *queueAdapter) Drain(ctx context.Context, runID string, kind domain.QueuedInputKind, mode domain.QueueMode) ([]domain.QueuedInput, error) {
	return a.repo.Drain(ctx, runID, kind, mode)
}

func loadProjectWorkspace(ctx context.Context, db *sql.DB, sessionID string) (*domain.ProjectWorkspace, error) {
	sessionRepo := &store.SessionRepo{DB: db}
	session, err := sessionRepo.FindByID(ctx, sessionID)
	if err != nil || session == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	projectRepo := &store.ProjectRepo{DB: db}
	project, err := projectRepo.FindByID(ctx, session.ProjectID)
	if err != nil || project == nil {
		return nil, fmt.Errorf("project not found: %s", session.ProjectID)
	}
	_ = project
	row := db.QueryRowContext(ctx,
		`SELECT id, project_id, kind, host_path, virtual_path, status, path_fingerprint, created_at
		 FROM project_workspaces WHERE project_id = ? AND status = 'active' LIMIT 1`,
		project.ID,
	)
	var ws domain.ProjectWorkspace
	var createdAt string
	if err := row.Scan(&ws.ID, &ws.ProjectID, &ws.Kind, &ws.HostPath, &ws.VirtualPath,
		&ws.Status, &ws.PathFingerprint, &createdAt); err != nil {
		return nil, fmt.Errorf("workspace not found for project %s: %w", project.ID, err)
	}
	ws.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &ws, nil
}

func msgToChat(msg domain.Message) domain.ChatMessage {
	return domain.ChatMessage{Role: domain.Role(msg.Role), Content: append([]domain.ContentBlock(nil), msg.Parts...)}
}

func main() {
	if err := run(); err != nil {
		slog.Error("ennoworker stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.BootstrapToken == "" {
		return fmt.Errorf("ENNOTE_BOOTSTRAP_TOKEN is required")
	}

	db, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	hub := events.NewHub()
	runRepo := &store.RunRepo{DB: db, Publisher: hub}
	queuedRuns, err := runRepo.RecoverActive(context.Background())
	if err != nil {
		return fmt.Errorf("recover active runs: %w", err)
	}

	eventWriter := events.NewWriter(&store.EventRepo{DB: db}, hub)
	artifactService := &artifacts.Service{DB: db, Root: filepath.Join(cfg.HomeDir, "artifacts")}
	if err := artifactService.Reconcile(context.Background()); err != nil {
		return fmt.Errorf("reconcile artifact storage: %w", err)
	}
	callRepo := &store.CallRepo{DB: db, Publisher: hub}
	compactionRepo := &store.CompactionRepo{DB: db, Publisher: hub}
	runCompactionRepo := &store.RunCompactionRepo{DB: db, Publisher: hub}
	approvalRepo := &store.ApprovalRepo{DB: db, Publisher: hub}
	executor := &agentExecutor{
		db: db, writer: eventWriter, runs: runRepo,
		calls:     callRepo,
		sessionDB: &store.SessionRepo{DB: db}, msgRepo: &store.MessageRepo{DB: db},
		skillRepo: &store.SkillSnapshotRepo{DB: db},
		skillsDir: cfg.SkillsDir, builtinDir: cfg.BuiltinSkillsDir,
		sandbox: cfg.SandboxMode, artifacts: artifactService, approvals: approvalRepo,
	}
	executor.compaction = &compaction.Service{Repo: compactionRepo, RunRepo: runCompactionRepo, Calls: callRepo,
		Messages: executor.msgRepo, Events: eventWriter, Providers: executor.resolveRuntimeProvider}
	coordinator := runs.NewCoordinator(runRepo, executor, cfg.MaxConcurrentRuns)
	for _, runID := range queuedRuns {
		if err := coordinator.Enqueue(context.Background(), runID); err != nil {
			return fmt.Errorf("re-enqueue recovered run %s: %w", runID, err)
		}
	}
	if len(queuedRuns) > 0 {
		slog.Info("queued runs recovered", "count", len(queuedRuns))
	}
	instanceID, err := runtimeinfo.NewInstanceID()
	if err != nil {
		return err
	}
	providerRepo := &store.ProviderRepo{DB: db}
	modelRepo := &store.ModelRepo{DB: db}
	doctor := &providerdoctor.Service{Providers: providerRepo, Models: modelRepo,
		Credentials: llm.CredentialResolver{}, Timeout: 15 * time.Second}
	server := &api.Server{
		DB: db, Token: cfg.BootstrapToken, Sandbox: cfg.SandboxMode,
		Projects: &store.ProjectRepo{DB: db}, Providers: providerRepo,
		Models: modelRepo, Doctor: doctor, Policies: &store.PolicyRepo{DB: db}, Artifacts: artifactService,
		Sessions: &store.SessionRepo{DB: db}, Branches: &store.BranchRepo{DB: db},
		Messages: executor.msgRepo, Compactions: compactionRepo,
		Approvals: approvalRepo, Runs: runRepo, Queue: &store.QueueRepo{DB: db}, Events: &store.EventRepo{DB: db},
		Hub: hub, Control: api.CoordinatorController{Coordinator: coordinator}, InstanceID: instanceID,
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
	slog.Info("ennoworker ready", "address", listener.Addr().String(), "sandbox", cfg.SandboxMode)

	stateFile := filepath.Join(filepath.Dir(cfg.DatabasePath), "worker-state.json")
	if err := runtimeinfo.WriteAtomic(stateFile, runtimeinfo.WorkerState{
		Version: runtimeinfo.StateVersion, URL: fmt.Sprintf("http://%s", listener.Addr().String()),
		PID: os.Getpid(), InstanceID: instanceID, BootstrapToken: cfg.BootstrapToken,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		_ = httpServer.Close()
		return fmt.Errorf("write worker runtime state: %w", err)
	}
	defer func() {
		if err := runtimeinfo.RemoveIfOwner(stateFile, os.Getpid(), instanceID); err != nil {
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
