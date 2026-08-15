package fileconfig

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const ModelsSchemaVersion = 1

var providerKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

type ModelsDocument struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Providers     map[string]ProviderConfig `json:"providers"`
}

type ProviderConfig struct {
	Name       string              `json:"name"`
	Type       domain.ProviderType `json:"type"`
	API        string              `json:"api"`
	BaseURL    string              `json:"baseUrl"`
	Credential string              `json:"credential"`
	Proxy      string              `json:"proxy,omitempty"`
	Models     []ModelConfig       `json:"models"`
}

type ModelConfig struct {
	ID                            string                  `json:"id"`
	Name                          string                  `json:"name,omitempty"`
	ContextWindow                 int                     `json:"contextWindow"`
	MaxTokens                     int                     `json:"maxTokens"`
	InputCostUSDMicrosPerMillion  int64                   `json:"inputCostUsdMicrosPerMillion"`
	OutputCostUSDMicrosPerMillion int64                   `json:"outputCostUsdMicrosPerMillion"`
	Vision                        bool                    `json:"vision"`
	ToolUse                       bool                    `json:"toolUse"`
	Thinking                      bool                    `json:"thinking"`
	ThinkingDialect               domain.ThinkingDialect  `json:"thinkingDialect"`
	ThinkingEfforts               []domain.ThinkingEffort `json:"thinkingEfforts"`
}

type CreateProviderInput struct {
	Key          string
	Name         string
	ProviderType domain.ProviderType
	BaseURL      string
	APIKey       string
	Proxy        string
}

type CreateModelInput struct {
	ProviderID                    string
	ModelName                     string
	DisplayName                   string
	ContextWindow                 int
	MaxOutputTokens               int
	InputCostUSDMicrosPerMillion  int64
	OutputCostUSDMicrosPerMillion int64
	SupportsVision                bool
	SupportsToolUse               bool
	SupportsThinking              bool
	ThinkingDialect               domain.ThinkingDialect
	SupportedThinkingEfforts      []domain.ThinkingEffort
	IsDefault                     bool
}

type ModelStore struct {
	Models      string
	Credentials *CredentialStore
	Settings    *SettingsStore
	mu          sync.RWMutex
	Now         func() time.Time
}

func NewModelStore(modelsPath, credentialsPath, settingsPath string) *ModelStore {
	return &ModelStore{
		Models:      modelsPath,
		Credentials: &CredentialStore{Path: credentialsPath},
		Settings:    &SettingsStore{Path: settingsPath},
	}
}

