package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

type ProviderRepo struct{ DB *sql.DB }

type CreateProviderInput struct {
	Name          string
	ProviderType  domain.ProviderType
	BaseURL       string
	CredentialRef string
	Proxy         string
}

func (r *ProviderRepo) Create(ctx context.Context, input CreateProviderInput) (*domain.ProviderProfile, error) {
	if input.ProviderType != domain.ProviderOpenAICompatible {
		return nil, fmt.Errorf("unsupported provider type: %s", input.ProviderType)
	}
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.CredentialRef = strings.TrimSpace(input.CredentialRef)
	if input.Name == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	parsedURL, err := url.Parse(input.BaseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return nil, fmt.Errorf("provider base URL must be an absolute HTTP URL")
	}
	if err := validateCredentialReference(input.CredentialRef); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	profile := &domain.ProviderProfile{
		ID: uuid.NewString(), Name: input.Name, ProviderType: input.ProviderType,
		BaseURL: input.BaseURL, CredentialRef: input.CredentialRef, Proxy: input.Proxy,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	}
	_, err = r.DB.ExecContext(ctx,
		`INSERT INTO provider_profiles
		 (id, name, provider_type, base_url, credential_ref, proxy, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		profile.ID, profile.Name, profile.ProviderType, profile.BaseURL,
		profile.CredentialRef, profile.Proxy, profile.Status,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("create provider profile: %w", err)
	}
	return profile, nil
}

func validateCredentialReference(ref string) error {
	scheme, value, ok := strings.Cut(ref, ":")
	if !ok || strings.TrimSpace(value) == "" {
		return fmt.Errorf("credentialRef must use env:, file:, or keyring:")
	}
	switch scheme {
	case "env", "file":
		return nil
	case "keyring":
		service, account, valid := strings.Cut(value, "/")
		if !valid || service == "" || account == "" {
			return fmt.Errorf("keyring credentialRef must be keyring:<service>/<account>")
		}
		return nil
	default:
		return fmt.Errorf("credentialRef must use env:, file:, or keyring:")
	}
}

func (r *ProviderRepo) FindByID(ctx context.Context, id string) (*domain.ProviderProfile, error) {
	var profile domain.ProviderProfile
	var createdAt, updatedAt string
	err := r.DB.QueryRowContext(ctx, `SELECT id,name,provider_type,base_url,credential_ref,proxy,status,created_at,updated_at
		FROM provider_profiles WHERE id=? AND status!='deleted'`, strings.TrimSpace(id)).Scan(
		&profile.ID, &profile.Name, &profile.ProviderType, &profile.BaseURL, &profile.CredentialRef,
		&profile.Proxy, &profile.Status, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find provider profile: %w", err)
	}
	profile.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	profile.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &profile, nil
}

func (r *ProviderRepo) List(ctx context.Context) ([]domain.ProviderProfile, error) {
	rows, err := r.DB.QueryContext(ctx,
		`SELECT id, name, provider_type, base_url, credential_ref, proxy, status, created_at, updated_at
		 FROM provider_profiles WHERE status != 'deleted' ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list provider profiles: %w", err)
	}
	defer rows.Close()

	profiles := make([]domain.ProviderProfile, 0)
	for rows.Next() {
		var profile domain.ProviderProfile
		var createdAt, updatedAt string
		if err := rows.Scan(
			&profile.ID, &profile.Name, &profile.ProviderType, &profile.BaseURL,
			&profile.CredentialRef, &profile.Proxy, &profile.Status, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan provider profile: %w", err)
		}
		profile.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		profile.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}
