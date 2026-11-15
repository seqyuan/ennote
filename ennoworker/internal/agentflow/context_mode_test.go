package agentflow

// contextMode: the two-value selector for what a task's child assignment carries
// beyond its resolved goal. These tests pin both halves of the rule - publish
// refuses a value it cannot honour, and the runtime renders a bounded,
// deterministic upstream RESULT DATA section (and nothing at all for isolated).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// padded fills the fixture payload; it never appears in the framing text.
const padded = "x"

// contextModeProducerYAML is a hero task (producer) feeding a reviewer that
// declares the context mode under test. Everything else is valid so a
// diagnostic cannot be blamed on another check.
func contextModeProducerYAML(extra string) string {
	return `schemaVersion: 1
id: context-mode-graph
outputs:
  decision: {type: string}
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  reviewer:
    hero: reader@1
    goal: Review
    depends: [producer]
` + extra + `  accept:
    terminal: {status: success, output: decision}
    output: decision
    depends: [reviewer]
`
}

func TestValidationContextModeAccepted(t *testing.T) {
	for _, extra := range []string{
		"",                             // omitted == isolated, exactly as before
		"    context_mode: isolated\n", // the documented default, spelled out
		"    context_mode: snapshot\n", // upstream results are carried
	} {
		result := validateAcceptanceYAML(t, contextModeProducerYAML(extra))
		assert.True(t, result.Valid, "%q: %v", extra, result.Diagnostics)
	}
}

// A value outside the two is refused at publish: the field selects a payload, so
// there is nothing sensible to do with a third name.
func TestValidationContextModeInvalidValue(t *testing.T) {
	result := validateAcceptanceYAML(t, contextModeProducerYAML("    context_mode: full_history\n"))
	require.False(t, result.Valid)
	diagnostic := acceptanceDiagnostic(result, "context_mode_invalid")
	require.NotNil(t, diagnostic, "expected context_mode_invalid, got %v", result.Diagnostics)
	assert.Equal(t, "tasks.reviewer.contextMode", diagnostic.Field)
	assert.Contains(t, diagnostic.Message, `"reviewer"`)
	assert.Contains(t, diagnostic.Message, "full_history")
	assert.Contains(t, diagnostic.Message, "isolated")
	assert.Contains(t, diagnostic.Message, "snapshot")
}

// check, terminal and fan_out tasks have no single child assignment to extend,
// so snapshot is refused there instead of being accepted and silently ignored.
func TestValidationContextModeRefusedWithoutAChildAssignment(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		field string
	}{
		{
			name: "check task",
			yaml: `schemaVersion: 1
id: context-mode-check
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
    context_mode: snapshot
`,
			field: "tasks.gate.contextMode",
		},
		{
			name: "terminal task",
			yaml: `schemaVersion: 1
id: context-mode-terminal
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  accept:
    terminal: {status: success}
    depends: [producer]
    context_mode: snapshot
`,
			field: "tasks.accept.contextMode",
		},
		{
			name: "fan_out task",
			yaml: `schemaVersion: 1
id: context-mode-fan-out
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  scan:
    hero: reader@1
    goal: Scan
    depends: [producer]
    fan_out: {min: 2, max: 2}
    context_mode: snapshot
`,
			field: "tasks.scan.contextMode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := validateAcceptanceYAML(t, tc.yaml)
			require.False(t, result.Valid)
			diagnostic := acceptanceDiagnostic(result, "context_mode_unsupported")
			require.NotNil(t, diagnostic, "expected context_mode_unsupported, got %v", result.Diagnostics)
			assert.Equal(t, tc.field, diagnostic.Field)
		})
	}
}

