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
	"syscall"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/api"
	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/compaction"
	"github.com/seqyuan/ennote/ennoworker/internal/config"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/hooks"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/projectcontext"
	"github.com/seqyuan/ennote/ennoworker/internal/providerdoctor"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/runtimeinfo"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/tools"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

type agentExecutor struct {
	db          *sql.DB
	writer      *events.Writer
	hub         *events.Hub
	homeDir     string
	trustStore  *workspace.TrustStore
	outboxStore *hooks.OutboxStore
	runs        *store.RunRepo
	calls       *store.CallRepo
	sessionDB   *store.SessionRepo
	msgRepo     *store.MessageRepo
	skillRepo   *store.SkillSnapshotRepo
	skillsDir   string
	builtinDir  string
	sandbox     string
	artifacts   *artifacts.Service
	compaction  *compaction.Service
	approvals   *store.ApprovalRepo
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
	// Compute canonical workspace root and trust before hooks.
	canonicalRoot, err := workspace.CanonicalWorkspaceRoot(wSpace.HostPath)
	if err != nil {
		return domain.RunOutput{}, fmt.Errorf("canonical workspace root: %w", err)
	}
	trusted, trustErr := e.trustStore.IsTrusted(wSpace.ID, canonicalRoot)
	if trustErr != nil {
		return domain.RunOutput{}, fmt.Errorf("check workspace trust: %w", trustErr)
	}
	var trustedAt time.Time
	if trusted && e.trustStore != nil {
		records, _ := e.trustStore.List()
		for _, r := range records {
			if r.WorkspaceID == wSpace.ID && r.CanonicalRoot == canonicalRoot {
				trustedAt = r.TrustedAt
				break
			}
		}
	}

	// Resolve hooks config (Phase 2): global + workspace layers, trust-gated.
	// The resolved set is frozen into the effective config so later Phase 3
	// lifecycle wiring reads a stable snapshot.
	if err := e.resolveAndFreezeHooks(ctx, run, wSpace, &resolved.Effective, canonicalRoot, trusted, trustedAt); err != nil {
		return domain.RunOutput{}, fmt.Errorf("hooks config: %w", err)
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
	ioDir := filepath.Join(wDir, "io")
	if err := os.MkdirAll(ioDir, 0o700); err != nil {
		return domain.RunOutput{}, fmt.Errorf("create runtime io dir: %w", err)
	}
	snapDir := filepath.Join(wDir, "skills")

	systemPrompt := "You are a helpful assistant."
	var skillCatalogState string
	var skillCatalogDigest string
	if resumeState != nil {
		skillCatalogState = resumeState.SkillCatalogState
		if resumeState.SkillCatalogState == "materialized" && resumeState.SkillCatalogDigest != "" {
			if err := skills.VerifyMaterializedCatalog(snapDir, resumeState.SkillCatalogDigest); err != nil {
				return domain.RunOutput{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, fmt.Errorf("catalog verification failed: %w", err))
			}
		}
		skillCatalogDigest = resumeState.SkillCatalogDigest
		systemPrompt = resumeState.SystemPrompt
	} else {
		// Check if read is allowed by frozen tool policy
		allowRead := agent.AllowsTool(resolved.Effective.ToolPolicy.Config, "read")

		if allowRead {
			// Build catalog from source roots
			sources := []skills.SourceRoot{
				{Name: "user", Path: e.skillsDir, Priority: 0},
			}
			if e.builtinDir != "" {
				sources = append(sources, skills.SourceRoot{Name: "builtin", Path: e.builtinDir, Priority: 1})
			}
			catalog := skills.BuildCatalog(sources)

			// Determine template vars based on sandbox mode
			sandboxMode := workspace.SandboxMode(e.sandbox)
			vars := skills.TemplateVars{Workspace: "/workspace", SkillDir: "/skills"}
			if sandboxMode == workspace.SandboxNone {
				// For none mode, skill_dir uses the absolute host path
				absSnapDir, _ := filepath.Abs(snapDir)
				vars = skills.TemplateVars{Workspace: ".", SkillDir: absSnapDir}
			}

			plan, planErr := skills.PlanMaterialization(catalog, vars)
			if planErr != nil {
				slog.Warn("plan materialization failed", "error", planErr)
			} else {
				result, matErr := skills.MaterializeCatalog(wDir, plan, catalog)
				if matErr != nil {
					slog.Warn("materialize catalog failed", "error", matErr)
				} else {
					// Save snapshot records to DB
					if saveErr := e.skillRepo.SaveCatalog(ctx, run.ID, result.Records); saveErr != nil {
						slog.Warn("save catalog failed", "error", saveErr)
					}
				skillCatalogState = "materialized"
				skillCatalogDigest = result.CatalogDigest
					// Build catalog prompt
					catalogPrompt := skills.BuildCatalogPrompt(catalog, 16*1024)
					if catalogPrompt != "" {
						// Load project context files
						projCtx, loadErr := e.loadProjectContext(canonicalRoot, trusted)
						if loadErr != nil {
							slog.Warn("load project context failed", "error", loadErr)
						}
						systemPrompt = projCtx.BuildPrompt("You are a helpful assistant.", catalogPrompt)
					}
				}
			}
		} else {
			skillCatalogState = "disabled"
			slog.Info("read tool not allowed by policy, skipping skill catalog")
		}
	}

	var wManager *workspace.Manager
	wManager, err = workspace.NewManagerWithSkills(canonicalRoot, ioDir, snapDir, workspace.SandboxMode(e.sandbox))
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
		WorkspaceID: wSpace.ID, Events: e.writer, Hub: e.hub, Recorder: e.calls,
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
	if e.hub != nil {
		loop.LivePublisher = e.hub
	}
	var runStartContext string
	if !resolved.Effective.HookConfig.IsEmpty() {
		loop.HookLife = agent.NewHookLifecycle(resolved.Effective.HookConfig).WithRun(run.ID, session.ID)
		// RunStart hook: inject additionalContext as a one-shot reminder for
		// iteration 1 only (never repeated on later iterations or resumes).
		runStartSource := "initial"
		if resumeState != nil {
			runStartSource = "retry"
		}
		runStartContext = loop.HookLife.RunStart(ctx, runStartSource)
	}
	if runStartContext != "" {
		loop.Reminders.Register(&agent.RunStartReminderProvider{Context: runStartContext})
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
		SkillCatalogState: skillCatalogState, SkillCatalogDigest: skillCatalogDigest,
	})

	// Queue observer hooks (RunEnd / SessionEnd) via durable outbox.
	e.queueRunEndObserver(ctx, run, session, wSpace, resolved.Effective, err, result.Iterations)

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
	ioDir := filepath.Join(workDir, "io")
	if err := os.MkdirAll(ioDir, 0o700); err != nil {
		return domain.RunOutput{}, err
	}
	manager, err := workspace.NewManager(workspaceRecord.HostPath, ioDir, filepath.Join(workDir, "skills"), workspace.SandboxMode(e.sandbox))
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

// buildTrustedSkillVars returns the trusted template variables for Skill
// prompt rendering based on the current sandbox mode and snapshot path.
func buildTrustedSkillVars(mode workspace.SandboxMode, snapPath string) map[string]string {
	vars := map[string]string{
		"skill_dir": snapPath,
	}
	switch mode {
	case workspace.SandboxBubblewrap:
		vars["workspace"] = "/workspace"
	case workspace.SandboxNone:
		vars["workspace"] = "."
	}
	return vars
}

type queueAdapter struct{ repo *store.QueueRepo }

func (a *queueAdapter) Drain(ctx context.Context, runID string, kind domain.QueuedInputKind, mode domain.QueueMode) ([]domain.QueuedInput, error) {
	return a.repo.Drain(ctx, runID, kind, mode)
}

func (e *agentExecutor) resolveAndFreezeHooks(ctx context.Context, run *domain.AgentRun, wSpace *domain.ProjectWorkspace, effective *domain.EffectiveRunConfig, canonicalRoot string, trusted bool, trustedAt time.Time) error {
	// Load configuration layers.
	globalLayer, err := hooks.LoadGlobalHookLayer(e.homeDir)
	if err != nil {
		return err
	}
	envLayer, err := hooks.LoadEnvHookLayer(e.homeDir)
	if err != nil {
		return err
	}

	// Workspace hooks only when trusted.
	var wsLayer *hooks.HookLayer
	if trusted {
		wsLayer, err = hooks.LoadWorkspaceHookLayer(canonicalRoot)
		if err != nil {
			return err
		}
	}

	// Resolve the merged hook set.
	resolved, err := hooks.ResolveHookSet(globalLayer, envLayer, wsLayer)
	if err != nil {
		return err
	}

	// Compute frozen hook config.
	digest, _ := resolved.Digest()
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("encode resolved hooks: %w", err)
	}

	effective.HookConfig = domain.EffectiveHookConfig{
		ResolvedHookSet: encoded,
		HookSetDigest:   fmt.Sprintf("%x", digest),
		WorkspaceID:     wSpace.ID,
		WorkspaceRoot:   canonicalRoot,
		TrustedAt:       trustedAt,
	}

	// Freeze workspace security snapshot (independent of hooks)
	effective.WorkspaceSecurity = &domain.WorkspaceSecuritySnapshot{
		WorkspaceID:   wSpace.ID,
		CanonicalRoot: canonicalRoot,
		Trusted:       trusted,
		TrustedAt:     trustedAt,
	}

	// Persist the updated effective config with hooks frozen.
	updatedConfig, err := json.Marshal(effective)
	if err != nil {
		return fmt.Errorf("encode effective config with hooks: %w", err)
	}
	_, err = e.db.ExecContext(ctx, `UPDATE agent_runs SET effective_config_json = ? WHERE id = ?`,
		string(updatedConfig), run.ID)
	if err != nil {
		return fmt.Errorf("freeze hooks in effective config: %w", err)
	}
	run.EffectiveConfig = updatedConfig

	return nil
}

