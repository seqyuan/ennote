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

// --- contextFrom: a subscription, not an edge ---------------------------------

// contextFromYAML is the producer → reviewer fixture with a third, independent
// task the reviewer wants to read without depending on it. Everything else is
// valid so a diagnostic cannot be blamed on another check.
func contextFromYAML(mode, reviewerExtra string) string {
	return `schemaVersion: 1
id: context-from-graph
outputs:
  decision: {type: string}
budget: {max_total_tokens: 200000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  sidecar:
    hero: writer@1
    goal: Produce the sidecar finding
    budget: {tokens: 50000}
    depends: [producer]
  reviewer:
    hero: reader@1
    goal: Review
    depends: [producer]
    context_mode: ` + mode + `
` + reviewerExtra + `  accept:
    terminal: {status: success, output: decision}
    depends: [reviewer]
`
}

// A subscription is a valid declaration when the mode carries results and the
// referenced task cannot run later than the subscriber.
func TestValidationContextFromAccepted(t *testing.T) {
	result := validateAcceptanceYAML(t, contextFromYAML("snapshot", "    context_from: [sidecar, sidecar]\n"))
	assert.True(t, result.Valid, "%v", result.Diagnostics)
	// The list is canonicalized at parse time, so a duplicate never survives and
	// the digest input does not depend on the authoring order.
	def, err := ParseDefinition([]byte(contextFromYAML("snapshot", "    context_from: [ sidecar , sidecar,  ]\n")))
	require.NoError(t, err)
	assert.Equal(t, []string{"sidecar"}, def.Tasks["reviewer"].ContextFrom)
}

// An id that is not a task is refused: the renderer would silently show nothing.
func TestValidationContextFromUnknownTask(t *testing.T) {
	result := validateAcceptanceYAML(t, contextFromYAML("snapshot", "    context_from: [ghost]\n"))
	require.False(t, result.Valid)
	diagnostic := acceptanceDiagnostic(result, "context_from_unknown")
	require.NotNil(t, diagnostic, "expected context_from_unknown, got %v", result.Diagnostics)
	assert.Equal(t, "tasks.reviewer.contextFrom", diagnostic.Field)
	assert.Contains(t, diagnostic.Message, `"ghost"`)
}

// Without snapshot the field would silently carry nothing, so publish refuses it
// instead of accepting a no-op. The default (omitted) mode is reported as what it
// is: isolated.
func TestValidationContextFromRequiresSnapshot(t *testing.T) {
	for _, mode := range []string{"isolated", ""} {
		def, err := ParseDefinition([]byte(contextFromYAML("isolated", "    context_from: [sidecar]\n")))
		require.NoError(t, err)
		reviewer := def.Tasks["reviewer"]
		reviewer.ContextMode = mode
		def.Tasks["reviewer"] = reviewer
		result := validator().Validate(context.Background(), def)
		require.False(t, result.Valid)
		diagnostic := acceptanceDiagnostic(result, "context_from_requires_snapshot")
		require.NotNil(t, diagnostic, "mode %q: expected context_from_requires_snapshot, got %v", mode, result.Diagnostics)
		assert.Equal(t, "tasks.reviewer.contextFrom", diagnostic.Field)
		assert.Contains(t, diagnostic.Message, "isolated")
		assert.Contains(t, diagnostic.Message, "snapshot")
	}
}

// The whole point of the field is a task that is NOT a prerequisite, so a
// reference that runs later (reachable FROM this task through depends) is
// refused: its result cannot exist yet. Self-reference is the smallest case.
func TestValidationContextFromDownstream(t *testing.T) {
	yaml := `schemaVersion: 1
id: context-from-downstream
outputs:
  decision: {type: string}
budget: {max_total_tokens: 200000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
    context_mode: snapshot
    context_from: [reviewer]
  reviewer:
    hero: reader@1
    goal: Review
    depends: [producer]
  accept:
    terminal: {status: success, output: decision}
    depends: [reviewer]
`
	result := validateAcceptanceYAML(t, yaml)
	require.False(t, result.Valid)
	diagnostic := acceptanceDiagnostic(result, "context_from_downstream")
	require.NotNil(t, diagnostic, "expected context_from_downstream, got %v", result.Diagnostics)
	assert.Equal(t, "tasks.producer.contextFrom", diagnostic.Field)
	assert.Contains(t, diagnostic.Message, `"reviewer"`)
	assert.Contains(t, diagnostic.Message, "runs later")
}

