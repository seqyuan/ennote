package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const (
	defaultMaxIterations          = 32
	defaultMaxConcurrentReadTools = 4
)

type ResolvedRunConfig struct {
	Effective    domain.EffectiveRunConfig
	Provider     domain.ProviderProfile
	Model        domain.ModelProfile
	SystemPrompt domain.SystemPromptSnapshot
}

type requestedRunConfig struct {
	ModelProfileID            string                `json:"modelProfileId"`
	CandidateModelProfileIDs  []string              `json:"candidateModelProfileIds"`
	AllowAutoRoute            bool                  `json:"allowAutoRoute"`
	ToolPolicyProfileID       string                `json:"toolPolicyProfileId"`
	TurnPolicyProfileID       string                `json:"turnPolicyProfileId"`
	VisionPolicyProfileID     string                `json:"visionPolicyProfileId"`
	CompactionPolicyProfileID string                `json:"compactionPolicyProfileId"`
	MaxIterations             int                   `json:"maxIterations"`
	ToolExecution             string                `json:"toolExecution"`
	MaxConcurrentReadTools    int                   `json:"maxConcurrentReadTools"`
	ThinkingEffort            domain.ThinkingEffort `json:"thinkingEffort"`
}

func (r *RunRepo) ResolveAndFreezeConfig(ctx context.Context, run *domain.AgentRun) (*ResolvedRunConfig, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin effective config transaction: %w", err)
	}
	defer tx.Rollback()

	var currentStatus domain.RunStatus
	var storedEffective, storedPromptSnapshot, storedPromptDigest, requested string
	var sessionModelID, sessionAgentID, sessionCompactionPolicyID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ar.status, ar.effective_config_json,
		ar.system_prompt_snapshot_json, ar.system_prompt_digest, ar.requested_config_json,
		s.default_model_profile_id, s.default_agent_profile_id, s.compaction_policy_profile_id
		FROM agent_runs ar JOIN sessions s ON s.id = ar.session_id WHERE ar.id = ?`, run.ID).
		Scan(&currentStatus, &storedEffective, &storedPromptSnapshot, &storedPromptDigest, &requested, &sessionModelID, &sessionAgentID, &sessionCompactionPolicyID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("load run config inputs: %w", err)
	}
	if currentStatus != domain.RunRunning {
		return nil, fmt.Errorf("%w: effective config requires running run", ErrInvalidRunState)
	}

	if strings.TrimSpace(storedEffective) != "" && strings.TrimSpace(storedEffective) != "{}" {
		var effective domain.EffectiveRunConfig
		if err := json.Unmarshal([]byte(storedEffective), &effective); err != nil {
			return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, fmt.Errorf("decode frozen effective config: %w", err))
		}
		resolved, err := loadResolvedProfilesTx(ctx, tx, effective)
		if err != nil {
			return nil, err
		}
		promptSnapshot, err := decodeSystemPromptSnapshot(storedPromptSnapshot, storedPromptDigest)
		if err != nil {
			return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, err)
		}
		resolved.SystemPrompt = promptSnapshot
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return resolved, nil
	}

	var requestedConfig requestedRunConfig
	if strings.TrimSpace(requested) != "" {
		if err := json.Unmarshal([]byte(requested), &requestedConfig); err != nil {
			return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, fmt.Errorf("decode requested config: %w", err))
		}
	}

	var frozenRole *domain.FrozenRoleExecution
	var roleDefinition domain.RoleDefinition
	var rolePrompt string
	invocationTargetKind := "host"
	var roleID, versionID, definitionJSON, configDigest, contextMode string
	var roleVersion int
	if run.CommitFormatVersion == domain.CommitFormatSpeakerV2 {
		if run.RunKind == domain.RunKindDelegatedAgent {
			// Private children resolve the exact Role version from the frozen
			// delegation item; there is no public Turn target.
			invocationTargetKind = "role"
			if err := tx.QueryRowContext(ctx, `SELECT v.agent_profile_id,v.id,v.version,v.definition_json,v.config_digest
				FROM delegation_items di JOIN agent_profile_versions v ON v.id=di.role_version_id
				WHERE di.child_run_id=?`, run.ID).
				Scan(&roleID, &versionID, &roleVersion, &definitionJSON, &configDigest); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
						errors.New("child Run has no frozen delegation Role version"))
				}
				return nil, err
			}
			contextMode = string(domain.InvocationContextTask)
		} else {
			if err := tx.QueryRowContext(ctx, `SELECT target_kind FROM turns WHERE id=?`, run.TurnID).Scan(&invocationTargetKind); err != nil {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("load Run target: %w", err))
			}
			if invocationTargetKind != "host" && invocationTargetKind != "role" {
				return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, fmt.Errorf("unsupported Run target %q", invocationTargetKind))
			}
		}
	}
	if invocationTargetKind == "role" {
		if roleID == "" {
			if err := tx.QueryRowContext(ctx, `SELECT t.target_object_id,t.target_version_id,
				v.version,v.definition_json,v.config_digest,t.context_mode
				FROM turns t JOIN agent_profiles p ON p.id=t.target_object_id
				JOIN agent_profile_versions v ON v.id=t.target_version_id AND v.agent_profile_id=p.id
				WHERE t.id=? AND t.target_kind='role' AND p.object_kind='role'`, run.TurnID).
				Scan(&roleID, &versionID, &roleVersion, &definitionJSON, &configDigest, &contextMode); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid, errors.New("frozen Role version is unavailable"))
				}
				return nil, fmt.Errorf("load frozen Role version: %w", err)
			}
		}
		if err := json.Unmarshal([]byte(definitionJSON), &roleDefinition); err != nil {
			return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, fmt.Errorf("decode frozen Role definition: %w", err))
		}
		if roleDefinition.Authority == domain.RoleAuthorityReadOnly {
			for _, tool := range roleDefinition.AllowedTools {
				if roleToolRequiresMutation(tool) {
					return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
						fmt.Errorf("read-only Role version contains mutation tool %s", tool))
				}
			}
		}
		var frozenSpeaker struct {
			ObjectID     string `json:"objectId"`
			VersionID    string `json:"versionId"`
			Handle       string `json:"handle"`
			DisplayName  string `json:"displayName"`
			ConfigDigest string `json:"configDigest"`
		}
		if err := json.Unmarshal(run.SpeakerSnapshot, &frozenSpeaker); err != nil || frozenSpeaker.ObjectID != roleID ||
			frozenSpeaker.VersionID != versionID || frozenSpeaker.ConfigDigest != configDigest || frozenSpeaker.Handle == "" {
			return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
				errors.New("Role speaker snapshot does not match the published version"))
		}
		allowedContext := false
		for _, allowed := range roleDefinition.ContextPolicy.AllowedModes {
			if string(allowed) == contextMode || (allowed == domain.RoleContextReply && contextMode == string(domain.InvocationContextReplyTo)) {
				allowedContext = true
				break
			}
		}
		if !allowedContext {
			return nil, domain.NewCodedError(domain.ErrorInvocationTargetInvalid,
				fmt.Errorf("Role version does not allow %s context", contextMode))
		}
		if requestedConfig.ModelProfileID != "" || len(requestedConfig.CandidateModelProfileIDs) != 0 ||
			requestedConfig.AllowAutoRoute || requestedConfig.ThinkingEffort != "" || requestedConfig.MaxIterations != 0 ||
			requestedConfig.ToolPolicyProfileID != "" {
			return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
				errors.New("direct Role execution does not allow runtime identity, model, loop, or permission overrides"))
		}
		frozenRole = &domain.FrozenRoleExecution{ObjectID: roleID, VersionID: versionID, Version: roleVersion,
			Handle: frozenSpeaker.Handle, DisplayName: frozenSpeaker.DisplayName, ConfigDigest: configDigest, Authority: roleDefinition.Authority,
			PermissionCeiling: roleDefinition.PermissionCeiling,
			AllowedTools:      append([]string(nil), roleDefinition.AllowedTools...), Skills: roleDefinition.Skills,
			OutputContract: roleDefinition.OutputContract}
		rolePrompt = roleDefinition.RolePrompt
	}

	var agentModelID, agentToolPolicyID, agentTurnPolicyID, agentVisionPolicyID, agentCompactionPolicyID sql.NullString
	var agentPrompt string
	if frozenRole == nil && sessionAgentID.Valid {
		err := tx.QueryRowContext(ctx, `SELECT default_model_id,tool_policy_profile_id,
			turn_policy_profile_id,vision_policy_profile_id,compaction_policy_profile_id,system_prompt
			FROM agent_profiles WHERE id=? AND status='active'`, sessionAgentID.String).Scan(
			&agentModelID, &agentToolPolicyID, &agentTurnPolicyID, &agentVisionPolicyID, &agentCompactionPolicyID, &agentPrompt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
				fmt.Errorf("active agent profile not found: %s", sessionAgentID.String))
		}
		if err != nil {
			return nil, err
		}
	}
	modelID := strings.TrimSpace(requestedConfig.ModelProfileID)
	if frozenRole != nil {
		modelID = roleDefinition.ModelBinding.ModelProfileID
		if modelID == "" && roleDefinition.ModelBinding.Mode == domain.RoleModelInherit && run.ParentRunID != "" {
			// A child Run with mode=inherit resolves the parent's frozen model at
			// runtime; the child never re-parses a live draft or current profile.
			var parentEffective string
			if err := tx.QueryRowContext(ctx, `SELECT effective_config_json FROM agent_runs WHERE id=?`, run.ParentRunID).
				Scan(&parentEffective); err != nil {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					fmt.Errorf("load parent frozen config for inherit binding: %w", err))
			}
			var parentConfig domain.EffectiveRunConfig
			if err := json.Unmarshal([]byte(parentEffective), &parentConfig); err != nil || parentConfig.ModelProfileID == "" {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
					fmt.Errorf("parent Run has no frozen model for inherit binding"))
			}
			modelID = parentConfig.ModelProfileID
		}
	}
	if modelID == "" && sessionModelID.Valid {
		modelID = sessionModelID.String
	}
	if modelID == "" && agentModelID.Valid {
		modelID = agentModelID.String
	}
	if modelID == "" {
		_ = tx.QueryRowContext(ctx, `SELECT m.id FROM settings s
			JOIN model_profiles m ON m.id = s.value AND m.status = 'active'
			JOIN provider_profiles p ON p.id = m.provider_id AND p.status = 'active'
			WHERE s.key = ?`, defaultModelSettingKey).Scan(&modelID)
	}
	if modelID == "" {
		_ = tx.QueryRowContext(ctx, `SELECT m.id FROM model_profiles m
			JOIN provider_profiles p ON p.id = m.provider_id AND p.status = 'active'
			WHERE m.status = 'active' ORDER BY m.updated_at DESC, m.id LIMIT 1`).Scan(&modelID)
	}
	if modelID == "" {
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, errors.New("no active model profile is configured"))
	}

	model, provider, err := loadModelAndProviderTx(ctx, tx, modelID)
	if err != nil {
		return nil, err
	}
	initialRuntime := runtimeSnapshot(model, provider)
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

	toolPolicyID := firstNonEmpty(requestedConfig.ToolPolicyProfileID, nullString(agentToolPolicyID))
	if frozenRole != nil {
		switch roleDefinition.PermissionCeiling {
		case domain.PermissionDiscuss:
			toolPolicyID = "builtin-tool-discuss-v2"
		case domain.PermissionAsk:
			toolPolicyID = "builtin-tool-ask-v1"
		case domain.PermissionAuto:
			toolPolicyID = "builtin-tool-auto-v1"
		default:
			return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, errors.New("invalid Role permission ceiling"))
		}
	}
	turnPolicyID := firstNonEmpty(requestedConfig.TurnPolicyProfileID, nullString(agentTurnPolicyID))
	visionPolicyID := firstNonEmpty(requestedConfig.VisionPolicyProfileID, nullString(agentVisionPolicyID))
	compactionPolicyID := firstNonEmpty(requestedConfig.CompactionPolicyProfileID,
		nullString(sessionCompactionPolicyID), nullString(agentCompactionPolicyID))
	toolPolicy, err := loadPolicySnapshotTx(ctx, tx, toolPolicyID, domain.PolicyKindTool, "default_tool_policy_profile_id")
	if err != nil {
		return nil, err
	}
	turnPolicy, err := loadPolicySnapshotTx(ctx, tx, turnPolicyID, domain.PolicyKindTurn, "default_turn_policy_profile_id")
	if err != nil {
		return nil, err
	}
	visionPolicy, err := loadPolicySnapshotTx(ctx, tx, visionPolicyID, domain.PolicyKindVision, "default_vision_policy_profile_id")
	if err != nil {
		return nil, err
	}
	compactionPolicy, err := loadPolicySnapshotTx(ctx, tx, compactionPolicyID, domain.PolicyKindCompaction, "default_compaction_policy_profile_id")
	if err != nil {
		return nil, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, err)
	}
	var compactionConfig domain.CompactionPolicyConfig
	if err := json.Unmarshal(compactionPolicy.Config, &compactionConfig); err != nil {
		return nil, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, fmt.Errorf("decode frozen compaction policy: %w", err))
	}
	compactionRuntime := initialRuntime
	if compactionConfig.CompactionModelProfileID != nil && strings.TrimSpace(*compactionConfig.CompactionModelProfileID) != "" {
		compactionModel, compactionProvider, loadErr := loadModelAndProviderTx(ctx, tx, strings.TrimSpace(*compactionConfig.CompactionModelProfileID))
		if loadErr != nil {
			return nil, domain.NewCodedError(domain.ErrorCompactionModelUnavailable, loadErr)
		}
		compactionRuntime = runtimeSnapshot(compactionModel, compactionProvider)
	}
	if compactionConfig.SummaryMaxOutputTokens >= compactionRuntime.ContextTokens {
		return nil, domain.NewCodedError(domain.ErrorCompactionConfigInvalid, errors.New("summary output reservation exceeds compaction model context window"))
	}

	var turnConfig domain.TurnPolicyConfig
	if err := json.Unmarshal(turnPolicy.Config, &turnConfig); err != nil {
		return nil, domain.NewCodedError(domain.ErrorTurnPolicyFailed, fmt.Errorf("decode frozen turn policy: %w", err))
	}
	candidateIDs := append([]string(nil), requestedConfig.CandidateModelProfileIDs...)
	if frozenRole != nil {
		candidateIDs = append([]string(nil), roleDefinition.ModelBinding.FallbackModelProfileIDs...)
	} else if len(candidateIDs) == 0 {
		candidateIDs = append(candidateIDs, turnConfig.CandidateModelProfileIDs...)
	}
	var visionConfig domain.VisionPolicyConfig
	if err := json.Unmarshal(visionPolicy.Config, &visionConfig); err != nil {
		return nil, domain.NewCodedError(domain.ErrorVisionFallbackFailed, fmt.Errorf("decode frozen vision policy: %w", err))
	}
	candidateIDs = append(candidateIDs, modelID, visionConfig.DescriptorModelProfileID)
	candidates, err := loadRuntimeCandidatesTx(ctx, tx, candidateIDs)
	if err != nil {
		return nil, err
	}
	if thinkingEffort != domain.ThinkingDefault {
		for _, candidate := range candidates {
			if err := validateThinkingSelection(candidate, thinkingEffort); err != nil {
				return nil, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid, err)
			}
		}
	}
	threshold := turnConfig.Threshold
	if threshold == 0 {
		threshold = 0.7
	}
	allowAutoRoute := requestedConfig.AllowAutoRoute || (turnConfig.Mode == "context_upgrade" && requestedConfig.ModelProfileID == "")
	if frozenRole != nil {
		allowAutoRoute = false
	}

	maxIterations := requestedConfig.MaxIterations
	if frozenRole != nil {
		maxIterations = roleDefinition.MaxLoopIterations
	}
	if maxIterations == 0 {
		maxIterations = defaultMaxIterations
	}
	if maxIterations < 1 {
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, errors.New("maxIterations must be at least 1"))
	}
	mode := requestedConfig.ToolExecution
	if mode == "" {
		mode = "sequential"
	}
	if mode != "sequential" && mode != "safe_parallel" {
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, fmt.Errorf("invalid tool execution mode %q", mode))
	}
	maxReadTools := requestedConfig.MaxConcurrentReadTools
	if maxReadTools == 0 {
		maxReadTools = defaultMaxConcurrentReadTools
	}
	if maxReadTools < 1 {
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, errors.New("maxConcurrentReadTools must be at least 1"))
	}

	effective := domain.EffectiveRunConfig{
		ProviderProfileID: provider.ID,
		ModelProfileID:    model.ID,
		APIModel:          model.ModelName,
		ContextTokens:     model.ContextWindow,
		MaxOutputTokens:   model.MaxOutputTokens,
		MaxIterations:     maxIterations,
		ThinkingEffort:    thinkingEffort,
		ToolExecution: domain.ToolExecutionConfig{
			Mode: mode, MaxConcurrentReadTools: maxReadTools,
		},
		InitialRuntime: initialRuntime,
		Routing: domain.FrozenRoutingConfig{Candidates: candidates, Threshold: threshold,
			Pinned:         frozenRole != nil || (requestedConfig.ModelProfileID != "" && !requestedConfig.AllowAutoRoute),
			AllowAutoRoute: allowAutoRoute},
		ToolPolicy: toolPolicy, TurnPolicy: turnPolicy, VisionPolicy: visionPolicy,
		CompactionPolicy: compactionPolicy, CompactionRuntime: compactionRuntime,
		Role: frozenRole,
	}
	if run.ParentRunID == "" && run.RunKind == domain.RunKindAgent {
		// Top-level Host Runs freeze the Session's delegation policy and create
		// their root budget ledger in the same transaction as the effective
		// config, so every later delegation reservation contends on it.
		snapshot, err := freezeDelegationPolicyTx(ctx, tx, run.ID, run.SessionID)
		if err != nil {
			return nil, domain.NewCodedError(domain.ErrorDelegationBudgetExceeded, err)
		}
		effective.Delegation = snapshot
	}
	encoded, err := json.Marshal(effective)
	if err != nil {
		return nil, fmt.Errorf("encode effective config: %w", err)
	}
	promptProfileID := nullString(sessionAgentID)
	promptText := agentPrompt
	if frozenRole != nil {
		promptProfileID = frozenRole.ObjectID
		promptText = rolePrompt
	}
	promptSnapshot, err := newSystemPromptSnapshot(promptProfileID, promptText)
	if err != nil {
		return nil, fmt.Errorf("encode system prompt snapshot: %w", err)
	}
	encodedPrompt, err := json.Marshal(promptSnapshot)
	if err != nil {
		return nil, fmt.Errorf("encode system prompt snapshot: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET effective_config_json = ?,
		system_prompt_snapshot_json = ?, system_prompt_digest = ?
		WHERE id = ? AND status = 'running' AND effective_config_json = '{}'`,
		string(encoded), string(encodedPrompt), promptSnapshot.Digest, run.ID)
	if err != nil {
		return nil, fmt.Errorf("freeze effective config: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, fmt.Errorf("%w: effective config was already changed", ErrInvalidRunState)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit effective config: %w", err)
	}
	run.EffectiveConfig = append(json.RawMessage(nil), encoded...)
	return &ResolvedRunConfig{Effective: effective, Provider: provider, Model: model, SystemPrompt: promptSnapshot}, nil
}

