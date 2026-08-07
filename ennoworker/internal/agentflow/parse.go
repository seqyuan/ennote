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
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"gopkg.in/yaml.v3"
)

// ParseDefinition parses a flow definition YAML document. Parsing is strict:
// schemaVersion is required and must be the supported version; unknown task
// types and malformed structures fail loudly.
func ParseDefinition(data []byte) (*domain.FlowDefinition, error) {
	var raw struct {
		SchemaVersion *int                       `yaml:"schemaVersion"`
		ID            string                     `yaml:"id"`
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
		return nil, fmt.Errorf("flow definition requires at least one task")
	}
	def := &domain.FlowDefinition{
		SchemaVersion: *raw.SchemaVersion,
		ID:            raw.ID,
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
		task, err := decodeTask(node)
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", name, err)
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

func decodeTask(node yaml.Node) (domain.FlowTask, error) {
	var task domain.FlowTask
	if err := node.Decode(&task); err != nil {
		return domain.FlowTask{}, err
	}
	task.Type = strings.TrimSpace(task.Type)
	if task.Type == "" {
		task.Type = domain.FlowTaskRole
	}
	switch task.Type {
	case domain.FlowTaskRole, domain.FlowTaskCheck:
	default:
		return domain.FlowTask{}, fmt.Errorf("unsupported task type %q", task.Type)
	}
	if task.Type == domain.FlowTaskCheck {
		// Check tasks are deterministic gates: no role, no skills, no budget.
		task.Role = ""
		task.Skills = nil
		task.Budget = nil
	} else {
		task.Command = ""
	}
	return task, nil
}

// normalizeDefinition canonicalizes a parsed definition: trims strings, sorts
// dependent slices so the config digest is stable regardless of authoring
// order. Maps are already sorted by encoding/json.
func normalizeDefinition(def *domain.FlowDefinition) error {
	def.ID = strings.TrimSpace(def.ID)
	def.Description = strings.TrimSpace(def.Description)
	for name, task := range def.Tasks {
		task.Role = strings.TrimSpace(task.Role)
		task.Goal = strings.TrimSpace(task.Goal)
		task.Command = strings.TrimSpace(task.Command)
		task.Output = strings.TrimSpace(task.Output)
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