func (s *ModelStore) CreateProvider(_ context.Context, input CreateProviderInput) (*domain.ProviderProfile, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Key = strings.TrimSpace(input.Key)
	if input.Key == "" {
		input.Key = providerKey(input.Name)
	}
	if !providerKeyPattern.MatchString(input.Key) {
		return nil, fmt.Errorf("provider key must match %s", providerKeyPattern)
	}
	if input.Name == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	if input.ProviderType != domain.ProviderOpenAICompatible {
		return nil, fmt.Errorf("unsupported provider type: %s", input.ProviderType)
	}
	if err := validateBaseURL(input.BaseURL); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	document, _, err := s.load()
	if err != nil {
		return nil, err
	}
	if _, exists := document.Providers[input.Key]; exists {
		return nil, fmt.Errorf("provider %q already exists", input.Key)
	}
	credentialID := input.Key
	if strings.TrimSpace(input.APIKey) != "" {
		if err := s.Credentials.Put(credentialID, input.APIKey); err != nil {
			return nil, err
		}
	}
	document.Providers[input.Key] = ProviderConfig{
		Name: input.Name, Type: input.ProviderType, API: "openai-completions",
		BaseURL: strings.TrimSpace(input.BaseURL), Credential: credentialID,
		Proxy: strings.TrimSpace(input.Proxy), Models: []ModelConfig{},
	}
	if err := writeJSONAtomic(s.Models, document, 0o600); err != nil {
		return nil, fmt.Errorf("write models catalog: %w", err)
	}
	now := s.now()
	return &domain.ProviderProfile{
		ID: input.Key, Name: input.Name, ProviderType: input.ProviderType,
		BaseURL: strings.TrimSpace(input.BaseURL), CredentialConfigured: strings.TrimSpace(input.APIKey) != "",
		Proxy: strings.TrimSpace(input.Proxy), Status: "active", CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *ModelStore) ListProviders(_ context.Context) ([]domain.ProviderProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	document, modifiedAt, err := s.load()
	if err != nil {
		return nil, err
	}
	profiles := make([]domain.ProviderProfile, 0, len(document.Providers))
	for id, provider := range document.Providers {
		profile := providerProfile(id, provider, "", modifiedAt)
		if value, resolveErr := s.Credentials.Resolve(provider.Credential); resolveErr == nil && value != "" {
			profile.CredentialConfigured = true
		} else if resolveErr != nil && !IsCredentialUnavailable(resolveErr) {
			return nil, resolveErr
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	return profiles, nil
}

// FindProvider resolves credentials for trusted Worker-internal callers. API
// list/create responses use ListProviders/CreateProvider and remain redacted.
func (s *ModelStore) FindProvider(_ context.Context, id string) (*domain.ProviderProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	document, modifiedAt, err := s.load()
	if err != nil {
		return nil, err
	}
	provider, ok := document.Providers[strings.TrimSpace(id)]
	if !ok {
		return nil, nil
	}
	apiKey, err := s.Credentials.Resolve(provider.Credential)
	if err != nil && !IsCredentialUnavailable(err) {
		return nil, err
	}
	profile := providerProfile(id, provider, apiKey, modifiedAt)
	return &profile, nil
}

func (s *ModelStore) DeleteProvider(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	document, _, err := s.load()
	if err != nil {
		return err
	}
	provider, exists := document.Providers[id]
	if !exists {
		return fmt.Errorf("provider profile not found: %s", id)
	}
	delete(document.Providers, id)
	if err := writeJSONAtomic(s.Models, document, 0o600); err != nil {
		return fmt.Errorf("write models catalog: %w", err)
	}
	settings, err := s.Settings.Read()
	if err != nil {
		return err
	}
	if strings.HasPrefix(settings.DefaultModel, id+"/") {
		if err := s.Settings.SetDefaultModel(""); err != nil {
			return err
		}
	}
	return s.Credentials.Delete(provider.Credential)
}

func (s *ModelStore) CreateModel(_ context.Context, input CreateModelInput) (*domain.ModelProfile, error) {
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.ModelName = strings.TrimSpace(input.ModelName)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName == "" {
		input.DisplayName = input.ModelName
	}
	model, err := modelConfig(input)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, modifiedAt, err := s.load()
	if err != nil {
		return nil, err
	}
	provider, exists := document.Providers[input.ProviderID]
	if !exists {
		return nil, fmt.Errorf("active provider profile not found: %s", input.ProviderID)
	}
	for _, existing := range provider.Models {
		if existing.ID == model.ID {
			return nil, fmt.Errorf("model profile already exists: %s/%s", input.ProviderID, model.ID)
		}
	}
	provider.Models = append(provider.Models, model)
	sort.Slice(provider.Models, func(i, j int) bool { return provider.Models[i].ID < provider.Models[j].ID })
	document.Providers[input.ProviderID] = provider
	if err := writeJSONAtomic(s.Models, document, 0o600); err != nil {
		return nil, fmt.Errorf("write models catalog: %w", err)
	}
	ref := input.ProviderID + "/" + input.ModelName
	if input.IsDefault {
		if err := s.Settings.SetDefaultModel(ref); err != nil {
			return nil, err
		}
	}
	return modelProfile(input.ProviderID, model, input.IsDefault, maxTime(modifiedAt, s.now())), nil
}

func (s *ModelStore) ListModels(_ context.Context) ([]domain.ModelProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	document, modifiedAt, err := s.load()
	if err != nil {
		return nil, err
	}
	settings, err := s.Settings.Read()
	if err != nil {
		return nil, err
	}
	profiles := make([]domain.ModelProfile, 0)
	for providerID, provider := range document.Providers {
		for _, model := range provider.Models {
			ref := providerID + "/" + model.ID
			profiles = append(profiles, *modelProfile(providerID, model, ref == settings.DefaultModel, modifiedAt))
		}
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].IsDefault != profiles[j].IsDefault {
			return profiles[i].IsDefault
		}
		return profiles[i].ID < profiles[j].ID
	})
	return profiles, nil
}

func (s *ModelStore) FindModel(ctx context.Context, ref string) (*domain.ModelProfile, error) {
	models, err := s.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	for i := range models {
		if models[i].ID == ref {
			return &models[i], nil
		}
	}
	return nil, nil
}

func (s *ModelStore) ResolvePortableRef(ctx context.Context, ref string) (*domain.ModelProfile, error) {
	if _, _, err := SplitModelRef(ref); err != nil {
		return nil, err
	}
	profile, err := s.FindModel(ctx, ref)
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, fmt.Errorf("active model reference %q not found", ref)
	}
	return profile, nil
}

func (s *ModelStore) FirstByProvider(ctx context.Context, providerID string) (*domain.ModelProfile, error) {
	models, err := s.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		if models[i].ProviderID == providerID {
			return &models[i], nil
		}
	}
	return nil, nil
}

