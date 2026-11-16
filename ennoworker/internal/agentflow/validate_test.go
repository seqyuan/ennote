package agentflow

import (
	"context"
	"fmt"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubResolver resolves a fixed hero and skill/tool catalog.
type stubResolver struct {
	heroDefs map[string]domain.HeroDefinition
	skills   map[string]bool
	readOnly map[string]bool
}

func (s *stubResolver) ResolveHero(ctx context.Context, heroRef string) (*HeroInfo, error) {
	def, ok := s.heroDefs[heroRef]
	if !ok {
		return nil, fmt.Errorf("hero %q is not published", heroRef)
	}
	return &HeroInfo{Definition: def}, nil
}

func (s *stubResolver) KnownSkill(ctx context.Context, name string) bool { return s.skills[name] }
func (s *stubResolver) ToolReadOnly(ctx context.Context, tool string) bool {
	return s.readOnly[tool]
}

func newStubResolver() *stubResolver {
	return &stubResolver{
		heroDefs: map[string]domain.HeroDefinition{
			"writer@1": {
				Authority: domain.HeroAuthorityMutation, PermissionCeiling: domain.PermissionAuto,
				AllowedTools: []string{"read", "write", "exec"},
				ContextPolicy: domain.HeroContextPolicy{
					DefaultMode: domain.HeroContextTask, AllowedModes: []domain.HeroContextMode{domain.HeroContextTask},
				},
				DelegationPolicy: domain.HeroDelegationPolicy{
					Admission:          domain.DelegationAutoWithinBudget,
					AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
					MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
					BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
						MaxTotalTokens: 60000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000},
				},
			},
			"reader@1": {
				Authority: domain.HeroAuthorityReadOnly, PermissionCeiling: domain.PermissionDiscuss,
				AllowedTools: []string{"read", "grep", "find"},
				ContextPolicy: domain.HeroContextPolicy{
					DefaultMode: domain.HeroContextTask, AllowedModes: []domain.HeroContextMode{domain.HeroContextTask},
				},
				DelegationPolicy: domain.HeroDelegationPolicy{
					Admission:          domain.DelegationAutoWithinBudget,
					AllowedCallerKinds: []string{"host"}, AllowedStrategies: []string{"single"},
					MaxInvocationsPerParentRun: 16, MaxConcurrentInstances: 16,
					BudgetCeiling: domain.DelegationBudgetCeiling{MaxModelCalls: 4, MaxToolCalls: 8,
						MaxTotalTokens: 60000, MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000},
				},
			},
		},
		skills:   map[string]bool{"go-dev": true},
		readOnly: map[string]bool{"read": true, "grep": true, "find": true, "ls": true},
	}
}

func validDefYAML() string {
	return `schemaVersion: 1
id: maker-checker
inputs:
  target: {type: path, required: true}
outputs:
  decision: {type: string}
budget:
  max_total_tokens: 120000
tasks:
  producer:
    hero: writer@1
    goal: "Implement {inputs.target}"
    budget: {tokens: 50000}
  reviewer:
    hero: reader@1
    goal: "Review {task.producer.output.changed_files}"
    depends: [producer]
  decision:
    type: check
    command: "go test ./..."
    depends: [reviewer]
  accept:
    terminal: {status: success, output: decision}
    output: decision
    depends: [decision]
`
}

func validator() *Validator {
	return &Validator{
		Resolver:       newStubResolver(),
		CheckAllowlist: []string{"go", "python3", "sh", "node", "git", "make"},
		MaxFanOut:      64,
		MaxRounds:      100,
	}
}

func parseValid(t *testing.T) *domain.FlowDefinition {
	t.Helper()
	def, err := ParseDefinition([]byte(validDefYAML()))
	require.NoError(t, err)
	return def
}

func validateDef(t *testing.T, yaml string) *ValidationResult {
	t.Helper()
	def, err := ParseDefinition([]byte(yaml))
	require.NoError(t, err, "parse should succeed so validation is exercised")
	return validator().Validate(context.Background(), def)
}

func TestValidationAllChecksPass(t *testing.T) {
	def := parseValid(t)
	result := validator().Validate(context.Background(), def)
	assert.True(t, result.Valid, "%v", result.Diagnostics)
	assert.NotEmpty(t, result.ConfigDigest)
	assert.Len(t, result.Diagnostics, 0)
}

// Matrix 1: entry task count.
func TestValidationEntryTask(t *testing.T) {
	// Two entries.
	result := validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a"}
  b: {hero: reader@1, goal: "b"}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "entry_task_count"))

	// Zero entries (cycle-free but two tasks depending on each other is a
	// cycle; zero entries means a cycle or empty depends chain).
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a", depends: [b]}
  b: {hero: reader@1, goal: "b", depends: [a]}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "dag_cycle"))

	// Regression: a legal single-entry flow with two downstream tasks must
	// pass (the entry is the task with NO depends, not the sink).
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  b: {hero: reader@1, goal: "b", depends: [a], budget: {tokens: 100}}
  c: {hero: reader@1, goal: "c", depends: [a], budget: {tokens: 100}}
