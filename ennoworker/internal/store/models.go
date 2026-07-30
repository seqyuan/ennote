package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const defaultModelSettingKey = "default_model_profile_id"

type ModelRepo struct{ DB *sql.DB }

type CreateModelInput struct {
	ProviderID       string
	ModelName        string
	DisplayName      string
	ContextWindow    int
	MaxOutputTokens  int
	SupportsVision   bool
	SupportsToolUse  bool
	SupportsThinking bool
	IsDefault        bool
}

func (r *ModelRepo) Create(ctx context.Context, input CreateModelInput) (*domain.ModelProfile, error) {
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.ModelName = strings.TrimSpace(input.ModelName)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.ProviderID == "" || input.ModelName == "" {
		return nil, fmt.Errorf("providerId and modelName are required")
	}
	if input.DisplayName == "" {
		input.DisplayName = input.ModelName
	}
	if input.ContextWindow <= 0 || input.MaxOutputTokens <= 0 || input.MaxOutputTokens > input.ContextWindow {
		return nil, fmt.Errorf("contextWindow and maxOutputTokens must be positive and maxOutputTokens cannot exceed contextWindow")
	}
	now := time.Now().UTC()
	profile := &domain.ModelProfile{
		ID: uuid.NewString(), ProviderID: input.ProviderID, ModelName: input.ModelName,
		DisplayName: input.DisplayName, ContextWindow: input.ContextWindow,
		MaxOutputTokens: input.MaxOutputTokens, SupportsVision: input.SupportsVision,
		SupportsToolUse: input.SupportsToolUse, SupportsThinking: input.SupportsThinking,
		IsDefault: input.IsDefault, Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin model profile transaction: %w", err)
	}
	defer tx.Rollback()
	var providerExists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_profiles WHERE id = ? AND status = 'active'`, input.ProviderID).Scan(&providerExists); err != nil {
		return nil, err
	}
	if providerExists != 1 {
		return nil, fmt.Errorf("active provider profile not found: %s", input.ProviderID)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO model_profiles
		(id, provider_id, model_name, display_name, context_window, max_output_tokens,
		 supports_vision, supports_tool_use, supports_thinking, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		profile.ID, profile.ProviderID, profile.ModelName, profile.DisplayName,
		profile.ContextWindow, profile.MaxOutputTokens, boolInt(profile.SupportsVision),
		boolInt(profile.SupportsToolUse), boolInt(profile.SupportsThinking),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("create model profile: %w", err)
	}
	if input.IsDefault {
		if err := setDefaultModelTx(ctx, tx, profile.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit model profile: %w", err)
	}
	return profile, nil
}

func (r *ModelRepo) List(ctx context.Context) ([]domain.ModelProfile, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT m.id, m.provider_id, m.model_name, m.display_name,
		m.context_window, m.max_output_tokens, m.supports_vision, m.supports_tool_use,
		m.supports_thinking, m.status, m.created_at, m.updated_at,
		CASE WHEN m.id = (SELECT value FROM settings WHERE key = ?) THEN 1 ELSE 0 END
		FROM model_profiles m WHERE m.status = 'active'
		ORDER BY 13 DESC, m.display_name, m.id`, defaultModelSettingKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]domain.ModelProfile, 0)
	for rows.Next() {
		var profile domain.ModelProfile
		var vision, tools, thinking, isDefault int
		var createdAt, updatedAt string
		if err := rows.Scan(&profile.ID, &profile.ProviderID, &profile.ModelName, &profile.DisplayName,
			&profile.ContextWindow, &profile.MaxOutputTokens, &vision, &tools, &thinking,
			&profile.Status, &createdAt, &updatedAt, &isDefault); err != nil {
			return nil, err
		}
		profile.SupportsVision = vision != 0
		profile.SupportsToolUse = tools != 0
		profile.SupportsThinking = thinking != 0
		profile.IsDefault = isDefault != 0
		profile.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		profile.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (r *ModelRepo) FindByID(ctx context.Context, modelID string) (*domain.ModelProfile, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT id,provider_id,model_name,display_name,context_window,max_output_tokens,
		supports_vision,supports_tool_use,supports_thinking,status,created_at,updated_at
		FROM model_profiles WHERE id=? AND status='active'`, strings.TrimSpace(modelID))
	profile, err := scanModelProfile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find model profile: %w", err)
	}
	return &profile, nil
}

func (r *ModelRepo) FirstByProvider(ctx context.Context, providerID string) (*domain.ModelProfile, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT id,provider_id,model_name,display_name,context_window,max_output_tokens,
		supports_vision,supports_tool_use,supports_thinking,status,created_at,updated_at
		FROM model_profiles WHERE provider_id=? AND status='active' ORDER BY updated_at DESC,id LIMIT 1`, strings.TrimSpace(providerID))
	profile, err := scanModelProfile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find provider model: %w", err)
	}
	return &profile, nil
}

type modelScanner interface{ Scan(...any) error }

func scanModelProfile(scanner modelScanner) (domain.ModelProfile, error) {
	var profile domain.ModelProfile
	var vision, tools, thinking int
	var createdAt, updatedAt string
	err := scanner.Scan(&profile.ID, &profile.ProviderID, &profile.ModelName, &profile.DisplayName,
		&profile.ContextWindow, &profile.MaxOutputTokens, &vision, &tools, &thinking,
		&profile.Status, &createdAt, &updatedAt)
	if err != nil {
		return profile, err
	}
	profile.SupportsVision = vision != 0
	profile.SupportsToolUse = tools != 0
	profile.SupportsThinking = thinking != 0
	profile.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	profile.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return profile, nil
}

func (r *ModelRepo) SetDefault(ctx context.Context, modelID string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setDefaultModelTx(ctx, tx, strings.TrimSpace(modelID)); err != nil {
		return err
	}
	return tx.Commit()
}

func setDefaultModelTx(ctx context.Context, tx *sql.Tx, modelID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_profiles m
		JOIN provider_profiles p ON p.id = m.provider_id
		WHERE m.id = ? AND m.status = 'active' AND p.status = 'active'`, modelID).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return fmt.Errorf("active model profile not found: %s", modelID)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, defaultModelSettingKey, modelID)
	return err
}
