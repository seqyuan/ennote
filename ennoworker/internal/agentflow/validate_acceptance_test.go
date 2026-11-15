package agentflow

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// acceptanceProducerYAML is a graph whose entry task (producer) carries the
// acceptance block under test. Everything else is valid so a diagnostic cannot
// be blamed on another check.
func acceptanceProducerYAML(producerAcceptance string) string {
	return `schemaVersion: 1
id: acceptance-graph
outputs:
  decision: {type: string}
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
` + producerAcceptance + `  reviewer:
    hero: reader@1
    goal: Review
    depends: [producer]
  accept:
    terminal: {status: success, output: decision}
    output: decision
    depends: [reviewer]
`
}

// acceptanceBlock renders an acceptance list indented for a task body.
func acceptanceBlock(entries ...string) string {
	if len(entries) == 0 {
		return "    acceptance: []\n"
	}
	var b strings.Builder
	b.WriteString("    acceptance:\n")
	for _, entry := range entries {
		b.WriteString("      - ")
		b.WriteString(entry)
		b.WriteString("\n")
	}
	return b.String()
}

func validateAcceptanceYAML(t *testing.T, yaml string) *ValidationResult {
	t.Helper()
	def, err := ParseDefinition([]byte(yaml))
	require.NoError(t, err, "parse must succeed so validation is exercised")
	return validator().Validate(context.Background(), def)
}

func acceptanceDiagnostic(result *ValidationResult, code string) *ValidationDiagnostic {
	for i := range result.Diagnostics {
		if result.Diagnostics[i].Code == code {
			return &result.Diagnostics[i]
		}
	}
	return nil
}

// A3: a well-formed acceptance block publishes.
func TestValidationAcceptanceValid(t *testing.T) {
	yaml := acceptanceProducerYAML(acceptanceBlock(
		`{file: outputs/qc.csv, check: csv-columns, columns: [sample, reads, dup_rate]}`,
		`{file: outputs/qc.json, check: json-valid}`,
		`{file: outputs/report.md, check: non-empty}`,
	))
	result := validateAcceptanceYAML(t, yaml)
	assert.True(t, result.Valid, "%v", result.Diagnostics)

	// Every valid leaf is accepted individually too (single-entry lists).
	for _, entry := range []string{
		`{file: outputs/qc.csv, check: csv-columns, columns: [sample]}`,
		`{file: outputs/qc.json, check: json-valid}`,
		`{file: outputs/report.md, check: non-empty}`,
		`{file: outputs/report.md, check: exists}`,
	} {
		result := validateAcceptanceYAML(t, acceptanceProducerYAML(acceptanceBlock(entry)))
		assert.True(t, result.Valid, "%s: %v", entry, result.Diagnostics)
	}

	// Column order, extra columns and workspace subtrees other than outputs/
	// are allowed in this slice.
	result = validateAcceptanceYAML(t, acceptanceProducerYAML(acceptanceBlock(
		`{file: work/qc.csv, check: csv-columns, columns: [reads, sample]}`,
	)))
	assert.True(t, result.Valid, "%v", result.Diagnostics)

	// The published-artifact leaf is accepted alone and next to a file leaf, and
	// it is the only leaf that takes no path at all.
	result = validateAcceptanceYAML(t, acceptanceProducerYAML(acceptanceBlock(
		`{artifact: outputs/qc.csv, check: artifact-published}`,
		`{file: outputs/report.md, check: non-empty}`,
	)))
	assert.True(t, result.Valid, "%v", result.Diagnostics)
}

