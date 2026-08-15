package fileconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const policiesSchemaVersion = 1

type PolicyDefinition struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Kind      domain.PolicyKind `json:"kind"`
	Version   int               `json:"version"`
	Config    json.RawMessage   `json:"config"`
	Status    string            `json:"status"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

type PoliciesDocument struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Defaults      map[domain.PolicyKind]string `json:"defaults"`
	Policies      []PolicyDefinition           `json:"policies"`
}

type PolicyStore struct {
	Path string
	mu   sync.RWMutex
}

func (s *PolicyStore) Resolve(_ context.Context, id string, kind domain.PolicyKind) (domain.PolicySnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	policies, defaults, err := s.load()
	if err != nil {
		return domain.PolicySnapshot{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		id = defaults[kind]
	}
	policy, ok := policies[id]
	if !ok || policy.Kind != kind || policy.Status != "active" {
		return domain.PolicySnapshot{}, fmt.Errorf("active %s policy profile not found: %s", kind, id)
	}
	return domain.PolicySnapshot{ID: policy.ID, Kind: policy.Kind, Version: policy.Version, Config: append(json.RawMessage(nil), policy.Config...)}, nil
}

func (s *PolicyStore) List(_ context.Context) ([]domain.PolicySnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	policies, _, err := s.load()
	if err != nil {
		return nil, err
	}
	result := make([]domain.PolicySnapshot, 0, len(policies))
	for _, policy := range policies {
		result = append(result, domain.PolicySnapshot{ID: policy.ID, Kind: policy.Kind, Version: policy.Version, Config: append(json.RawMessage(nil), policy.Config...)})
	}
	return result, nil
}

func (s *PolicyStore) Profiles(_ context.Context, kind domain.PolicyKind) ([]domain.PolicyProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	policies, _, err := s.load()
	if err != nil {
		return nil, err
	}
	profiles := make([]domain.PolicyProfile, 0, len(policies))
	for _, policy := range policies {
		if kind != "" && policy.Kind != kind {
			continue
		}
		profiles = append(profiles, domain.PolicyProfile{
			ID: policy.ID, Name: policy.Name, Kind: policy.Kind, Version: policy.Version,
			Config: append(json.RawMessage(nil), policy.Config...), Status: policy.Status,
			CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
		})
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Kind != profiles[j].Kind {
			return profiles[i].Kind < profiles[j].Kind
		}
		if profiles[i].Name != profiles[j].Name {
			return profiles[i].Name < profiles[j].Name
		}
		return profiles[i].Version > profiles[j].Version
	})
	return profiles, nil
}

func (s *PolicyStore) FindProfile(ctx context.Context, id string) (*domain.PolicyProfile, error) {
	profiles, err := s.Profiles(ctx, "")
	if err != nil {
		return nil, err
	}
	for index := range profiles {
		if profiles[index].ID == id {
			return &profiles[index], nil
		}
	}
	return nil, nil
}

func (s *PolicyStore) CreateVersion(_ context.Context, name string, kind domain.PolicyKind, config json.RawMessage) (*domain.PolicyProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("policy name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	custom, err := s.loadCustom()
	if err != nil {
		return nil, err
	}
	all, _, err := s.load()
	if err != nil {
		return nil, err
	}
	version := 1
	for _, policy := range all {
		if policy.Kind == kind && policy.Name == name && policy.Version >= version {
			version = policy.Version + 1
		}
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("custom-%s-%s-v%03d", strings.ReplaceAll(string(kind), "_", "-"), providerKey(name), version)
	definition := PolicyDefinition{ID: id, Name: name, Kind: kind, Version: version,
		Config: append(json.RawMessage(nil), config...), Status: "active", CreatedAt: now, UpdatedAt: now}
	custom.Policies = append(custom.Policies, definition)
	if err := writeJSONAtomic(s.Path, custom, 0o600); err != nil {
		return nil, err
	}
	return &domain.PolicyProfile{ID: id, Name: name, Kind: kind, Version: version,
		Config: append(json.RawMessage(nil), config...), Status: "active", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *PolicyStore) SetDefaultProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, _, err := s.load()
	if err != nil {
		return err
	}
	policy, ok := all[id]
	if !ok || policy.Status != "active" {
		return fmt.Errorf("policy profile not found")
	}
	custom, err := s.loadCustom()
	if err != nil {
		return err
	}
	if custom.Defaults == nil {
		custom.Defaults = map[domain.PolicyKind]string{}
	}
	custom.Defaults[policy.Kind] = id
	return writeJSONAtomic(s.Path, custom, 0o600)
}

func (s *PolicyStore) DeactivateProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, builtin := builtinPolicies()[id]; builtin {
		return fmt.Errorf("builtin policies cannot be deactivated")
	}
	custom, err := s.loadCustom()
	if err != nil {
		return err
	}
	found := false
	for index := range custom.Policies {
		if custom.Policies[index].ID == id && custom.Policies[index].Status == "active" {
			custom.Policies[index].Status = "inactive"
			custom.Policies[index].UpdatedAt = time.Now().UTC()
			found = true
		}
	}
	if !found {
		return fmt.Errorf("policy profile not found")
	}
	for kind, defaultID := range custom.Defaults {
		if defaultID == id {
			delete(custom.Defaults, kind)
		}
	}
	return writeJSONAtomic(s.Path, custom, 0o600)
}

func (s *PolicyStore) loadCustom() (PoliciesDocument, error) {
	document := PoliciesDocument{SchemaVersion: policiesSchemaVersion,
		Defaults: map[domain.PolicyKind]string{}, Policies: []PolicyDefinition{}}
	found, err := readStrictJSON(s.Path, &document)
	if err != nil {
		return PoliciesDocument{}, fmt.Errorf("read policies: %w", err)
	}
	if !found {
		return document, nil
	}
	if document.SchemaVersion != policiesSchemaVersion {
		return PoliciesDocument{}, fmt.Errorf("unsupported policies schemaVersion %d", document.SchemaVersion)
	}
	if document.Defaults == nil {
		document.Defaults = map[domain.PolicyKind]string{}
	}
	if document.Policies == nil {
		document.Policies = []PolicyDefinition{}
	}
	return document, nil
}

func (s *PolicyStore) load() (map[string]PolicyDefinition, map[domain.PolicyKind]string, error) {
	policies := builtinPolicies()
	defaults := map[domain.PolicyKind]string{
		domain.PolicyKindTool:           "builtin-tool-allow-existing-v1",
		domain.PolicyKindTurn:           "builtin-turn-fixed-model-v1",
		domain.PolicyKindVision:         "builtin-vision-reject-v1",
		domain.PolicyKindCompaction:     "builtin-compaction-manual-only-v1",
		domain.PolicyKind("delegation"): "builtin-hosted-delegation-v1",
	}
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return policies, defaults, nil
	}
	document := PoliciesDocument{SchemaVersion: policiesSchemaVersion, Defaults: map[domain.PolicyKind]string{}, Policies: []PolicyDefinition{}}
	found, err := readStrictJSON(s.Path, &document)
	if err != nil {
		return nil, nil, fmt.Errorf("read policies: %w", err)
	}
	if !found {
		return policies, defaults, nil
	}
	if document.SchemaVersion != policiesSchemaVersion {
		return nil, nil, fmt.Errorf("unsupported policies schemaVersion %d", document.SchemaVersion)
	}
	for _, policy := range document.Policies {
		if strings.TrimSpace(policy.ID) == "" || strings.TrimSpace(policy.Name) == "" || policy.Version < 1 || !json.Valid(policy.Config) ||
			(policy.Status != "active" && policy.Status != "inactive") {
			return nil, nil, fmt.Errorf("custom policy definition is invalid")
		}
		if _, builtin := policies[policy.ID]; builtin {
			return nil, nil, fmt.Errorf("custom policy cannot override builtin %q", policy.ID)
		}
		if _, duplicate := policies[policy.ID]; duplicate {
			return nil, nil, fmt.Errorf("duplicate policy %q", policy.ID)
		}
		policies[policy.ID] = policy
	}
	for kind, id := range document.Defaults {
		policy, ok := policies[id]
		if !ok || policy.Kind != kind || policy.Status != "active" {
			return nil, nil, fmt.Errorf("default %s policy %q is unavailable", kind, id)
		}
		defaults[kind] = id
	}
	return policies, defaults, nil
}

func builtinPolicies() map[string]PolicyDefinition {
	builtinAt := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	definitions := []PolicyDefinition{
		{ID: "builtin-tool-allow-existing-v1", Name: "allow_existing_behavior", Kind: domain.PolicyKindTool, Version: 1, Config: raw(`{"mode":"allow_existing_behavior"}`), Status: "active", CreatedAt: builtinAt, UpdatedAt: builtinAt},
		{ID: "builtin-tool-ask-v1", Name: "Ask", Kind: domain.PolicyKindTool, Version: 1, Config: raw(`{"mode":"ask","allowedTools":[],"allowedExecutables":["git","rg","ls","cat","sed","find","head","tail","wc","pwd","mkdir","cp","mv","touch","npm","npx","node","go","gofmt","make"],"deniedSubcommands":{"git":["push"],"npm":["publish"]},"allowPipes":true,"allowCommandSubstitution":false,"allowedWriteRoots":["/workspace"],"maxTimeoutSeconds":300,"redactPatterns":["(?i)(api[_-]?key|token|secret|password)\\s*[:=]\\s*[^\\s]+"]}`), Status: "active", CreatedAt: builtinAt, UpdatedAt: builtinAt},
		{ID: "builtin-tool-auto-v1", Name: "Auto", Kind: domain.PolicyKindTool, Version: 1, Config: raw(`{"mode":"auto","deniedSubcommands":{"git":["push","clean"]},"allowPipes":true,"allowedWriteRoots":["/workspace"],"maxTimeoutSeconds":300}`), Status: "active", CreatedAt: builtinAt, UpdatedAt: builtinAt},
		{ID: "builtin-tool-discuss-v3", Name: "Discuss", Kind: domain.PolicyKindTool, Version: 3, Config: raw(`{"mode":"discuss","allowedTools":["read","ls","grep","find","search_compacted_history","todo","git_readonly"],"maxTimeoutSeconds":300}`), Status: "active", CreatedAt: builtinAt, UpdatedAt: builtinAt},
		{ID: "builtin-turn-fixed-model-v1", Name: "fixed_model", Kind: domain.PolicyKindTurn, Version: 1, Config: raw(`{"mode":"fixed_model","threshold":0.7}`), Status: "active", CreatedAt: builtinAt, UpdatedAt: builtinAt},
		{ID: "builtin-vision-reject-v1", Name: "reject", Kind: domain.PolicyKindVision, Version: 1, Config: raw(`{"mode":"reject","maxImageBytes":10485760,"maxPixels":40000000}`), Status: "active", CreatedAt: builtinAt, UpdatedAt: builtinAt},
		{ID: "builtin-compaction-manual-only-v1", Name: "manual_only", Kind: domain.PolicyKindCompaction, Version: 1, Config: raw(`{"mode":"manual_only","triggerRatio":0.75,"keepRecentTurns":2,"tailTokenRatio":0.20,"tailMinTokens":8000,"tailMaxTokens":32000,"summaryInputRatio":0.70,"compactionModelProfileId":null,"summaryMaxOutputTokens":4096,"includeReasoning":false,"allowHistoryLookup":true,"allowOverflowRecovery":true,"maxOverflowRecoveries":1,"ineffectiveReclaimRatio":0.10,"ineffectiveLimit":3,"failureCooldownSeconds":600,"promptVersion":"v1"}`), Status: "active", CreatedAt: builtinAt, UpdatedAt: builtinAt},
		{ID: "builtin-hosted-delegation-v1", Name: "hosted_delegation", Kind: domain.PolicyKind("delegation"), Version: 1, Config: raw(`{"maxConcurrentChildren":8,"budget":{"maxModelCalls":256,"maxToolCalls":1024,"maxTotalTokens":8000000,"maxOutputTokens":524288,"maxCostUsdMicros":400000000,"maxWallTimeMs":0}}`), Status: "active", CreatedAt: builtinAt, UpdatedAt: builtinAt},
	}
	result := make(map[string]PolicyDefinition, len(definitions))
	for _, definition := range definitions {
		result[definition.ID] = definition
	}
	return result
}

func raw(value string) json.RawMessage {
	return json.RawMessage(value)
}
