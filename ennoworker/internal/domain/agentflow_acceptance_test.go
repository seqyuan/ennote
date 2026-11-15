package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The acceptance vocabulary is exactly three values. Two would force an
// implementation to call an undecidable condition a pass or a failure.
func TestAcceptanceResultVocabulary(t *testing.T) {
	assert.Equal(t, AcceptanceResult("holds"), AcceptanceHolds)
	assert.Equal(t, AcceptanceResult("does_not_hold"), AcceptanceDoesNotHold)
	assert.Equal(t, AcceptanceResult("unverified"), AcceptanceUnverified)

	values := map[AcceptanceResult]bool{
		AcceptanceHolds:       true,
		AcceptanceDoesNotHold: true,
		AcceptanceUnverified:  true,
	}
	assert.Len(t, values, 3)
	for _, forbidden := range []AcceptanceResult{"verified", "passed", "failed", "true", "false", ""} {
		assert.False(t, values[forbidden], "acceptance vocabulary must not contain %q", forbidden)
	}
}

func TestFlowAcceptanceJSONShape(t *testing.T) {
	task := FlowTask{
		Hero: "analyst@2",
		Goal: "compute per-sample QC metrics",
		Acceptance: []FlowAcceptance{
			{File: "outputs/qc.csv", Check: "csv-columns", Columns: []string{"sample", "reads", "dup_rate"}},
			{File: "outputs/qc.json", Check: "json-valid"},
		},
	}
	encoded, err := json.Marshal(task)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"acceptance":[`)

	var decoded FlowTask
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, task.Acceptance, decoded.Acceptance)

	// A task without acceptance keeps its existing JSON shape.
	plain, err := json.Marshal(FlowTask{Hero: "reader@1", Goal: "review"})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "acceptance")

	// Acceptance without columns omits the key (no command field exists).
	noLeaf, err := json.Marshal(FlowAcceptance{File: "outputs/report.md", Check: "non-empty"})
	require.NoError(t, err)
	assert.NotContains(t, string(noLeaf), "columns")
	assert.NotContains(t, string(noLeaf), "command")
}

// UnknownTaskFields is derived from the YAML text, so it must never be part of
// the definition's JSON form: the config digest is computed over that JSON.
func TestFlowDefinitionUnknownTaskFieldsAreNotSerialized(t *testing.T) {
	def := FlowDefinition{
		SchemaVersion:     FlowSchemaVersion,
		ID:                "digest-shape",
		Budget:            FlowBudget{MaxTotalTokens: 1000},
		Tasks:             map[string]FlowTask{"a": {Hero: "h@1", Goal: "g"}},
		UnknownTaskFields: map[string][]string{"a": {"depend"}},
	}
	encoded, err := json.Marshal(def)
	require.NoError(t, err)
	lower := strings.ToLower(string(encoded))
	assert.NotContains(t, lower, "unknowntaskfields")
	assert.NotContains(t, lower, "depend")
}
