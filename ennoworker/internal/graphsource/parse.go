// Package graphsource parses the Worker-global, file-authored Graph format.
package graphsource

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

const MaxGraphFileBytes = 512 * 1024

var (
	graphIDPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	taskIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	resourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
)

type Task struct {
	Name     string                `json:"name" yaml:"name"`
	Role     string                `json:"role,omitempty" yaml:"role,omitempty"`
	Model    string                `json:"model,omitempty" yaml:"model,omitempty"`
	Thinking domain.ThinkingEffort `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Skills   []string              `json:"skills,omitempty" yaml:"skills,omitempty"`
	Goal     string                `json:"goal" yaml:"goal"`
	Writes   []string              `json:"writes,omitempty" yaml:"writes,omitempty"`
	Budget   *TaskBudget           `json:"budget,omitempty" yaml:"budget,omitempty"`
}

type TaskBudget struct {
	Tokens int64 `json:"tokens,omitempty" yaml:"tokens,omitempty"`
}

type Document struct {
	SchemaVersion int                 `json:"schemaVersion" yaml:"schema_version"`
	ID            string              `json:"id" yaml:"id"`
	Name          string              `json:"name" yaml:"name"`
	Description   string              `json:"description,omitempty" yaml:"description,omitempty"`
	Tasks         map[string]Task     `json:"tasks" yaml:"tasks"`
	Graph         map[string][]string `json:"graph" yaml:"graph"`
}

func Parse(data []byte) (*Document, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("Graph file is empty")
	}
	if len(data) > MaxGraphFileBytes {
		return nil, fmt.Errorf("Graph file exceeds 512 KiB")
	}

	var document Document
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse Graph YAML: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("Graph file must contain one YAML document")
		}
		return nil, fmt.Errorf("parse Graph YAML: %w", err)
	}
	if err := normalizeAndValidate(&document); err != nil {
		return nil, err
	}
	return &document, nil
}

func SourceDigest(document *Document) (string, error) {
	if document == nil {
		return "", fmt.Errorf("Graph source document is required")
	}
	copy := clone(document)
	if err := normalizeAndValidate(copy); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode Graph source: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func Encode(document *Document) ([]byte, error) {
	if document == nil {
		return nil, fmt.Errorf("Graph source document is required")
	}
	copy := clone(document)
	if err := normalizeAndValidate(copy); err != nil {
		return nil, err
	}
	encoded, err := yaml.Marshal(copy)
	if err != nil {
		return nil, fmt.Errorf("encode Graph YAML: %w", err)
	}
	return encoded, nil
}

func normalizeAndValidate(document *Document) error {
	document.ID = strings.TrimSpace(document.ID)
	document.Name = strings.TrimSpace(document.Name)
	document.Description = strings.TrimSpace(document.Description)
	if document.SchemaVersion != 1 {
		return fmt.Errorf("Graph schema_version must be 1")
	}
	if !graphIDPattern.MatchString(document.ID) {
		return fmt.Errorf("Graph id must match %s", graphIDPattern.String())
	}
	if document.Name == "" {
		return fmt.Errorf("Graph name is required")
	}
	if document.Tasks == nil {
		document.Tasks = map[string]Task{}
	}
	if document.Graph == nil {
		return fmt.Errorf("Graph topology is required")
	}

	for id, task := range document.Tasks {
		if !taskIDPattern.MatchString(id) {
			return fmt.Errorf("Task id %q must match %s", id, taskIDPattern.String())
		}
		task.Name = strings.TrimSpace(task.Name)
		task.Role = strings.TrimSpace(task.Role)
		task.Model = strings.TrimSpace(task.Model)
		task.Goal = strings.TrimSpace(task.Goal)
		if task.Name == "" {
			return fmt.Errorf("Task %q name is required", id)
		}
		if task.Goal == "" {
			return fmt.Errorf("Task %q goal is required", id)
		}

		if task.Role != "" {
			if err := validateScopedRef(task.Role); err != nil {
				return fmt.Errorf("Task %q role: %w", id, err)
			}
			if task.Model != "" || task.Thinking != "" || len(task.Skills) != 0 {
				return fmt.Errorf("Task %q Role-backed execution cannot declare model, thinking, or skills", id)
			}
		} else {
			if err := validateModelRef(task.Model); err != nil {
				return fmt.Errorf("Task %q model: %w", id, err)
			}
			if task.Thinking == "" {
				task.Thinking = domain.ThinkingDefault
			}
			switch task.Thinking {
			case domain.ThinkingDefault, domain.ThinkingLow, domain.ThinkingMedium, domain.ThinkingHigh:
			default:
				return fmt.Errorf("Task %q has unsupported thinking effort %q", id, task.Thinking)
			}
		}

		seenSkills := make(map[string]bool, len(task.Skills))
		for index := range task.Skills {
			task.Skills[index] = strings.TrimSpace(task.Skills[index])
			if err := validateScopedRef(task.Skills[index]); err != nil {
				return fmt.Errorf("Task %q skill %d: %w", id, index, err)
			}
			if seenSkills[task.Skills[index]] {
				return fmt.Errorf("Task %q has duplicate skill %q", id, task.Skills[index])
			}
			seenSkills[task.Skills[index]] = true
		}
		sort.Strings(task.Skills)
		task.Writes = sortedUniqueTrimmed(task.Writes)
		document.Tasks[id] = task
	}

	if len(document.Graph) != len(document.Tasks) {
		return fmt.Errorf("tasks and graph must contain exactly the same Task ids")
	}
	for id := range document.Tasks {
		if _, ok := document.Graph[id]; !ok {
			return fmt.Errorf("Task %q is missing from graph", id)
		}
	}
	for id, dependencies := range document.Graph {
		if _, ok := document.Tasks[id]; !ok {
			return fmt.Errorf("graph references unknown Task %q", id)
		}
		seen := make(map[string]bool, len(dependencies))
		for index := range dependencies {
			dependencies[index] = strings.TrimSpace(dependencies[index])
			dependency := dependencies[index]
			if _, ok := document.Tasks[dependency]; !ok {
				return fmt.Errorf("Task %q depends on unknown Task %q", id, dependency)
			}
			if dependency == id {
				return fmt.Errorf("Task %q cannot depend on itself", id)
			}
			if seen[dependency] {
				return fmt.Errorf("Task %q has duplicate dependency %q", id, dependency)
			}
			seen[dependency] = true
		}
		sort.Strings(dependencies)
		document.Graph[id] = dependencies
	}
	if err := validateAcyclic(document.Graph); err != nil {
		return err
	}
	return nil
}

func validateAcyclic(graph map[string][]string) error {
	indegree := make(map[string]int, len(graph))
	children := make(map[string][]string, len(graph))
	for id := range graph {
		indegree[id] = 0
	}
	for id, dependencies := range graph {
		indegree[id] = len(dependencies)
		for _, dependency := range dependencies {
			children[dependency] = append(children[dependency], id)
		}
	}
	ready := make([]string, 0, len(graph))
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	processed := 0
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		processed++
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
			}
		}
	}
	if processed != len(graph) {
		return fmt.Errorf("Graph contains a dependency cycle")
	}
	return nil
}

func validateScopedRef(ref string) error {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || (parts[0] != "local" && parts[0] != "global") || !resourceIDPattern.MatchString(parts[1]) {
		return fmt.Errorf("must use local/<id> or global/<id>")
	}
	return nil
}

func validateModelRef(ref string) error {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("must use provider-name/model-name")
	}
	return nil
}

func sortedUniqueTrimmed(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func clone(document *Document) *Document {
	copy := *document
	copy.Tasks = make(map[string]Task, len(document.Tasks))
	for id, task := range document.Tasks {
		task.Skills = append([]string(nil), task.Skills...)
		task.Writes = append([]string(nil), task.Writes...)
		copy.Tasks[id] = task
	}
	copy.Graph = make(map[string][]string, len(document.Graph))
	for id, dependencies := range document.Graph {
		copy.Graph[id] = append([]string(nil), dependencies...)
	}
	return &copy
}
