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
	"github.com/seqyuan/ennote/ennoworker/internal/modelcatalog"
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

// ModelConfig is the on-disk (config/models.json) shape of one model. Every
// field except ID/Name is a pointer: nil means "not declared" (the built-in
// catalog supplies a default via overlayModel), while a non-nil pointer means
// "explicitly declared" — including an explicit false or 0 that must win over
// the catalog default.
type ModelConfig struct {
	ID                            string                   `json:"id"`
	Name                          string                   `json:"name,omitempty"`
	ContextWindow                 *int                     `json:"contextWindow,omitempty"`
	MaxTokens                     *int                     `json:"maxTokens,omitempty"`
	InputCostUSDMicrosPerMillion  *int64                   `json:"inputCostUsdMicrosPerMillion,omitempty"`
	OutputCostUSDMicrosPerMillion *int64                   `json:"outputCostUsdMicrosPerMillion,omitempty"`
	Vision                        *bool                    `json:"vision,omitempty"`
	ToolUse                       *bool                    `json:"toolUse,omitempty"`
	Thinking                      *bool                    `json:"thinking,omitempty"`
	ThinkingDialect               *domain.ThinkingDialect  `json:"thinkingDialect,omitempty"`
	ThinkingEfforts               *[]domain.ThinkingEffort `json:"thinkingEfforts,omitempty"`
}

type CreateProviderInput struct {
	Key          string
	Name         string
	ProviderType domain.ProviderType
	BaseURL      string
	APIKey       string
	Proxy        string
}

