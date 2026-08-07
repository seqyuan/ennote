package main

import (
	"bytes"
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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/agent"
	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/api"
	"github.com/seqyuan/ennote/ennoworker/internal/artifacts"
	"github.com/seqyuan/ennote/ennoworker/internal/compaction"
	"github.com/seqyuan/ennote/ennoworker/internal/config"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/events"
	"github.com/seqyuan/ennote/ennoworker/internal/hooks"
	"github.com/seqyuan/ennote/ennoworker/internal/llm"
	"github.com/seqyuan/ennote/ennoworker/internal/mcpclient"
	"github.com/seqyuan/ennote/ennoworker/internal/projectcontext"
	"github.com/seqyuan/ennote/ennoworker/internal/prompts"
	"github.com/seqyuan/ennote/ennoworker/internal/providerdoctor"
	"github.com/seqyuan/ennote/ennoworker/internal/runs"
	"github.com/seqyuan/ennote/ennoworker/internal/runtimeinfo"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/seqyuan/ennote/ennoworker/internal/skillsmgmt"
	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/seqyuan/ennote/ennoworker/internal/tools"
	"github.com/seqyuan/ennote/ennoworker/internal/workspace"
)

// mcpBytesPublisher publishes MCP binary/image content into immutable
// conversation artifacts so the model only sees a bounded reference.
type mcpBytesPublisher struct {
	service   *artifacts.Service
	projectID string
	sessionID string
	runID     string
}

func (p *mcpBytesPublisher) PublishBytes(ctx context.Context, toolCallID, name, mime string, data []byte) (domain.ArtifactReference, error) {
	if p.service == nil {
		return domain.ArtifactReference{}, fmt.Errorf("artifact service is unavailable")
	}
	artifact, err := p.service.Store(ctx, artifacts.PublishInput{
		ProjectID: p.projectID, SessionID: p.sessionID, RunID: p.runID, ToolCallID: toolCallID,
		Name: name, SourceKind: "mcp", RetentionClass: "project",
	}, bytes.NewReader(data))
	if err != nil {
		return domain.ArtifactReference{}, err
	}
	return artifact.Reference(), nil
}

// mcpRecorder persists the MCP request state machine (planned/dispatched ->
// completed/failed/cancelled/outcome_unknown).
type mcpRecorder struct {
	runs  *store.MCPRunRepo
	runID string
}

func (r *mcpRecorder) RecordMCPStep(runServerID, runToolID, toolCallID string, generation int,
	status domain.MCPRequestStatus, requestDigest, responseDigest, errorCode string) {
	if r.runs == nil || toolCallID == "" {
		return
	}
	ctx := context.Background()
	record := store.MCPRequestRecord{
		RunID: r.runID, RunServerID: runServerID, RunToolID: runToolID, ToolCallID: toolCallID,
		ConnectionGeneration: generation, Status: status,
		RequestDigest: requestDigest, ResponseDigest: responseDigest, ErrorCode: errorCode,
	}
	// CreateRequest advances the state machine with CAS; an illegal transition
	// (e.g. terminal -> dispatched) is a violation and must not be swallowed
	// silently — it means the call is being replayed after terminalization.
	if _, err := r.runs.CreateRequest(ctx, record); err != nil {
		slog.Warn("record mcp request", "run", r.runID, "toolCall", toolCallID, "status", status, "error", err)
	}
}

type agentExecutor struct {
	db                *sql.DB
	writer            *events.Writer
	hub               *events.Hub
	homeDir           string
	trustStore        *workspace.TrustStore
	outboxStore       *hooks.OutboxStore
	runs              *store.RunRepo
	calls             *store.CallRepo
	sessionDB         *store.SessionRepo
	msgRepo           *store.MessageRepo
	skillRepo         *store.SkillSnapshotRepo
	skillsDir         string
	builtinDir        string
	sandbox           string
	artifacts         *artifacts.Service
	compaction        *compaction.Service
	approvals         *store.ApprovalRepo
	standingApprovals *store.StandingApprovalRepo
	// OnChildRunsCreated is called after delegate_roles creates children.
	// The coordinator provides this to enqueue child runs.
	OnChildRunsCreated func(ctx context.Context, runIDs []string)
	// MCP wires the MCP tools-only client stores for Run freezing.
	MCP *api.MCPServer
}

// freezeMCPIntoRegistry freezes the Run's MCP snapshots (atomic server+tool
// rows), registers frozen McpTool adapters, and returns a per-Run connection
// set. Required server failures abort Run initialization; optional failures
// freeze an unavailable snapshot and continue. Returns nil connection set when
// MCP is disabled.
//
// allowlist filters exposed MCP tools to the exact intersection with a Role's
// allowedTools (delegation children): a Role never sees MCP tools outside its
// frozen allowlist. nil allowlist means no filtering (Host runs).
func (e *agentExecutor) freezeMCPIntoRegistry(ctx context.Context, run *domain.AgentRun,
	session *domain.Session, toolReg *tools.Registry, allowlist []string) (*mcpclient.RunConnectionSet, error) {
	if e.MCP == nil {
		return nil, nil
	}
	allowed := map[string]bool{}
	if allowlist != nil {
		for _, name := range allowlist {
			allowed[name] = true
		}
	}
	// FreezeRun is idempotent per Run: on approval resume / rewind it reuses
	// the already-frozen snapshots verbatim, so a Run's capability set is
	// immutable even if bindings changed in between.
	servers, err := e.MCP.FreezeRun(ctx, run.ID, session.ProjectID)
	if err != nil {
		return nil, err
	}
	connSet := mcpclient.NewRunConnectionSet(run.ID)
	// tools/list_changed from any server marks the matching binding's FUTURE
	// catalog stale; the active Run's frozen Registry is never hot-updated.
	connSet.SetListChangedHandler(func() {
		ctx := context.Background()
		for _, server := range servers {
			if err := e.MCP.Catalogs.MarkCatalogStale(ctx, server.Snapshot.BindingID, 0); err != nil {
				slog.Warn("mark mcp catalog stale", "binding", server.Snapshot.BindingID, "error", err)
			}
		}
	})
	publisher := &mcpBytesPublisher{service: e.artifacts, projectID: session.ProjectID,
		sessionID: run.SessionID, runID: run.ID}
	recorder := &mcpRecorder{runs: e.MCP.Runs, runID: run.ID}
	for _, server := range servers {
		serverID := server.Snapshot.ID
		if server.Snapshot.UnavailableReason != "" {
			// Optional server unavailable: freeze the unavailable fact; no tools.
			continue
		}
		version := server.Version
		for _, frozen := range server.Tools {
			toolID := frozen.ID
			remoteName := frozen.RemoteName
			exposedName := frozen.ExposedName
			// Delegation child: expose only the exact intersection with the
			// Role's frozen allowlist. Host runs (nil allowlist) expose all
			// binding-selected tools.
			if allowlist != nil && !allowed[exposedName] {
				continue
			}
			definition := domain.ToolDefinition{
				Name:        exposedName,
				Description: frozen.Description,
				Parameters:  frozen.InputSchema,
				RiskClass:   frozen.RiskClass,
			}
			mcpTool := &mcpclient.Tool{
				DefinitionSnapshot: definition,
				ServerSlug:         exposedName,
				RemoteName:         remoteName,
				Recorder:           recorder,
				Publisher:          publisher,
				RunServerID:        serverID,
				RunToolID:          toolID,
				ProfileVersionID:   server.Snapshot.ProfileVersionID,
				ProjectID:          session.ProjectID,
				BindingID:          server.Snapshot.BindingID,
				BindingRevision:    server.Snapshot.BindingRevision,
				CatalogDigest:      server.Snapshot.CatalogDigest,
				SchemaDigest:       frozen.SchemaDigest,
				GenerationProvider: func() int { return connSet.CurrentGeneration(serverID) },
				ConnectionProvider: func() *mcpclient.Session {
					sess, _, err := connSet.GetOrConnect(ctx, serverID, version, e.mcpConnectOption(), slog.Default())
					if err != nil {
						slog.Warn("mcp reconnect failed", "run", run.ID, "server", serverID, "error", err)
						return nil
					}
					return sess
				},
			}
			if err := toolReg.Register(mcpTool); err != nil {
				connSet.Close()
				return nil, fmt.Errorf("register mcp tool %s: %w", exposedName, err)
			}
		}
	}
	return connSet, nil
}