func loadResolvedProfilesTx(ctx context.Context, tx *sql.Tx, effective domain.EffectiveRunConfig) (*ResolvedRunConfig, error) {
	if effective.InitialRuntime.ModelProfileID != "" {
		runtime := effective.InitialRuntime
		model := domain.ModelProfile{ID: runtime.ModelProfileID, ProviderID: runtime.ProviderProfileID,
			ModelName: runtime.APIModel, DisplayName: runtime.APIModel, ContextWindow: runtime.ContextTokens,
			MaxOutputTokens: runtime.MaxOutputTokens, SupportsVision: runtime.SupportsVision,
			SupportsToolUse: runtime.SupportsToolUse, SupportsThinking: runtime.SupportsThinking,
			ThinkingDialect:          runtime.ThinkingDialect,
			SupportedThinkingEfforts: append([]domain.ThinkingEffort(nil), runtime.SupportedThinkingEfforts...), Status: "frozen"}
		provider := domain.ProviderProfile{ID: runtime.ProviderProfileID, Name: runtime.ProviderProfileID,
			ProviderType: domain.ProviderOpenAICompatible, BaseURL: runtime.BaseURL,
			CredentialRef: runtime.CredentialRef, Proxy: runtime.Proxy, Status: "frozen"}
		return &ResolvedRunConfig{Effective: effective, Provider: provider, Model: model}, nil
	}
	// Compatibility for Runs frozen before v0.3. New Runs always use InitialRuntime.
	model, provider, err := loadModelAndProviderTx(ctx, tx, effective.ModelProfileID)
	if err != nil {
		return nil, err
	}
	if provider.ID != effective.ProviderProfileID || model.ModelName != effective.APIModel {
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable, errors.New("frozen model configuration no longer matches its profiles"))
	}
	return &ResolvedRunConfig{Effective: effective, Provider: provider, Model: model}, nil
}