// A definition written before the field existed is byte-identical in the JSON
// the config digest commits: an omitted value is not defaulted to "isolated",
// so no published digest moves.
func TestContextModeOmittedKeepsTheDigestInputUnchanged(t *testing.T) {
	def, err := ParseDefinition([]byte(validDefYAML()))
	require.NoError(t, err)
	encoded, err := json.Marshal(def)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "contextMode")
	assert.NotContains(t, string(encoded), "context_mode")
	for _, task := range def.Tasks {
		assert.Empty(t, task.ContextMode, "an omitted context_mode must stay empty, not become a default")
	}
}

// --- runtime assignment -----------------------------------------------------

// snapshot increments a request: the reviewer's assignment carries the
// producer's recorded output_ref, framed as upstream result data. The isolated
// reviewer gets the plain goal, exactly as before this field existed.
func TestSnapshotTaskAssignmentCarriesUpstreamOutput(t *testing.T) {
	assignment := func(t *testing.T, mode string) (string, string) {
		t.Helper()
		def := threeTaskDef()
		reviewer := def.Tasks["reviewer"]
		reviewer.ContextMode = mode
		def.Tasks["reviewer"] = reviewer
		fake, err := newFakeStore(def)
		require.NoError(t, err)
		children := newFakeChildren()
		orch := newOrchestrator(fake, children, &fakeEvents{})
		orch.Start(context.Background(), "flow-run-1")
		require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

		var spec *ChildSpec
		for i := range children.created {
			if children.created[i].Handle == "reviewer" {
				spec = &children.created[i]
			}
		}
		require.NotNil(t, spec)
		return spec.Assignment, nodesByHandle(t, fake)["reviewer"].GoalText
	}

	snapshotAssignment, snapshotGoal := assignment(t, domain.FlowTaskContextSnapshot)
	assert.Contains(t, snapshotAssignment, "Upstream results (RESULT DATA")
	assert.Contains(t, snapshotAssignment, "--- upstream task producer output_ref ---")
	assert.Contains(t, snapshotAssignment, `{"changedFiles":["a.go"]}`)
	assert.Equal(t, `Review ["a.go"]`, snapshotGoal,
		"the frozen goalText stays the resolved goal; the snapshot lives only in the assignment")

	isolatedAssignment, isolatedGoal := assignment(t, "")
	assert.Equal(t, `Review ["a.go"]`, isolatedAssignment, "isolated adds nothing to the goal")
	assert.NotContains(t, isolatedAssignment, "Upstream results")
	assert.NotContains(t, isolatedAssignment, "changedFiles")
	assert.Equal(t, snapshotGoal, isolatedGoal)
}

// Nothing to show means no section at all: an entry task has no dependencies, so
// its snapshot assignment is the goal byte for byte.
func TestSnapshotTaskWithoutUpstreamOutputAddsNoSection(t *testing.T) {
	def := threeTaskDef()
	producer := def.Tasks["producer"]
	producer.ContextMode = domain.FlowTaskContextSnapshot
	def.Tasks["producer"] = producer
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	orch := newOrchestrator(fake, children, &fakeEvents{})
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	require.NotEmpty(t, children.created)
	assert.Equal(t, "Implement src/main.go and fast", children.created[0].Assignment)
}

// bigPayload is a valid JSON document of exactly size bytes: one string field
// filled to the requested length, so a per-task or total cut is measurable.
func bigPayload(t *testing.T, size int) json.RawMessage {
	t.Helper()
	const envelope = `{"blob":""}`
	payload := `{"blob":"` + strings.Repeat(padded, size-len(envelope)) + `"}`
	require.Len(t, payload, size)
	require.True(t, json.Valid([]byte(payload)))
	return json.RawMessage(payload)
}