// A3: every acceptance diagnostic code has a failing case naming the task.
func TestValidationAcceptanceCodes(t *testing.T) {
	tooManyColumns := make([]string, 0, 65)
	for i := 0; i < 65; i++ {
		tooManyColumns = append(tooManyColumns, "c"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	longColumn := strings.Repeat("c", 129)

	checkTaskYAML := `schemaVersion: 1
id: acceptance-check-task
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  gate:
    type: check
    command: "go test ./..."
    depends: [producer]
    acceptance:
      - {file: outputs/report.md, check: non-empty}
`
	terminalTaskYAML := `schemaVersion: 1
id: acceptance-terminal-task
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  accept:
    terminal: {status: success}
    depends: [producer]
    acceptance:
      - {file: outputs/report.md, check: non-empty}
`
	fanOutTaskYAML := `schemaVersion: 1
id: acceptance-fan-out
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  fan:
    hero: reader@1
    goal: Fan out the read
    depends: [producer]
    fan_out: {min: 2, max: 4}
    acceptance:
      - {file: outputs/report.md, check: non-empty}
`

	tests := []struct {
		name            string
		yaml            string
		code            string
		field           string
		messageContains []string
	}{
		{
			name:            "empty acceptance list",
			yaml:            acceptanceProducerYAML(acceptanceBlock()),
			code:            "acceptance_empty",
			field:           "tasks.producer.acceptance",
			messageContains: []string{"producer", "empty"},
		},
		{
			name:            "file required",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{check: exists}`)),
			code:            "acceptance_file_required",
			field:           "tasks.producer.acceptance[0].file",
			messageContains: []string{"producer", "acceptance[0]"},
		},
		{
			name:            "artifact required",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{check: artifact-published}`)),
			code:            "acceptance_artifact_required",
			field:           "tasks.producer.acceptance[0].artifact",
			messageContains: []string{"producer", "acceptance[0]", "artifact-published", "artifact field"},
		},
		{
			name:            "artifact-published refuses a file path",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{artifact: outputs/qc.csv, file: outputs/qc.csv, check: artifact-published}`)),
			code:            "acceptance_file_forbidden",
			field:           "tasks.producer.acceptance[0].file",
			messageContains: []string{"producer", "artifact-published", "not a file path"},
		},
		{
			name:            "artifact-published refuses columns",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{artifact: outputs/qc.csv, check: artifact-published, columns: [sample]}`)),
			code:            "acceptance_columns_forbidden",
			field:           "tasks.producer.acceptance[0].columns",
			messageContains: []string{"producer", "artifact-published", "does not take columns"},
		},
		{
			name:            "csv-columns refuses an artifact",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/qc.csv, check: csv-columns, columns: [sample], artifact: outputs/qc.csv}`)),
			code:            "acceptance_artifact_forbidden",
			field:           "tasks.producer.acceptance[0].artifact",
			messageContains: []string{"producer", "csv-columns", "only artifact-published"},
		},
		{
			name:            "file leaves refuse an artifact",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/report.md, check: non-empty, artifact: outputs/qc.csv}`)),
			code:            "acceptance_artifact_forbidden",
			field:           "tasks.producer.acceptance[0].artifact",
			messageContains: []string{"producer", "non-empty", "only artifact-published"},
		},
		{
			name:            "check unknown names tests-passed",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/report.md, check: tests-passed}`)),
			code:            "acceptance_check_unknown",
			field:           "tasks.producer.acceptance[0].check",
			messageContains: []string{"producer", "tests-passed is not supported yet"},
		},
		{
			name:            "csv-columns requires columns",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/qc.csv, check: csv-columns}`)),
			code:            "acceptance_columns_required",
			field:           "tasks.producer.acceptance[0].columns",
			messageContains: []string{"producer", "csv-columns"},
		},
		{
			name:            "columns forbidden for other leaves",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/report.md, check: exists, columns: [sample]}`)),
			code:            "acceptance_columns_forbidden",
			field:           "tasks.producer.acceptance[0].columns",
			messageContains: []string{"producer", "exists"},
		},
		{
			name:            "columns duplicate",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/qc.csv, check: csv-columns, columns: [sample, sample]}`)),
			code:            "acceptance_columns_invalid",
			field:           "tasks.producer.acceptance[0].columns",
			messageContains: []string{"producer", "twice"},
		},
		{
			name:            "columns blank",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/qc.csv, check: csv-columns, columns: [sample, "  "]}`)),
			code:            "acceptance_columns_invalid",
			field:           "tasks.producer.acceptance[0].columns",
			messageContains: []string{"producer", "blank"},
		},
		{
			name:            "columns too many",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/qc.csv, check: csv-columns, columns: [` + strings.Join(tooManyColumns, ", ") + `]}`)),
			code:            "acceptance_columns_invalid",
			field:           "tasks.producer.acceptance[0].columns",
			messageContains: []string{"producer", "at most 64"},
		},
		{
			name:            "column too long",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: outputs/qc.csv, check: csv-columns, columns: ["` + longColumn + `"]}`)),
			code:            "acceptance_columns_invalid",
			field:           "tasks.producer.acceptance[0].columns",
			messageContains: []string{"producer", "longer than 128"},
		},
		{
			name:            "absolute path",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: /etc/passwd, check: exists}`)),
			code:            "acceptance_path_absolute",
			field:           "tasks.producer.acceptance[0].file",
			messageContains: []string{"producer", "workspace-relative"},
		},
		{
			name:            "dot-dot path",
			yaml:            acceptanceProducerYAML(acceptanceBlock(`{file: ../escape.md, check: exists}`)),
			code:            "acceptance_path_absolute",
			field:           "tasks.producer.acceptance[0].file",
			messageContains: []string{"producer", ".."},
		},
		{
			name:            "check task cannot declare acceptance",
			yaml:            checkTaskYAML,
			code:            "acceptance_not_supported",
			field:           "tasks.gate.acceptance",
			messageContains: []string{"gate", "check task"},
		},
		{
			name:            "terminal task cannot declare acceptance",
			yaml:            terminalTaskYAML,
			code:            "acceptance_not_supported",
			field:           "tasks.accept.acceptance",
			messageContains: []string{"accept", "terminal task"},
		},
		{
			name:            "fan_out parent cannot declare acceptance",
			yaml:            fanOutTaskYAML,
			code:            "acceptance_fan_out_unsupported",
			field:           "tasks.fan.acceptance",
			messageContains: []string{"fan", "fan_out"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := validateAcceptanceYAML(t, tc.yaml)
			require.False(t, result.Valid, "expected a diagnostic, got %v", result.Diagnostics)
			diagnostic := acceptanceDiagnostic(result, tc.code)
			require.NotNil(t, diagnostic, "expected %s, got %v", tc.code, result.Diagnostics)
			assert.Equal(t, tc.field, diagnostic.Field)
			for _, want := range tc.messageContains {
				assert.Contains(t, diagnostic.Message, want)
			}
		})
	}
}

