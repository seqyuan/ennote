package rolesource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"gopkg.in/yaml.v3"
)

const maxRoleFileBytes = 128 * 1024

var roleHandlePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

type ModelBinding struct {
	Ref            string                `json:"ref" yaml:"ref"`
	ThinkingEffort domain.ThinkingEffort `json:"thinkingEffort" yaml:"thinkingEffort"`
	Fallbacks      []string              `json:"fallbacks" yaml:"fallbacks"`
}

type SkillBinding struct {
	ID   string               `json:"id" yaml:"id"`
	Mode domain.RoleSkillMode `json:"mode" yaml:"mode"`
}

type ContextPolicy struct {
	DefaultMode            domain.RoleContextMode       `json:"defaultMode" yaml:"defaultMode"`
	AllowedModes           []domain.RoleContextMode     `json:"allowedModes" yaml:"allowedModes"`
	OwnExecutionContinuity domain.RolePrivateContinuity `json:"ownExecutionContinuity" yaml:"ownExecutionContinuity"`
}

type DelegationBudgetCeiling struct {
	MaxModelCalls    int   `json:"maxModelCalls" yaml:"maxModelCalls"`
	MaxToolCalls     int   `json:"maxToolCalls" yaml:"maxToolCalls"`
	MaxTotalTokens   int64 `json:"maxTotalTokens" yaml:"maxTotalTokens"`
	MaxOutputTokens  int64 `json:"maxOutputTokens" yaml:"maxOutputTokens"`
	MaxCostUSDMicros int64 `json:"maxCostUsdMicros" yaml:"maxCostUsdMicros"`
	MaxWallTimeMS    int64 `json:"maxWallTimeMs" yaml:"maxWallTimeMs"`
}

type DelegationPolicy struct {
	Admission                  domain.DelegationAdmission `json:"admission" yaml:"admission"`
	AllowedCallerKinds         []string                   `json:"allowedCallerKinds" yaml:"allowedCallerKinds"`
	AllowedStrategies          []string                   `json:"allowedStrategies" yaml:"allowedStrategies"`
	MaxInvocationsPerParentRun int                        `json:"maxInvocationsPerParentRun" yaml:"maxInvocationsPerParentRun"`
	MaxConcurrentInstances     int                        `json:"maxConcurrentInstances" yaml:"maxConcurrentInstances"`
	BudgetCeiling              DelegationBudgetCeiling    `json:"budgetCeiling" yaml:"budgetCeiling"`
}

type Document struct {
	SchemaVersion     int                   `json:"schemaVersion" yaml:"schemaVersion"`
	Handle            string                `json:"handle" yaml:"handle"`
	Name              string                `json:"name" yaml:"name"`
	Description       string                `json:"description" yaml:"description"`
	Positioning       string                `json:"positioning" yaml:"positioning"`
	Icon              string                `json:"icon" yaml:"icon"`
	Color             string                `json:"color" yaml:"color"`
	Model             ModelBinding          `json:"model" yaml:"model"`
	Skills            []SkillBinding        `json:"skills" yaml:"skills"`
	Authority         domain.RoleAuthority  `json:"authority" yaml:"authority"`
	PermissionCeiling domain.PermissionMode `json:"permissionCeiling" yaml:"permissionCeiling"`
	AllowedTools      []string              `json:"allowedTools" yaml:"allowedTools"`
	Context           ContextPolicy         `json:"context" yaml:"context"`
	Delegation        DelegationPolicy      `json:"delegation" yaml:"delegation"`
	OutputContract    string                `json:"outputContract" yaml:"outputContract"`
	MaxLoopIterations int                   `json:"maxLoopIterations" yaml:"maxLoopIterations"`
	Prompt            string                `json:"prompt" yaml:"-"`
}

func Parse(data []byte) (*Document, error) {
	if len(data) > maxRoleFileBytes {
		return nil, fmt.Errorf("Role file exceeds 128 KiB")
	}
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return nil, fmt.Errorf("Role file requires an opening frontmatter delimiter")
	}
	closing := bytes.Index(normalized[4:], []byte("\n---\n"))
	if closing < 0 {
		return nil, fmt.Errorf("Role file requires a closing frontmatter delimiter")
	}
	closing += 4
	frontmatter := normalized[4:closing]
	body := normalized[closing+5:]
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(bytes.TrimSpace(body)) == 0 || len(body) > 65536 {
		return nil, fmt.Errorf("Role prompt must contain 1 to 65536 bytes")
	}

	var document Document
	decoder := yaml.NewDecoder(bytes.NewReader(frontmatter))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse Role frontmatter: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("Role frontmatter must contain one YAML document")
		}
		return nil, fmt.Errorf("parse Role frontmatter: %w", err)
	}
	document.Prompt = string(body)
	if err := normalizeAndValidate(&document); err != nil {
		return nil, err
	}
	return &document, nil
}