// A check, terminal or fan_out task has no child assignment at all, so
// contextFrom on it is the SAME refusal as snapshot on it - one rule, one code.
func TestValidationContextFromRefusedWithoutAChildAssignment(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		field string
	}{
		{
			name: "check task",
			yaml: `schemaVersion: 1
id: context-from-check
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
    context_from: [producer]
`,
			field: "tasks.gate.contextFrom",
		},
		{
			name: "terminal task",
			yaml: `schemaVersion: 1
id: context-from-terminal
budget: {max_total_tokens: 120000}
tasks:
  producer:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
  accept:
    terminal: {status: success}
    depends: [producer]
    context_from: [producer]
`,
			field: "tasks.accept.contextFrom",
		},
		{
			name: "fan_out task",
			yaml: `schemaVersion: 1
id: context-from-fan-out
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
    context_from: [producer]
`,
			field: "tasks.scan.contextFrom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := validateAcceptanceYAML(t, tc.yaml)
			require.False(t, result.Valid)
			diagnostic := acceptanceDiagnostic(result, "context_mode_unsupported")
			require.NotNil(t, diagnostic, "expected context_mode_unsupported, got %v", result.Diagnostics)
			assert.Equal(t, tc.field, diagnostic.Field)
			assert.False(t, hasCode(result, "context_from_requires_snapshot"),
				"one refusal, not two: %v", result.Diagnostics)
		})
	}
}

// parseCanonicalizesContextFrom pins the parse-time normalization: trim, sort,
// dedupe, exactly what `depends` gets.
func TestParseCanonicalizesContextFrom(t *testing.T) {
	def, err := ParseDefinition([]byte(contextFromYAML("snapshot",
		"    context_from: [\" sidecar \", sidecar, alpha]\n")))
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "sidecar"}, def.Tasks["reviewer"].ContextFrom,
		"trim, dedupe and sort so the digest does not depend on authoring order")
	// An absent list stays absent: no key in the JSON the digest commits.
	encoded, err := json.Marshal(def.Tasks["producer"])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "contextFrom")
}

// subscriptionDef is a graph where "reader" depends on "zeta" and subscribes to
// "alpha". If a subscription were an edge, "alpha" would become a prerequisite of
// the reader and both the task order and the ready set would change.
func subscriptionDef(contextFrom ...string) *domain.FlowDefinition {
	return &domain.FlowDefinition{
		SchemaVersion: 1, ID: "context-from-order",
		Outputs: map[string]domain.FlowPort{"report": {Type: domain.PortTypeString}},
		Budget:  domain.FlowBudget{MaxTotalTokens: 200000},
		Tasks: map[string]domain.FlowTask{
			"reader": {Type: domain.FlowTaskHero, Hero: "reader@1", Goal: "Review",
				Depends: []string{"zeta"}, ContextFrom: contextFrom,
				ContextMode: domain.FlowTaskContextSnapshot},
			"zeta":  {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "Produce zeta"},
			"alpha": {Type: domain.FlowTaskHero, Hero: "writer@1", Goal: "Produce alpha"},
			"done": {Type: domain.FlowTaskHero, Depends: []string{"reader"},
				Terminal: &domain.FlowTerminal{Status: "success", Output: "report"}},
		},
	}
}

// A subscription is not an edge: it changes neither the task order nor the ready
// set, so declaring a sibling's finding can never reorder dispatch.
func TestContextFromDoesNotChangeTaskOrderOrReadySet(t *testing.T) {
	without := subscriptionDef()
	with := subscriptionDef("alpha")
	orderWithout, err := TopologicalOrder(without.Tasks)
	require.NoError(t, err)
	orderWith, err := TopologicalOrder(with.Tasks)
	require.NoError(t, err)
	assert.Equal(t, orderWithout, orderWith, "contextFrom must not affect TaskOrder")

	// The ready set after "zeta" completes: the reader is dispatchable even
	// though its contextFrom source has no checkpoint at all.
	fake, err := newFakeStore(with)
	require.NoError(t, err)
	nodes, err := fake.ListNodes(context.Background(), "flow-run-1")
	require.NoError(t, err)
	completed := map[string]*domain.RunAgentFlowNode{}
	for _, node := range nodes {
		if node.Handle == "zeta" {
			node.TerminalState = domain.FlowNodeCompleted
			completed[node.Handle] = node
		}
	}
	ready := readyHeroTasks(nodes, completed, with)
	var handles []string
	for _, node := range ready {
		handles = append(handles, node.Handle)
	}
	assert.Contains(t, handles, "reader",
		"a contextFrom source must never gate readiness; ready=%v", handles)
	assert.NotContains(t, handles, "done", "the terminal gate is never dispatched")

	// Same ready set with and without the subscription: nothing moved.
	readyWithout := readyHeroTasks(nodes, completed, without)
	var handlesWithout []string
	for _, node := range readyWithout {
		handlesWithout = append(handlesWithout, node.Handle)
	}
	assert.Equal(t, handlesWithout, handles)
}