func runtimeSnapshot(model domain.ModelProfile, provider domain.ProviderProfile) domain.ModelRuntimeSnapshot {
	return domain.ModelRuntimeSnapshot{
		ProviderProfileID: provider.ID, ModelProfileID: model.ID, APIModel: model.ModelName,
		BaseURL: provider.BaseURL, CredentialRef: provider.CredentialRef, Proxy: provider.Proxy,
		ContextTokens: model.ContextWindow, MaxOutputTokens: model.MaxOutputTokens,
		SupportsVision: model.SupportsVision, SupportsToolUse: model.SupportsToolUse,
		SupportsThinking: model.SupportsThinking, ThinkingDialect: model.ThinkingDialect,
		SupportedThinkingEfforts: append([]domain.ThinkingEffort(nil), model.SupportedThinkingEfforts...),
	}
}

func loadRuntimeCandidatesTx(ctx context.Context, tx *sql.Tx, ids []string) ([]domain.ModelRuntimeSnapshot, error) {
	seen := make(map[string]struct{}, len(ids))
	candidates := make([]domain.ModelRuntimeSnapshot, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		model, provider, err := loadModelAndProviderTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, runtimeSnapshot(model, provider))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ContextTokens == candidates[j].ContextTokens {
			return candidates[i].ModelProfileID < candidates[j].ModelProfileID
		}
		return candidates[i].ContextTokens < candidates[j].ContextTokens
	})
	return candidates, nil
}