func (s *ModelStore) SetDefault(ctx context.Context, ref string) error {
	profile, err := s.FindModel(ctx, ref)
	if err != nil {
		return err
	}
	if profile == nil {
		return fmt.Errorf("active model profile not found: %s", ref)
	}
	return s.Settings.SetDefaultModel(profile.ID)
}

func (s *ModelStore) DeleteModel(ctx context.Context, ref string) error {
	providerID, modelID, err := SplitModelRef(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	document, _, err := s.load()
	if err != nil {
		return err
	}
	provider, exists := document.Providers[providerID]
	if !exists {
		return fmt.Errorf("model profile not found: %s", ref)
	}
	filtered := provider.Models[:0]
	found := false
	for _, model := range provider.Models {
		if model.ID == modelID {
			found = true
			continue
		}
		filtered = append(filtered, model)
	}
	if !found {
		return fmt.Errorf("model profile not found: %s", ref)
	}
	provider.Models = filtered
	document.Providers[providerID] = provider
	if err := writeJSONAtomic(s.Models, document, 0o600); err != nil {
		return err
	}
	settings, err := s.Settings.Read()
	if err != nil {
		return err
	}
	if settings.DefaultModel == ref {
		return s.Settings.SetDefaultModel("")
	}
	return nil
}

func (s *ModelStore) load() (ModelsDocument, time.Time, error) {
	document := ModelsDocument{SchemaVersion: ModelsSchemaVersion, Providers: map[string]ProviderConfig{}}
	found, err := readStrictJSON(s.Models, &document)
	if err != nil {
		return ModelsDocument{}, time.Time{}, fmt.Errorf("read models catalog: %w", err)
	}
	if !found {
		return document, s.now(), nil
	}
	if document.SchemaVersion != ModelsSchemaVersion {
		return ModelsDocument{}, time.Time{}, fmt.Errorf("unsupported models schemaVersion %d", document.SchemaVersion)
	}
	if document.Providers == nil {
		document.Providers = map[string]ProviderConfig{}
	}
	for key, provider := range document.Providers {
		if err := validateProvider(key, provider); err != nil {
			return ModelsDocument{}, time.Time{}, err
		}
	}
	info, err := os.Stat(s.Models)
	if err != nil {
		return ModelsDocument{}, time.Time{}, err
	}
	return document, info.ModTime().UTC(), nil
}

func validateProvider(key string, provider ProviderConfig) error {
	if !providerKeyPattern.MatchString(key) {
		return fmt.Errorf("provider key %q must match %s", key, providerKeyPattern)
	}
	if strings.TrimSpace(provider.Name) == "" {
		return fmt.Errorf("provider %q name is required", key)
	}
	if provider.Type != domain.ProviderOpenAICompatible {
		return fmt.Errorf("provider %q has unsupported type %q", key, provider.Type)
	}
	if provider.API != "openai-completions" {
		return fmt.Errorf("provider %q has unsupported api %q", key, provider.API)
	}
	if err := validateBaseURL(provider.BaseURL); err != nil {
		return fmt.Errorf("provider %q: %w", key, err)
	}
	if !credentialIDPattern.MatchString(provider.Credential) {
		return fmt.Errorf("provider %q credential is invalid", key)
	}
	seen := map[string]bool{}
	for _, model := range provider.Models {
		if seen[model.ID] {
			return fmt.Errorf("provider %q has duplicate model %q", key, model.ID)
		}
		seen[model.ID] = true
		if err := validateModel(model); err != nil {
			return fmt.Errorf("provider %q model %q: %w", key, model.ID, err)
		}
	}
	return nil
}

func modelConfig(input CreateModelInput) (ModelConfig, error) {
	model := ModelConfig{
		ID: input.ModelName, Name: input.DisplayName, ContextWindow: input.ContextWindow,
		MaxTokens:                     input.MaxOutputTokens,
		InputCostUSDMicrosPerMillion:  input.InputCostUSDMicrosPerMillion,
		OutputCostUSDMicrosPerMillion: input.OutputCostUSDMicrosPerMillion,
		Vision:                        input.SupportsVision, ToolUse: input.SupportsToolUse,
		Thinking: input.SupportsThinking, ThinkingDialect: input.ThinkingDialect,
		ThinkingEfforts: append([]domain.ThinkingEffort(nil), input.SupportedThinkingEfforts...),
	}
	if model.ThinkingDialect == "" {
		model.ThinkingDialect = domain.ThinkingDialectNone
	}
	if len(model.ThinkingEfforts) == 0 {
		model.ThinkingEfforts = []domain.ThinkingEffort{domain.ThinkingDefault}
	}
	if err := validateModel(model); err != nil {
		return ModelConfig{}, err
	}
	return model, nil
}

func validateModel(model ModelConfig) error {
	if strings.TrimSpace(model.ID) == "" || strings.ContainsAny(model.ID, " \t\r\n") || len(model.ID) > 200 {
		return fmt.Errorf("model id must be non-empty, contain no whitespace, and be at most 200 bytes")
	}
	if model.ContextWindow <= 0 || model.MaxTokens <= 0 || model.MaxTokens > model.ContextWindow {
		return fmt.Errorf("contextWindow and maxTokens must be positive and maxTokens cannot exceed contextWindow")
	}
	const maxPrice = int64(1_000_000_000)
	if model.InputCostUSDMicrosPerMillion < 0 || model.OutputCostUSDMicrosPerMillion < 0 ||
		model.InputCostUSDMicrosPerMillion > maxPrice || model.OutputCostUSDMicrosPerMillion > maxPrice {
		return fmt.Errorf("model token prices must be between 0 and %d USD micros per million", maxPrice)
	}
	if err := validateThinking(model.Thinking, model.ThinkingDialect, model.ThinkingEfforts); err != nil {
		return err
	}
	return nil
}

func validateThinking(enabled bool, dialect domain.ThinkingDialect, efforts []domain.ThinkingEffort) error {
	if dialect == "" {
		dialect = domain.ThinkingDialectNone
	}
	if len(efforts) == 0 {
		return fmt.Errorf("thinkingEfforts must include default")
	}
	seen := map[domain.ThinkingEffort]bool{}
	for _, effort := range efforts {
		switch effort {
		case domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium, domain.ThinkingHigh:
		default:
			return fmt.Errorf("unsupported thinking effort %q", effort)
		}
		if seen[effort] {
			return fmt.Errorf("duplicate thinking effort %q", effort)
		}
		seen[effort] = true
	}
	if !seen[domain.ThinkingDefault] {
		return fmt.Errorf("thinkingEfforts must include default")
	}
	switch dialect {
	case domain.ThinkingDialectNone:
		if enabled || len(efforts) != 1 {
			return fmt.Errorf("thinking dialect none only supports default")
		}
	case domain.ThinkingDialectOpenAIReasoningEffort:
		if !enabled {
			return fmt.Errorf("thinking must be enabled for dialect %q", dialect)
		}
	default:
		return fmt.Errorf("unsupported thinking dialect %q", dialect)
	}
	return nil
}

func SplitModelRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	separator := strings.IndexByte(ref, '/')
	if separator <= 0 || separator == len(ref)-1 {
		return "", "", fmt.Errorf("model reference must use provider-name/model-name")
	}
	provider, model := ref[:separator], ref[separator+1:]
	if !providerKeyPattern.MatchString(provider) || strings.ContainsAny(model, " \t\r\n") {
		return "", "", fmt.Errorf("model reference must use provider-name/model-name")
	}
	return provider, model, nil
}