func (e *agentExecutor) queueRunEndObserver(ctx context.Context, run *domain.AgentRun, session *domain.Session,
	wSpace *domain.ProjectWorkspace, effective domain.EffectiveRunConfig, runErr error, iterations int) {
	if e.outboxStore == nil || effective.HookConfig.IsEmpty() {
		return
	}

	status := "succeeded"
	errCode := ""
	if runErr != nil {
		status = "failed"
		errCode = string(domain.ErrorCodeOf(runErr))
	}

	payloadJSON, _ := json.Marshal(map[string]any{
		"status":     status,
		"error_code": errCode,
		"iterations": iterations,
	})

	canonicalRoot, _ := filepath.Abs(wSpace.HostPath)
	deliveryID := fmt.Sprintf("runend_%s", run.ID)
	entry := hooks.OutboxEntry{
		DeliveryID:    deliveryID,
		EventID:       0, // event_id is UNIQUE; we use delivery_id as primary
		RunID:         run.ID,
		SessionID:     session.ID,
		EventType:     "RunEnd",
		PayloadJSON:   string(payloadJSON),
		WorkspaceID:   wSpace.ID,
		WorkspaceRoot: canonicalRoot,
		Status:        hooks.OutboxStatusPending,
		CreatedAt:     time.Now(),
	}
	// Use a background context so the outbox write is not cancelled with the run.
	tx, err := e.db.BeginTx(context.Background(), nil)
	if err != nil {
		slog.Warn("outbox: begin tx for RunEnd", "run_id", run.ID, "error", err)
		return
	}
	if err := e.outboxStore.InsertOutbox(ctx, tx, entry); err != nil {
		slog.Warn("outbox: insert RunEnd", "run_id", run.ID, "error", err)
		tx.Rollback()
		return
	}
	tx.Commit()
}