// UpdateProviderInput carries the fields the Models tab can edit on an existing
// provider. Zero/empty fields mean "leave unchanged" — an empty APIKey never
// deletes a stored credential.
type UpdateProviderInput struct {
	Name    string
	BaseURL string
	APIKey  string
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
	snap        fileSnapshot[ModelsDocument]
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
	api := apiForType(input.ProviderType)
	if api == "" {
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
		Name: input.Name, Type: input.ProviderType, API: api,
		BaseURL: strings.TrimSpace(input.BaseURL), Credential: credentialID,
		Proxy: strings.TrimSpace(input.Proxy), Models: []ModelConfig{},
	}
	if err := writeJSONAtomic(s.Models, document, 0o600); err != nil {
		return nil, fmt.Errorf("write models catalog: %w", err)
	}
	now := s.now()
	return &domain.ProviderProfile{
		ID: input.Key, Name: input.Name, ProviderType: input.ProviderType, API: api,
		BaseURL: strings.TrimSpace(input.BaseURL), CredentialConfigured: strings.TrimSpace(input.APIKey) != "",
		Proxy: strings.TrimSpace(input.Proxy), Status: "active", Custom: !modelcatalog.HasProvider(input.Key),
		ModelsCustomized: false, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// UpdateProvider edits the name, base URL, and (optionally) API key of an
// existing provider. A blank API key leaves the stored credential untouched;
// a non-blank key replaces it through the credential store.
func (s *ModelStore) UpdateProvider(_ context.Context, id string, input UpdateProviderInput) (*domain.ProviderProfile, error) {
	id = strings.TrimSpace(id)
	if !providerKeyPattern.MatchString(id) {
		return nil, fmt.Errorf("provider key must match %s", providerKeyPattern)
	}
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	if input.BaseURL != "" {
		if err := validateBaseURL(input.BaseURL); err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	document, modifiedAt, err := s.load()
	if err != nil {
		return nil, err
	}
	provider, exists := document.Providers[id]
	if !exists {
		return nil, fmt.Errorf("provider profile not found: %s", id)
	}
	if input.Name != "" {
		provider.Name = input.Name
	}
	if input.BaseURL != "" {
		provider.BaseURL = input.BaseURL
	}
	if strings.TrimSpace(input.APIKey) != "" {
		if err := s.Credentials.Put(provider.Credential, input.APIKey); err != nil {
			return nil, err
		}
	}
	document.Providers[id] = provider
	if err := writeJSONAtomic(s.Models, document, 0o600); err != nil {
		return nil, fmt.Errorf("write models catalog: %w", err)
	}
	profile := providerProfile(id, provider, "", maxTime(modifiedAt, s.now()))
	if value, resolveErr := s.Credentials.Resolve(provider.Credential); resolveErr == nil && value != "" {
		profile.CredentialConfigured = true
	} else if resolveErr != nil && !IsCredentialUnavailable(resolveErr) {
		return nil, resolveErr
	}
	return &profile, nil
}

func (s *ModelStore) ListProviders(_ context.Context) ([]domain.ProviderProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	document, modifiedAt, err := s.loadForRead()
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
	document, modifiedAt, err := s.loadForRead()
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
	model := modelConfig(input)
	resolved := overlayModel(input.ProviderID, model)
	if err := validateModel(resolved); err != nil {
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
	return modelProfile(input.ProviderID, resolved, input.IsDefault, maxTime(modifiedAt, s.now())), nil
}

func (s *ModelStore) ListModels(_ context.Context) ([]domain.ModelProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	document, modifiedAt, err := s.loadForRead()
	if err != nil {
		return nil, err
	}
	settings, err := s.Settings.Read()
	if err != nil {
		return nil, err
	}
	profiles := make([]domain.ModelProfile, 0)
	for providerID, provider := range document.Providers {
		// An empty model list means "serve this provider's built-in catalog":
		// materialize each catalog model as an overlay-only entry (nil fields,
		// filled by overlayModel below). A provider the catalog does not
		// describe therefore still lists nothing.
		models := provider.Models
		if len(models) == 0 {
			for _, catalogID := range modelcatalog.ProviderModelIDs(providerID) {
				models = append(models, ModelConfig{ID: catalogID})
			}
		}
		for _, model := range models {
			resolved := overlayModel(providerID, model)
			ref := providerID + "/" + resolved.ID
			profiles = append(profiles, *modelProfile(providerID, resolved, ref == settings.DefaultModel, modifiedAt))
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

// load reads the current on-disk document and, on success, caches it as the
// store's latest valid snapshot. A parse/validation failure leaves the
// snapshot untouched. Callers that mutate the document must treat a non-nil
// error as fail-closed and not write.
func (s *ModelStore) load() (ModelsDocument, time.Time, error) {
	document, modified, err := s.loadDisk()
	if err == nil {
		s.snap.set(document, modified)
	}
	return document, modified, err
}

func (s *ModelStore) loadDisk() (ModelsDocument, time.Time, error) {
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

// loadForRead is the read-path entry: it prefers the current on-disk document
// and degrades to the latest valid snapshot when the file is currently
// unparsable. It returns an error only when there is no snapshot to fall back
// to, so a running Worker keeps serving its last valid configuration instead
// of failing reads after an external edit breaks the file.
func (s *ModelStore) loadForRead() (ModelsDocument, time.Time, error) {
	document, modified, err := s.load()
	if err == nil {
		return document, modified, nil
	}
	if snap, snapModified, ok := s.snap.get(); ok {
		return snap, snapModified, nil
	}
	return document, modified, err
}

// StartWatch begins watching the models catalog. File changes re-load the
// snapshot on a debounce. A watch that cannot be established is a no-op
// (reads re-read on every access, so hot reload still works). It returns a
// stop function.
func (s *ModelStore) StartWatch() (stop func()) {
	return watchFile(s.Models, 100*time.Millisecond, func() {
		s.mu.Lock()
		_, _, _ = s.load()
		s.mu.Unlock()
	})
}

// apiForType maps a provider type to its wire protocol. Unknown types return
// an empty string, which callers treat as "unsupported type".
func apiForType(t domain.ProviderType) string {
	switch t {
	case domain.ProviderOpenAICompatible:
		return domain.APIOpenAICompletions
	case domain.ProviderAnthropic:
		return domain.APIAnthropicMessages
	default:
		return ""
	}
}

func validateProvider(key string, provider ProviderConfig) error {
	if !providerKeyPattern.MatchString(key) {
		return fmt.Errorf("provider key %q must match %s", key, providerKeyPattern)
	}
	if strings.TrimSpace(provider.Name) == "" {
		return fmt.Errorf("provider %q name is required", key)
	}
	expectedAPI := apiForType(provider.Type)
	if expectedAPI == "" {
		return fmt.Errorf("provider %q has unsupported type %q", key, provider.Type)
	}
	if provider.API != expectedAPI {
		return fmt.Errorf("provider %q has api %q incompatible with type %q", key, provider.API, provider.Type)
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
		if err := validateModel(overlayModel(key, model)); err != nil {
			return fmt.Errorf("provider %q model %q: %w", key, model.ID, err)
		}
	}
	return nil
}

// modelConfig builds the on-disk ModelConfig shape from a CreateModel input.
// A zero-valued input field is treated as "not declared" (nil on disk) so the
// built-in catalog can supply it; a non-zero field is an explicit declaration.
// This means a caller cannot explicitly declare a false bool or a zero cost
// through this API — use a hand-written models.json for exact field-level
// overrides. A catalog miss on a required field is reported by validateModel.
func modelConfig(input CreateModelInput) ModelConfig {
	model := ModelConfig{ID: input.ModelName, Name: input.DisplayName}
	if input.ContextWindow > 0 {
		model.ContextWindow = &input.ContextWindow
	}
	if input.MaxOutputTokens > 0 {
		model.MaxTokens = &input.MaxOutputTokens
	}
	if input.InputCostUSDMicrosPerMillion != 0 {
		model.InputCostUSDMicrosPerMillion = &input.InputCostUSDMicrosPerMillion
	}
	if input.OutputCostUSDMicrosPerMillion != 0 {
		model.OutputCostUSDMicrosPerMillion = &input.OutputCostUSDMicrosPerMillion
	}
	if input.SupportsVision {
		model.Vision = &input.SupportsVision
	}
	if input.SupportsToolUse {
		model.ToolUse = &input.SupportsToolUse
	}
	if input.SupportsThinking {
		model.Thinking = &input.SupportsThinking
	}
	if input.ThinkingDialect != "" {
		model.ThinkingDialect = &input.ThinkingDialect
	}
	if len(input.SupportedThinkingEfforts) > 0 {
		efforts := append([]domain.ThinkingEffort(nil), input.SupportedThinkingEfforts...)
		model.ThinkingEfforts = &efforts
	}
	return model
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// overlayModel fills a model's nil fields from the built-in catalog. A model
// the catalog does not describe is returned unchanged (its nil fields stay nil
// and validateModel reports them as missing); a model the catalog does
// describe has every nil field filled with the catalog default while every
// explicitly declared field (including an explicit false or 0) is preserved.
func overlayModel(providerKey string, model ModelConfig) ModelConfig {
	defaults, ok := modelcatalog.Lookup(providerKey, model.ID)
	if !ok {
		return model
	}
	if model.ContextWindow == nil {
		model.ContextWindow = &defaults.ContextWindow
	}
	if model.MaxTokens == nil {
		model.MaxTokens = &defaults.MaxTokens
	}
	if model.InputCostUSDMicrosPerMillion == nil {
		model.InputCostUSDMicrosPerMillion = &defaults.InputCostUSDMicrosPerMillion
	}
	if model.OutputCostUSDMicrosPerMillion == nil {
		model.OutputCostUSDMicrosPerMillion = &defaults.OutputCostUSDMicrosPerMillion
	}
	if model.Vision == nil {
		model.Vision = &defaults.Vision
	}
	if model.ToolUse == nil {
		model.ToolUse = &defaults.ToolUse
	}
	if model.Thinking == nil {
		model.Thinking = &defaults.Thinking
	}
	if model.ThinkingDialect == nil {
		model.ThinkingDialect = &defaults.ThinkingDialect
	}
	if model.ThinkingEfforts == nil {
		efforts := append([]domain.ThinkingEffort(nil), defaults.ThinkingEfforts...)
		model.ThinkingEfforts = &efforts
	}
	return model
}

// validateModel checks a catalog-overlaid (fully populated) ModelConfig.
// Callers run overlayModel first so nil contextWindow/maxTokens already hold a
// catalog value; a remaining nil means the model is not in the catalog and the
// field was not declared.
func validateModel(model ModelConfig) error {
	if strings.TrimSpace(model.ID) == "" || strings.ContainsAny(model.ID, " \t\r\n") || len(model.ID) > 200 {
		return fmt.Errorf("model id must be non-empty, contain no whitespace, and be at most 200 bytes")
	}
	if model.ContextWindow == nil || model.MaxTokens == nil {
		return fmt.Errorf("contextWindow and maxTokens are required (no catalog entry for this model)")
	}
	if *model.ContextWindow <= 0 || *model.MaxTokens <= 0 || *model.MaxTokens > *model.ContextWindow {
		return fmt.Errorf("contextWindow and maxTokens must be positive and maxTokens cannot exceed contextWindow")
	}
	const maxPrice = int64(1_000_000_000)
	inputCost, outputCost := derefInt64(model.InputCostUSDMicrosPerMillion), derefInt64(model.OutputCostUSDMicrosPerMillion)
	if inputCost < 0 || outputCost < 0 || inputCost > maxPrice || outputCost > maxPrice {
		return fmt.Errorf("model token prices must be between 0 and %d USD micros per million", maxPrice)
	}
	thinking := derefBool(model.Thinking)
	dialect := domain.ThinkingDialectNone
	if model.ThinkingDialect != nil {
		dialect = *model.ThinkingDialect
	}
	efforts := []domain.ThinkingEffort{}
	if model.ThinkingEfforts != nil {
		efforts = *model.ThinkingEfforts
	}
	if len(efforts) == 0 {
		efforts = []domain.ThinkingEffort{domain.ThinkingDefault}
	}
	if err := validateThinking(thinking, dialect, efforts); err != nil {
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
		ID: id, Name: provider.Name, ProviderType: provider.Type, API: provider.API, BaseURL: provider.BaseURL,
		CredentialRef: provider.Credential, APIKey: apiKey, CredentialConfigured: apiKey != "", Proxy: provider.Proxy, Status: "active",
		Custom:           !modelcatalog.HasProvider(id),
		ModelsCustomized: len(provider.Models) > 0,
		CreatedAt:        modifiedAt, UpdatedAt: modifiedAt,
	}
}

// modelProfile builds a domain.ModelProfile from a catalog-overlaid (fully
// populated) ModelConfig. The overlaid shape guarantees the pointer fields are
// non-nil; deref helpers keep the body defensive anyway.
func modelProfile(providerID string, model ModelConfig, isDefault bool, modifiedAt time.Time) *domain.ModelProfile {
	name := strings.TrimSpace(model.Name)
	if name == "" {
		name = model.ID
	}
	dialect := domain.ThinkingDialectNone
	if model.ThinkingDialect != nil {
		dialect = *model.ThinkingDialect
	}
	var efforts []domain.ThinkingEffort
	if model.ThinkingEfforts != nil {
		efforts = append([]domain.ThinkingEffort(nil), *model.ThinkingEfforts...)
	}
	return &domain.ModelProfile{
		ID: providerID + "/" + model.ID, ProviderID: providerID, ModelName: model.ID,
		DisplayName: name, ContextWindow: derefInt(model.ContextWindow), MaxOutputTokens: derefInt(model.MaxTokens),
		InputCostUSDMicrosPerMillion:  derefInt64(model.InputCostUSDMicrosPerMillion),
		OutputCostUSDMicrosPerMillion: derefInt64(model.OutputCostUSDMicrosPerMillion),
		SupportsVision:                derefBool(model.Vision), SupportsToolUse: derefBool(model.ToolUse),
		SupportsThinking: derefBool(model.Thinking), ThinkingDialect: dialect,
		SupportedThinkingEfforts: efforts,
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