func (e *agentExecutor) mcpConnectOption() mcpclient.ConnectOption {
	return mcpclient.ConnectOption{
		ResolveSecret:          e.MCP.ResolveSecret,
		Logger:                 slog.Default(),
		AllowedPrivateNetworks: e.MCP.AllowedPrivate,
	}
}

func (e *agentExecutor) Execute(ctx context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
	if run.RunKind == domain.RunKindContextCompaction {
		return e.executeContextCompaction(ctx, run)
	}
	if run.RunKind == domain.RunKindDelegatedAgent {
		return e.executeDelegatedChild(ctx, run)
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
		// Validate the digest-version matrix (v3/v4 legacy → V1, v5 → V2).
		switch {
		case resumeState.Version <= 4:
			if resumeState.ApprovalDigestVersion != 0 && resumeState.ApprovalDigestVersion != agent.ApprovalDigestV1 {
				return domain.RunOutput{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
					fmt.Errorf("legacy checkpoint %d carries unsupported digest version %d",
						resumeState.Version, resumeState.ApprovalDigestVersion))
			}
			resumeState.ApprovalDigestVersion = agent.ApprovalDigestV1
		case resumeState.Version == agent.ResumeStateVersion:
			if resumeState.ApprovalDigestVersion != agent.ApprovalDigestV2 {
				return domain.RunOutput{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
					fmt.Errorf("checkpoint version %d requires digest version %d, got %d",
						resumeState.Version, agent.ApprovalDigestV2, resumeState.ApprovalDigestVersion))
			}
		default:
			return domain.RunOutput{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid,
				fmt.Errorf("unsupported checkpoint version %d", resumeState.Version))
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
	var history []domain.Message
	if run.CommitFormatVersion == domain.CommitFormatSpeakerV2 {
		projected, projectErr := (&store.ContextProjector{DB: e.db}).ProjectAndFreeze(ctx, *run)
		if projectErr != nil {
			return domain.RunOutput{}, fmt.Errorf("project target-aware context: %w", projectErr)
		}
		history = projected.Messages
	} else {
		history, err = e.msgRepo.Lineage(ctx, run.SessionID, run.BaseMessageID)
		if err != nil {
			return domain.RunOutput{}, fmt.Errorf("load message history: %w", err)
		}
	}

	provider, err := e.resolveProvider(resolved)
	if err != nil {
		return domain.RunOutput{}, err
	}
	router := &agent.SnapshotModelRouter{Factory: e.resolveRuntimeProvider}

	wDir := filepath.Join(os.Getenv("ENNOTE_HOME"), "runtime", "runs", run.ID)
	ioDir := filepath.Join(wDir, "io")
	if err := os.MkdirAll(ioDir, 0o700); err != nil {
		return domain.RunOutput{}, fmt.Errorf("create runtime io dir: %w", err)
	}
	snapDir := filepath.Join(wDir, "skills")

	baseSystemPrompt := agent.BaseSystemPrompt(resolved.SystemPrompt.AgentPrompt)
	if resolved.Effective.Role != nil {
		baseSystemPrompt = agent.RoleSystemPrompt(*resolved.Effective.Role, resolved.SystemPrompt.AgentPrompt)
	}
	systemPrompt := baseSystemPrompt
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
		// Load project context files (AGENTS/MEMORY). This is independent of
		// skill catalog availability and must always run for a fresh run.
		projCtx, loadErr := e.loadProjectContext(canonicalRoot, trusted)
		if loadErr != nil {
			slog.Warn("load project context failed", "error", loadErr)
			projCtx = &projectcontext.Context{}
		}

		// Check if read is allowed by frozen tool policy
		allowRead := agent.AllowsTool(resolved.Effective.ToolPolicy.Config, "read") &&
			(resolved.Effective.Role == nil || slices.Contains(resolved.Effective.Role.AllowedTools, "read"))
		catalogPrompt := ""
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
			vars := skills.TemplateVars{Mode: "bwrap", Workspace: "/workspace", SkillDir: "/skills"}
			if sandboxMode == workspace.SandboxNone {
				// For none mode, skill_dir uses the absolute host path
				absSnapDir, _ := filepath.Abs(snapDir)
				vars = skills.TemplateVars{Mode: "none", Workspace: ".", SkillDir: absSnapDir}
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
				}
			}
			catalogPrompt = skills.BuildCatalogPrompt(catalog, 16*1024)
		} else {
			skillCatalogState = "disabled"
			slog.Info("read tool not allowed by policy, skipping skill catalog")
		}

		systemPrompt = projCtx.BuildPrompt(baseSystemPrompt, catalogPrompt)
		if preloaded := e.rolePreloadPrompt(resolved.Effective.Role); preloaded != "" {
			systemPrompt += preloaded
		}
	}

	var wManager *workspace.Manager
	if skillCatalogState == "disabled" {
		// Read is statically denied: register no /skills mount and do not
		// create an empty snapshot directory.
		wManager, err = workspace.NewManager(canonicalRoot, ioDir, "", workspace.SandboxMode(e.sandbox))
	} else {
		wManager, err = workspace.NewManagerWithSkills(canonicalRoot, ioDir, snapDir, workspace.SandboxMode(e.sandbox))
	}
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
	// Register delegate_tasks with the Host delegation provider, plus its
	// legacy delegate_roles alias so runs started before the rename can replay
	// persisted tool calls and approval records. Models only ever see
	// delegate_tasks (aliases are hidden by the Registry).
	delegateTasks := &tools.DelegateTasksTool{Provider: e.delegateProvider(run), RunID: run.ID, SessionID: run.SessionID}
	if err := toolReg.Register(delegateTasks); err != nil {
		return domain.RunOutput{}, fmt.Errorf("register delegate_tasks: %w", err)
	}
	if err := toolReg.RegisterAlias("delegate_roles", "delegate_tasks", delegateTasks.LegacySchema()); err != nil {
		return domain.RunOutput{}, fmt.Errorf("register delegate_roles alias: %w", err)
	}
	if resolved.Effective.Role != nil {
		toolReg.Restrict(resolved.Effective.Role.AllowedTools)
	}

	// Freeze MCP server/tool snapshots BEFORE the first Provider request and
	// register the frozen McpTool adapters. Required server failures abort the
	// Run; optional servers freeze an unavailable snapshot and continue.
	mcpConnSet, mcpErr := e.freezeMCPIntoRegistry(ctx, run, session, toolReg, nil)
	if mcpErr != nil {
		return domain.RunOutput{}, mcpErr
	}
	if mcpConnSet != nil {
		defer mcpConnSet.Close()
	}

	prepared, err := e.compaction.Prepare(ctx, run, history, resolved.Effective, systemPrompt, toolReg.Definitions())
	if err != nil {
		return domain.RunOutput{}, err
	}
	chatHistory := prepared.Messages
	// Restore the private parent transcript after delegated children settle.
	// It carries the original assistant tool_call followed by the folded result.
	chatHistory, resumeMessages, err := e.injectToolCallResults(ctx, run.ID, chatHistory)
	if err != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorTranscriptCorrupt, err)
	}
	var compactionConfig domain.CompactionPolicyConfig
	if err := json.Unmarshal(resolved.Effective.CompactionPolicy.Config, &compactionConfig); err != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, err)
	}
	var historyTool *tools.CompactedHistoryTool
	roleAllowsHistory := resolved.Effective.Role == nil || slices.Contains(resolved.Effective.Role.AllowedTools, "search_compacted_history")
	if roleAllowsHistory && compactionConfig.AllowHistoryLookup &&
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
	// BuiltinToolPolicy must observe the FINAL effective tool set (after Role
	// Restrict and conditional registration), so it is constructed from the
	// completed Registry. Unknown/restricted tools resolve to RiskSensitive.
	tp, policyErr := agent.NewBuiltinToolPolicy(resolved.Effective.ToolPolicy, toolReg)
	if policyErr != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorToolPolicyFailed, policyErr)
	}
	var effectiveToolPolicy agent.ToolPolicy = tp
	if resolved.Effective.Role == nil {
		effectiveToolPolicy = &delegationAdmissionToolPolicy{Base: tp,
			Delegations: &store.DelegationRepo{DB: e.db}, SessionID: run.SessionID}
	}
	loop := &agent.Loop{
		Provider: provider, ModelRouter: router, TurnPlanner: agent.ContextTurnPlanner{},
		MidRunCompactor:   runCompactor,
		VisionResolver:    &agent.BuiltinVisionResolver{Loader: e.artifacts},
		ImageDescriptions: &store.ImageDescriptionRepo{DB: e.db},
		Tools:             toolReg, ToolPolicy: effectiveToolPolicy, ToolPolicySnapshot: resolved.Effective.ToolPolicy,
		StandingScopeResolver: toolReg, StandingApprovals: e.standingApprovals,
		WorkspaceID: wSpace.ID, SessionID: session.ID, Events: e.writer, Hub: e.hub, Recorder: e.calls,
		QueuedInputs: &queueAdapter{repo: &store.QueueRepo{DB: e.db}},
		SteeringMode: domain.QueueOneAtATime, FollowUpMode: domain.QueueOneAtATime,
		MaxIterations:      resolved.Effective.MaxIterations,
		ContextTokens:      resolved.Effective.ContextTokens,
		MaxOutput:          resolved.Effective.MaxOutputTokens,
		ToolExecution:      resolved.Effective.ToolExecution,
		TodoStore:          todoStore,
		DelegationDetector: e.runs,
		Reminders: agent.NewReminderRegistry(
			&agent.TodoReminderProvider{Store: todoStore},
			&agent.BudgetReminderProvider{},
		),
	}
	if resumeState != nil {
		// Resume pins the digest version and replays the frozen standing
		// authorization snapshot instead of querying live rules.
		loop.ApprovalDigestVersion = resumeState.ApprovalDigestVersion
		loop.StandingAuthorizationSnapshot = resumeState.StandingAuthorizations
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
	// Determine request generation: Claim count on this run from run_started
	// events. Re-claims (e.g. parent resume after waiting_children) must start
	// at generation = count-1 to avoid duplicate model_calls entries.
	var claimCount int
	if err := e.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM run_events WHERE run_id=? AND event_type='run_started'`,
		run.ID).Scan(&claimCount); err != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorEventPersistence, err)
	}
	claimGen := max(0, claimCount-1)

	result, err := loop.Run(ctx, agent.RunInput{
		RunID: run.ID, Model: resolved.Effective.APIModel,
		ProviderProfileID: resolved.Effective.ProviderProfileID,
		ModelProfileID:    resolved.Effective.ModelProfileID,
		RequestedConfig:   run.RequestedConfig, EffectiveConfig: run.EffectiveConfig,
		InitialRuntime: resolved.Effective.InitialRuntime, Routing: resolved.Effective.Routing,
		VisionPolicy: resolved.Effective.VisionPolicy, ThinkingEffort: resolved.Effective.ThinkingEffort,
		SystemPrompt: systemPrompt, History: chatHistory, OverflowRecovery: overflowRecovery,
		Resume: resumeState, Approval: approvalResolution,
		SkillCatalogState: skillCatalogState, SkillCatalogDigest: skillCatalogDigest,
		RequestGeneration: claimGen,
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
				encoded, approvalRequired.Items, approvalRequired.StandingCandidates); suspendErr != nil {
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
	outputMessages := result.Generated
	if len(resumeMessages) != 0 {
		outputMessages = make([]domain.ChatMessage, 0, len(resumeMessages)+len(result.Generated))
		outputMessages = append(outputMessages, resumeMessages...)
		outputMessages = append(outputMessages, result.Generated...)
	}
	if result.Waiting {
		// Persist the complete generated transcript so parent resume preserves the
		// provider protocol: assistant tool_call immediately followed by tool result.
		persistCtx := context.WithoutCancel(ctx)
		tx, txErr := e.db.BeginTx(persistCtx, nil)
		if txErr != nil {
			return domain.RunOutput{}, domain.NewCodedError(domain.ErrorEventPersistence, txErr)
		}
		if _, _, appendErr := store.AppendRunMessagesTx(persistCtx, tx, run.ID,
			run.CommitFormatVersion, outputMessages, time.Now().UTC()); appendErr != nil {
			_ = tx.Rollback()
			return domain.RunOutput{}, domain.NewCodedError(domain.ErrorEventPersistence, appendErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return domain.RunOutput{}, domain.NewCodedError(domain.ErrorEventPersistence, commitErr)
		}
		e.enqueueQueuedChildren(persistCtx, run.ID)
	}
	return domain.RunOutput{Messages: outputMessages, Waiting: result.Waiting}, nil
}

// enqueueQueuedChildren finds queued child runs for this parent whose task
// dependencies are satisfied (dynamic task graph readiness) and notifies the
// coordinator so children start executing. Dependent tasks whose dependencies
// have not settled stay queued and are woken by the coordinator's
// enqueueReadySuccessors when their dependencies settle.
func (e *agentExecutor) enqueueQueuedChildren(ctx context.Context, parentRunID string) {
	if e.OnChildRunsCreated == nil {
		return
	}
	rows, err := e.db.QueryContext(ctx,
		`SELECT id FROM agent_runs WHERE parent_run_id=? AND status='queued'`, parentRunID)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil || closeErr != nil {
		return
	}
	if len(ids) > 0 {
		ready, filterErr := (&store.DelegationRepo{DB: e.db}).ReadyChildrenForEnqueue(ctx, ids)
		if filterErr != nil {
			slog.Warn("filter queued children by task readiness", "parentRunID", parentRunID, "error", filterErr)
			return
		}
		if len(ready) > 0 {
			e.OnChildRunsCreated(ctx, ready)
		}
	}
}

// executeDelegatedChild runs a private delegated_agent child: it resolves the
// frozen Role version from the delegation item, builds a task_only context from
// the assignment, restricts tools to the Role allowlist plus submit_result, and
// requires the terminal contract to end the Run.
func (e *agentExecutor) executeDelegatedChild(ctx context.Context, run *domain.AgentRun) (domain.RunOutput, error) {
	budget := &store.DelegationRepo{DB: e.db}
	remainingWall, err := budget.BeginBudget(ctx, run.ID)
	if err != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorDelegationBudgetExceeded, err)
	}
	if remainingWall > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remainingWall)
		defer cancel()
	}
	resolved, err := e.runs.ResolveAndFreezeConfig(ctx, run)
	if err != nil {
		return domain.RunOutput{}, err
	}
	if resolved.Effective.Role == nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
			errors.New("child Run has no frozen Role execution"))
	}
	assignment, err := (&store.DelegationRepo{DB: e.db}).AssignmentForChild(ctx, run.ID)
	if err != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
			fmt.Errorf("load child assignment: %w", err))
	}

	session, err := e.sessionDB.FindByID(ctx, run.SessionID)
	if err != nil || session == nil {
		return domain.RunOutput{}, fmt.Errorf("load session: %w", err)
	}
	wSpace, err := loadProjectWorkspace(ctx, e.db, run.SessionID)
	if err != nil {
		return domain.RunOutput{}, fmt.Errorf("load project: %w", err)
	}
	canonicalRoot, err := workspace.CanonicalWorkspaceRoot(wSpace.HostPath)
	if err != nil {
		return domain.RunOutput{}, fmt.Errorf("canonical workspace root: %w", err)
	}
	trusted, trustErr := e.trustStore.IsTrusted(wSpace.ID, canonicalRoot)
	if trustErr != nil {
		return domain.RunOutput{}, fmt.Errorf("check workspace trust: %w", trustErr)
	}

	provider, err := e.resolveProvider(resolved)
	if err != nil {
		return domain.RunOutput{}, err
	}
	router := &agent.SnapshotModelRouter{Factory: e.resolveRuntimeProvider}

	wDir := filepath.Join(os.Getenv("ENNOTE_HOME"), "runtime", "runs", run.ID)
	ioDir := filepath.Join(wDir, "io")
	if err := os.MkdirAll(ioDir, 0o700); err != nil {
		return domain.RunOutput{}, fmt.Errorf("create runtime io dir: %w", err)
	}
	var wManager *workspace.Manager
	if trusted {
		wManager, err = workspace.NewManager(canonicalRoot, ioDir, "", workspace.SandboxMode(e.sandbox))
	} else {
		wManager, err = workspace.NewManager(canonicalRoot, ioDir, "", workspace.SandboxMode(e.sandbox))
	}
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
	toolReg.Restrict(resolved.Effective.Role.AllowedTools)
	if err := toolReg.Register(&tools.SubmitResultTool{}); err != nil {
		return domain.RunOutput{}, fmt.Errorf("register submit_result: %w", err)
	}

	// Freeze MCP tools for the child: only the exact intersection with the
	// Role's frozen allowlist is exposed. A Role that does not list MCP tool
	// names (the default) receives none — fail closed.
	childMCPSet, mcpErr := e.freezeMCPIntoRegistry(ctx, run, session, toolReg, resolved.Effective.Role.AllowedTools)
	if mcpErr != nil {
		return domain.RunOutput{}, mcpErr
	}
	if childMCPSet != nil {
		defer childMCPSet.Close()
	}

	systemPrompt := agent.RoleSystemPrompt(*resolved.Effective.Role, resolved.SystemPrompt.AgentPrompt)
	// task_only context: the frozen assignment is the only history, except for
	// continuation children, which replay the exact source attempt's private
	// transcript plus one explicit user instruction.
	delegationRepo := &store.DelegationRepo{DB: e.db}
	seed, seedErr := delegationRepo.ContinuationSeedForChild(ctx, run.ID)
	if seedErr != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorTranscriptCorrupt,
			fmt.Errorf("load continuation seed: %w", seedErr))
	}
	var chatHistory []domain.ChatMessage
	if seed != nil {
		chatHistory = append(chatHistory, seed.Transcript...)
		chatHistory = append(chatHistory, domain.ChatMessage{
			Role: domain.RoleUser,
			Content: []domain.ContentBlock{{
				Kind: domain.ContentText,
				Text: "Continue the previous work. Additional instruction: " + seed.Instruction,
			}},
		})
	} else {
		chatHistory = []domain.ChatMessage{{
			Role: domain.RoleUser,
			Content: []domain.ContentBlock{{
				Kind: domain.ContentText,
				Text: "Task assignment: " + string(assignment),
			}},
		}}
	}

	// BuiltinToolPolicy observes the FINAL child tool set: todo registered,
	// Role Restrict applied, then submit_result registered.
	tp, policyErr := agent.NewBuiltinToolPolicy(resolved.Effective.ToolPolicy, toolReg)
	if policyErr != nil {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorToolPolicyFailed, policyErr)
	}
	loop := &agent.Loop{
		Provider: provider, ModelRouter: router, TurnPlanner: agent.ContextTurnPlanner{},
		VisionResolver:    &agent.BuiltinVisionResolver{Loader: e.artifacts},
		ImageDescriptions: &store.ImageDescriptionRepo{DB: e.db},
		Tools:             toolReg, ToolPolicy: tp, ToolPolicySnapshot: resolved.Effective.ToolPolicy,
		StandingScopeResolver: toolReg, StandingApprovals: e.standingApprovals,
		WorkspaceID: wSpace.ID, SessionID: session.ID, Events: e.writer, Hub: e.hub, Recorder: e.calls,
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
		SubmitResultGate: &agent.SubmitResultGate{}, BudgetController: budget,
	}
	if e.hub != nil {
		loop.LivePublisher = e.livePublisherFor(run)
	}

	result, runErr := loop.Run(ctx, agent.RunInput{
		RunID: run.ID, Model: resolved.Effective.APIModel,
		ProviderProfileID: resolved.Effective.ProviderProfileID,
		ModelProfileID:    resolved.Effective.ModelProfileID,
		RequestedConfig:   run.RequestedConfig, EffectiveConfig: run.EffectiveConfig,
		InitialRuntime: resolved.Effective.InitialRuntime, Routing: resolved.Effective.Routing,
		VisionPolicy: resolved.Effective.VisionPolicy, ThinkingEffort: resolved.Effective.ThinkingEffort,
		SystemPrompt: systemPrompt, History: chatHistory,
	})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorDelegationBudgetExceeded,
			errors.New("delegated child wall-time budget exceeded"))
	}
	if runErr != nil {
		var approvalRequired *agent.ApprovalRequiredError
		if errors.As(runErr, &approvalRequired) && e.approvals != nil {
			encoded, encodeErr := json.Marshal(approvalRequired.State)
			if encodeErr != nil {
				return domain.RunOutput{}, domain.NewCodedError(domain.ErrorApprovalCheckpointInvalid, encodeErr)
			}
			if _, suspendErr := e.approvals.Suspend(context.WithoutCancel(ctx), run.ID,
				agent.ResumeStateVersion, approvalRequired.State.Iteration, approvalRequired.BatchDigest,
				encoded, approvalRequired.Items, approvalRequired.StandingCandidates); suspendErr != nil {
				return domain.RunOutput{}, domain.NewCodedError(domain.ErrorEventPersistence, suspendErr)
			}
			return domain.RunOutput{Suspended: true}, nil
		}
		return domain.RunOutput{}, runErr
	}
	if loop.SubmitResultGate.Result == nil {
		// The model stopped without calling submit_result: the terminal contract
		// is incomplete. V1 fails closed; the parent sees a failed child item.
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorIncompleteTerminalContract,
			errors.New("child Run ended without calling submit_result"))
	}
	if resolved.Effective.Role.OutputContract == "structured-v1" && len(loop.SubmitResultGate.Result.Payload) == 0 {
		return domain.RunOutput{}, domain.NewCodedError(domain.ErrorIncompleteTerminalContract,
			errors.New("structured-v1 child result requires an object payload"))
	}
	return domain.RunOutput{Messages: result.Generated, Terminal: loop.SubmitResultGate.Result}, nil
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

// injectToolCallResults appends the Waiting run's private transcript after the
// canonical history. RunMessageRepo performs the folded-result projection by
// provider-visible tool_call_id and closes SQLite rows between queries.
func (e *agentExecutor) injectToolCallResults(ctx context.Context, runID string,
	history []domain.ChatMessage) ([]domain.ChatMessage, []domain.ChatMessage, error) {
	resumeMessages, err := (&store.RunMessageRepo{DB: e.db}).ResumeMessages(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	return append(history, resumeMessages...), resumeMessages, nil
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

// delegateProvider creates a DelegateRolesProvider that executes delegation
// via the Store's DelegationRepo and RunRepo.
func (e *agentExecutor) delegateProvider(run *domain.AgentRun) *agentExecutorDelegationProvider {
	return &agentExecutorDelegationProvider{
		db: e.db, runs: e.runs, sessions: e.sessionDB,
		runID: run.ID, sessionID: run.SessionID,
	}
}

type delegationAdmissionToolPolicy struct {
	Base        agent.ToolPolicy
	Delegations *store.DelegationRepo
	SessionID   string
}

func (p *delegationAdmissionToolPolicy) BeforeToolBatch(ctx context.Context, batch agent.ToolBatchContext,
	calls []domain.ToolCall) ([]agent.ToolDecision, error) {
	decisions, err := p.Base.BeforeToolBatch(ctx, batch, calls)
	if err != nil {
		return nil, err
	}
	for index, call := range calls {
		if !domain.IsDelegationToolName(call.Name) || index >= len(decisions) || decisions[index].Action == agent.ToolTerminateBatch {
			continue
		}
		var input struct {
			Tasks       []domain.TaskSpec `json:"tasks"`
			Delegations []domain.TaskSpec `json:"delegations"` // legacy replay compat
		}
		effectiveArguments := call.Arguments
		if len(decisions[index].Arguments) > 0 {
			effectiveArguments = decisions[index].Arguments
		}
		if json.Unmarshal(effectiveArguments, &input) != nil {
			continue // Registry schema validation reports malformed arguments.
		}
		specs := input.Tasks
		if len(specs) == 0 {
			specs = input.Delegations
		}
		if len(specs) == 0 {
			continue
		}
		requiresApproval := false
		denial := ""
		for specIndex := range specs {
			spec := &specs[specIndex]
			spec.Normalize()
			snapshot, resolveErr := p.Delegations.ResolveRoleForDelegation(ctx, p.SessionID, spec.Role)
			if errors.Is(resolveErr, store.ErrDelegationRoleUnavailable) {
				denial = fmt.Sprintf("Role %q is unavailable in this project", spec.RoleHandle)
				break
			}
			if resolveErr != nil {
				return nil, resolveErr
			}
			if !snapshot.DelegationEnabled || snapshot.Definition.DelegationPolicy.Admission == domain.DelegationDenied {
				denial = fmt.Sprintf("Role %q does not allow Host delegation", spec.RoleHandle)
				break
			}
			spec.RoleVersionID = snapshot.VersionID
			requiresApproval = requiresApproval ||
				snapshot.Definition.DelegationPolicy.Admission == domain.DelegationApprovalRequired
		}
		if denial != "" {
			decisions[index] = agent.ToolDecision{Action: agent.ToolDeny, Code: string(domain.ErrorDelegationNotAuthorized),
				Reason: denial, RiskClass: domain.RiskDelegation}
			continue
		}
		input.Tasks = specs
		input.Delegations = nil
		encodedArguments, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		decisions[index].Arguments = encodedArguments
		if requiresApproval && decisions[index].Action == agent.ToolAllow {
			decisions[index].Action = agent.ToolRequireApproval
			decisions[index].Code = "role_delegation_approval_required"
			decisions[index].Reason = "The selected Role requires delegation approval"
			decisions[index].RiskClass = domain.RiskDelegation
		}
	}
	return decisions, nil
}

func (p *delegationAdmissionToolPolicy) AfterToolCall(ctx context.Context, callCtx agent.ToolCallContext,
	call domain.ToolCall, result domain.ToolResult) (agent.AfterToolDecision, error) {
	return p.Base.AfterToolCall(ctx, callCtx, call, result)
}

type agentExecutorDelegationProvider struct {
	db        *sql.DB
	runs      *store.RunRepo
	sessions  *store.SessionRepo
	runID     string
	sessionID string
}

func delegationStrategy(itemCount int) domain.DelegationStrategy {
	if itemCount > 1 {
		return domain.DelegationStrategyParallel
	}
	return domain.DelegationStrategySingle
}

func effectiveDelegationBudget(request domain.BudgetCeilingJSON, ceiling domain.DelegationBudgetCeiling) domain.BudgetCeilingJSON {
	if request.MaxModelCalls == 0 {
		request.MaxModelCalls = ceiling.MaxModelCalls
	}
	if request.MaxToolCalls == 0 {
		request.MaxToolCalls = ceiling.MaxToolCalls
	}
	if request.MaxTotalTokens == 0 {
		request.MaxTotalTokens = ceiling.MaxTotalTokens
	}
	if request.MaxOutputTokens == 0 {
		request.MaxOutputTokens = ceiling.MaxOutputTokens
	}
	if request.MaxWallTimeMS == 0 {
		request.MaxWallTimeMS = ceiling.MaxWallTimeMS
	}
	// A zero cost request means no monetary cap. Unlike token and wall limits,
	// it cannot safely inherit when the chosen model has no pricing metadata.
	return request
}

func (p *agentExecutorDelegationProvider) ExecuteDelegation(ctx context.Context, runID, sessionID, toolCallID string, specs []domain.TaskSpec) (*tools.DelegateTasksResult, error) {
	delegations := &store.DelegationRepo{DB: p.db}

	executionMode := domain.DelegationExecutionBlocking
	autoResume := false
	// Background mode and auto-resume are frozen with the group; admission
	// digests them so the client cannot alter them after approval.
	if mode, ok := ctx.Value(tools.DelegateExecutionModeKey).(string); ok && mode == string(domain.DelegationExecutionBackground) {
		executionMode = domain.DelegationExecutionBackground
	}
	if resume, ok := ctx.Value(tools.DelegateAutoResumeKey).(bool); ok {
		autoResume = resume
	}

	type resolvedSpec struct {
		spec     domain.TaskSpec
		snapshot *store.DelegationRoleSnapshot
	}
	var resolved []resolvedSpec
	for _, spec := range specs {
		spec.Normalize()
		snapshot, err := delegations.ResolveRoleForDelegation(ctx, sessionID, spec.Role)
		if err != nil {
			return nil, fmt.Errorf("resolve role %q: %w", spec.Role, err)
		}
		if spec.RoleVersionID == "" || spec.RoleVersionID != snapshot.VersionID {
			return nil, fmt.Errorf("role %q version changed after delegation admission", spec.Role)
		}
		spec.Budget = effectiveDelegationBudget(spec.Budget, snapshot.Definition.DelegationPolicy.BudgetCeiling)
		if spec.OutputContract == "" {
			spec.OutputContract = snapshot.Definition.OutputContract
		}
		resolved = append(resolved, resolvedSpec{spec: spec, snapshot: snapshot})
	}
	admissionApproved, err := delegations.DelegationToolCallApproved(ctx, runID, toolCallID)
	if err != nil {
		return nil, fmt.Errorf("resolve delegation approval: %w", err)
	}

	items := make([]store.CreateDelegationItemInput, len(resolved))
	for i, r := range resolved {
		outputContract := r.spec.OutputContract
		if outputContract == "" {
			outputContract = "text-v1"
		}
		items[i] = store.CreateDelegationItemInput{
			Name:           r.spec.Name,
			RoleVersionID:  r.snapshot.VersionID,
			AssignmentJSON: json.RawMessage(fmt.Sprintf(`{"task":%q}`, r.spec.Goal)),
			OutputContract: outputContract,
			Budget:         r.spec.Budget,
			Depends:        r.spec.Depends,
		}
	}

	group, groupItems, children, err := delegations.CreateGroupWithChildren(ctx, store.CreateDelegationGroupInput{
		ParentRunID: runID, ParentToolCallID: toolCallID,
		Strategy:          delegationStrategy(len(items)),
		Items:             items,
		ExecutionMode:     executionMode,
		AutoResume:        autoResume,
		AdmissionApproved: admissionApproved,
	}, sessionID)
	if err != nil {
		return nil, fmt.Errorf("materialize delegation tree: %w", err)
	}
	result := &tools.DelegateTasksResult{Status: "delegated", GroupID: group.ID, ExecutionMode: string(executionMode)}
	if executionMode == domain.DelegationExecutionBackground {
		handle, handleErr := delegations.HandleForGroup(ctx, group.ID)
		if handleErr != nil {
			return nil, fmt.Errorf("resolve delegation handle: %w", handleErr)
		}
		result.Status = "accepted"
		result.HandleID = handle.ID
	}
	for index, item := range groupItems {
		result.Items = append(result.Items, tools.DelegateTasksItemResult{
			Name: item.Name, ItemID: item.ID, ChildRunID: children[index].ID,
		})
	}
	return result, nil
}

// livePublisherFor returns the live delta publisher for a Run. Delegated
// children additionally translate their tool/turn activity into live
// child_progress events on the parent run's channel (bounded, non-durable) so
// the parent surface can render per-task activity without a second SSE
// subscription. Returns nil when no Hub is configured.
func (e *agentExecutor) livePublisherFor(run *domain.AgentRun) events.LivePublisher {
	if e.hub == nil {
		return nil
	}
	if run.RunKind != domain.RunKindDelegatedAgent || run.ParentRunID == "" {
		return e.hub
	}
	var groupID, taskName string
	if err := e.db.QueryRow(`SELECT i.group_id,i.name FROM delegation_item_attempts a
		JOIN delegation_items i ON i.id=a.item_id
		WHERE a.child_run_id=?`, run.ID).Scan(&groupID, &taskName); err != nil {
		return e.hub // not a resolvable delegation child: plain forwarding
	}
	return &childProgressPublisher{
		hub: e.hub, childRunID: run.ID, parentRunID: run.ParentRunID,
		groupID: groupID, taskName: taskName, reported: make(map[string]struct{}),
	}
}

// rolePreloadPrompt returns the frozen preload Skill prompts for a Role as an
// inline system-prompt fragment. Preload Skills are data injected at execution
// time and do not require filesystem access, so they remain available even when
// the read tool is not in the Role allowlist.
func (e *agentExecutor) rolePreloadPrompt(role *domain.FrozenRoleExecution) string {
	if role == nil {
		return ""
	}
	var preloads []string
	for _, entry := range role.Skills.Entries {
		if entry.Mode == domain.RoleSkillPreload {
			preloads = append(preloads, entry.SkillID)
		}
	}
	if len(preloads) == 0 {
		return ""
	}
	loaded := make(map[string]*skills.LoadedSkill)
	for _, skill := range skills.Discover(e.skillsDir, e.builtinDir) {
		loaded[skill.Manifest.ID] = skill
	}
	var builder strings.Builder
	for _, id := range preloads {
		skill, ok := loaded[id]
		if !ok {
			// Publish validation already guarantees the Skill exists; skip
			// defensively if the on-disk catalog changed since publication.
			continue
		}
		builder.WriteString("\n\n<preloaded_skill id=\"")
		builder.WriteString(id)
		builder.WriteString("\">\n")
		builder.WriteString(skill.PromptText)
		builder.WriteString("\n</preloaded_skill>")
	}
	return builder.String()
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

	canonicalRoot, _ := workspace.CanonicalWorkspaceRoot(wSpace.HostPath)
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
	canonicalRoot, err := workspace.CanonicalWorkspaceRoot(wSpace.HostPath)
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
	// Reap orphans AFTER RecoverActive: parents terminated by the recovery scan
	// (e.g. interrupted waiting_children) may leave children with a terminal
	// parent that this pass catches.
	if _, err := (&store.DelegationRepo{DB: db}).ReapOrphans(context.Background()); err != nil {
		return fmt.Errorf("reap orphaned children: %w", err)
	}
	// Settle any generation whose attempts are all terminal and re-reconcile
	// terminal child budgets (idempotent).
	if _, err := (&store.DelegationRepo{DB: db}).RecoverDelegation(context.Background()); err != nil {
		return fmt.Errorf("recover delegation state: %w", err)
	}
	// Reconstruct any lost completion/delivery projections from terminal facts.
	delegationRepo := &store.DelegationRepo{DB: db}
	if _, err := delegationRepo.RebuildMissingCompletions(context.Background(), 100); err != nil {
		return fmt.Errorf("rebuild delegation completions: %w", err)
	}
	if _, err := delegationRepo.RebuildMissingDeliveryEvents(context.Background(), 100); err != nil {
		return fmt.Errorf("rebuild delivery events: %w", err)
	}
	// Reconstruct missing cross-session attention projections from source facts.
	if _, err := (&store.AttentionRepo{DB: db}).RebuildAttention(context.Background(), 100); err != nil {
		return fmt.Errorf("rebuild attention projections: %w", err)
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
	mcpServer := &api.MCPServer{
		Profiles: &store.MCPProfileRepo{DB: db},
		Bindings: &store.MCPBindingRepo{DB: db},
		Catalogs: &store.MCPCatalogRepo{DB: db},
		Runs:     &store.MCPRunRepo{DB: db},
		ResolveSecret: func(ref string) (string, error) {
			secret, err := (&llm.CredentialResolver{}).Resolve(ref)
			if err != nil {
				return "", err
			}
			return secret.Reveal(), nil
		},
		Logger:         slog.Default(),
		AllowedPrivate: false,
		Bundled:        mcpclient.NewBundledRegistry(),
	}
	executor := &agentExecutor{
		db: db, writer: eventWriter, hub: hub, homeDir: cfg.HomeDir, trustStore: trustStore, outboxStore: outboxStore, runs: runRepo,
		calls:     callRepo,
		sessionDB: &store.SessionRepo{DB: db}, msgRepo: &store.MessageRepo{DB: db},
		skillRepo: &store.SkillSnapshotRepo{DB: db},
		skillsDir: cfg.SkillsDir, builtinDir: cfg.BuiltinSkillsDir,
		sandbox: cfg.SandboxMode, artifacts: artifactService, approvals: approvalRepo,
		standingApprovals: &store.StandingApprovalRepo{DB: db},
		MCP:               mcpServer,
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
	coordinator.SetRunSettledHook(func(ctx context.Context, run *domain.AgentRun) error {
		return tickSessionAutoResume(ctx, db, coordinator, run.SessionID)
	})
	// Wire child run enqueuing: when delegate_roles creates children,
	// the executor notifies the coordinator to start executing them.
	executor.OnChildRunsCreated = func(_ context.Context, ids []string) {
		for _, id := range ids {
			// A child is durable work. Do not derive its scheduler lifetime from
			// the parent claim context, which is cancelled when the parent yields.
			if err := coordinator.Enqueue(context.Background(), id); err != nil {
				slog.Warn("enqueue child run failed", "runID", id, "error", err)
			}
		}
	}
	// Agent Flow meta-Run orchestrator (roadmap item 7, Phase 1). The
	// orchestrator is a pure state machine: it dispatches one child Run per
	// task and never calls a Provider itself.
	flowSkillCatalog := make(map[string]string)
	for _, skill := range skills.Discover(cfg.SkillsDir, cfg.BuiltinSkillsDir) {
		flowSkillCatalog[skill.Manifest.ID] = skill.Manifest.ID
	}
	flowSkillKnown := make(map[string]bool)
	for name := range flowSkillCatalog {
		flowSkillKnown[name] = true
	}
	flowRuns := &store.AgentFlowRunRepo{DB: db, SkillCatalog: flowSkillCatalog}
	flowProfiles := &store.AgentFlowProfileRepo{DB: db}
	flowBindings := &store.AgentFlowBindingRepo{DB: db}
	checkRunner := &store.CheckTaskRunner{
		DB: db, MaxOutputBytes: 32 * 1024, DefaultTimeoutSeconds: 300,
		ManagerBuilder: func(ctx context.Context, sessionID string) (*workspace.Manager, error) {
			session, err := (&store.SessionRepo{DB: db}).FindByID(ctx, sessionID)
			if err != nil {
				return nil, fmt.Errorf("resolve check session: %w", err)
			}
			wSpace, err := (&store.ProjectRepo{DB: db}).FindWorkspaceByProjectID(ctx, session.ProjectID)
			if err != nil {
				return nil, fmt.Errorf("resolve check workspace: %w", err)
			}
			canonicalRoot, err := workspace.CanonicalWorkspaceRoot(wSpace.HostPath)
			if err != nil {
				return nil, fmt.Errorf("resolve canonical root: %w", err)
			}
			ioDir := filepath.Join(os.Getenv("ENNOTE_HOME"), "runtime", "flows", sessionID)
			if err := os.MkdirAll(ioDir, 0o700); err != nil {
				return nil, err
			}
			return workspace.NewManager(canonicalRoot, ioDir, "", workspace.SandboxMode(cfg.SandboxMode))
		},
	}
	flowOrchestrator := &agentflow.Orchestrator{
		Store:    &store.OrchestratorStore{Runs: flowRuns, Profiles: flowProfiles},
		Children: &store.OrchestratorChildren{DB: db, Delegations: &store.DelegationRepo{DB: db}},
		Events:   &store.FlowEventSink{Writer: eventWriter},
		Checker:  checkRunner,
		Enqueue: func(ctx context.Context, runID string) error {
			return coordinator.Enqueue(ctx, runID)
		},
		PollInterval: 250 * time.Millisecond,
	}
	flowStartRun := func(ctx context.Context, projectID, flowVersionID, sessionID string,
		inputs, vars map[string]any) (*domain.RunAgentFlow, error) {
		version, err := flowProfiles.GetVersion(ctx, flowVersionID)
		if err != nil {
			return nil, fmt.Errorf("load flow version: %w", err)
		}
		var def domain.FlowDefinition
		if err := json.Unmarshal(version.DefinitionJSON, &def); err != nil {
			return nil, fmt.Errorf("decode flow version: %w", err)
		}
		inputsJSON, err := store.NormalizeFlowInputs(&def, inputs, vars)
		if err != nil {
			return nil, err
		}
		freeze, diagnostics, err := flowRuns.FreezeFlowDefinition(ctx, projectID, &def, inputsJSON)
		if err != nil {
			return nil, fmt.Errorf("freeze flow: %v", append([]string(nil), diagnostics...))
		}
		run, err := flowRuns.CreateFlowRun(ctx, store.CreateFlowRunInput{
			SessionID: sessionID, ProjectID: projectID, FlowVersionID: flowVersionID, InputsJSON: inputsJSON,
		}, freeze)
		if err != nil {
			return nil, err
		}
		flowOrchestrator.Start(ctx, run.RunID)
		return run, nil
	}
	flowStartRecovered := func(ctx context.Context, runID string) error {
		flowOrchestrator.Start(ctx, runID)
		return nil
	}
	// Recover non-terminal meta-Runs after the delegation recovery passes have
	// settled interrupted children; the orchestrator re-dispatches only
	// incomplete tasks (checkpoint continuation).
	if recoveredFlows, flowErr := flowRuns.ListRecoverableRuns(context.Background()); flowErr == nil {
		for _, flowRunID := range recoveredFlows {
			flowOrchestrator.Start(context.Background(), flowRunID)
		}
		if len(recoveredFlows) > 0 {
			slog.Info("agent flow runs recovered", "count", len(recoveredFlows))
		}
	} else {
		slog.Warn("agent flow recovery scan failed", "error", flowErr)
	}
	// Filter recovered queued runs through task-graph readiness: a task-graph
	// child whose dependencies have not settled stays queued and is woken by
	// enqueueReadySuccessors when its dependencies settle; top-level runs and
	// ready children are re-enqueued now.
	recovered, filterErr := (&store.DelegationRepo{DB: db}).ReadyChildrenForEnqueue(context.Background(), queuedRuns)
	if filterErr != nil {
		return fmt.Errorf("filter recovered runs by task readiness: %w", filterErr)
	}
	for _, runID := range recovered {
		if err := coordinator.Enqueue(context.Background(), runID); err != nil {
			return fmt.Errorf("re-enqueue recovered run %s: %w", runID, err)
		}
	}
	if len(queuedRuns) > 0 {
		slog.Info("queued runs recovered", "count", len(queuedRuns), "ready", len(recovered))
	}
	// Deliver any pending auto-resume completions for idle sessions, in
	// sequence order. Each tick creates at most one continuation Run.
	if err := tickIdleAutoResume(context.Background(), db, coordinator); err != nil {
		return fmt.Errorf("deliver auto-resume completions: %w", err)
	}
	instanceID, err := runtimeinfo.NewInstanceID()
	if err != nil {
		return err
	}
	providerRepo := &store.ProviderRepo{DB: db}
	modelRepo := &store.ModelRepo{DB: db}
	knownSkills := make(map[string]bool)
	for _, skill := range skills.Discover(cfg.SkillsDir, cfg.BuiltinSkillsDir) {
		knownSkills[skill.Manifest.ID] = true
	}
	roleRepo := &store.RoleRepo{DB: db, KnownTools: map[string]bool{
		"read": true, "write": true, "edit": true, "ls": true, "grep": true, "find": true,
		"exec": true, "bash": true, "web_fetch": true, "publish_artifact": true,
		"todo": true, "search_compacted_history": true, "git_readonly": true,
		"submit_result": true, "delegate_tasks": true, "delegate_roles": true, // legacy alias stays known for old Role definitions
	}, KnownSkills: knownSkills}
	doctor := &providerdoctor.Service{Providers: providerRepo, Models: modelRepo,
		Credentials: llm.CredentialResolver{}, Timeout: 15 * time.Second}

	// Initialize prompts subsystem.
	builtins, err := prompts.LoadBuiltins()
	if err != nil {
		return fmt.Errorf("load builtin prompts: %w", err)
	}
	globalStore, err := prompts.OpenGlobalStore(os.Getenv("ENNOTE_HOME"))
	if err != nil {
		return fmt.Errorf("open global prompt store: %w", err)
	}
	promptService := &prompts.Service{
		HomeDir:     os.Getenv("ENNOTE_HOME"),
		Projects:    &store.ProjectRepo{DB: db},
		TrustStore:  trustStore,
		Builtins:    builtins,
		GlobalStore: globalStore,
	}

	// Seed ecosystem skill roots (pi/claude/codex/cursor) once at startup so
	// existing marketplace installs appear in the catalog. Ecosystem dirs and
	// the skills.sh global lock live under the OS user home, not ENNOTE_HOME.
	userHome, _ := os.UserHomeDir()
	seedSkillRoots(context.Background(), db, userHome)

	server := &api.Server{
		DB: db, Token: cfg.BootstrapToken, Sandbox: cfg.SandboxMode,
		Projects: &store.ProjectRepo{DB: db}, Providers: providerRepo,
		Models: modelRepo, Roles: roleRepo, Doctor: doctor, Policies: &store.PolicyRepo{DB: db}, Artifacts: artifactService,
		Sessions: &store.SessionRepo{DB: db}, Branches: &store.BranchRepo{DB: db},
		Messages: executor.msgRepo, Compactions: compactionRepo,
		Approvals: approvalRepo, StandingApprovals: executor.standingApprovals, Delegations: &store.DelegationRepo{DB: db},
		DelegationApprovals: &store.DelegationApprovalRepo{DB: db},
		Attention:           &store.AttentionRepo{DB: db},
		Runs:                runRepo, Queue: &store.QueueRepo{DB: db}, Events: &store.EventRepo{DB: db},
		Hub: hub, Control: api.CoordinatorController{Coordinator: coordinator}, InstanceID: instanceID,
		PromptGate: executor,
		Prompts:    promptService,
		MCP:        mcpServer,
		Skills: &skillsmgmt.Service{
			UserRoot:    cfg.SkillsDir,
			BuiltinRoot: cfg.BuiltinSkillsDir,
			HomeDir:     userHome, // OS user home: lock files + ecosystem dirs
			AdditionalRoots: loadSkillRoots(db),
		},
		SkillRoots: &store.SkillRootRepo{DB: db},
		Trust: trustStore,
		AgentFlows: &api.AgentFlowServer{
			Profiles: flowProfiles, Bindings: flowBindings, Runs: flowRuns,
			Projects:  &store.ProjectRepo{DB: db},
			Sessions:  &store.SessionRepo{DB: db},
			Checks:    checkRunner,
			Discovery: &store.AgentFlowDiscovery{Profiles: flowProfiles},
			Skills:    flowSkillKnown,
			StartRun:  flowStartRun, StartRecovered: flowStartRecovered,
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

// tickIdleAutoResume delivers one pending auto-resume completion per idle
// active session, in completion sequence order, and enqueues the resulting
// continuation Runs.
func tickIdleAutoResume(ctx context.Context, db *sql.DB, coordinator *runs.Coordinator) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM sessions WHERE status='active' ORDER BY updated_at`)
	if err != nil {
		return err
	}
	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		sessionIDs = append(sessionIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, sessionID := range sessionIDs {
		if err := tickSessionAutoResume(ctx, db, coordinator, sessionID); err != nil {
			return err
		}
	}
	return nil
}