func providerProfile(id string, provider ProviderConfig, apiKey string, modifiedAt time.Time) domain.ProviderProfile {
	return domain.ProviderProfile{
		ID: id, Name: provider.Name, ProviderType: provider.Type, BaseURL: provider.BaseURL,
		CredentialRef: provider.Credential, APIKey: apiKey, CredentialConfigured: apiKey != "", Proxy: provider.Proxy, Status: "active",
		CreatedAt: modifiedAt, UpdatedAt: modifiedAt,
	}
}

func modelProfile(providerID string, model ModelConfig, isDefault bool, modifiedAt time.Time) *domain.ModelProfile {
	name := strings.TrimSpace(model.Name)
	if name == "" {
		name = model.ID
	}
	return &domain.ModelProfile{
		ID: providerID + "/" + model.ID, ProviderID: providerID, ModelName: model.ID,
		DisplayName: name, ContextWindow: model.ContextWindow, MaxOutputTokens: model.MaxTokens,
		InputCostUSDMicrosPerMillion:  model.InputCostUSDMicrosPerMillion,
		OutputCostUSDMicrosPerMillion: model.OutputCostUSDMicrosPerMillion,
		SupportsVision:                model.Vision, SupportsToolUse: model.ToolUse,
		SupportsThinking: model.Thinking, ThinkingDialect: model.ThinkingDialect,
		SupportedThinkingEfforts: append([]domain.ThinkingEffort(nil), model.ThinkingEfforts...),
		IsDefault:                isDefault, Status: "active", CreatedAt: modifiedAt, UpdatedAt: modifiedAt,
	}
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("provider base URL must be an absolute HTTP URL")
	}
	return nil
}

func providerKey(name string) string {
	value := strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
			lastDash = false
		} else if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func (s *ModelStore) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func maxTime(first, second time.Time) time.Time {
	if first.After(second) {
		return first
	}
	return second
}
