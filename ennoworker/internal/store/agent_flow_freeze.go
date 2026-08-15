package store

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// FlowDefinitionInputs separates run inputs from runtime vars.
type FlowDefinitionInputs struct {
	Inputs map[string]any `json:"inputs,omitempty"`
	Vars   map[string]any `json:"vars,omitempty"`
}

// NormalizeFlowInputs builds the frozen inputs payload from user-supplied
// inputs and vars, validating input names against the declared ports.
func NormalizeFlowInputs(def *domain.FlowDefinition, inputs, vars map[string]any) (json.RawMessage, error) {
	normalized := FlowDefinitionInputs{Inputs: make(map[string]any), Vars: vars}
	unknown := make([]string, 0)
	for name, value := range inputs {
		if _, declared := def.Inputs[name]; !declared {
			unknown = append(unknown, name)
			continue
		}
		normalized.Inputs[name] = value
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown flow inputs: %s", joinStrings(unknown, ", "))
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func joinStrings(values []string, sep string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += sep
		}
		out += v
	}
	return out
}
