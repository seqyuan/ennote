// Package modelcatalog ships the built-in model catalog that supplies
// field-level defaults for config/models.json entries. The catalog is a
// fallback, never an authority: user-declared fields always win, and a model
// the catalog does not describe still requires full declaration. The catalog
// is compiled into the binary and never needs network access, keeping Ennote
// fully functional with no account, no connectivity, and cloud unavailable.
package modelcatalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

//go:embed catalog.json
var catalogFS embed.FS

var providerKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

// ModelDefaults are the catalog-supplied default values for one model. Every
// field is a complete value (not a pointer): the catalog answers "what should
// this be when the user did not say".
type ModelDefaults struct {
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

type providerEntry struct {
	Name    string                   `json:"name"`
	API     string                   `json:"api"`
	BaseURL string                   `json:"baseUrl"`
	Models  map[string]ModelDefaults `json:"models"`
}

type document struct {
	Providers map[string]providerEntry `json:"providers"`
}

var catalog document

func init() {
	data, err := catalogFS.ReadFile("catalog.json")
	if err != nil {
		panic(fmt.Sprintf("modelcatalog: read embedded catalog: %v", err))
	}
	parsed, err := parseCatalog(data)
	if err != nil {
		panic(fmt.Sprintf("modelcatalog: %v", err))
	}
	catalog = parsed
}

// parseCatalog decodes and validates a catalog document. Exposed for tests;
// the embedded catalog is parsed through this in init.
func parseCatalog(data []byte) (document, error) {
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return document{}, fmt.Errorf("parse catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return document{}, fmt.Errorf("catalog must contain one JSON value")
	}
	if err := validate(doc); err != nil {
		return document{}, err
	}
	return doc, nil
}

func validate(doc document) error {
	if len(doc.Providers) == 0 {
		return fmt.Errorf("catalog has no providers")
	}
	for key, provider := range doc.Providers {
		if !providerKeyPattern.MatchString(key) {
			return fmt.Errorf("provider key %q invalid", key)
		}
		if strings.TrimSpace(provider.Name) == "" {
			return fmt.Errorf("provider %q name is required", key)
		}
		if strings.TrimSpace(provider.API) == "" {
			return fmt.Errorf("provider %q api is required", key)
		}
		if len(provider.Models) == 0 {
			return fmt.Errorf("provider %q has no models", key)
		}
		for modelID, defaults := range provider.Models {
			if err := validateModel(key, modelID, defaults); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateModel(providerKey, modelID string, defaults ModelDefaults) error {
	if strings.TrimSpace(modelID) == "" || strings.ContainsAny(modelID, " \t\r\n") {
		return fmt.Errorf("provider %q model id %q invalid", providerKey, modelID)
	}
	if defaults.ContextWindow <= 0 || defaults.MaxTokens <= 0 || defaults.MaxTokens > defaults.ContextWindow {
		return fmt.Errorf("provider %q model %q: contextWindow/maxTokens invalid", providerKey, modelID)
	}
	if defaults.InputCostUSDMicrosPerMillion < 0 || defaults.OutputCostUSDMicrosPerMillion < 0 {
		return fmt.Errorf("provider %q model %q: negative cost", providerKey, modelID)
	}
	if err := validateThinking(defaults.Thinking, defaults.ThinkingDialect, defaults.ThinkingEfforts); err != nil {
		return fmt.Errorf("provider %q model %q: %w", providerKey, modelID, err)
	}
	return nil
}

// validateThinking mirrors fileconfig's thinking validation so the embedded
// catalog is checked against the same rules without importing fileconfig
// (which would create a cycle). The two copies are intentionally kept in
// lockstep; a later cleanup may lift this into domain.
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

// Lookup returns the catalog defaults for one provider/model, or ok=false
// when either the provider or the model is not described.
func Lookup(providerKey, modelID string) (ModelDefaults, bool) {
	provider, ok := catalog.Providers[providerKey]
	if !ok {
		return ModelDefaults{}, false
	}
	defaults, ok := provider.Models[modelID]
	if !ok {
		return ModelDefaults{}, false
	}
	return defaults, true
}

// HasProvider reports whether the catalog describes a provider key.
func HasProvider(providerKey string) bool {
	_, ok := catalog.Providers[providerKey]
	return ok
}

// ProviderKeys returns the directory provider keys, sorted. This is the
// dormant-provider list the Models tab offers for adoption.
func ProviderKeys() []string {
	keys := make([]string, 0, len(catalog.Providers))
	for key := range catalog.Providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ProviderModelIDs returns the catalog's model ids for a provider, sorted. An
// unknown provider yields nil. This is the "inherited" model list a provider
// with no explicitly declared models serves.
func ProviderModelIDs(providerKey string) []string {
	provider, ok := catalog.Providers[providerKey]
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(provider.Models))
	for id := range provider.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ProviderDefaultName returns the directory display name for a provider.
func ProviderDefaultName(providerKey string) (string, bool) {
	provider, ok := catalog.Providers[providerKey]
	if !ok {
		return "", false
	}
	return provider.Name, true
}

// ProviderDefaultAPI returns the default wire protocol for a provider.
func ProviderDefaultAPI(providerKey string) (string, bool) {
	provider, ok := catalog.Providers[providerKey]
	if !ok {
		return "", false
	}
	return provider.API, true
}

// ProviderDefaultBaseURL returns the default endpoint for a provider.
func ProviderDefaultBaseURL(providerKey string) (string, bool) {
	provider, ok := catalog.Providers[providerKey]
	if !ok {
		return "", false
	}
	return provider.BaseURL, true
}
