package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
)

var ErrPolicyNotFound = errors.New("policy profile not found")

// PolicyRepo resolves Policy profiles from the file-native policy catalog
// (V2). The legacy global policy_profiles SQL table was removed.
type PolicyRepo struct {
	Files *fileconfig.PolicyStore
}

type CreatePolicyInput struct {
	Name   string
	Kind   domain.PolicyKind
	Config json.RawMessage
}

func (r *PolicyRepo) CreateVersion(ctx context.Context, input CreatePolicyInput) (*domain.PolicyProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return nil, fmt.Errorf("policy name is required")
	}
	config, err := validatePolicyConfig(input.Kind, input.Config)
	if err != nil {
		return nil, err
	}
	return r.Files.CreateVersion(ctx, input.Name, input.Kind, config)
}

func (r *PolicyRepo) List(ctx context.Context, kind domain.PolicyKind) ([]domain.PolicyProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	return r.Files.Profiles(ctx, kind)
}

func (r *PolicyRepo) FindByID(ctx context.Context, id string) (*domain.PolicyProfile, error) {
	if r == nil || r.Files == nil {
		return nil, ErrFileBackedStoreRequired
	}
	profile, err := r.Files.FindProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, ErrPolicyNotFound
	}
	return profile, nil
}

func (r *PolicyRepo) SetDefault(ctx context.Context, id string) error {
	if r == nil || r.Files == nil {
		return ErrFileBackedStoreRequired
	}
	return r.Files.SetDefaultProfile(id)
}

func (r *PolicyRepo) Deactivate(ctx context.Context, id string) error {
	if r == nil || r.Files == nil {
		return ErrFileBackedStoreRequired
	}
	return r.Files.DeactivateProfile(id)
}

func validatePolicyConfig(kind domain.PolicyKind, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	switch kind {
	case domain.PolicyKindTool:
		var config domain.ToolPolicyConfig
		if err := decodeStrictJSON(raw, &config); err != nil {
			return nil, fmt.Errorf("invalid tool policy config: %w", err)
		}
		if config.Mode != "allow_existing_behavior" && config.Mode != "restricted" &&
			config.Mode != string(domain.PermissionDiscuss) && config.Mode != string(domain.PermissionAuto) {
			return nil, fmt.Errorf("invalid tool policy mode %q", config.Mode)
		}
		if config.MaxTimeoutSeconds < 0 {
			return nil, fmt.Errorf("maxTimeoutSeconds cannot be negative")
		}
	case domain.PolicyKindTurn:
		var config domain.TurnPolicyConfig
		if err := decodeStrictJSON(raw, &config); err != nil {
			return nil, fmt.Errorf("invalid turn policy config: %w", err)
		}
		if config.Mode != "fixed_model" && config.Mode != "context_upgrade" {
			return nil, fmt.Errorf("invalid turn policy mode %q", config.Mode)
		}
		if config.Threshold == 0 {
			config.Threshold = 0.7
		}
		if config.Threshold <= 0 || config.Threshold >= 1 {
			return nil, fmt.Errorf("turn policy threshold must be between 0 and 1")
		}
		raw, _ = json.Marshal(config)
	case domain.PolicyKindVision:
		var config domain.VisionPolicyConfig
		if err := decodeStrictJSON(raw, &config); err != nil {
			return nil, fmt.Errorf("invalid vision policy config: %w", err)
		}
		if config.Mode != "reject" && config.Mode != "route" && config.Mode != "describe" {
			return nil, fmt.Errorf("invalid vision policy mode %q", config.Mode)
		}
		if config.Mode == "describe" && strings.TrimSpace(config.DescriptorModelProfileID) == "" {
			return nil, fmt.Errorf("descriptorModelProfileId is required in describe mode")
		}
		if config.MaxImageBytes < 0 || config.MaxPixels < 0 {
			return nil, fmt.Errorf("vision limits cannot be negative")
		}
	case domain.PolicyKindCompaction:
		defaults := domain.DefaultCompactionPolicy()
		config := defaults
		if err := decodeStrictJSON(raw, &config); err != nil {
			return nil, fmt.Errorf("invalid compaction policy config: %w", err)
		}
		if config.Mode != domain.CompactionDisabled && config.Mode != domain.CompactionManualOnly && config.Mode != domain.CompactionManualAndAuto {
			return nil, fmt.Errorf("invalid compaction policy mode %q", config.Mode)
		}
		if config.TriggerRatio <= 0 || config.TriggerRatio >= 1 || config.SummaryInputRatio <= 0 || config.SummaryInputRatio >= 1 {
			return nil, fmt.Errorf("compaction triggerRatio and summaryInputRatio must be between 0 and 1")
		}
		if config.TailTokenRatio <= 0 || config.TailTokenRatio >= 1 || config.KeepRecentTurns < 1 ||
			config.TailMinTokens < 1 || config.TailMaxTokens < config.TailMinTokens || config.SummaryMaxOutputTokens < 1 {
			return nil, fmt.Errorf("invalid compaction tail or summary budget")
		}
		if config.MaxOverflowRecoveries < 0 || config.MaxOverflowRecoveries > 1 ||
			config.IneffectiveReclaimRatio < 0 || config.IneffectiveReclaimRatio >= 1 ||
			config.IneffectiveLimit < 1 || config.FailureCooldownSeconds < 0 || strings.TrimSpace(config.PromptVersion) == "" {
			return nil, fmt.Errorf("invalid compaction recovery or breaker configuration")
		}
		raw, _ = json.Marshal(config)
	default:
		return nil, fmt.Errorf("invalid policy kind %q", kind)
	}
	canonical := make(json.RawMessage, len(raw))
	copy(canonical, raw)
	return canonical, nil
}

func decodeStrictJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
