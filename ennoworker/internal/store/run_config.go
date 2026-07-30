package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const (
	defaultMaxIterations          = 32
	defaultMaxConcurrentReadTools = 4
)

type ResolvedRunConfig struct {
	Effective domain.EffectiveRunConfig
	Provider  domain.ProviderProfile
	Model     domain.ModelProfile
}

type requestedRunConfig struct {
	ModelProfileID            string   `json:"modelProfileId"`
	CandidateModelProfileIDs  []string `json:"candidateModelProfileIds"`
	AllowAutoRoute            bool     `json:"allowAutoRoute"`
	ToolPolicyProfileID       string   `json:"toolPolicyProfileId"`
	TurnPolicyProfileID       string   `json:"turnPolicyProfileId"`
	VisionPolicyProfileID     string   `json:"visionPolicyProfileId"`
	CompactionPolicyProfileID string   `json:"compactionPolicyProfileId"`
	MaxIterations             int      `json:"maxIterations"`
	ToolExecution             string   `json:"toolExecution"`
	MaxConcurrentReadTools    int      `json:"maxConcurrentReadTools"`
}

func (r *RunRepo) ResolveAndFreezeConfig(ctx context.Context, run *domain.AgentRun) (*ResolvedRunConfig, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin effective config transaction: %w", err)
	}
	defer tx.Rollback()

	var currentStatus domain.RunStatus
	var storedEffective, requested string
	var sessionModelID, sessionAgentID, sessionCompactionPolicyID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ar.status, ar.effective_config_json, ar.requested_config_json,
		s.default_model_profile_id, s.default_agent_profile_id, s.compaction_policy_profile_id
		FROM agent_runs ar JOIN sessions s ON s.id = ar.session_id WHERE ar.id = ?`, run.ID).
		Scan(&currentStatus, &storedEffective, &requested, &sessionModelID, &sessionAgentID, &sessionCompactionPolicyID); err != nil {
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

	var agentModelID, agentToolPolicyID, agentTurnPolicyID, agentVisionPolicyID, agentCompactionPolicyID sql.NullString
	if sessionAgentID.Valid {
		_ = tx.QueryRowContext(ctx, `SELECT default_model_id,tool_policy_profile_id,
			turn_policy_profile_id,vision_policy_profile_id,compaction_policy_profile_id FROM agent_profiles
			WHERE id=? AND status='active'`, sessionAgentID.String).Scan(
			&agentModelID, &agentToolPolicyID, &agentTurnPolicyID, &agentVisionPolicyID, &agentCompactionPolicyID)
	}
	modelID := strings.TrimSpace(requestedConfig.ModelProfileID)
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

	toolPolicyID := firstNonEmpty(requestedConfig.ToolPolicyProfileID, nullString(agentToolPolicyID))
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
	if len(candidateIDs) == 0 {
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
	threshold := turnConfig.Threshold
	if threshold == 0 {
		threshold = 0.7
	}
	allowAutoRoute := requestedConfig.AllowAutoRoute || (turnConfig.Mode == "context_upgrade" && requestedConfig.ModelProfileID == "")

	maxIterations := requestedConfig.MaxIterations
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
		ToolExecution: domain.ToolExecutionConfig{
			Mode: mode, MaxConcurrentReadTools: maxReadTools,
		},
		InitialRuntime: initialRuntime,
		Routing: domain.FrozenRoutingConfig{Candidates: candidates, Threshold: threshold,
			Pinned:         requestedConfig.ModelProfileID != "" && !requestedConfig.AllowAutoRoute,
			AllowAutoRoute: allowAutoRoute},
		ToolPolicy: toolPolicy, TurnPolicy: turnPolicy, VisionPolicy: visionPolicy,
		CompactionPolicy: compactionPolicy, CompactionRuntime: compactionRuntime,
	}
	encoded, err := json.Marshal(effective)
	if err != nil {
		return nil, fmt.Errorf("encode effective config: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET effective_config_json = ?
		WHERE id = ? AND status = 'running' AND effective_config_json = '{}'`, string(encoded), run.ID)
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
	return &ResolvedRunConfig{Effective: effective, Provider: provider, Model: model}, nil
}

func loadResolvedProfilesTx(ctx context.Context, tx *sql.Tx, effective domain.EffectiveRunConfig) (*ResolvedRunConfig, error) {
	if effective.InitialRuntime.ModelProfileID != "" {
		runtime := effective.InitialRuntime
		model := domain.ModelProfile{ID: runtime.ModelProfileID, ProviderID: runtime.ProviderProfileID,
			ModelName: runtime.APIModel, DisplayName: runtime.APIModel, ContextWindow: runtime.ContextTokens,
			MaxOutputTokens: runtime.MaxOutputTokens, SupportsVision: runtime.SupportsVision,
			SupportsToolUse: runtime.SupportsToolUse, SupportsThinking: runtime.SupportsThinking, Status: "frozen"}
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
		SupportsThinking: model.SupportsThinking,
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

func loadModelAndProviderTx(ctx context.Context, tx *sql.Tx, modelID string) (domain.ModelProfile, domain.ProviderProfile, error) {
	var model domain.ModelProfile
	var provider domain.ProviderProfile
	var vision, tools, thinking int
	err := tx.QueryRowContext(ctx, `SELECT m.id, m.provider_id, m.model_name, m.display_name,
		m.context_window, m.max_output_tokens, m.supports_vision, m.supports_tool_use,
		m.supports_thinking, m.status, p.id, p.name, p.provider_type, p.base_url,
		p.credential_ref, p.proxy, p.status
		FROM model_profiles m JOIN provider_profiles p ON p.id = m.provider_id
		WHERE m.id = ? AND m.status = 'active' AND p.status = 'active'`, modelID).Scan(
		&model.ID, &model.ProviderID, &model.ModelName, &model.DisplayName,
		&model.ContextWindow, &model.MaxOutputTokens, &vision, &tools, &thinking,
		&model.Status, &provider.ID, &provider.Name, &provider.ProviderType,
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
	return model, provider, nil
}
