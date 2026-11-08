package agentpack

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only Agent Pack manifest schema this Worker understands.
const SchemaVersion = 1

// ManifestName is the manifest file that must exist at the pack root.
const ManifestName = "pack.yaml"

// SignatureName is the detached signature path at the pack root. It is
// reserved for S5 and is excluded from the payload digest.
const SignatureName = "pack.sig"

// Flow formats. format: flow is a full Agent Flow FlowDefinition (check tasks,
// fan-out, terminal tasks, next routing); format: graph is the existing
// graphsource.Document, which can only express hero tasks.
const (
	FlowFormatFlow  = "flow"
	FlowFormatGraph = "graph"
)

// Role scopes. A flow-scoped hero is visible only inside the pack's flows; a
// global hero is installed as an ordinary Worker-global Role.
const (
	HeroScopeFlow   = "flow"
	HeroScopeGlobal = "global"
)

// Skill scopes. A flow-scoped vendored skill is installed below every flow that
// references it (agents/graphs/<flow>/skills/<id>); a global vendored skill is
// installed once under the user skills root. The default is global so every
// pack written before scope existed keeps its meaning.
const (
	SkillScopeFlow   = "flow"
	SkillScopeGlobal = "global"
)

// Vendored skill file names accepted at the skill directory root.
var skillPromptNames = []string{"SKILL.md", "skill.md"}

// maxManifestBytes bounds pack.yaml. It is smaller than maxFileBytes because
// the manifest is always echoed back in the inspect result.
const maxManifestBytes = 256 << 10

var (
	packIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}/[a-z0-9][a-z0-9._-]{0,63}$`)
	packVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	flowIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	heroHandlePattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)
	skillIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	externalSkillRef   = regexp.MustCompile(`^[^\s@]+@[^\s@]+$`)
	packRequiresRef    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9._-]*(?:@[0-9A-Za-z.+-]+)?$`)
	// externalMCPRef is the only accepted MCP declaration form: a reference to
	// an existing MCP profile (name@version), never an inline definition.
	externalMCPRef = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[0-9A-Za-z.^~*+<>=_-]+$`)
)

// capabilityVocabulary is the accepted model requirement vocabulary. It mirrors
// the model catalog's capability flags so a pack can only ask for abilities the
// Worker can actually select on.
var capabilityVocabulary = map[string]bool{
	"tool_use": true,
	"vision":   true,
	"thinking": true,
}

// portTypes is the accepted flow input vocabulary. It mirrors
// domain.FlowPort's typed ports.
var portTypes = map[string]bool{
	"path": true, "string": true, "int": true, "file": true, "artifact": true,
}

// Manifest is the strict pack.yaml contract. Decoding uses KnownFields(true), so
// every unrecognized key is a hard error instead of a silently ignored typo.
type Manifest struct {
	SchemaVersion     int                   `yaml:"schema_version"`
	ID                string                `yaml:"id"`
	Name              string                `yaml:"name"`
	Version           string                `yaml:"version"`
	Description       string                `yaml:"description"`
	License           string                `yaml:"license"`
	RequiresEnnote    string                `yaml:"requires_ennote"`
	ModelRequirements *ModelRequirements    `yaml:"model_requirements"`
	Inputs            map[string]Input      `yaml:"inputs"`
	Flows             []FlowEntry           `yaml:"flows"`
	Heroes             []HeroEntry           `yaml:"heroes"`
	Skills            []SkillEntry          `yaml:"skills"`
	MCP               []MCPEntry            `yaml:"mcp"`
	Requires          []string              `yaml:"requires"`
	Docs              *Docs                 `yaml:"docs"`
	Signature         *SignatureDeclaration `yaml:"signature"`
}

// ModelRequirements declares the model capabilities a pack needs. Packs never
// name concrete model ids; the reviewer maps capabilities onto local models.
type ModelRequirements struct {
	Defaults CapabilityRequirements            `yaml:"defaults"`
	Heroes    map[string]CapabilityRequirements `yaml:"heroes"`
}

// CapabilityRequirements is one capability request.
type CapabilityRequirements struct {
	Capabilities     []string `yaml:"capabilities"`
	MinContextWindow int64    `yaml:"min_context_window"`
}

// Input declares one typed pack input.
type Input struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required"`
	Default  any    `yaml:"default"`
}