func tickSessionAutoResume(ctx context.Context, db *sql.DB, coordinator *runs.Coordinator, sessionID string) error {
	continuation, err := (&store.DelegationRepo{DB: db}).TickSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if continuation == nil {
		return nil
	}
	if err := coordinator.Enqueue(context.Background(), continuation.ID); err != nil {
		return fmt.Errorf("enqueue continuation run %s: %w", continuation.ID, err)
	}
	return nil
}

// seedSkillRoots inserts enabled roots for existing pi/claude/codex/cursor
// ecosystem skill directories on first run, so marketplace-installed skills
// appear in the catalog without manual setup. Existing rows are left alone.
func seedSkillRoots(ctx context.Context, db *sql.DB, homeDir string) {
	repo := &store.SkillRootRepo{DB: db}
	existing, err := repo.List(ctx)
	if err != nil {
		slog.Warn("skill root seeding skipped", "error", err)
		return
	}
	havePath := map[string]bool{}
	for _, root := range existing {
		havePath[root.Path] = true
	}
	kinds := []struct {
		kind string
		sub  string
	}{
		{"pi", filepath.Join(".pi", "agent", "skills")},
		{"claude", filepath.Join(".claude", "skills")},
		{"codex", filepath.Join(".codex", "skills")},
		{"cursor", filepath.Join(".cursor", "skills")},
	}
	priority := 10
	for _, k := range kinds {
		p := filepath.Join(homeDir, k.sub)
		if havePath[p] {
			priority += 10
			continue
		}
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			priority += 10
			continue
		}
		if _, err := repo.Create(ctx, store.CreateSkillRootInput{
			Name: k.kind, Path: p, AgentKind: k.kind, Priority: priority, Enabled: true,
		}); err != nil {
			slog.Warn("skill root seed failed", "kind", k.kind, "error", err)
		}
		priority += 10
	}
}

// loadSkillRoots returns the enabled additional roots for the skills service.
func loadSkillRoots(db *sql.DB) []skillsmgmt.Root {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	roots, err := (&store.SkillRootRepo{DB: db}).EnabledPaths(ctx)
	if err != nil {
		slog.Warn("load additional skill roots failed", "error", err)
		return nil
	}
	out := make([]skillsmgmt.Root, 0, len(roots))
	for _, root := range roots {
		out = append(out, skillsmgmt.Root{Name: root.Name, Path: root.Path, Priority: root.Priority})
	}
	return out
}