`)
	assert.True(t, result.Valid, "%v", result.Diagnostics)

	// Regression: two entries merging into one sink must be rejected (only
	// a alone is not the unique entry).
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  b: {hero: writer@1, goal: "b", budget: {tokens: 100}}
  c: {hero: reader@1, goal: "c", depends: [a, b], budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "entry_task_count"))
}

// Matrix 2: goal variable scope.
func TestValidationGoalVariables(t *testing.T) {
	// {prev.*} forbidden.
	result := validateDef(t, `schemaVersion: 1
id: x
inputs: {target: {type: string}}
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "use {prev.output}", budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "goal_reference_forbidden"))

	// Reference to a non-depends task forbidden.
	result = validateDef(t, `schemaVersion: 1
id: x
inputs: {target: {type: string}}
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  b: {hero: reader@1, goal: "use {task.a.output.x}", depends: [a]}
  c: {hero: reader@1, goal: "use {task.b.output.x}", depends: [a]}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "goal_task_out_of_scope"))

	// Unknown input reference forbidden.
	result = validateDef(t, `schemaVersion: 1
id: x
inputs: {target: {type: string}}
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "use {inputs.nope}", budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "goal_input_unknown"))

	// In-scope references pass (inputs + depends task output + flow.vars).
	result = validateDef(t, `schemaVersion: 1
id: x
inputs: {target: {type: string}}
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "use {inputs.target} and {flow.vars.mode}", budget: {tokens: 100}}
  b: {hero: reader@1, goal: "use {task.a.output.changed_files} and {flow.vars.mode}", depends: [a]}
`)
	assert.True(t, result.Valid, "%v", result.Diagnostics)
}

// Matrix 3: convergence.
func TestValidationConvergence(t *testing.T) {
	// Convergence from/to must form an actual loop: c cannot reach a, so
	// declaring {from: a, to: c} is not a back-edge.
	result := validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  b: {hero: reader@1, goal: "b", depends: [a], budget: {tokens: 100}}
  c: {hero: reader@1, goal: "c", depends: [b], budget: {tokens: 100}}
convergence:
  - {from: a, to: c, max_rounds: 4}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "convergence_not_a_loop"))

	// A real back-edge: c -> a closes the loop a->b->c->a.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  b: {hero: reader@1, goal: "b", depends: [a], budget: {tokens: 100}}
  c: {hero: reader@1, goal: "c", depends: [b], budget: {tokens: 100}}
convergence:
  - {from: c, to: a, max_rounds: 4}
`)
	assert.True(t, result.Valid, "%v", result.Diagnostics)

	// maxRounds missing/unbounded rejected.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  b: {hero: reader@1, goal: "b", depends: [a], budget: {tokens: 100}}
  c: {hero: reader@1, goal: "c", depends: [b], budget: {tokens: 100}}
convergence:
  - {from: c, to: a, max_rounds: 0}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "convergence_rounds_invalid"))

	// A cycle NOT covered by a convergence declaration is rejected.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 20000}
tasks:
  a: {hero: writer@1, goal: "a", depends: [b]}
  b: {hero: reader@1, goal: "b", depends: [a]}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "dag_cycle"))
}

// Matrix 4: fan_out read-only.
func TestValidationFanOutReadOnly(t *testing.T) {
	// writer@1 has mutation tools -> fan_out rejected.
	result := validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: reader@1, goal: "a", budget: {tokens: 100}}
  fan:
    hero: writer@1
    goal: "f"
    depends: [a]
    fan_out: {min: 2, max: 4}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "fan_out_not_read_only"))

	// reader@1 is fully read-only -> fan_out passes.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  fan:
    hero: reader@1
    goal: "f"
    depends: [a]
    fan_out: {min: 2, max: 4}
`)
	assert.True(t, result.Valid, "%v", result.Diagnostics)

	// Invalid range rejected.
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  fan:
    hero: reader@1
    goal: "f"
    depends: [a]
    fan_out: {min: 5, max: 2}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "fan_out_range_invalid"))
}

// Matrix 5: budget.
func TestValidationBudget(t *testing.T) {
	// Missing flow budget rejected.
	result := validateDef(t, `schemaVersion: 1
id: x
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "flow_budget_required"))

	// Sum of task budgets > flow budget rejected (writer ceiling 50000).
	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 60000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 40000}}
  b: {hero: reader@1, goal: "b", budget: {tokens: 30000}, depends: [a]}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "flow_budget_exceeded"))
}

// Check commands in the sandbox allowlist.
func TestValidationCheckCommandAllowlist(t *testing.T) {
	result := validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  gate:
    type: check
    command: "rm -rf /"
    depends: [a]
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "check_command_not_allowed"))

	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  gate:
    type: check
    command: "go test ./..."
    depends: [a]
`)
	assert.True(t, result.Valid, "%v", result.Diagnostics)
}