// A3: the task-level gates suppress entry-level diagnostics for tasks that
// cannot carry acceptance at all.
func TestValidationAcceptanceTypeGatesSuppressEntryChecks(t *testing.T) {
	result := validateAcceptanceYAML(t, `schemaVersion: 1
id: acceptance-check-task
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  gate:
    type: check
    command: "go test ./..."
    depends: [producer]
    acceptance:
      - {file: /etc/passwd, check: tests-passed}
`)
	assert.True(t, hasCode(result, "acceptance_not_supported"))
	assert.False(t, hasCode(result, "acceptance_check_unknown"))
	assert.False(t, hasCode(result, "acceptance_path_absolute"))
}

// validatorForHeroes returns the standard publish validator over a Hero catalog
// the case can change, so one Hero's acceptanceKinds can be restricted while
// every other check keeps its usual facts.
func validatorForHeroes(resolver Resolver) *Validator {
	return &Validator{
		Resolver:       resolver,
		CheckAllowlist: []string{"go", "python3", "sh", "node", "git", "make"},
		MaxFanOut:      64,
		MaxRounds:      100,
	}
}

// A Hero states what it produces, so a task running it may only declare the
// acceptance leaves that Hero accepts. An empty acceptanceKinds list means "the
// kernel's whole vocabulary" and never fires: that is what keeps every Hero
// written before the field existed publishing the graphs it always published.
func TestValidationHeroAcceptanceKindsRestrictDeclaredLeaves(t *testing.T) {
	csvClause := acceptanceProducerYAML(acceptanceBlock(
		`{file: outputs/qc.csv, check: csv-columns, columns: [sample, reads]}`))
	jsonClause := acceptanceProducerYAML(acceptanceBlock(`{file: outputs/qc.json, check: json-valid}`))

	withWriterKinds := func(kinds ...string) *stubResolver {
		resolver := newStubResolver()
		writer := resolver.heroDefs["writer@1"]
		writer.AcceptanceKinds = kinds
		resolver.heroDefs["writer@1"] = writer
		return resolver
	}

	cases := []struct {
		name        string
		yaml        string
		kinds       []string
		wantAllowed bool
	}{
		{name: "declared kind the Hero does not accept", yaml: csvClause, kinds: []string{"json-valid"}},
		{name: "declared kind the Hero accepts", yaml: jsonClause, kinds: []string{"json-valid"}, wantAllowed: true},
		{name: "empty list is the whole vocabulary", yaml: csvClause, kinds: nil, wantAllowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def, err := ParseDefinition([]byte(tc.yaml))
			require.NoError(t, err)
			result := validatorForHeroes(withWriterKinds(tc.kinds...)).Validate(context.Background(), def)
			if tc.wantAllowed {
				assert.True(t, result.Valid, "%v", result.Diagnostics)
				assert.False(t, hasCode(result, "hero_acceptance_kind_unsupported"))
				return
			}
			require.False(t, result.Valid)
			diagnostic := acceptanceDiagnostic(result, "hero_acceptance_kind_unsupported")
			require.NotNil(t, diagnostic, "expected hero_acceptance_kind_unsupported, got %v", result.Diagnostics)
			assert.Equal(t, "tasks.producer.acceptance[0].check", diagnostic.Field)
			// The message has to name the task, the kind and the Hero reference:
			// the HTTP layer flattens diagnostics and drops Field.
			assert.Contains(t, diagnostic.Message, `"producer"`)
			assert.Contains(t, diagnostic.Message, `"csv-columns"`)
			assert.Contains(t, diagnostic.Message, `"writer@1"`)
			assert.Contains(t, diagnostic.Message, "json-valid")
		})
	}
}

// An unknown check is reported once, as acceptance_check_unknown: the Hero
// restriction does not add a second diagnostic about a leaf nobody can decide.
func TestValidationUnknownCheckIsNotAlsoBlamedOnTheHero(t *testing.T) {
	resolver := newStubResolver()
	writer := resolver.heroDefs["writer@1"]
	writer.AcceptanceKinds = []string{"json-valid"}
	resolver.heroDefs["writer@1"] = writer

	def, err := ParseDefinition([]byte(acceptanceProducerYAML(
		acceptanceBlock(`{file: outputs/qc.csv, check: tests-passed}`))))
	require.NoError(t, err)
	result := validatorForHeroes(resolver).Validate(context.Background(), def)
	require.False(t, result.Valid)
	assert.True(t, hasCode(result, "acceptance_check_unknown"))
	assert.False(t, hasCode(result, "hero_acceptance_kind_unsupported"))
}
