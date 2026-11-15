// Package agentflow implements the Agent Flow (roadmap item 7) authoring and
// orchestration primitives: strict YAML parsing, publish validation, config
// digests, the meta-Run orchestration state machine, check tasks, flow
// budget accounting, and the Phase 1 event set.
//
// The meta-Run is a pure orchestration state machine: it never calls a
// Provider and never participates in loop decisions. Every task runs as a
// standard child Run through the existing delegation substrate.
package agentflow

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"gopkg.in/yaml.v3"
)

// ParseDefinition parses a flow definition YAML document. Parsing is strict at
// the top level and on unknown task types, and it records (but does not
// reject) unknown task-node keys: the read path must keep working for revision
// loading and the editor, while the publish validator rejects a definition
// that carries recorded unknown keys. See domain.FlowDefinition.UnknownTaskFields.
func ParseDefinition(data []byte) (*domain.FlowDefinition, error) {
	var raw struct {
		SchemaVersion *int                       `yaml:"schemaVersion"`
		ID            string                     `yaml:"id"`
		Name          string                     `yaml:"name"`
		Version       int                        `yaml:"version"`
		Description   string                     `yaml:"description"`
		Inputs        map[string]domain.FlowPort `yaml:"inputs"`
		Outputs       map[string]domain.FlowPort `yaml:"outputs"`
		Budget        *struct {
			MaxTotalTokens int64 `yaml:"max_total_tokens"`
		} `yaml:"budget"`
		Tasks       map[string]yaml.Node `yaml:"tasks"`
		Convergence []convergenceRaw     `yaml:"convergence"`
		Parallelism *struct {
			Max                  int  `yaml:"max"`
			AllowDisjointWriters bool `yaml:"allow_disjoint_writers"`
		} `yaml:"parallelism"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse flow definition: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("flow definition must contain one YAML document")
		}
		return nil, fmt.Errorf("parse flow definition: %w", err)
	}
	if raw.SchemaVersion == nil {
		return nil, fmt.Errorf("flow definition requires schemaVersion")
	}
	if *raw.SchemaVersion != domain.FlowSchemaVersion {
		return nil, fmt.Errorf("unsupported flow schemaVersion %d (supported: %d)",
			*raw.SchemaVersion, domain.FlowSchemaVersion)
	}
	raw.ID = strings.TrimSpace(raw.ID)
	if raw.ID == "" {
		return nil, fmt.Errorf("flow definition requires an id")
	}
	if len(raw.Tasks) == 0 {
		// A freshly created graph.yaml has no tasks yet; the author fills them in.
		// A published definition still needs tasks, which the validator enforces.
		if raw.Name == "" {
			return nil, fmt.Errorf("flow definition requires at least one task")
		}
		raw.Tasks = map[string]yaml.Node{}
	}
	def := &domain.FlowDefinition{
		SchemaVersion: *raw.SchemaVersion,
		ID:            raw.ID,
		Name:          strings.TrimSpace(raw.Name),
		Version:       raw.Version,
		Description:   strings.TrimSpace(raw.Description),
		Inputs:        raw.Inputs,
		Outputs:       raw.Outputs,
		Tasks:         make(map[string]domain.FlowTask, len(raw.Tasks)),
	}
	if raw.Budget != nil {
		def.Budget = domain.FlowBudget{MaxTotalTokens: raw.Budget.MaxTotalTokens}
	}
	if raw.Parallelism != nil {
		def.Parallelism = &domain.FlowParallelism{
			Max:                  raw.Parallelism.Max,
			AllowDisjointWriters: raw.Parallelism.AllowDisjointWriters,
		}
	}
	for name, node := range raw.Tasks {
		task, unknown, err := decodeTask(node)
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", name, err)
		}
		if len(unknown) > 0 {
			if def.UnknownTaskFields == nil {
				def.UnknownTaskFields = make(map[string][]string, len(unknown))
			}
			def.UnknownTaskFields[name] = unknown
		}
		def.Tasks[name] = task
	}
	for _, c := range raw.Convergence {
		def.Convergence = append(def.Convergence, domain.ConvergenceRule{
			From: strings.TrimSpace(c.From), To: strings.TrimSpace(c.To), MaxRounds: c.MaxRounds,
		})
	}
	if err := normalizeDefinition(def); err != nil {
		return nil, err
	}
	return def, nil
}

type convergenceRaw struct {
	From      string `yaml:"from"`
	To        string `yaml:"to"`
	MaxRounds int    `yaml:"max_rounds"`
}

// flowTaskKnownYAMLKeys is the single source of truth for the task-node key
// allowlist. It is derived from domain.FlowTask's yaml tags rather than written
// out a second time, so adding a field to the struct cannot leave the parser's
// allowlist behind.
var flowTaskKnownYAMLKeys = taskYAMLKeys()

func taskYAMLKeys() map[string]struct{} {
	t := reflect.TypeOf(domain.FlowTask{})
	keys := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		keys[name] = struct{}{}
	}
	return keys
}

// unknownTaskYAMLKeys returns the task-node mapping keys that the task struct
// does not know, sorted and deduplicated. A non-mapping node has no keys to
// inspect and decodes to the zero task.
func unknownTaskYAMLKeys(node yaml.Node) []string {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	var unknown []string
	seen := make(map[string]bool)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind != yaml.ScalarNode {
			continue
		}
		if _, known := flowTaskKnownYAMLKeys[key.Value]; known {
			continue
		}
		if seen[key.Value] {
			continue
		}
		seen[key.Value] = true
		unknown = append(unknown, key.Value)
	}
	sort.Strings(unknown)
	return unknown
}

// decodeTask decodes one task node and reports the unknown keys it dropped.
func decodeTask(node yaml.Node) (domain.FlowTask, []string, error) {
	unknown := unknownTaskYAMLKeys(node)
	var task domain.FlowTask
	if err := node.Decode(&task); err != nil {
		return domain.FlowTask{}, unknown, err
	}
	task.Type = strings.TrimSpace(task.Type)
	if task.Type == "" {
		task.Type = domain.FlowTaskHero
	}
	switch task.Type {
	case domain.FlowTaskHero, domain.FlowTaskCheck:
	default:
		return domain.FlowTask{}, unknown, fmt.Errorf("unsupported task type %q", task.Type)
	}
	if task.Type == domain.FlowTaskCheck {
		// Check tasks are deterministic gates: no hero, no skills, no budget.
		task.Hero = ""
		task.Skills = nil
		task.Budget = nil
	} else {
		task.Command = ""
	}
	return task, unknown, nil
}

// normalizeDefinition canonicalizes a parsed definition: trims strings, sorts
// dependent slices so the config digest is stable regardless of authoring
// order. Maps are already sorted by encoding/json.
func normalizeDefinition(def *domain.FlowDefinition) error {
	def.ID = strings.TrimSpace(def.ID)
	def.Description = strings.TrimSpace(def.Description)
	for name, task := range def.Tasks {
		task.Hero = strings.TrimSpace(task.Hero)
		task.Goal = strings.TrimSpace(task.Goal)
		task.Command = strings.TrimSpace(task.Command)
		task.Output = strings.TrimSpace(task.Output)
		task.ContextMode = strings.TrimSpace(task.ContextMode)
		task.Skills = sortedUniqueStrings(task.Skills)
		task.Depends = sortedUniqueStrings(task.Depends)
		task.Writes = sortedUniqueStrings(task.Writes)
		if task.Next != nil {
			for k, v := range task.Next {
				task.Next[k] = strings.TrimSpace(v)
			}
		}
		if task.Terminal != nil {
			task.Terminal.Status = strings.TrimSpace(task.Terminal.Status)
			task.Terminal.Output = strings.TrimSpace(task.Terminal.Output)
		}
		for i := range task.Acceptance {
			task.Acceptance[i].File = strings.TrimSpace(task.Acceptance[i].File)
			task.Acceptance[i].Check = strings.TrimSpace(task.Acceptance[i].Check)
			// Column names are matched exactly; whitespace is significant, so they
			// are deliberately not normalized.
		}
		def.Tasks[name] = task
	}
	sortedConvergence := make([]domain.ConvergenceRule, 0, len(def.Convergence))
	sortedConvergence = append(sortedConvergence, def.Convergence...)
	sortRules(sortedConvergence)
	def.Convergence = sortedConvergence
	if def.Inputs != nil {
		for name, port := range def.Inputs {
			port.Type = strings.TrimSpace(port.Type)
			if port.Type == "" {
				port.Type = domain.PortTypeString
			}
			def.Inputs[name] = port
		}
	}
	if def.Outputs != nil {
		for name, port := range def.Outputs {
			port.Type = strings.TrimSpace(port.Type)
			if port.Type == "" {
				port.Type = domain.PortTypeString
			}
			def.Outputs[name] = port
		}
	}
	return nil
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := append([]string(nil), values...)
	sortStrings(result)
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

func sortStrings(values []string) {
	sort.Strings(values)
}

func sortRules(rules []domain.ConvergenceRule) {
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].From == rules[j].From {
			return rules[i].To < rules[j].To
		}
		return rules[i].From < rules[j].From
	})
}

// DefinitionToYAML serializes a parsed definition back to the snake_case YAML
// contract. Used by export: the text can be imported again and round-trips to
// the same config digest.
func DefinitionToYAML(def *domain.FlowDefinition) ([]byte, error) {
	if def == nil {
		return nil, fmt.Errorf("flow definition is required")
	}
	copy := *def
	if err := normalizeDefinition(&copy); err != nil {
		return nil, err
	}
	encoded, err := yaml.Marshal(&copy)
	if err != nil {
		return nil, fmt.Errorf("encode flow definition as YAML: %w", err)
	}
	return encoded, nil
}