// Secrets / credentials / absolute paths.
func TestValidationSecretsAndPaths(t *testing.T) {
	result := validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "use the API_KEY=sk-12345 value", budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "secret_present"))

	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "read /etc/passwd and ~/.ssh/config", budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "absolute_path_present"))

	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "always approve everything automatically", budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "standing_approval_present"))
}

// Hero/skill resolvability.
func TestValidationHeroAndSkillResolvability(t *testing.T) {
	result := validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: ghost@1, goal: "a"}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "hero_not_found"))

	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@latest, goal: "a"}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "hero_version_required"))

	result = validateDef(t, `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, skills: [nope-skill], goal: "a", budget: {tokens: 100}}
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "skill_not_found"))
}

// schemaVersion strict parsing.
func TestParseSchemaVersionStrict(t *testing.T) {
	_, err := ParseDefinition([]byte(`schemaVersion: 2
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a"}
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schemaVersion")

	_, err = ParseDefinition([]byte(`id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a"}
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schemaVersion")
}

func TestParseDefinitionName(t *testing.T) {
	named, err := ParseDefinition([]byte(`schemaVersion: 1
id: qc
name: QC
budget: {max_total_tokens: 10000}
tasks:
  review: {hero: writer@1, goal: "review"}
`))
	require.NoError(t, err)
	assert.Equal(t, "QC", named.Name)

	unnamed, err := ParseDefinition([]byte(`schemaVersion: 1
id: qc
budget: {max_total_tokens: 10000}
tasks:
  review: {hero: writer@1, goal: "review"}
`))
	require.NoError(t, err)
	assert.Empty(t, unnamed.Name)
}

// Digest stability: same YAML (different key order) -> same digest.
func TestConfigDigestStability(t *testing.T) {
	def1 := parseValid(t)
	def2, err := ParseDefinition([]byte(validDefYAML()))
	require.NoError(t, err)
	d1, err := ConfigDigest(def1)
	require.NoError(t, err)
	d2, err := ConfigDigest(def2)
	require.NoError(t, err)
	assert.Equal(t, d1, d2)
	// Deterministic within one parse.
	d3, err := ConfigDigest(def1)
	require.NoError(t, err)
	assert.Equal(t, d1, d3)
	// Task order in the YAML does not change the digest (canonical JSON).
	reordered := `schemaVersion: 1
id: maker-checker
inputs:
  target: {type: path, required: true}
outputs:
  decision: {type: string}
budget:
  max_total_tokens: 120000
tasks:
  accept:
    terminal: {status: success, output: decision}
    output: decision
    depends: [decision]
  producer:
    hero: writer@1
    goal: "Implement {inputs.target}"
    budget: {tokens: 50000}
  decision:
    type: check
    command: "go test ./..."
    depends: [reviewer]
  reviewer:
    hero: reader@1
    goal: "Review {task.producer.output.changed_files}"
    depends: [producer]
`
	def3, err := ParseDefinition([]byte(reordered))
	require.NoError(t, err)
	d4, err := ConfigDigest(def3)
	require.NoError(t, err)
	assert.Equal(t, d1, d4)
}

// Terminal exclusivity: a flow may declare any number of terminal tasks, but
// every pair must be mutually exclusive — at least one check gate has to demand
// pass for one and fail for the other. The terminal gate resolves the flow
// outputs from the one unlocked terminal, so a non-exclusive pair would make the
// published outputs ambiguous.
func TestValidationTerminalExclusivity(t *testing.T) {
	const head = `schemaVersion: 1
id: x
budget: {max_total_tokens: 10000}
tasks:
  a: {hero: writer@1, goal: "a"}
`

	// Two terminals fed by the same completed producer, with no check gate
	// between them: both can be unlocked at once, so publish rejects them and
	// names both handles.
	result := validateDef(t, head+`  t1: {terminal: {status: success}, depends: [a]}
  t2: {terminal: {status: success}, depends: [a]}
`)
	require.False(t, result.Valid)
	terminalDiagnostics := diagnosticsWithCode(result, "terminal_not_exclusive")
	require.Len(t, terminalDiagnostics, 1)
	assert.Equal(t, "tasks", terminalDiagnostics[0].Field)
	assert.Contains(t, terminalDiagnostics[0].Message, "t1")
	assert.Contains(t, terminalDiagnostics[0].Message, "t2")

	// One terminal stays valid.
	result = validateDef(t, head+`  t1: {terminal: {status: success}, depends: [a]}
`)
	assert.True(t, result.Valid, "%v", result.Diagnostics)
	assert.False(t, hasCode(result, "terminal_not_exclusive"))

	// Zero terminals stays valid.
	result = validateDef(t, head)
	assert.True(t, result.Valid, "%v", result.Diagnostics)
	assert.False(t, hasCode(result, "terminal_not_exclusive"))

	// Two terminals separated by a check gate are mutually exclusive: the
	// unselected branch stays pending, so the two can never both be unlocked.
	result = validateDef(t, head+`  g:
    type: check
    command: "go test ./..."
    depends: [a]
    next: {pass: p, fail: f}
  p: {hero: reader@1, goal: "p", depends: [g], budget: {tokens: 100}}
  f: {hero: reader@1, goal: "f", depends: [g], budget: {tokens: 100}}
  tp: {terminal: {status: success}, depends: [p]}
  tf: {terminal: {status: success}, depends: [f]}
`)
	assert.True(t, result.Valid, "%v", result.Diagnostics)
	assert.False(t, hasCode(result, "terminal_not_exclusive"))

	// Three terminals: tp/tf are exclusive through gate g, but t3 hangs off the
	// same producer as tp, so the pairs involving t3 are not exclusive.
	result = validateDef(t, head+`  g:
    type: check
    command: "go test ./..."
    depends: [a]
    next: {pass: p, fail: f}
  p: {hero: reader@1, goal: "p", depends: [g], budget: {tokens: 100}}
  f: {hero: reader@1, goal: "f", depends: [g], budget: {tokens: 100}}
  tp: {terminal: {status: success}, depends: [p]}
  tf: {terminal: {status: success}, depends: [f]}
  t3: {terminal: {status: success}, depends: [a]}
`)
	require.False(t, result.Valid)
	terminalDiagnostics = diagnosticsWithCode(result, "terminal_not_exclusive")
	require.Len(t, terminalDiagnostics, 1)
	assert.Equal(t, "tasks", terminalDiagnostics[0].Field)
	assert.Contains(t, terminalDiagnostics[0].Message, "t3")
	assert.Contains(t, terminalDiagnostics[0].Message, "tp")
	assert.Contains(t, terminalDiagnostics[0].Message, "tf")
}

// Hero admissibility: a Hero that can only fail when a Run starts must fail at
// publish time instead of publishing and then failing at Run start.
func TestValidationHeroAdmissibility(t *testing.T) {
	// Fully admissible Hero: no diagnostic at all. Every condition the Graph
	// Run freeze enforces must hold: auto_within_budget admission, task_only
	// context, host caller, single lane, usable ceilings and a task budget
	// within the ceiling.
	result := validateWithHero(t, admissibleHeroDefinition())
	assert.True(t, result.Valid, "%v", result.Diagnostics)
	assert.Empty(t, result.Diagnostics)
	assert.False(t, hasCode(result, "hero_admission_incompatible"))
	assert.False(t, hasCode(result, "hero_context_incompatible"))

	// Graph Runs only admit auto_within_budget Heroes.
	approvalHero := admissibleHeroDefinition()
	approvalHero.DelegationPolicy.Admission = domain.DelegationApprovalRequired
	result = validateWithHero(t, approvalHero)
	require.False(t, result.Valid)
	admissionDiagnostics := diagnosticsWithCode(result, "hero_admission_incompatible")
	require.Len(t, admissionDiagnostics, 1)
	assert.Equal(t, "tasks.a.hero", admissionDiagnostics[0].Field)
	assert.Contains(t, admissionDiagnostics[0].Message, "approval_required")
	assert.False(t, hasCode(result, "hero_context_incompatible"))

	// A child Run loads its config with task_only context.
	roomHero := admissibleHeroDefinition()
	roomHero.ContextPolicy.AllowedModes = []domain.HeroContextMode{domain.HeroContextRoom}
	result = validateWithHero(t, roomHero)
	require.False(t, result.Valid)
	contextDiagnostics := diagnosticsWithCode(result, "hero_context_incompatible")
	require.Len(t, contextDiagnostics, 1)
	assert.Equal(t, "tasks.a.hero", contextDiagnostics[0].Field)
	assert.Contains(t, contextDiagnostics[0].Message, "task_only")
	assert.False(t, hasCode(result, "hero_admission_incompatible"))
}

// Graph admission rules the Run freeze enforces on every task child: the same
// conditions must fail publish, otherwise a Graph publishes and then cannot
// start.
func TestValidationHeroAdmissionRules(t *testing.T) {
	t.Run("caller_kind", func(t *testing.T) {
		result := validateWithHero(t, admissibleHeroDefinition())
		assert.True(t, result.Valid, "%v", result.Diagnostics)
		assert.False(t, hasCode(result, "hero_caller_kind_incompatible"), "%v", result.Diagnostics)

		heroDef := admissibleHeroDefinition()
		heroDef.DelegationPolicy.AllowedCallerKinds = []string{"parent_hero"}
		result = validateWithHero(t, heroDef)
		require.False(t, result.Valid)
		diagnostics := diagnosticsWithCode(result, "hero_caller_kind_incompatible")
		require.Len(t, diagnostics, 1)
		assert.Equal(t, "tasks.a.hero", diagnostics[0].Field)
		assert.Contains(t, diagnostics[0].Message, "allowedCallerKinds")
		assert.Contains(t, diagnostics[0].Message, "host")
		assert.Contains(t, diagnostics[0].Message, "parent_hero")
	})

	t.Run("strategy", func(t *testing.T) {
		result := validateWithHero(t, admissibleHeroDefinition())
		assert.True(t, result.Valid, "%v", result.Diagnostics)
		assert.False(t, hasCode(result, "hero_strategy_incompatible"), "%v", result.Diagnostics)

		heroDef := admissibleHeroDefinition()
		heroDef.DelegationPolicy.AllowedStrategies = []string{"parallel"}
		result = validateWithHero(t, heroDef)
		require.False(t, result.Valid)
		diagnostics := diagnosticsWithCode(result, "hero_strategy_incompatible")
		require.Len(t, diagnostics, 1)
		assert.Equal(t, "tasks.a.hero", diagnostics[0].Field)
		assert.Contains(t, diagnostics[0].Message, "allowedStrategies")
		assert.Contains(t, diagnostics[0].Message, "single")
		assert.Contains(t, diagnostics[0].Message, "parallel")
	})

	t.Run("ceiling", func(t *testing.T) {
		result := validateWithHero(t, admissibleHeroDefinition())
		assert.True(t, result.Valid, "%v", result.Diagnostics)
		assert.False(t, hasCode(result, "hero_ceiling_invalid"), "%v", result.Diagnostics)

		// The delegation request is built from the ceiling, so a zero tool call
		// ceiling publishes today and then fails child Run creation.
		heroDef := admissibleHeroDefinition()
		heroDef.DelegationPolicy.BudgetCeiling.MaxToolCalls = 0
		result = validateWithHero(t, heroDef)
		require.False(t, result.Valid)
		diagnostics := diagnosticsWithCode(result, "hero_ceiling_invalid")
		require.Len(t, diagnostics, 1)
		assert.Equal(t, "tasks.a.hero", diagnostics[0].Field)
		assert.Contains(t, diagnostics[0].Message, "maxToolCalls")
		assert.Contains(t, diagnostics[0].Message, "0")
	})

	t.Run("budget_exceeds_ceiling", func(t *testing.T) {
		result := validateWithHeroTaskBudget(t, admissibleHeroDefinition(), 5000)
		assert.True(t, result.Valid, "%v", result.Diagnostics)
		assert.False(t, hasCode(result, "hero_budget_exceeds_ceiling"), "%v", result.Diagnostics)

		result = validateWithHeroTaskBudget(t, admissibleHeroDefinition(), 9000)
		require.False(t, result.Valid)
		diagnostics := diagnosticsWithCode(result, "hero_budget_exceeds_ceiling")
		require.Len(t, diagnostics, 1)
		// The fix is in the Graph task, not in the Hero file.
		assert.Equal(t, "tasks.a.budget.tokens", diagnostics[0].Field)
		assert.Contains(t, diagnostics[0].Message, "maxTotalTokens")
		assert.Contains(t, diagnostics[0].Message, "5000")
		assert.Contains(t, diagnostics[0].Message, "9000")
	})

	t.Run("limit_minimums", func(t *testing.T) {
		result := validateWithHero(t, admissibleHeroDefinition())
		assert.False(t, hasCode(result, "hero_limit_invalid"), "%v", result.Diagnostics)

		// A pack-installed or hand-edited Graph-local Hero bypasses the Hero
		// editor's minimums, and store child creation rejects every child of a
		// Hero whose limit is below 1.
		for _, limit := range []string{"maxInvocationsPerParentRun", "maxConcurrentInstances"} {
			heroDef := admissibleHeroDefinition()
			if limit == "maxInvocationsPerParentRun" {
				heroDef.DelegationPolicy.MaxInvocationsPerParentRun = 0
			} else {
				heroDef.DelegationPolicy.MaxConcurrentInstances = 0
			}
			result = validateWithHero(t, heroDef)
			require.False(t, result.Valid)
			diagnostics := diagnosticsWithCode(result, "hero_limit_invalid")
			require.Len(t, diagnostics, 1, "%v", result.Diagnostics)
			assert.Equal(t, "tasks.a.hero", diagnostics[0].Field)
			assert.Contains(t, diagnostics[0].Message, limit)
			// The HTTP layer flattens diagnostics and drops Field, so the message
			// has to name the Hero file itself.
			assert.Contains(t, diagnostics[0].Message, "hero@1")
		}
	})

	t.Run("invocation_total", func(t *testing.T) {
		heroDef := admissibleHeroDefinition()
		heroDef.DelegationPolicy.MaxInvocationsPerParentRun = 1
		// One task fits the UI's default limit of 1.
		result := validateWithHero(t, heroDef)
		assert.False(t, hasCode(result, "hero_invocation_limit_exceeded"), "%v", result.Diagnostics)

		// Two tasks referencing one Hero consume two invocations of the same
		// bucket, so the second one always fails at Run start.
		result = validateHeroTasks(t, heroDef, `  a:
    hero: hero@1
    goal: "a"
  b:
    hero: hero@1
    goal: "b"
    depends: [a]
`)
		require.False(t, result.Valid)
		diagnostics := diagnosticsWithCode(result, "hero_invocation_limit_exceeded")
		require.Len(t, diagnostics, 2, "%v", result.Diagnostics)
		assert.Equal(t, "tasks.a.hero", diagnostics[0].Field)
		assert.Equal(t, "tasks.b.hero", diagnostics[1].Field)
		assert.Contains(t, diagnostics[0].Message, "2 invocations")
		assert.Contains(t, diagnostics[0].Message, "maxInvocationsPerParentRun is 1")

		// The bucket is the Hero object id up to "@": two pinned versions of
		// one Hero share it, exactly like the store's count.
		result = validateHeroTasks(t, heroDef, `  a:
    hero: hero@1
    goal: "a"
  b:
    hero: hero@2
    goal: "b"
    depends: [a]
`)
		assert.Len(t, diagnosticsWithCode(result, "hero_invocation_limit_exceeded"), 2, "%v", result.Diagnostics)
	})

	t.Run("fan_out_concurrency", func(t *testing.T) {
		heroDef := graphReadOnlyHeroDefinition()
		heroDef.DelegationPolicy.MaxConcurrentInstances = 2
		result := validateWithHeroFanOut(t, heroDef, 2)
		assert.False(t, hasCode(result, "hero_concurrency_limit_exceeded"), "%v", result.Diagnostics)

		// fan_out.min opens that many children at once, so a limit below it
		// always fails.
		heroDef.DelegationPolicy.MaxConcurrentInstances = 1
		result = validateWithHeroFanOut(t, heroDef, 2)
		require.False(t, result.Valid)
		diagnostics := diagnosticsWithCode(result, "hero_concurrency_limit_exceeded")
		require.Len(t, diagnostics, 1, "%v", result.Diagnostics)
		assert.Equal(t, "tasks.a.hero", diagnostics[0].Field)
		assert.Contains(t, diagnostics[0].Message, "fan_out.min 2")
		assert.Contains(t, diagnostics[0].Message, "maxConcurrentInstances is 1")
	})
}

// The gate itself is the shared contract between publish and Run start.
func TestHeroAdmissibilityDiagnosticsAdmissible(t *testing.T) {
	heroDef := admissibleHeroDefinition()
	assert.Empty(t, HeroAdmissibilityDiagnostics(HeroGraphAdmission{
		Hero: &heroDef, HeroRef: "global/checker", TaskBudgetTokens: 5000,
		SameHeroTaskInvocations: 2, FanOutMin: 0, CallerKind: "host", Strategy: "single",
	}))
}

// graphAdmissibleCeiling is a ceiling every field of which clears the delegation
// request minimum the store enforces at child Run creation.
func graphAdmissibleCeiling() domain.DelegationBudgetCeiling {
	return domain.DelegationBudgetCeiling{
		MaxModelCalls: 4, MaxToolCalls: 8, MaxTotalTokens: 5000,
		MaxOutputTokens: 4000, MaxCostUSDMicros: 100000, MaxWallTimeMS: 120000,
	}
}

func admissibleHeroDefinition() domain.HeroDefinition {
	return domain.HeroDefinition{
		Authority: domain.HeroAuthorityMutation, PermissionCeiling: domain.PermissionAuto,
		AllowedTools: []string{"read", "write"},
		ContextPolicy: domain.HeroContextPolicy{
			DefaultMode:  domain.HeroContextTask,
			AllowedModes: []domain.HeroContextMode{domain.HeroContextTask},
		},
		DelegationPolicy: domain.HeroDelegationPolicy{
			Admission:                  domain.DelegationAutoWithinBudget,
			AllowedCallerKinds:         []string{"host"},
			AllowedStrategies:          []string{"single"},
			MaxInvocationsPerParentRun: 16,
			MaxConcurrentInstances:     16,
			BudgetCeiling:              graphAdmissibleCeiling(),
		},
	}
}

// graphReadOnlyHeroDefinition is the fan-out capable admissible Hero: fan_out
// requires a fully read-only Hero.
func graphReadOnlyHeroDefinition() domain.HeroDefinition {
	def := admissibleHeroDefinition()
	def.Authority = domain.HeroAuthorityReadOnly
	def.PermissionCeiling = domain.PermissionDiscuss
	def.AllowedTools = []string{"read"}
	return def
}

func validateWithHero(t *testing.T, heroDef domain.HeroDefinition) *ValidationResult {
	t.Helper()
	return validateHeroTask(t, heroDef, `  a: {hero: hero@1, goal: "a"}`)
}

func validateWithHeroFanOut(t *testing.T, heroDef domain.HeroDefinition, fanOutMin int) *ValidationResult {
	t.Helper()
	return validateHeroTask(t, heroDef, fmt.Sprintf(`  a: {hero: hero@1, goal: "a", fan_out: {min: %d, max: %d}}`, fanOutMin, fanOutMin))
}

func validateWithHeroTaskBudget(t *testing.T, heroDef domain.HeroDefinition, tokens int64) *ValidationResult {
	t.Helper()
	return validateHeroTask(t, heroDef, fmt.Sprintf(`  a: {hero: hero@1, goal: "a", budget: {tokens: %d}}`, tokens))
}

func validateHeroTask(t *testing.T, heroDef domain.HeroDefinition, taskLine string) *ValidationResult {
	t.Helper()
	return validateHeroTasks(t, heroDef, taskLine+"\n")
}

func validateHeroTasks(t *testing.T, heroDef domain.HeroDefinition, tasks string) *ValidationResult {
	t.Helper()
	def, err := ParseDefinition([]byte(`schemaVersion: 1
id: x
budget: {max_total_tokens: 100000}
tasks:
` + tasks))
	require.NoError(t, err)
	resolver := newStubResolver()
	resolver.heroDefs["hero@1"] = heroDef
	resolver.heroDefs["hero@2"] = heroDef
	v := validator()
	v.Resolver = resolver
	return v.Validate(context.Background(), def)
}

func diagnosticsWithCode(result *ValidationResult, code string) []ValidationDiagnostic {
	var found []ValidationDiagnostic
	for _, d := range result.Diagnostics {
		if d.Code == code {
			found = append(found, d)
		}
	}
	return found
}

func hasCode(result *ValidationResult, code string) bool {
	for _, d := range result.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}

// --- Scheduling knobs: publish validation (P2 D6) ---

// schedulingDefYAML is the valid fixture with one task's scheduling declaration
// spliced in. Writing the YAML (rather than mutating the parsed struct) is the
// point: it pins that `retry`, `timeout_ms` and `optional` are real task keys and
// not silently dropped by the parser's allowlist.
func schedulingDefYAML(producerExtra string) string {
	return `schemaVersion: 1
id: maker-checker
inputs:
  target: {type: path, required: true}
outputs:
  decision: {type: string}
budget:
  max_total_tokens: 120000
tasks:
  producer:
    hero: writer@1
    goal: "Implement {inputs.target}"
    budget: {tokens: 50000}
` + producerExtra + `  reviewer:
    hero: reader@1
    goal: "Review {task.producer.output.changed_files}"
    depends: [producer]
  decision:
    type: check
    command: "go test ./..."
    depends: [reviewer]
  accept:
    terminal: {status: success, output: decision}
    output: decision
    depends: [decision]
`
}

// The three knobs are accepted with their documented bounds, and they are part
// of the definition (they survive the parser and the config digest).
func TestValidationSchedulingKnobsAccepted(t *testing.T) {
	result := validateDef(t, schedulingDefYAML("    retry: {max: 2}\n    timeout_ms: 600000\n    optional: true\n"))
	require.True(t, result.Valid, "%v", result.Diagnostics)

	def, err := ParseDefinition([]byte(schedulingDefYAML("    retry: {max: 2}\n    timeout_ms: 600000\n    optional: true\n")))
	require.NoError(t, err)
	producer := def.Tasks["producer"]
	require.NotNil(t, producer.Retry)
	assert.Equal(t, 2, producer.Retry.Max)
	assert.Equal(t, int64(600000), producer.TimeoutMS)
	assert.True(t, producer.Optional)

	// The bounds themselves are legal: 1..5 extra attempts, 1s..24h.
	for _, extra := range []string{"    retry: {max: 1}\n", "    retry: {max: 5}\n",
		"    timeout_ms: 1000\n", "    timeout_ms: 86400000\n"} {
		bounded := validateDef(t, schedulingDefYAML(extra))
		assert.True(t, bounded.Valid, "%q: %v", extra, bounded.Diagnostics)
	}

	// A knob changes the published definition, so it must change the digest: two
	// Graphs that differ only in a retry budget cannot share one immutable version.
	yamlA := schedulingDefYAML("    retry: {max: 1}\n")
	yamlB := schedulingDefYAML("    retry: {max: 2}\n")
	digestA := validateDef(t, yamlA).ConfigDigest
	digestB := validateDef(t, yamlB).ConfigDigest
	require.NotEmpty(t, digestA)
	assert.NotEqual(t, digestA, digestB, "the retry budget is part of the config identity")
}

// retry.max is 1..5: 0 would mean "declare a retry and never use it", and the
// upper bound keeps a wedged task from being re-dispatched without limit.
func TestValidationRetryRange(t *testing.T) {
	for _, value := range []string{"0", "-1", "6"} {
		result := validateDef(t, schedulingDefYAML("    retry: {max: "+value+"}\n"))
		assert.False(t, result.Valid, "retry.max=%s", value)
		assert.True(t, hasCode(result, "retry_invalid"), "retry.max=%s: %v", value, result.Diagnostics)
	}
	// No retry key at all is not an error: the field is optional.
	assert.False(t, hasCode(validateDef(t, schedulingDefYAML("")), "retry_invalid"))
}

// timeout_ms is 1000..86400000: below a second it is a race against the poll
// interval, above a day it is not a decision any more.
func TestValidationTimeoutRange(t *testing.T) {
	for _, value := range []string{"999", "86400001", "-1"} {
		result := validateDef(t, schedulingDefYAML("    timeout_ms: "+value+"\n"))
		assert.False(t, result.Valid, "timeout_ms=%s", value)
		assert.True(t, hasCode(result, "timeout_invalid"), "timeout_ms=%s: %v", value, result.Diagnostics)
	}
	assert.False(t, hasCode(validateDef(t, schedulingDefYAML("")), "timeout_invalid"))
}

// A check, terminal or fan_out task has no single attempt to retry or bound, so
// the knobs are refused instead of ignored: a `timeout_ms` that silently does
// nothing reads as a bounded gate that is not bounded.
func TestValidationSchedulingUnsupportedShapes(t *testing.T) {
	// A check task cannot bound its own execution.
	result := validateDef(t, `schemaVersion: 1
id: check-scheduling
budget: {max_total_tokens: 10000}
tasks:
  producer: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  gate:
    type: check
    command: "go test ./..."
    timeout_ms: 60000
    depends: [producer]
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "scheduling_unsupported"), "%v", result.Diagnostics)

	// A terminal task cannot retry: it dispatches nothing.
	result = validateDef(t, `schemaVersion: 1
id: terminal-scheduling
budget: {max_total_tokens: 10000}
tasks:
  producer: {hero: writer@1, goal: "a", budget: {tokens: 100}}
  accept:
    terminal: {status: success}
    retry: {max: 2}
    depends: [producer]
`)
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "scheduling_unsupported"), "%v", result.Diagnostics)

	// A fan_out parent has one attempt per instance, not one attempt: the same
	// definition is valid without a knob and invalid with one.
	fanOutYAML := func(extra string) string {
		return `schemaVersion: 1
id: fanout-scheduling
budget: {max_total_tokens: 10000}
tasks:
  producer: {hero: reader@1, goal: "a", budget: {tokens: 100}, fan_out: {min: 2, max: 2}` + extra + `}
  accept: {terminal: {status: success}, depends: [producer]}
`
	}
	plain := validateDef(t, fanOutYAML(""))
	require.True(t, plain.Valid, "%v", plain.Diagnostics)
	for _, extra := range []string{", retry: {max: 2}", ", timeout_ms: 60000"} {
		withKnob := validateDef(t, fanOutYAML(extra))
		assert.False(t, withKnob.Valid, extra)
		assert.True(t, hasCode(withKnob, "scheduling_unsupported"), "%s: %v", extra, withKnob.Diagnostics)
	}
}

