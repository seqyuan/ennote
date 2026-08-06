package agentflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

const configDigestSchemaTag = "agentflow-config-v1"

// ConfigDigest computes the immutable version digest of a flow definition.
// It commits the normalized definition JSON (schemaVersion + definition text,
// including role references, skills, check commands, and the flow budget) with
// a schema tag. The digest is reproducible from the authoring YAML alone so
// candidate discovery can reuse an existing immutable version for identical
// project files.
func ConfigDigest(def *domain.FlowDefinition) (string, error) {
	if def == nil {
		return "", fmt.Errorf("flow definition is required")
	}
	copy := *def
	if err := normalizeDefinition(&copy); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(&copy)
	if err != nil {
		return "", fmt.Errorf("encode flow definition: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(configDigestSchemaTag))
	h.Write([]byte{0})
	h.Write(encoded)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TaskGoalDigest computes the frozen digest of one resolved task goal. It is
// part of the node snapshot identity: resume only accepts the same digest.
func TaskGoalDigest(goal string) string {
	h := sha256.New()
	h.Write([]byte("agentflow-goal-v1"))
	h.Write([]byte{0})
	h.Write([]byte(goal))
	return hex.EncodeToString(h.Sum(nil))
}

// TopologicalOrder returns task names in dependency order (dependencies first)
// using Kahn's algorithm. The order is deterministic: at each step the
// ready task with the smallest name wins.
func TopologicalOrder(tasks map[string]domain.FlowTask) ([]string, error) {
	indegree := make(map[string]int, len(tasks))
	children := make(map[string][]string, len(tasks))
	for name := range tasks {
		indegree[name] = 0
	}
	for name, task := range tasks {
		for _, dep := range task.Depends {
			if _, ok := tasks[dep]; !ok {
				return nil, fmt.Errorf("task %q depends on unknown task %q", name, dep)
			}
			children[dep] = append(children[dep], name)
			indegree[name]++
		}
	}
	var ready []string
	for name, degree := range indegree {
		if degree == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(tasks))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		order = append(order, name)
		for _, child := range children[name] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(tasks) {
		return nil, fmt.Errorf("flow task graph contains a cycle")
	}
	return order, nil
}

// TaskSkillDigest computes the frozen digest of a task's resolved skill set.
func TaskSkillDigest(skillIDs []string) string {
	ids := sortedUniqueStrings(skillIDs)
	h := sha256.New()
	h.Write([]byte("agentflow-skills-v1"))
	for _, id := range ids {
		h.Write([]byte{0})
		h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ManifestDigest is the per-run freeze identity: it commits the immutable
// version digest AND the frozen run inputs/vars, so a resume with different
// inputs fails closed.
func ManifestDigest(versionDigest string, inputsJSON []byte) (string, error) {
	if len(inputsJSON) == 0 {
		inputsJSON = []byte(`{}`)
	}
	if !json.Valid(inputsJSON) {
		return "", fmt.Errorf("flow inputs are not valid JSON")
	}
	h := sha256.New()
	h.Write([]byte("agentflow-manifest-v1"))
	h.Write([]byte{0})
	h.Write([]byte(versionDigest))
	h.Write([]byte{0})
	h.Write(inputsJSON)
	return hex.EncodeToString(h.Sum(nil)), nil
}