func loadPolicySnapshotTx(ctx context.Context, tx *sql.Tx, requestedID string, kind domain.PolicyKind, settingKey string) (domain.PolicySnapshot, error) {
	id := strings.TrimSpace(requestedID)
	if id == "" {
		_ = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, settingKey).Scan(&id)
	}
	if id == "" {
		return domain.PolicySnapshot{}, domain.NewCodedError(domain.ErrorProviderUnavailable,
			fmt.Errorf("no default %s policy is configured", kind))
	}
	var snapshot domain.PolicySnapshot
	var configText string
	var status string
	err := tx.QueryRowContext(ctx, `SELECT id,kind,version,config_json,status FROM policy_profiles WHERE id=?`, id).
		Scan(&snapshot.ID, &snapshot.Kind, &snapshot.Version, &configText, &status)
	if errors.Is(err, sql.ErrNoRows) || status != "active" {
		return snapshot, domain.NewCodedError(domain.ErrorProviderUnavailable,
			fmt.Errorf("active %s policy profile not found: %s", kind, id))
	}
	if err != nil {
		return snapshot, err
	}
	if snapshot.Kind != kind {
		return snapshot, domain.NewCodedError(domain.ErrorProviderUnavailable,
			fmt.Errorf("policy %s has kind %s, expected %s", id, snapshot.Kind, kind))
	}
	snapshot.Config = json.RawMessage(configText)
	return snapshot, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