// A misspelled knob must fail publish instead of silently disabling the limit —
// the same reason every task key has a diagnostic.
func TestValidationSchedulingTypoIsRejected(t *testing.T) {
	result := validateDef(t, schedulingDefYAML("    retries: {max: 2}\n"))
	assert.False(t, result.Valid)
	assert.True(t, hasCode(result, "flow_task_unknown_field"), "%v", result.Diagnostics)
}

// The frozen definition round-trips: a Run freezes the definition it validated,
// so the scheduling knobs must survive YAML export and re-parse unchanged.
func TestSchedulingKnobsRoundTrip(t *testing.T) {
	def, err := ParseDefinition([]byte(schedulingDefYAML("    retry: {max: 3}\n    timeout_ms: 5000\n    optional: true\n")))
	require.NoError(t, err)
	encoded, err := DefinitionToYAML(def)
	require.NoError(t, err)
	reparsed, err := ParseDefinition(encoded)
	require.NoError(t, err)
	assert.Equal(t, def.Tasks["producer"].Retry, reparsed.Tasks["producer"].Retry)
	assert.Equal(t, def.Tasks["producer"].TimeoutMS, reparsed.Tasks["producer"].TimeoutMS)
	assert.True(t, reparsed.Tasks["producer"].Optional)
	assert.NotContains(t, string(encoded), "timeoutMS", "the YAML contract keeps the snake_case key")
}