// CheckPrompt implements api.PromptHookGate: evaluates UserPromptSubmit hooks
// before a new run is created. Fail-open on any infrastructure error.
func (e *agentExecutor) CheckPrompt(ctx context.Context, sessionID, prompt string, parts []domain.ContentBlock) api.PromptHookOutcome {
	wSpace, err := loadProjectWorkspace(ctx, e.db, sessionID)
	if err != nil {
		return api.PromptHookOutcome{Error: fmt.Errorf("load workspace for prompt hook: %w", err)}
	}
	canonicalRoot, err := filepath.Abs(wSpace.HostPath)
	if err != nil {
		return api.PromptHookOutcome{Error: fmt.Errorf("resolve workspace root: %w", err)}
	}

	globalLayer, err := hooks.LoadGlobalHookLayer(e.homeDir)
	if err != nil {
		return api.PromptHookOutcome{Error: err}
	}
	envLayer, err := hooks.LoadEnvHookLayer(e.homeDir)
	if err != nil {
		return api.PromptHookOutcome{Error: err}
	}
	trusted, trustErr := e.trustStore.IsTrusted(wSpace.ID, canonicalRoot)
	if trustErr != nil {
		return api.PromptHookOutcome{Error: fmt.Errorf("check workspace trust: %w", trustErr)}
	}
	var wsLayer *hooks.HookLayer
	if trusted {
		wsLayer, err = hooks.LoadWorkspaceHookLayer(canonicalRoot)
		if err != nil {
			return api.PromptHookOutcome{Error: err}
		}
	}
	set, err := hooks.ResolveHookSet(globalLayer, envLayer, wsLayer)
	if err != nil {
		return api.PromptHookOutcome{Error: err}
	}
	if set.IsEmpty() || len(set["UserPromptSubmit"].Matchers) == 0 {
		return api.PromptHookOutcome{}
	}

	d := hooks.NewDispatcher(set, canonicalRoot, nil)
	if d == nil {
		return api.PromptHookOutcome{}
	}
	dec := d.Dispatch(ctx, "UserPromptSubmit", "", hooks.HookInput{
		DeliveryID:    "prompt_" + sessionID,
		EventType:     "UserPromptSubmit",
		SessionID:     sessionID,
		WorkspaceID:   wSpace.ID,
		WorkspaceRoot: canonicalRoot,
		Prompt:        prompt,
	})
	return api.PromptHookOutcome{
		Blocked:           dec.Block,
		Reason:            dec.Reason,
		AdditionalContext: dec.AdditionalContext,
	}
}

