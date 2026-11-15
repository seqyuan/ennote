package agentflow

import (
	"context"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const unknownFieldDefYAML = `schemaVersion: 1
id: lenient-read-path
name: Lenient
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
    depend: [reviewer]
    acceptence:
      - file: outputs/report.md
        check: non-empty
  reviewer:
    hero: reader@1
    goal: Review
`

// A1: an unknown key inside a task is recorded on the definition instead of
// being silently dropped, and parsing still succeeds so the read path (editor
// load, revision load) keeps working.
func TestParseDefinitionRecordsUnknownTaskFields(t *testing.T) {
	def, err := ParseDefinition([]byte(unknownFieldDefYAML))
	require.NoError(t, err, "parse must stay lenient on the read path")
	require.NotNil(t, def)

	assert.Equal(t, []string{"acceptence", "depend"}, def.UnknownTaskFields["producer"])
	assert.NotContains(t, def.UnknownTaskFields, "reviewer")

	// The typo is visible as an effect: depends and acceptance are empty.
	assert.Empty(t, def.Tasks["producer"].Depends)
	assert.Empty(t, def.Tasks["producer"].Acceptance)
	assert.Equal(t, "Do the work", def.Tasks["producer"].Goal)
}

// A1: every key the task struct knows parses and records nothing, including
// the new acceptance clause.
func TestParseDefinitionKnownTaskKeysRecordNothing(t *testing.T) {
	yaml := `schemaVersion: 1
id: known-keys
budget: {max_total_tokens: 120000}
tasks:
  a:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 100}
    depends: []
    writes: [outputs/**]
    output: main
    next: {pass: b}
    acceptance:
      - file: outputs/qc.csv
        check: csv-columns
        columns: [sample, reads]
  b:
    hero: reader@1
    goal: Review
    depends: [a]
    terminal: {status: success, output: main}
outputs:
  main: {type: string}
`
	def, err := ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	assert.Empty(t, def.UnknownTaskFields)

	task := def.Tasks["a"]
	require.Len(t, task.Acceptance, 1)
	assert.Equal(t, "outputs/qc.csv", task.Acceptance[0].File)
	assert.Equal(t, "csv-columns", task.Acceptance[0].Check)
	assert.Equal(t, []string{"sample", "reads"}, task.Acceptance[0].Columns)

	// Every yaml key of the task struct is in the allowlist; acceptance must be
	// part of it, otherwise its own key would be reported as unknown.
	assert.Contains(t, flowTaskKnownYAMLKeys, "acceptance")
}

// A1: the recorded field is derived from the text, so it must not affect the
// config digest or round-trip into an exported definition.
func TestUnknownTaskFieldsDoNotAffectDigestOrExport(t *testing.T) {
	clean := strings.Replace(unknownFieldDefYAML,
		"    depend: [reviewer]\n    acceptence:\n      - file: outputs/report.md\n        check: non-empty\n", "", 1)
	require.NotContains(t, clean, "depend:")

	cleanDef, err := ParseDefinition([]byte(clean))
	require.NoError(t, err)
	typoDef, err := ParseDefinition([]byte(unknownFieldDefYAML))
	require.NoError(t, err)
	require.NotEmpty(t, typoDef.UnknownTaskFields)

	cleanDigest, err := ConfigDigest(cleanDef)
	require.NoError(t, err)
	typoDigest, err := ConfigDigest(typoDef)
	require.NoError(t, err)
	assert.Equal(t, cleanDigest, typoDigest)

	exported, err := DefinitionToYAML(typoDef)
	require.NoError(t, err)
	assert.NotContains(t, strings.ToLower(string(exported)), "unknowntaskfields")
}

// A1: publish rejects a recorded unknown task key, naming the task and the key.
func TestValidationUnknownTaskField(t *testing.T) {
	def, err := ParseDefinition([]byte(unknownFieldDefYAML))
	require.NoError(t, err)

	result := validator().Validate(context.Background(), def)
	require.False(t, result.Valid)

	var found *ValidationDiagnostic
	for i := range result.Diagnostics {
		if result.Diagnostics[i].Code == "flow_task_unknown_field" {
			found = &result.Diagnostics[i]
			break
		}
	}
	require.NotNil(t, found, "expected flow_task_unknown_field, got %v", result.Diagnostics)
	assert.Equal(t, "tasks.producer", found.Field)
	assert.Contains(t, found.Message, "producer")
	assert.Contains(t, found.Message, "acceptence")
	assert.Contains(t, found.Message, "depend")
	assert.NotContains(t, found.Message, "reviewer")

	// A definition without unknown keys reports nothing.
	cleanDef, err := ParseDefinition([]byte(validDefYAML()))
	require.NoError(t, err)
	cleanResult := validator().Validate(context.Background(), cleanDef)
	assert.False(t, hasCode(cleanResult, "flow_task_unknown_field"), "%v", cleanResult.Diagnostics)
}

// A1: acceptance is decoded from the task node into the domain type.
func TestParseDefinitionDecodesAcceptance(t *testing.T) {
	yaml := `schemaVersion: 1
id: acceptance-parse
budget: {max_total_tokens: 120000}
tasks:
  a:
    hero: writer@1
    goal: Do the work
    acceptance:
      - file: outputs/qc.csv
        check: csv-columns
        columns: [sample, reads, dup_rate]
      - file: outputs/qc.json
        check: json-valid
      - file: outputs/report.md
        check: non-empty
`
	def, err := ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	require.Len(t, def.Tasks["a"].Acceptance, 3)
	assert.Equal(t, domain.FlowAcceptance{File: "outputs/qc.csv", Check: "csv-columns",
		Columns: []string{"sample", "reads", "dup_rate"}}, def.Tasks["a"].Acceptance[0])
	assert.Equal(t, domain.FlowAcceptance{File: "outputs/qc.json", Check: "json-valid"},
		def.Tasks["a"].Acceptance[1])
	assert.Empty(t, def.Tasks["a"].Acceptance[2].Columns)
}