// delegationPolicyConfig is the wire shape of a 'delegation' kind policy
// profile. It is a ceiling applied to the whole top-level Run, never a grant.
type delegationPolicyConfig struct {
	MaxConcurrentChildren int                      `json:"maxConcurrentChildren"`
	Budget                domain.BudgetCeilingJSON `json:"budget"`
}

// freezeDelegationPolicyTx resolves the Session's delegation policy, computes
// the canonical digest, and creates the root budget ledger row. It runs inside
// the effective-config transaction so snapshot and ledger freeze together.
func freezeDelegationPolicyTx(ctx context.Context, tx *sql.Tx, runID, sessionID string) (*domain.DelegationPolicySnapshot, error) {
	var policyID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(delegation_policy_profile_id,'') FROM sessions WHERE id=?`,
		sessionID).Scan(&policyID); err != nil {
		return nil, fmt.Errorf("load session delegation policy: %w", err)
	}
	if policyID == "" {
		policyID = "builtin-hosted-delegation-v1"
	}
	var configText, status string
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT version,config_json,status FROM policy_profiles
		WHERE id=? AND kind='delegation'`, policyID).Scan(&version, &configText, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("delegation policy %s is not configured", policyID)
		}
		return nil, fmt.Errorf("load delegation policy %s: %w", policyID, err)
	}
	if status != "active" {
		return nil, fmt.Errorf("delegation policy %s is not active", policyID)
	}
	var config delegationPolicyConfig
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		return nil, fmt.Errorf("decode delegation policy %s: %w", policyID, err)
	}
	if config.MaxConcurrentChildren < 1 || config.Budget.MaxModelCalls < 1 || config.Budget.MaxToolCalls < 1 ||
		config.Budget.MaxTotalTokens < 1 || config.Budget.MaxOutputTokens < 1 {
		return nil, fmt.Errorf("delegation policy %s has invalid ceilings", policyID)
	}
	snapshot := &domain.DelegationPolicySnapshot{
		ID: policyID, Version: version,
		MaxConcurrentChildren: config.MaxConcurrentChildren,
		Budget:                config.Budget,
	}
	digest, err := digestDelegationPolicy(snapshot)
	if err != nil {
		return nil, err
	}
	snapshot.Digest = digest
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO delegation_root_budgets
		(root_run_id,policy_snapshot_json,policy_snapshot_digest,
		 max_model_calls,max_tool_calls,max_total_tokens,max_output_tokens,max_cost_usd_micros,max_concurrent_children,
		 created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		runID, string(snapshotJSON), digest, config.Budget.MaxModelCalls, config.Budget.MaxToolCalls,
		config.Budget.MaxTotalTokens, config.Budget.MaxOutputTokens, config.Budget.MaxCostMicros,
		config.MaxConcurrentChildren, now, now); err != nil {
		return nil, fmt.Errorf("create root budget ledger: %w", err)
	}
	return snapshot, nil
}

