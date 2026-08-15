package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

func (r *RunRepo) resolveAndFreezeFileConfig(ctx context.Context, run *domain.AgentRun) (*ResolvedRunConfig, error) {
	if r.Providers == nil || r.Models == nil {
		return nil, fmt.Errorf("file-backed model resolver is incomplete")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin effective config transaction: %w", err)
	}
	defer tx.Rollback()
	var currentStatus domain.RunStatus
	var storedEffective, storedPromptSnapshot, storedPromptDigest, requested string
	var sessionModelID, sessionAgentID, sessionCompactionPolicyID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ar.status,ar.effective_config_json,
		ar.system_prompt_snapshot_json,ar.system_prompt_digest,ar.requested_config_json,
		s.default_model_profile_id,s.default_agent_profile_id,s.compaction_policy_profile_id
		FROM agent_runs ar JOIN sessions s ON s.id=ar.session_id WHERE ar.id=?`, run.ID).Scan(
		&currentStatus, &storedEffective, &storedPromptSnapshot, &storedPromptDigest, &requested,
		&sessionModelID, &sessionAgentID, &sessionCompactionPolicyID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if currentStatus != domain.RunRunning {
		return nil, fmt.Errorf("%w: effective config requires running run", ErrInvalidRunState)
	}
	if strings.TrimSpace(storedEffective) != "" && strings.TrimSpace(storedEffective) != "{}" {
		var effective domain.EffectiveRunConfig
		if err := json.Unmarshal([]byte(storedEffective), &effective); err != nil {
			return nil, err
		}
		resolved, err := r.resolvedFromFrozenRuntime(effective)
		if err != nil {
			return nil, err
		}
		prompt, err := decodeSystemPromptSnapshot(storedPromptSnapshot, storedPromptDigest)
		if err != nil {
			return nil, err
		}
		resolved.SystemPrompt = prompt
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return resolved, nil
	}
	var requestedConfig requestedRunConfig
	if strings.TrimSpace(requested) != "" {
		if err := json.Unmarshal([]byte(requested), &requestedConfig); err != nil {
			return nil, fmt.Errorf("decode requested config: %w", err)
		}
	}
	var frozenRole *domain.FrozenRoleExecution
	var roleDefinition domain.RoleDefinition
	var rolePrompt string
	if run.CommitFormatVersion == domain.CommitFormatSpeakerV2 {
		if run.RunKind == domain.RunKindDelegatedAgent {
			// Private children resolve the exact Role facts frozen on the
			// delegation item at Run start; no global role SQL is consulted.
			var metaJSON string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(di.role_meta_json, '{}')
				FROM delegation_item_attempts a
				JOIN delegation_items di ON di.id=a.item_id
				WHERE a.child_run_id=?`, run.ID).Scan(&metaJSON); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
						errors.New("child Run has no frozen delegation Role meta"))
				}
				return nil, err
			}
			if strings.TrimSpace(metaJSON) == "" || strings.TrimSpace(metaJSON) == "{}" {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
					errors.New("file-backed delegated Role execution requires a frozen Role meta"))
			}
			var meta DelegationRoleMeta
			if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					fmt.Errorf("decode frozen delegation Role meta: %w", err))
			}
			if err := validateFrozenDelegationMeta(meta); err != nil {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, err)
			}
			roleDefinition = meta.Definition
			var frozenSpeaker struct {
				ObjectID     string `json:"objectId"`
				VersionID    string `json:"versionId"`
				Handle       string `json:"handle"`
				DisplayName  string `json:"displayName"`
				ConfigDigest string `json:"configDigest"`
			}
			if err := json.Unmarshal(run.SpeakerSnapshot, &frozenSpeaker); err != nil || frozenSpeaker.ObjectID != meta.ObjectID ||
				frozenSpeaker.VersionID != meta.VersionID || frozenSpeaker.ConfigDigest != meta.ConfigDigest ||
				frozenSpeaker.Handle != meta.Handle || frozenSpeaker.Handle == "" {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					errors.New("child speaker snapshot does not match the frozen Role meta"))
			}
			allowedContext := false
			for _, allowed := range roleDefinition.ContextPolicy.AllowedModes {
				allowedContext = allowedContext || allowed == domain.RoleContextTask
			}
			if !allowedContext {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
					errors.New("frozen Role revision does not allow task_only context"))
			}
			if roleDefinition.Authority == domain.RoleAuthorityReadOnly {
				for _, tool := range roleDefinition.AllowedTools {
					if roleToolRequiresMutation(tool) {
						return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
							fmt.Errorf("read-only Role revision contains mutation tool %s", tool))
					}
				}
			}
			frozenRole = &domain.FrozenRoleExecution{
				ObjectID: meta.ObjectID, VersionID: meta.VersionID, Version: frozenRoleVersion(meta.VersionID),
				Handle: meta.Handle, DisplayName: meta.DisplayName, ConfigDigest: meta.ConfigDigest,
				Authority: roleDefinition.Authority, PermissionCeiling: roleDefinition.PermissionCeiling,
				AllowedTools: append([]string(nil), roleDefinition.AllowedTools...), Skills: roleDefinition.Skills,
				OutputContract: roleDefinition.OutputContract,
			}
			rolePrompt = roleDefinition.RolePrompt
		} else {
			var roleID, versionID, contextMode string
			if err := tx.QueryRowContext(ctx, `SELECT target_object_id,target_version_id,context_mode
				FROM turns WHERE id=? AND target_kind='role'`, run.TurnID).Scan(&roleID, &versionID, &contextMode); err != nil {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("load Role target: %w", err))
			}
			resolvedRole, err := r.resolveFileRole(ctx, roleID, versionID)
			if err != nil {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, err)
			}
			roleDefinition = resolvedRole.Definition
			allowedContext := false
			for _, allowed := range roleDefinition.ContextPolicy.AllowedModes {
				allowedContext = allowedContext || string(allowed) == contextMode ||
					(allowed == domain.RoleContextReply && contextMode == string(domain.InvocationContextReplyTo))
			}
			if !allowedContext {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
					fmt.Errorf("Role revision does not allow %s context", contextMode))
			}
			var speaker struct {
				ObjectID, VersionID, Handle, DisplayName, ConfigDigest string
			}
			if err := json.Unmarshal(run.SpeakerSnapshot, &speaker); err != nil || speaker.ObjectID != roleID ||
				speaker.VersionID != versionID || speaker.ConfigDigest != resolvedRole.Revision.Digest || speaker.Handle != resolvedRole.Document.Handle {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					errors.New("Role speaker snapshot does not match the immutable revision"))
			}
			if requestedConfig.ModelProfileID != "" || len(requestedConfig.CandidateModelProfileIDs) != 0 ||
				requestedConfig.AllowAutoRoute || requestedConfig.ThinkingEffort != "" || requestedConfig.MaxIterations != 0 ||
				requestedConfig.ToolPolicyProfileID != "" {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					errors.New("direct Role execution does not allow runtime identity, model, loop, or permission overrides"))
			}
			if roleDefinition.Authority == domain.RoleAuthorityReadOnly {
				for _, tool := range roleDefinition.AllowedTools {
					if roleToolRequiresMutation(tool) {
						return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
							fmt.Errorf("read-only Role revision contains mutation tool %s", tool))
					}
				}
			}
			frozenRole = &domain.FrozenRoleExecution{
				ObjectID: roleID, VersionID: versionID, Version: resolvedRole.Revision.Version,
				Handle: resolvedRole.Document.Handle, DisplayName: resolvedRole.Document.Name,
				ConfigDigest: resolvedRole.Revision.Digest, Authority: roleDefinition.Authority,
				PermissionCeiling: roleDefinition.PermissionCeiling,
				AllowedTools:      append([]string(nil), roleDefinition.AllowedTools...), Skills: roleDefinition.Skills,
				OutputContract: roleDefinition.OutputContract,
			}
			rolePrompt = roleDefinition.RolePrompt
		}
	}
	if frozenRole == nil && sessionAgentID.Valid {
		return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("managed agent profile %s is unsupported by the V2 file store", sessionAgentID.String))
	}
	modelID := strings.TrimSpace(requestedConfig.ModelProfileID)
	if frozenRole != nil {
		modelID = roleDefinition.ModelBinding.ModelProfileID
	}
	if modelID == "" && sessionModelID.Valid {
		modelID = sessionModelID.String
	}
	if modelID == "" {
		settings, err := r.Models.Files.Settings.Read()
		if err != nil {
			return nil, err
		}
		modelID = settings.DefaultModel
	}
	if modelID == "" {
		models, err := r.Models.List(ctx)
		if err != nil {
			return nil, err
		}
		if len(models) != 0 {
			modelID = models[0].ID
		}
	}
	if modelID == "" {
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, errors.New("no active model profile is configured"))
	}
	model, err := r.Models.FindByID(ctx, modelID)
	if err != nil || model == nil {
		if err == nil {
			err = fmt.Errorf("active model profile not found: %s", modelID)
		}
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, err)
	}
	provider, err := r.Providers.FindByID(ctx, model.ProviderID)
	if err != nil || provider == nil {
		if err == nil {
			err = fmt.Errorf("active provider profile not found: %s", model.ProviderID)
		}
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, err)
	}
	initialRuntime := runtimeSnapshot(*model, *provider)
	thinkingEffort := requestedConfig.ThinkingEffort
	if frozenRole != nil {
		thinkingEffort = roleDefinition.ModelBinding.ThinkingEffort
	}
	if thinkingEffort == "" {
		thinkingEffort = domain.ThinkingDefault
	}
	if err := validateThinkingSelection(initialRuntime, thinkingEffort); err != nil {
		return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, err)
	}
	policies := r.Policies
	if policies == nil {
		policies = &fileconfig.PolicyStore{}
	}
	// A frozen Role's permission ceiling pins the tool policy the same way the
	// published definition does: discuss/ask/auto map to the builtin profiles.
	toolPolicyID := requestedConfig.ToolPolicyProfileID
	if frozenRole != nil {
		switch roleDefinition.PermissionCeiling {
		case domain.PermissionDiscuss:
			toolPolicyID = "builtin-tool-discuss-v3"
		case domain.PermissionAsk:
			toolPolicyID = "builtin-tool-ask-v1"
		case domain.PermissionAuto:
			toolPolicyID = "builtin-tool-auto-v1"
		default:
			return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
				errors.New("invalid Role permission ceiling"))
		}
	}
	toolPolicy, err := policies.Resolve(ctx, toolPolicyID, domain.PolicyKindTool)
	if err != nil {
		return nil, err
	}
	turnPolicy, err := policies.Resolve(ctx, requestedConfig.TurnPolicyProfileID, domain.PolicyKindTurn)
	if err != nil {
		return nil, err
	}
	visionPolicy, err := policies.Resolve(ctx, requestedConfig.VisionPolicyProfileID, domain.PolicyKindVision)
	if err != nil {
		return nil, err
	}
	compactionID := firstNonEmpty(requestedConfig.CompactionPolicyProfileID, nullString(sessionCompactionPolicyID))
	compactionPolicy, err := policies.Resolve(ctx, compactionID, domain.PolicyKindCompaction)
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, err)
	}
	var compactionConfig domain.CompactionPolicyConfig
	if err := json.Unmarshal(compactionPolicy.Config, &compactionConfig); err != nil {
		return nil, err
	}
	compactionRuntime := initialRuntime
	if compactionConfig.CompactionModelProfileID != nil && strings.TrimSpace(*compactionConfig.CompactionModelProfileID) != "" {
		compactionModel, err := r.Models.FindByID(ctx, strings.TrimSpace(*compactionConfig.CompactionModelProfileID))
		if err != nil || compactionModel == nil {
			return nil, domain.NewCodedError(domain.ErrorCompactionModelUnavailable, fmt.Errorf("compaction model is unavailable"))
		}
		compactionProvider, err := r.Providers.FindByID(ctx, compactionModel.ProviderID)
		if err != nil || compactionProvider == nil {
			return nil, domain.NewCodedError(domain.ErrorCompactionModelUnavailable, fmt.Errorf("compaction provider is unavailable"))
		}
		compactionRuntime = runtimeSnapshot(*compactionModel, *compactionProvider)
	}
	var turnConfig domain.TurnPolicyConfig
	if err := json.Unmarshal(turnPolicy.Config, &turnConfig); err != nil {
		return nil, err
	}
	candidateIDs := append([]string(nil), requestedConfig.CandidateModelProfileIDs...)
	if frozenRole != nil {
		candidateIDs = append([]string(nil), roleDefinition.ModelBinding.FallbackModelProfileIDs...)
	} else if len(candidateIDs) == 0 {
		candidateIDs = append(candidateIDs, turnConfig.CandidateModelProfileIDs...)
	}
	candidateIDs = append(candidateIDs, model.ID)
	candidates, err := r.fileRuntimeCandidates(ctx, candidateIDs)
	if err != nil {
		return nil, err
	}
	threshold := turnConfig.Threshold
	if threshold == 0 {
		threshold = 0.7
	}
	maxIterations := requestedConfig.MaxIterations
	if frozenRole != nil {
		maxIterations = roleDefinition.MaxLoopIterations
	}
	if maxIterations == 0 {
		maxIterations = defaultMaxIterations
	}
	if maxIterations < 1 {
		return nil, fmt.Errorf("maxIterations must be at least 1")
	}
	toolMode := requestedConfig.ToolExecution
	if toolMode == "" {
		toolMode = "sequential"
	}
	if toolMode != "sequential" && toolMode != "safe_parallel" {
		return nil, fmt.Errorf("invalid tool execution mode %q", toolMode)
	}
	maxReadTools := requestedConfig.MaxConcurrentReadTools
	if maxReadTools == 0 {
		maxReadTools = defaultMaxConcurrentReadTools
	}
	effective := domain.EffectiveRunConfig{
		ProviderProfileID: provider.ID, ModelProfileID: model.ID, APIModel: model.ModelName,
		ContextTokens: model.ContextWindow, MaxOutputTokens: model.MaxOutputTokens,
		MaxIterations: maxIterations, ThinkingEffort: thinkingEffort,
		ToolExecution:  domain.ToolExecutionConfig{Mode: toolMode, MaxConcurrentReadTools: maxReadTools},
		InitialRuntime: initialRuntime,
		Routing: domain.FrozenRoutingConfig{Candidates: candidates, Threshold: threshold,
			Pinned:         frozenRole != nil || (requestedConfig.ModelProfileID != "" && !requestedConfig.AllowAutoRoute),
			AllowAutoRoute: frozenRole == nil && (requestedConfig.AllowAutoRoute || (turnConfig.Mode == "context_upgrade" && requestedConfig.ModelProfileID == ""))},
		ToolPolicy: toolPolicy, TurnPolicy: turnPolicy, VisionPolicy: visionPolicy,
		CompactionPolicy: compactionPolicy, CompactionRuntime: compactionRuntime, Role: frozenRole,
	}
	if run.ParentRunID == "" && run.RunKind == domain.RunKindAgent {
		snapshot, err := freezeFileDelegationPolicyTx(ctx, tx, policies, run.ID)
		if err != nil {
			return nil, err
		}
		effective.Delegation = snapshot
	}
	encoded, err := json.Marshal(effective)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(encoded), provider.APIKey) && provider.APIKey != "" {
		return nil, errors.New("credential leaked into effective config")
	}
	promptProfileID := ""
	if frozenRole != nil {
		promptProfileID = frozenRole.ObjectID
	}
	prompt, err := newSystemPromptSnapshot(promptProfileID, rolePrompt)
	if err != nil {
		return nil, err
	}
	encodedPrompt, _ := json.Marshal(prompt)
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET effective_config_json=?,
		system_prompt_snapshot_json=?,system_prompt_digest=?
		WHERE id=? AND status='running' AND effective_config_json='{}'`, string(encoded), string(encodedPrompt), prompt.Digest, run.ID)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, fmt.Errorf("%w: effective config was already changed", ErrInvalidRunState)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	run.EffectiveConfig = append(json.RawMessage(nil), encoded...)
	return &ResolvedRunConfig{Effective: effective, Provider: *provider, Model: *model, SystemPrompt: prompt}, nil
}

func (r *RunRepo) resolvedFromFrozenRuntime(effective domain.EffectiveRunConfig) (*ResolvedRunConfig, error) {
	runtime := effective.InitialRuntime
	if runtime.ModelProfileID == "" {
		return nil, errors.New("frozen effective config has no model runtime")
	}
	credentialRef := firstNonEmpty(runtime.CredentialRef, runtime.ProviderProfileID)
	apiKey, err := r.Models.Files.Credentials.Resolve(credentialRef)
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorProviderCredentialUnavailable, err)
	}
	runtime.APIKey = apiKey
	effective.InitialRuntime.APIKey = apiKey
	for index := range effective.Routing.Candidates {
		if effective.Routing.Candidates[index].CredentialRef == credentialRef {
			effective.Routing.Candidates[index].APIKey = apiKey
		}
	}
	if effective.CompactionRuntime.CredentialRef == credentialRef {
		effective.CompactionRuntime.APIKey = apiKey
	}
	model := domain.ModelProfile{
		ID: runtime.ModelProfileID, ProviderID: runtime.ProviderProfileID, ModelName: runtime.APIModel,
		DisplayName: runtime.APIModel, ContextWindow: runtime.ContextTokens, MaxOutputTokens: runtime.MaxOutputTokens,
		SupportsVision: runtime.SupportsVision, SupportsToolUse: runtime.SupportsToolUse,
		SupportsThinking: runtime.SupportsThinking, ThinkingDialect: runtime.ThinkingDialect,
		SupportedThinkingEfforts: append([]domain.ThinkingEffort(nil), runtime.SupportedThinkingEfforts...), Status: "frozen",
	}
	provider := domain.ProviderProfile{
		ID: runtime.ProviderProfileID, Name: runtime.ProviderProfileID,
		ProviderType: domain.ProviderOpenAICompatible, BaseURL: runtime.BaseURL,
		CredentialRef: credentialRef, APIKey: apiKey, Proxy: runtime.Proxy, Status: "frozen",
	}
	return &ResolvedRunConfig{Effective: effective, Provider: provider, Model: model}, nil
}

func (r *RunRepo) fileRuntimeCandidates(ctx context.Context, ids []string) ([]domain.ModelRuntimeSnapshot, error) {
	seen := map[string]bool{}
	candidates := make([]domain.ModelRuntimeSnapshot, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		model, err := r.Models.FindByID(ctx, id)
		if err != nil || model == nil {
			return nil, fmt.Errorf("active model profile not found: %s", id)
		}
		provider, err := r.Providers.FindByID(ctx, model.ProviderID)
		if err != nil || provider == nil {
			return nil, fmt.Errorf("active provider profile not found: %s", model.ProviderID)
		}
		candidates = append(candidates, runtimeSnapshot(*model, *provider))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ContextTokens == candidates[j].ContextTokens {
			return candidates[i].ModelProfileID < candidates[j].ModelProfileID
		}
		return candidates[i].ContextTokens < candidates[j].ContextTokens
	})
	return candidates, nil
}

func freezeFileDelegationPolicyTx(ctx context.Context, tx *sql.Tx, policies *fileconfig.PolicyStore, runID string) (*domain.DelegationPolicySnapshot, error) {
	policy, err := policies.Resolve(ctx, "", domain.PolicyKind("delegation"))
	if err != nil {
		return nil, err
	}
	var config delegationPolicyConfig
	if err := json.Unmarshal(policy.Config, &config); err != nil {
		return nil, err
	}
	snapshot := &domain.DelegationPolicySnapshot{
		ID: policy.ID, Version: policy.Version, MaxConcurrentChildren: config.MaxConcurrentChildren,
		Budget: config.Budget,
	}
	digest, err := digestDelegationPolicy(snapshot)
	if err != nil {
		return nil, err
	}
	snapshot.Digest = digest
	snapshotJSON, _ := json.Marshal(snapshot)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO delegation_root_budgets
		(root_run_id,policy_snapshot_json,policy_snapshot_digest,max_model_calls,max_tool_calls,
		 max_total_tokens,max_output_tokens,max_cost_usd_micros,max_concurrent_children,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, runID, string(snapshotJSON), digest,
		config.Budget.MaxModelCalls, config.Budget.MaxToolCalls, config.Budget.MaxTotalTokens,
		config.Budget.MaxOutputTokens, config.Budget.MaxCostMicros, config.MaxConcurrentChildren, now, now)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}