// A contextFrom reference does not create an entry task either: the topology
// checks still see two tasks with no depends.
func TestContextFromIsNotAnEntryEdge(t *testing.T) {
	yaml := `schemaVersion: 1
id: context-from-entry
outputs: {decision: {type: string}}
budget: {max_total_tokens: 200000}
tasks:
  first:
    hero: writer@1
    goal: Do the work
    budget: {tokens: 50000}
    context_mode: snapshot
    context_from: [second]
  second:
    hero: writer@1
    goal: Other work
    budget: {tokens: 50000}
  accept:
    terminal: {status: success, output: decision}
    depends: [first, second]
`
	result := validateAcceptanceYAML(t, yaml)
	require.False(t, result.Valid)
	diagnostic := acceptanceDiagnostic(result, "entry_task_count")
	require.NotNil(t, diagnostic, "expected entry_task_count, got %v", result.Diagnostics)
	assert.Contains(t, diagnostic.Message, "second")
	assert.False(t, hasCode(result, "context_from_downstream"),
		"neither task is downstream of the other: %v", result.Diagnostics)
}

// The snapshot is the union of depends and contextFrom, in the definition's
// canonical task order, deduplicated: a task named in both renders once, and a
// source with no recorded output renders nothing for itself.
func TestSnapshotUnionsDependsAndContextFrom(t *testing.T) {
	task := domain.FlowTask{Depends: []string{"zeta"}, ContextFrom: []string{"alpha", "zeta", "later"},
		ContextMode: domain.FlowTaskContextSnapshot}
	outputs := map[string]json.RawMessage{
		"alpha": json.RawMessage(`{"finding":"alpha"}`),
		"zeta":  json.RawMessage(`{"finding":"zeta"}`),
		// "later" has no recorded output yet: a subscription is not a
		// prerequisite, so the reviewer runs without it.
	}
	assignment := renderTaskAssignment("Review", nil, task, outputs)

	assert.Equal(t, 1, strings.Count(assignment, "--- upstream task zeta output_ref ---"),
		"a task named in both depends and contextFrom renders once: %s", assignment)
	assert.Equal(t, 1, strings.Count(assignment, "--- upstream task alpha output_ref ---"))
	assert.NotContains(t, assignment, "upstream task later output_ref",
		"a source without a recorded payload renders nothing for itself")
	alphaIndex := strings.Index(assignment, "--- upstream task alpha output_ref ---")
	zetaIndex := strings.Index(assignment, "--- upstream task zeta output_ref ---")
	assert.Less(t, alphaIndex, zetaIndex, "canonical task order: alpha before zeta")

	// Isolated keeps rendering nothing, contextFrom or not.
	isolated := domain.FlowTask{Depends: []string{"zeta"}, ContextFrom: []string{"alpha"}}
	assert.Equal(t, "Review", renderTaskAssignment("Review", nil, isolated, outputs))
}

// depends and contextFrom share one budget: the union does not get a second
// 2 KiB per task / 8 KiB total allowance.
func TestSnapshotContextFromSharesTheBound(t *testing.T) {
	task := domain.FlowTask{Depends: []string{"p1", "p2"}, ContextFrom: []string{"p3", "p4", "p5"},
		ContextMode: domain.FlowTaskContextSnapshot}
	outputs := map[string]json.RawMessage{}
	for _, name := range []string{"p1", "p2", "p3", "p4", "p5"} {
		outputs[name] = bigPayload(t, 3000)
	}
	assignment := renderTaskAssignment("Review", nil, task, outputs)

	sections := upstreamSections(assignment)
	require.Len(t, sections, 4, "four 2 KiB contributions fit across both lists; the fifth has no budget left")
	for _, section := range sections {
		assert.Len(t, section, contextSnapshotPerTaskBytes)
	}
	assert.Equal(t, contextSnapshotTotalBytes, len(strings.Join(sections, "")),
		"the whole section is still capped at 8 KiB of upstream data")
	assert.Equal(t, 4, strings.Count(assignment, "[upstream result truncated"))
	assert.NotContains(t, assignment, "upstream task p5 output_ref")
}