// FlowEntry declares one flow shipped by the pack.
type FlowEntry struct {
	ID     string   `yaml:"id"`
	Path   string   `yaml:"path"`
	Format string   `yaml:"format"`
	Heroes  []string `yaml:"heroes"`
}

// HeroEntry declares one hero shipped by the pack.
type HeroEntry struct {
	Handle string `yaml:"handle"`
	Path   string `yaml:"path"`
	Scope  string `yaml:"scope"`
}

// SkillEntry declares a vendored skill directory or an external skill
// reference. Exactly one of ID/Path and Ref must be set. Scope only applies to
// vendored skills and defaults to global.
type SkillEntry struct {
	ID    string `yaml:"id"`
	Path  string `yaml:"path"`
	Ref   string `yaml:"ref"`
	Scope string `yaml:"scope"`
}

// MCPEntry declares a reference to an existing MCP profile. S1 accepts only
// ref: KnownFields(true) therefore rejects an inline command/args/env block
// outright, and the reference is never resolved or executed.
type MCPEntry struct {
	Ref string `yaml:"ref"`
}

// Docs points at the pack's human documentation.
type Docs struct {
	Readme string `yaml:"readme"`
}

// SignatureDeclaration is parsed but never verified in S1.
type SignatureDeclaration struct {
	Algorithm string `yaml:"algorithm"`
	Path      string `yaml:"path"`
}