// The upstream section is bounded twice: 2 KiB per task and 8 KiB in total, in
// task order, with an explicit marker instead of a silent cut.
func TestSnapshotAssignmentIsBounded(t *testing.T) {
	def := &domain.FlowDefinition{
		SchemaVersion: 1, ID: "snapshot-bounds",
		Budget: domain.FlowBudget{MaxTotalTokens: 100000},
		Tasks: map[string]domain.FlowTask{
			"reviewer": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "Review everything",
				Depends: []string{"p1", "p2", "p3", "p4", "p5"}, ContextMode: domain.FlowTaskContextSnapshot},
		},
	}
	for _, name := range []string{"p1", "p2", "p3", "p4", "p5"} {
		def.Tasks[name] = domain.FlowTask{Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "Produce " + name}
	}
	fake, err := newFakeStore(def)
	require.NoError(t, err)
	children := newFakeChildren()
	for _, name := range []string{"p1", "p2", "p3", "p4", "p5"} {
		children.results[name] = &domain.SubmitResult{Status: domain.SubmitCompleted, Payload: bigPayload(t, 3000)}
	}
	orch := newOrchestrator(fake, children, &fakeEvents{})
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))

	var assignment string
	for i := range children.created {
		if children.created[i].Handle == "reviewer" {
			assignment = children.created[i].Assignment
		}
	}
	require.NotEmpty(t, assignment)

	// 2 KiB per task, 8 KiB in total, in task order.
	sections := upstreamSections(assignment)
	require.Len(t, sections, 4, "four 2 KiB contributions fit; the fifth has no budget left")
	for _, section := range sections {
		assert.Len(t, section, contextSnapshotPerTaskBytes, "one task's contribution is capped at 2 KiB")
	}
	assert.Equal(t, contextSnapshotTotalBytes, len(strings.Join(sections, "")),
		"the whole section is capped at 8 KiB of upstream data")
	assert.Equal(t, 4, strings.Count(assignment, "[upstream result truncated"),
		"a cut is always marked, never silent")
	assert.NotContains(t, assignment, "upstream task p5 output_ref")

	// Deterministic ordering: dependencies render in task order.
	last := -1
	for _, name := range []string{"p1", "p2", "p3", "p4"} {
		index := strings.Index(assignment, "--- upstream task "+name+" output_ref ---")
		require.Greater(t, index, last, "%s must render in task order", name)
		last = index
	}
}

// upstreamSections returns the data bytes of each rendered upstream section, in
// order, with the truncation marker stripped.
func upstreamSections(assignment string) []string {
	const head, tail = " output_ref ---\n", "\n\n--- upstream task "
	var sections []string
	rest := assignment
	for {
		start := strings.Index(rest, head)
		if start < 0 {
			return sections
		}
		rest = rest[start+len(head):]
		end := strings.Index(rest, tail)
		data := rest
		if end >= 0 {
			data = rest[:end]
		}
		sections = append(sections, strings.TrimSuffix(data, contextSnapshotTruncated))
		if end < 0 {
			return sections
		}
		rest = rest[end:]
	}
}

// Regression: a definition written before either field existed parses,
// validates and runs exactly as before - no contextMode in its digest input, no
// acceptance restriction from a Hero that declares none, and no upstream section
// in any assignment.
func TestLegacyDefinitionWithoutEitherFieldIsUnchanged(t *testing.T) {
	def, err := ParseDefinition([]byte(validDefYAML()))
	require.NoError(t, err)
	result := validatorForHeroes(newStubResolver()).Validate(context.Background(), def)
	require.True(t, result.Valid, "%v", result.Diagnostics)
	assert.False(t, hasCode(result, "hero_acceptance_kind_unsupported"))
	assert.False(t, hasCode(result, "context_mode_invalid"))

	fake, err := newFakeStore(threeTaskDef())
	require.NoError(t, err)
	children := newFakeChildren()
	orch := newOrchestrator(fake, children, &fakeEvents{})
	orch.Start(context.Background(), "flow-run-1")
	require.Equal(t, domain.FlowStateCompleted, waitTerminal(t, fake))
	require.NotEmpty(t, children.created)
	for _, spec := range children.created {
		assert.NotContains(t, spec.Assignment, "Upstream results",
			"task %q must not gain an upstream section without context_mode: snapshot", spec.Handle)
	}
}