func (e *agentExecutor) loadProjectContext(canonicalRoot string, trusted bool) (*projectcontext.Context, error) {
	sec := projectcontext.SecurityContext{
		WorkspaceID:   "", // not needed for context loading
		CanonicalRoot: canonicalRoot,
		Trusted:       trusted,
	}
	return projectcontext.Load(sec, e.homeDir)
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
	trustStore, err := workspace.NewTrustStore(cfg.HomeDir)
	if err != nil {
		return fmt.Errorf("init trust store: %w", err)
	}
	outboxStore := &hooks.OutboxStore{DB: db}
	executor := &agentExecutor{
		db: db, writer: eventWriter, hub: hub, homeDir: cfg.HomeDir, trustStore: trustStore, outboxStore: outboxStore, runs: runRepo,
		calls:     callRepo,
		sessionDB: &store.SessionRepo{DB: db}, msgRepo: &store.MessageRepo{DB: db},
		skillRepo: &store.SkillSnapshotRepo{DB: db},
		skillsDir: cfg.SkillsDir, builtinDir: cfg.BuiltinSkillsDir,
		sandbox: cfg.SandboxMode, artifacts: artifactService, approvals: approvalRepo,
	}
	executor.compaction = &compaction.Service{Repo: compactionRepo, RunRepo: runCompactionRepo, Calls: callRepo,
		Messages: executor.msgRepo, Events: eventWriter, Providers: executor.resolveRuntimeProvider}

	// Background observer-hook outbox worker: at-least-once delivery of
	// RunEnd / ApprovalRequested / Notification hooks across restarts.
	outboxWorker := &hooks.OutboxWorker{
		Store: outboxStore,
		Resolver: func(ctx context.Context, runID string) (hooks.HookSet, string, string, error) {
			stored, err := runRepo.Get(ctx, runID)
			if err != nil {
				return nil, "", "", err
			}
			var effective domain.EffectiveRunConfig
			if err := json.Unmarshal(stored.EffectiveConfig, &effective); err != nil {
				return nil, "", "", err
			}
			if effective.HookConfig.IsEmpty() {
				return hooks.HookSet{}, effective.HookConfig.WorkspaceID, effective.HookConfig.WorkspaceRoot, nil
			}
			var set hooks.HookSet
			if err := json.Unmarshal(effective.HookConfig.ResolvedHookSet, &set); err != nil {
				return nil, "", "", err
			}
			return set, effective.HookConfig.WorkspaceID, effective.HookConfig.WorkspaceRoot, nil
		},
	}
	outboxCtx, outboxCancel := context.WithCancel(context.Background())
	defer outboxCancel()
	go outboxWorker.Start(outboxCtx)

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
		PromptGate: executor,
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
