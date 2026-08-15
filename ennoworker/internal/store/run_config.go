package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	// V2: effective-config freezing is file-native only. The legacy global
	// provider/model/policy SQL path was removed; a RunRepo without a
	// file-backed model resolver cannot freeze a config.
	if r.Models == nil || r.Models.Files == nil {
		return nil, domain.NewCodedError(domain.ErrorProviderUnavailable,
			errors.New("file-backed model resolver is required to freeze an effective config"))
	}
	return r.resolveAndFreezeFileConfig(ctx, run)
}

func runtimeSnapshot(model domain.ModelProfile, provider domain.ProviderProfile) domain.ModelRuntimeSnapshot {
	return domain.ModelRuntimeSnapshot{
		ProviderProfileID: provider.ID, ModelProfileID: model.ID, APIModel: model.ModelName,
		BaseURL: provider.BaseURL, CredentialRef: firstNonEmpty(provider.CredentialRef, provider.ID),
		APIKey: provider.APIKey, Proxy: provider.Proxy,
		ContextTokens: model.ContextWindow, MaxOutputTokens: model.MaxOutputTokens,
		SupportsVision: model.SupportsVision, SupportsToolUse: model.SupportsToolUse,
		SupportsThinking: model.SupportsThinking, ThinkingDialect: model.ThinkingDialect,
		SupportedThinkingEfforts: append([]domain.ThinkingEffort(nil), model.SupportedThinkingEfforts...),
	}
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

// digestDelegationPolicy derives the canonical snapshot digest. The digest is
// computed in Go only; SQL never duplicates this logic.
func digestJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// DigestJSON is the exported digest helper used by file-native Run freezers.
func DigestJSON(value any) (string, error) {
	return digestJSON(value)
}

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