func Encode(document *Document) ([]byte, error) {
	if document == nil {
		return nil, fmt.Errorf("Role source document is required")
	}
	copy := *document
	copy.Model.Fallbacks = append([]string(nil), document.Model.Fallbacks...)
	copy.Skills = append([]SkillBinding(nil), document.Skills...)
	copy.AllowedTools = append([]string(nil), document.AllowedTools...)
	copy.Context.AllowedModes = append([]domain.RoleContextMode(nil), document.Context.AllowedModes...)
	copy.Delegation.AllowedCallerKinds = append([]string(nil), document.Delegation.AllowedCallerKinds...)
	copy.Delegation.AllowedStrategies = append([]string(nil), document.Delegation.AllowedStrategies...)
	if err := normalizeAndValidate(&copy); err != nil {
		return nil, err
	}
	prompt := copy.Prompt
	copy.Prompt = ""
	frontmatter, err := yaml.Marshal(&copy)
	if err != nil {
		return nil, fmt.Errorf("encode Role frontmatter: %w", err)
	}
	return []byte("---\n" + string(frontmatter) + "---\n" + prompt + "\n"), nil
}

func SourceDigest(document *Document) (string, error) {
	if document == nil {
		return "", fmt.Errorf("Role source document is required")
	}
	copy := *document
	copy.Model.Fallbacks = append([]string(nil), document.Model.Fallbacks...)
	copy.Skills = append([]SkillBinding(nil), document.Skills...)
	copy.AllowedTools = append([]string(nil), document.AllowedTools...)
	copy.Context.AllowedModes = append([]domain.RoleContextMode(nil), document.Context.AllowedModes...)
	copy.Delegation.AllowedCallerKinds = append([]string(nil), document.Delegation.AllowedCallerKinds...)
	copy.Delegation.AllowedStrategies = append([]string(nil), document.Delegation.AllowedStrategies...)
	if err := normalizeAndValidate(&copy); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode Role source: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func normalizeAndValidate(document *Document) error {
	document.Handle = strings.TrimSpace(document.Handle)
	document.Name = strings.TrimSpace(document.Name)
	document.Description = strings.TrimSpace(document.Description)
	document.Positioning = strings.TrimSpace(document.Positioning)
	document.Icon = strings.TrimSpace(document.Icon)
	document.Color = strings.TrimSpace(document.Color)
	document.Model.Ref = strings.TrimSpace(document.Model.Ref)
	document.OutputContract = strings.TrimSpace(document.OutputContract)
	if document.SchemaVersion != 1 {
		return fmt.Errorf("Role schemaVersion must be 1")
	}
	if !roleHandlePattern.MatchString(document.Handle) {
		return fmt.Errorf("Role handle must match %s", roleHandlePattern.String())
	}
	if document.Name == "" {
		return fmt.Errorf("Role name is required")
	}
	if document.Icon == "" {
		document.Icon = "bot"
	}
	if document.Color == "" {
		document.Color = "neutral"
	}
	if err := validateModelRef(document.Model.Ref); err != nil {
		return fmt.Errorf("model.ref: %w", err)
	}
	if document.Model.ThinkingEffort == "" {
		document.Model.ThinkingEffort = domain.ThinkingDefault
	}
	for index := range document.Model.Fallbacks {
		document.Model.Fallbacks[index] = strings.TrimSpace(document.Model.Fallbacks[index])
		if err := validateModelRef(document.Model.Fallbacks[index]); err != nil {
			return fmt.Errorf("model.fallbacks[%d]: %w", index, err)
		}
	}
	document.Model.Fallbacks = sortedUnique(document.Model.Fallbacks)

	seenSkills := make(map[string]bool, len(document.Skills))
	for index := range document.Skills {
		document.Skills[index].ID = strings.TrimSpace(document.Skills[index].ID)
		if document.Skills[index].ID == "" {
			return fmt.Errorf("skill id is required")
		}
		if seenSkills[document.Skills[index].ID] {
			return fmt.Errorf("duplicate skill %q", document.Skills[index].ID)
		}
		seenSkills[document.Skills[index].ID] = true
		switch document.Skills[index].Mode {
		case domain.RoleSkillPreload, domain.RoleSkillAvailable:
		default:
			return fmt.Errorf("unsupported skill mode %q", document.Skills[index].Mode)
		}
	}
	sort.Slice(document.Skills, func(i, j int) bool { return document.Skills[i].ID < document.Skills[j].ID })
	document.AllowedTools = trimSortedUnique(document.AllowedTools)
	sort.Slice(document.Context.AllowedModes, func(i, j int) bool {
		return document.Context.AllowedModes[i] < document.Context.AllowedModes[j]
	})
	document.Delegation.AllowedCallerKinds = trimSortedUnique(document.Delegation.AllowedCallerKinds)
	document.Delegation.AllowedStrategies = trimSortedUnique(document.Delegation.AllowedStrategies)
	return nil
}

func validateModelRef(ref string) error {
	separator := strings.IndexByte(ref, '/')
	if separator <= 0 || separator == len(ref)-1 {
		return fmt.Errorf("must use provider-name/model-name")
	}
	return nil
}

func sortedUnique(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) == 0 {
		return []string{}
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

func trimSortedUnique(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return sortedUnique(trimmed)
}