// digestDelegationPolicy derives the canonical snapshot digest. The digest is
// computed in Go only; SQL never duplicates this logic.
func digestDelegationPolicy(snapshot *domain.DelegationPolicySnapshot) (string, error) {
	input := struct {
		ID                    string                   `json:"id"`
		Version               int                      `json:"version"`
		MaxConcurrentChildren int                      `json:"maxConcurrentChildren"`
		Budget                domain.BudgetCeilingJSON `json:"budget"`
	}{snapshot.ID, snapshot.Version, snapshot.MaxConcurrentChildren, snapshot.Budget}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func newSystemPromptSnapshot(agentProfileID, prompt string) (domain.SystemPromptSnapshot, error) {
	snapshot := domain.SystemPromptSnapshot{
		Version: 1, AgentProfileID: agentProfileID, AgentPrompt: prompt, PlatformVersion: "hosted-v1",
	}
	digestInput := struct {
		Version         int    `json:"version"`
		AgentProfileID  string `json:"agentProfileId,omitempty"`
		AgentPrompt     string `json:"agentPrompt"`
		PlatformVersion string `json:"platformVersion"`
	}{snapshot.Version, snapshot.AgentProfileID, snapshot.AgentPrompt, snapshot.PlatformVersion}
	encoded, err := json.Marshal(digestInput)
	if err != nil {
		return snapshot, err
	}
	sum := sha256.Sum256(encoded)
	snapshot.Digest = hex.EncodeToString(sum[:])
	return snapshot, nil
}

func decodeSystemPromptSnapshot(encoded, expectedDigest string) (domain.SystemPromptSnapshot, error) {
	var snapshot domain.SystemPromptSnapshot
	if strings.TrimSpace(encoded) == "" || strings.TrimSpace(encoded) == "{}" {
		return snapshot, errors.New("frozen effective config has no system prompt snapshot")
	}
	if err := json.Unmarshal([]byte(encoded), &snapshot); err != nil {
		return snapshot, fmt.Errorf("decode frozen system prompt snapshot: %w", err)
	}
	calculated, err := newSystemPromptSnapshot(snapshot.AgentProfileID, snapshot.AgentPrompt)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Version != 1 || snapshot.PlatformVersion != "hosted-v1" || snapshot.Digest == "" ||
		snapshot.Digest != calculated.Digest || expectedDigest != snapshot.Digest {
		return snapshot, errors.New("frozen system prompt snapshot digest mismatch")
	}
	return snapshot, nil
}

func validateThinkingSelection(runtime domain.ModelRuntimeSnapshot, effort domain.ThinkingEffort) error {
	if effort == domain.ThinkingDefault {
		return nil
	}
	if runtime.ThinkingDialect == domain.ThinkingDialectNone {
		return fmt.Errorf("thinking effort %q is not supported by model %s", effort, runtime.ModelProfileID)
	}
	for _, supported := range runtime.SupportedThinkingEfforts {
		if supported == effort {
			return nil
		}
	}
	return fmt.Errorf("thinking effort %q is not supported by model %s", effort, runtime.ModelProfileID)
}

func loadModelAndProviderTx(ctx context.Context, tx *sql.Tx, modelID string) (domain.ModelProfile, domain.ProviderProfile, error) {
	var model domain.ModelProfile
	var provider domain.ProviderProfile
	var vision, tools, thinking int
	var effortsJSON string
	err := tx.QueryRowContext(ctx, `SELECT m.id, m.provider_id, m.model_name, m.display_name,
		m.context_window, m.max_output_tokens, m.supports_vision, m.supports_tool_use,
		m.supports_thinking, m.thinking_dialect, m.supported_thinking_efforts_json,
		m.status, p.id, p.name, p.provider_type, p.base_url,
		p.credential_ref, p.proxy, p.status
		FROM model_profiles m JOIN provider_profiles p ON p.id = m.provider_id
		WHERE m.id = ? AND m.status = 'active' AND p.status = 'active'`, modelID).Scan(
		&model.ID, &model.ProviderID, &model.ModelName, &model.DisplayName,
		&model.ContextWindow, &model.MaxOutputTokens, &vision, &tools, &thinking,
		&model.ThinkingDialect, &effortsJSON, &model.Status, &provider.ID, &provider.Name, &provider.ProviderType,
		&provider.BaseURL, &provider.CredentialRef, &provider.Proxy, &provider.Status,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return model, provider, domain.NewCodedError(domain.ErrorProviderUnavailable, fmt.Errorf("active model profile not found: %s", modelID))
	}
	if err != nil {
		return model, provider, fmt.Errorf("load model configuration: %w", err)
	}
	if strings.TrimSpace(model.ModelName) == "" {
		return model, provider, domain.NewCodedError(domain.ErrorProviderUnavailable, errors.New("model profile has an empty API model id"))
	}
	model.SupportsVision = vision != 0
	model.SupportsToolUse = tools != 0
	model.SupportsThinking = thinking != 0
	if err := json.Unmarshal([]byte(effortsJSON), &model.SupportedThinkingEfforts); err != nil {
		return model, provider, domain.NewCodedError(domain.ErrorProviderConfigurationInvalid,
			fmt.Errorf("decode model thinking capabilities: %w", err))
	}
	return model, provider, nil
}