// ParseManifest strictly decodes pack.yaml and validates everything that does
// not depend on the pack's files. Path existence and cross-references are
// checked by Inspect, which has the payload available.
func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, newError(CodePackManifestInvalid, "pack.yaml is empty")
	}
	if len(data) > maxManifestBytes {
		return nil, errorf(CodePackTooLarge, "pack.yaml is %d bytes (limit %d)", len(data), maxManifestBytes)
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		if strings.Contains(err.Error(), "not found in type") || strings.Contains(err.Error(), "not found in struct") {
			return nil, errorf(CodePackManifestUnknownField, "pack.yaml has an unknown field: %v", err)
		}
		return nil, errorf(CodePackManifestInvalid, "parse pack.yaml: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, newError(CodePackManifestInvalid, "pack.yaml must contain one YAML document")
		}
		return nil, errorf(CodePackManifestInvalid, "parse pack.yaml: %v", err)
	}
	if err := validateManifest(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func validateManifest(manifest *Manifest) error {
	if manifest.SchemaVersion != SchemaVersion {
		return errorf(CodePackSchemaUnsupported,
			"unsupported schema_version %d (supported: %d)", manifest.SchemaVersion, SchemaVersion)
	}

	manifest.ID = strings.TrimSpace(manifest.ID)
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Version = strings.TrimSpace(manifest.Version)
	manifest.Description = strings.TrimSpace(manifest.Description)
	manifest.License = strings.TrimSpace(manifest.License)
	manifest.RequiresEnnote = strings.TrimSpace(manifest.RequiresEnnote)
	if !packIDPattern.MatchString(manifest.ID) {
		return fieldError(CodePackIdentityInvalid, "id",
			"id must be publisher/name using lowercase letters, digits, '-' or '.'")
	}
	if manifest.Name == "" {
		return fieldError(CodePackIdentityInvalid, "name", "name is required")
	}
	if !packVersionPattern.MatchString(manifest.Version) {
		return fieldError(CodePackIdentityInvalid, "version",
			"version must be a semantic version such as 1.3.0")
	}

	if err := validateInputs(manifest.Inputs); err != nil {
		return err
	}
	if err := validateModelRequirements(manifest.ModelRequirements); err != nil {
		return err
	}

	flowIDs := make(map[string]bool, len(manifest.Flows))
	for index := range manifest.Flows {
		flow := &manifest.Flows[index]
		field := fmt.Sprintf("flows[%d]", index)
		flow.ID = strings.TrimSpace(flow.ID)
		flow.Path = strings.TrimSpace(flow.Path)
		flow.Format = strings.TrimSpace(flow.Format)
		if !flowIDPattern.MatchString(flow.ID) {
			return fieldError(CodePackFlowInvalid, field+".id",
				"flow id %q must match %s", flow.ID, flowIDPattern.String())
		}
		if flowIDs[flow.ID] {
			return fieldError(CodePackDuplicateID, field+".id", "duplicate flow id %q", flow.ID)
		}
		flowIDs[flow.ID] = true
		if flow.Path == "" {
			return fieldError(CodePackFlowInvalid, field+".path", "flow path is required")
		}
		switch flow.Format {
		case FlowFormatFlow, FlowFormatGraph:
		case "":
			return fieldError(CodePackFlowInvalid, field+".format",
				"flow format is required (%q or %q)", FlowFormatFlow, FlowFormatGraph)
		default:
			return fieldError(CodePackFlowInvalid, field+".format",
				"unsupported flow format %q (supported: %s, %s)", flow.Format, FlowFormatFlow, FlowFormatGraph)
		}
		flow.Heroes = sortedUniqueHandles(flow.Heroes)
	}

	heroHandles := make(map[string]bool, len(manifest.Heroes))
	for index := range manifest.Heroes {
		hero := &manifest.Heroes[index]
		field := fmt.Sprintf("heroes[%d]", index)
		hero.Handle = strings.TrimSpace(hero.Handle)
		hero.Path = strings.TrimSpace(hero.Path)
		hero.Scope = strings.TrimSpace(hero.Scope)
		if !heroHandlePattern.MatchString(hero.Handle) {
			return fieldError(CodePackHeroInvalid, field+".handle",
				"hero handle %q must match %s", hero.Handle, heroHandlePattern.String())
		}
		if heroHandles[hero.Handle] {
			return fieldError(CodePackDuplicateID, field+".handle", "duplicate hero handle %q", hero.Handle)
		}
		heroHandles[hero.Handle] = true
		if hero.Path == "" {
			return fieldError(CodePackHeroInvalid, field+".path", "hero path is required")
		}
		switch hero.Scope {
		case HeroScopeFlow, HeroScopeGlobal:
		case "":
			return fieldError(CodePackHeroInvalid, field+".scope",
				"hero scope is required (%q or %q)", HeroScopeFlow, HeroScopeGlobal)
		default:
			return fieldError(CodePackHeroInvalid, field+".scope",
				"unsupported hero scope %q (supported: %s, %s)", hero.Scope, HeroScopeFlow, HeroScopeGlobal)
		}
	}

	skillIDs := make(map[string]bool, len(manifest.Skills))
	for index := range manifest.Skills {
		skill := &manifest.Skills[index]
		field := fmt.Sprintf("skills[%d]", index)
		skill.ID = strings.TrimSpace(skill.ID)
		skill.Path = strings.TrimSpace(skill.Path)
		skill.Ref = strings.TrimSpace(skill.Ref)
		skill.Scope = strings.TrimSpace(skill.Scope)
		switch {
		case skill.ID != "" && skill.Ref != "":
			return fieldError(CodePackSkillInvalid, field, "skill must declare either id+path or ref, not both")
		case skill.Ref != "":
			if skill.Path != "" {
				return fieldError(CodePackSkillInvalid, field+".path", "external skill ref must not declare a path")
			}
			if skill.Scope != "" {
				// An external ref is resolved from the Worker-global catalog: it has
				// no install target of its own, so a scope would be meaningless.
				return fieldError(CodePackSkillInvalid, field+".scope",
					"external skill ref must not declare a scope")
			}
			if !externalSkillRef.MatchString(skill.Ref) {
				return fieldError(CodePackSkillInvalid, field+".ref",
					"external skill ref %q must use name@version", skill.Ref)
			}
		case skill.ID != "" || skill.Path != "":
			if !skillIDPattern.MatchString(skill.ID) {
				return fieldError(CodePackSkillInvalid, field+".id",
					"vendored skill id %q must match %s", skill.ID, skillIDPattern.String())
			}
			if skill.Path == "" {
				return fieldError(CodePackSkillInvalid, field+".path", "vendored skill path is required")
			}
			switch skill.Scope {
			case "":
				skill.Scope = SkillScopeGlobal
			case SkillScopeFlow, SkillScopeGlobal:
			default:
				return fieldError(CodePackSkillInvalid, field+".scope",
					"unsupported skill scope %q (supported: %s, %s)", skill.Scope, SkillScopeFlow, SkillScopeGlobal)
			}
		default:
			return fieldError(CodePackSkillInvalid, field, "skill must declare id+path or ref")
		}
		if skill.ID != "" {
			if skillIDs[skill.ID] {
				return fieldError(CodePackDuplicateID, field+".id", "duplicate skill id %q", skill.ID)
			}
			skillIDs[skill.ID] = true
		}
	}

	mcpRefs := make([]string, 0, len(manifest.MCP))
	for index := range manifest.MCP {
		ref := strings.TrimSpace(manifest.MCP[index].Ref)
		field := fmt.Sprintf("mcp[%d].ref", index)
		if ref == "" {
			return fieldError(CodePackManifestInvalid, field, "mcp ref is required")
		}
		if !externalMCPRef.MatchString(ref) {
			return fieldError(CodePackManifestInvalid, field,
				"mcp ref %q must use name@version (lowercase profile name, non-empty version)", ref)
		}
		mcpRefs = append(mcpRefs, ref)
	}
	// MCP refs are a declaration set: duplicates collapse and order is stable so
	// two identical manifests always produce an identical result.
	mcpRefs = sortedUnique(mcpRefs)
	manifest.MCP = make([]MCPEntry, 0, len(mcpRefs))
	for _, ref := range mcpRefs {
		manifest.MCP = append(manifest.MCP, MCPEntry{Ref: ref})
	}

	if len(manifest.Flows) == 0 && len(manifest.Heroes) == 0 {
		return newError(CodePackEmpty, "pack must declare at least one flow or one hero")
	}

	for index := range manifest.Requires {
		manifest.Requires[index] = strings.TrimSpace(manifest.Requires[index])
		if !packRequiresRef.MatchString(manifest.Requires[index]) {
			return fieldError(CodePackManifestInvalid, fmt.Sprintf("requires[%d]", index),
				"pack dependency %q must use publisher/name or publisher/name@version", manifest.Requires[index])
		}
	}
	manifest.Requires = sortedUnique(manifest.Requires)

	if manifest.Docs != nil {
		manifest.Docs.Readme = strings.TrimSpace(manifest.Docs.Readme)
		if manifest.Docs.Readme == "" {
			// An empty docs block is equivalent to no docs at all.
			manifest.Docs = nil
		}
	}

	if manifest.Signature != nil {
		manifest.Signature.Algorithm = strings.TrimSpace(manifest.Signature.Algorithm)
		manifest.Signature.Path = strings.TrimSpace(manifest.Signature.Path)
		if manifest.Signature.Algorithm != "ed25519" {
			return fieldError(CodePackSignatureUnsupported, "signature.algorithm",
				"unsupported signature algorithm %q (supported: ed25519)", manifest.Signature.Algorithm)
		}
		if manifest.Signature.Path == "" {
			return fieldError(CodePackSignatureUnsupported, "signature.path",
				"signature path is required when a signature is declared")
		}
	}
	return nil
}

func validateInputs(inputs map[string]Input) error {
	for name, input := range inputs {
		field := "inputs." + name
		if strings.TrimSpace(name) == "" {
			return fieldError(CodePackManifestInvalid, "inputs", "input name must not be blank")
		}
		if !portTypes[strings.TrimSpace(input.Type)] {
			return fieldError(CodePackManifestInvalid, field+".type",
				"unsupported input type %q (supported: path, string, int, file, artifact)", input.Type)
		}
	}
	return nil
}

func validateModelRequirements(requirements *ModelRequirements) error {
	if requirements == nil {
		return nil
	}
	for _, group := range []struct {
		field string
		value CapabilityRequirements
	}{
		{field: "model_requirements.defaults", value: requirements.Defaults},
	} {
		if err := validateCapabilities(group.field, group.value.Capabilities); err != nil {
			return err
		}
	}
	for handle, value := range requirements.Heroes {
		if err := validateCapabilities("model_requirements.heroes."+handle, value.Capabilities); err != nil {
			return err
		}
	}
	return nil
}

func validateCapabilities(field string, capabilities []string) error {
	for index, capability := range capabilities {
		if !capabilityVocabulary[strings.TrimSpace(capability)] {
			return fieldError(CodePackCapabilityUnknown, fmt.Sprintf("%s[%d]", field, index),
				"unknown model capability %q (supported: tool_use, vision, thinking)", capability)
		}
	}
	return nil
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return sortedStrings(result)
}

func sortedUniqueHandles(values []string) []string {
	return sortedUnique(values)
}
