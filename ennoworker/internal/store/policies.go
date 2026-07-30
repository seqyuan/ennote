package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

var ErrPolicyNotFound = errors.New("policy profile not found")

type PolicyRepo struct{ DB *sql.DB }

type CreatePolicyInput struct {
	Name   string
	Kind   domain.PolicyKind
	Config json.RawMessage
}

func (r *PolicyRepo) CreateVersion(ctx context.Context, input CreatePolicyInput) (*domain.PolicyProfile, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return nil, fmt.Errorf("policy name is required")
	}
	config, err := validatePolicyConfig(input.Kind, input.Config)
	if err != nil {
		return nil, err
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM policy_profiles
		WHERE kind=? AND name=?`, input.Kind, input.Name).Scan(&version); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	profile := &domain.PolicyProfile{ID: uuid.NewString(), Name: input.Name, Kind: input.Kind,
		Version: version, Config: config, Status: "active", CreatedAt: now, UpdatedAt: now}
	_, err = tx.ExecContext(ctx, `INSERT INTO policy_profiles
		(id,name,kind,version,config_json,status,created_at,updated_at)
		VALUES (?,?,?,?,?,'active',?,?)`, profile.ID, profile.Name, profile.Kind, profile.Version,
		string(profile.Config), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return profile, nil
}

func (r *PolicyRepo) List(ctx context.Context, kind domain.PolicyKind) ([]domain.PolicyProfile, error) {
	query := `SELECT id,name,kind,version,config_json,status,created_at,updated_at
		FROM policy_profiles`
	args := []any{}
	if kind != "" {
		query += ` WHERE kind=?`
		args = append(args, kind)
	}
	query += ` ORDER BY kind,name,version DESC,id`
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]domain.PolicyProfile, 0)
	for rows.Next() {
		profile, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (r *PolicyRepo) FindByID(ctx context.Context, id string) (*domain.PolicyProfile, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT id,name,kind,version,config_json,status,created_at,updated_at
		FROM policy_profiles WHERE id=?`, id)
	profile, err := scanPolicy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	}
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (r *PolicyRepo) SetDefault(ctx context.Context, id string) error {
	profile, err := r.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if profile.Status != "active" {
		return ErrPolicyNotFound
	}
	key := map[domain.PolicyKind]string{
		domain.PolicyKindTool: "default_tool_policy_profile_id", domain.PolicyKindTurn: "default_turn_policy_profile_id",
		domain.PolicyKindVision: "default_vision_policy_profile_id", domain.PolicyKindCompaction: "default_compaction_policy_profile_id",
	}[profile.Kind]
	if key == "" {
		return fmt.Errorf("invalid policy kind %q", profile.Kind)
	}
	_, err = r.DB.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, id)
	return err
}

func (r *PolicyRepo) Deactivate(ctx context.Context, id string) error {
	result, err := r.DB.ExecContext(ctx, `UPDATE policy_profiles SET status='inactive',updated_at=?
		WHERE id=? AND status='active'`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrPolicyNotFound
	}
	return nil
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

type policyScanner interface{ Scan(...any) error }

func scanPolicy(scanner policyScanner) (domain.PolicyProfile, error) {
	var profile domain.PolicyProfile
	var configText, createdAt, updatedAt string
	err := scanner.Scan(&profile.ID, &profile.Name, &profile.Kind, &profile.Version, &configText,
		&profile.Status, &createdAt, &updatedAt)
	if err != nil {
		return profile, err
	}
	profile.Config = json.RawMessage(configText)
	profile.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	profile.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return profile, nil
}
