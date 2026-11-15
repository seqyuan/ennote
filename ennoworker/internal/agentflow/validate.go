package agentflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// ValidationDiagnostic is one publish-time failure.
type ValidationDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// ValidationResult is the fail-loud publish verdict.
type ValidationResult struct {
	Valid        bool                   `json:"valid"`
	Diagnostics  []ValidationDiagnostic `json:"diagnostics"`
	ConfigDigest string                 `json:"configDigest,omitempty"`
}

// HeroInfo is the publish-time resolution of one hero@version reference.
type HeroInfo struct {
	VersionID  string
	Definition domain.HeroDefinition
}

// Resolver resolves external references at publish time. It is the seam the
// store wires with live SQL/catalog lookups; tests use a stub.
type Resolver interface {
	ResolveHero(ctx context.Context, heroRef string) (*HeroInfo, error)
	KnownSkill(ctx context.Context, name string) bool
	ToolReadOnly(ctx context.Context, tool string) bool
}

// Validator runs the full §5.3 publish validation (9 checks, fail loud).
type Validator struct {
	Resolver Resolver
	// CheckAllowlist is the sandbox executable allowlist for check tasks.
	CheckAllowlist []string
	// CheckAllowlistAllowsAll mirrors a policy mode that already permits every
	// executable (allow_existing_behavior). It is opt-in so an empty allowlist
	// still fails closed by default.
	CheckAllowlistAllowsAll bool
	// MaxFanOut bounds fan_out.max.
	MaxFanOut int
	// MaxRounds bounds convergence max_rounds.
	MaxRounds int
}

func (v *Validator) fail(code, message, field string) ValidationDiagnostic {
	return ValidationDiagnostic{Code: code, Message: message, Field: field}
}

func (v *Validator) allowedCheckExec(command string) bool {
	if v.CheckAllowlistAllowsAll {
		return true
	}
	if v.CheckAllowlist == nil {
		return false
	}
	first := strings.Fields(command)
	if len(first) == 0 {
		return false
	}
	exec := first[0]
	for _, allowed := range v.CheckAllowlist {
		if exec == allowed {
			return true
		}
	}
	return false
}

// Validate runs all checks against a parsed definition. Resolver is required:
// nil fails closed (hero/skill/read-only checks cannot run).
func (v *Validator) Validate(ctx context.Context, def *domain.FlowDefinition) *ValidationResult {
	result := &ValidationResult{}
	add := func(d ValidationDiagnostic) {
		result.Diagnostics = append(result.Diagnostics, d)
	}
	if def == nil {
		result.Diagnostics = append(result.Diagnostics, v.fail("definition_required", "flow definition is required", ""))
		result.Valid = false
		return result
	}
	if v.Resolver == nil {
		result.Diagnostics = append(result.Diagnostics,
			v.fail("resolver_unavailable", "publish validation requires a resolver", ""))
		result.Valid = false
		return result
	}

	// 1. schemaVersion strictly parsed.
	if def.SchemaVersion != domain.FlowSchemaVersion {
		add(v.fail("schema_version_unsupported",
			fmt.Sprintf("flow schemaVersion must be %d", domain.FlowSchemaVersion), "schemaVersion"))
	}

	// 2. Exactly one entry task (in-degree 0 == 1). In-degree counts the
	// task's own depends (prerequisites): the entry is the task with no
	// depends at all.
	indegree := make(map[string]int, len(def.Tasks))
	for name := range def.Tasks {
		indegree[name] = 0
	}
	for name, task := range def.Tasks {
		for _, dep := range task.Depends {
			if _, ok := def.Tasks[dep]; !ok {
				add(v.fail("depends_unknown", fmt.Sprintf("task %q depends on unknown task %q", name, dep), "tasks."+name+".depends"))
				continue
			}
			indegree[name]++
		}
	}
	var entries []string
	for name, degree := range indegree {
		if degree == 0 {
			entries = append(entries, name)
		}
	}
	sort.Strings(entries)
	if len(entries) != 1 {
		message := fmt.Sprintf("exactly one entry task is required (tasks with no depends), found %d", len(entries))
		if len(entries) > 0 {
			message += ": " + strings.Join(entries, ", ")
		}
		add(v.fail("entry_task_count", message, "tasks"))
	}

	// 3. hero@version / skill resolvability; 6. fan_out read-only; 7. budget.
	totalTaskTokens := int64(0)
	checkAllowlistMissing := false
	// writerScopes collects tasks whose Hero can mutate (writer class); used by
	// the allow_disjoint_writers validation.
	writerScopes := map[string][]string{}
	// heroFacts carries every resolved Hero task to the admission gate, which
	// runs after the loop: the delegation limits are counted per Hero object id
	// across the whole flow, and those totals do not exist yet task by task.
	var heroFacts []heroAdmissionFacts
	for name, task := range def.Tasks {
		field := "tasks." + name
		if task.Terminal != nil {
			if task.Terminal.Status != "success" {
				add(v.fail("terminal_status_invalid",
					fmt.Sprintf("task %q terminal status must be \"success\"", name), field+".terminal.status"))
			}
			if task.Terminal.Output != "" {
				if _, ok := def.Outputs[task.Terminal.Output]; !ok {
					add(v.fail("terminal_output_unknown",
						fmt.Sprintf("task %q terminal output %q is not a declared flow output", name, task.Terminal.Output),
						field+".terminal.output"))
				}
			}
			continue
		}
		switch task.Type {
		case domain.FlowTaskCheck:
			if strings.TrimSpace(task.Command) == "" {
				add(v.fail("check_command_required", fmt.Sprintf("task %q check requires a command", name), field+".command"))
				continue
			}
			if !v.allowedCheckExec(task.Command) {
				add(v.fail("check_command_not_allowed",
					fmt.Sprintf("task %q check command is not in the sandbox allowlist", name), field+".command"))
				checkAllowlistMissing = true
			}
		case domain.FlowTaskHero:
			if strings.TrimSpace(task.Hero) == "" {
				add(v.fail("hero_required", fmt.Sprintf("task %q requires a hero", name), field+".hero"))
				continue
			}
			if strings.HasSuffix(strings.TrimSpace(task.Hero), "@latest") {
				add(v.fail("hero_version_required",
					fmt.Sprintf("task %q hero %q must pin an exact version (handle@version, no latest)", name, task.Hero),
					field+".hero"))
				continue
			}
			// A bare handle is a flow-local reference (resolves flow > shared
			// catalog at freeze time); the resolver enforces that the handle
			// actually resolves, failing loud otherwise.
			info, err := v.Resolver.ResolveHero(ctx, task.Hero)
			if err != nil {
				add(v.fail("hero_not_found", fmt.Sprintf("task %q: %v", name, err), field+".hero"))
				continue
			}
			heroDef := info.Definition
			var taskBudgetTokens int64
			if task.Budget != nil && task.Budget.Tokens > 0 {
				taskBudgetTokens = task.Budget.Tokens
			}
			fanOutMin := 0
			if task.FanOut != nil {
				fanOutMin = task.FanOut.Min
			}
			heroFacts = append(heroFacts, heroAdmissionFacts{
				taskName: name, heroRef: task.Hero,
				heroObjectID:     heroObjectID(info.VersionID, task.Hero),
				definition:       heroDef,
				taskBudgetTokens: taskBudgetTokens,
				fanOutMin:        fanOutMin,
			})
			readOnly := heroAllToolsReadOnly(ctx, v.Resolver, heroDef)
			if task.FanOut != nil {
				if task.FanOut.Min < 1 || task.FanOut.Max < task.FanOut.Min ||
					(v.MaxFanOut > 0 && task.FanOut.Max > v.MaxFanOut) {
					add(v.fail("fan_out_range_invalid",
						fmt.Sprintf("task %q fan_out range %d..%d is invalid", name, task.FanOut.Min, task.FanOut.Max),
						field+".fanOut"))
				}
				if !readOnly {
					add(v.fail("fan_out_not_read_only",
						fmt.Sprintf("task %q fan_out requires the Hero allowlist to be fully read-only", name),
						field+".fanOut"))
				}
			}
			// Writer class: any mutation-capable task is a candidate for the
			// disjoint-writes parallel lane; declared scopes are validated here
			// regardless of the flow flag (catches typos early).
			if !readOnly {
				for _, w := range task.Writes {
					if err := ValidateWritesScope(w); err != nil {
						add(v.fail("writes_glob_invalid",
							fmt.Sprintf("task %q writes scope %q: %v", name, w, err),
							field+".writes"))
					}
				}
				writerScopes[name] = task.Writes
			}
			effectiveTokens := heroDef.DelegationPolicy.BudgetCeiling.MaxTotalTokens
			if taskBudgetTokens > 0 {
				effectiveTokens = taskBudgetTokens
				totalTaskTokens += effectiveTokens
			}
			// Tasks without an explicit budget contribute 0 to the publish-time
			// sum: their effective ceiling is the Hero's, which the runtime flow
			// budget still caps unconditionally.
			for _, skill := range task.Skills {
				if !v.Resolver.KnownSkill(ctx, skill) {
					add(v.fail("skill_not_found", fmt.Sprintf("task %q references unknown skill %q", name, skill), field+".skills"))
				}
			}
		default:
			add(v.fail("task_type_invalid", fmt.Sprintf("task %q has unsupported type %q", name, task.Type), field+".type"))
		}
		// 5. goal variable references (per task).
		v.validateGoalReferences(ctx, add, def, name, task)
	}
	_ = checkAllowlistMissing

	// 3b. Hero admission. One gate for every Hero task, run with the flow-level
	// delegation totals: a Graph's child Runs impose a fixed admission shape
	// (auto_within_budget, task_only, host caller, single lane), and the
	// delegation limits are counted per Hero object id across the flow. The same
	// gate runs at Run start (graphrun freeze), so a graph that violates it fails
	// at publish instead of publishing and then failing when the user presses Run.
	validateHeroAdmission(heroFacts, add)

	// 4. DAG acyclicity + convergence back-edge binding.
	validateTopology(add, def)
	validateBranchRouting(add, def)
	// 2b. Multiple terminals are allowed only when every pair is mutually
	// exclusive; otherwise the runtime terminal gate could find two unlocked.
	validateTerminalExclusivity(add, def)

	// Parallelism: ready-set dispatch bounds + the disjoint-writers opt-in gate.
	if def.Parallelism != nil {
		if def.Parallelism.Max < 1 || def.Parallelism.Max > domain.MaxFlowParallelismMax {
			add(v.fail("parallelism_max_invalid",
				fmt.Sprintf("flow parallelism.max must be 1..%d", domain.MaxFlowParallelismMax),
				"parallelism.max"))
		}
		if def.Parallelism.AllowDisjointWriters {
			writerNames := make([]string, 0, len(writerScopes))
			for name := range writerScopes {
				writerNames = append(writerNames, name)
			}
			sort.Strings(writerNames)
			for _, name := range writerNames {
				if len(writerScopes[name]) == 0 {
					add(v.fail("disjoint_writers_unscoped",
						fmt.Sprintf("task %q is a writer but declares no writes scope; allow_disjoint_writers requires every writer to declare a non-empty writes scope", name),
						"tasks."+name+".writes"))
				}
			}
			for i := 0; i < len(writerNames); i++ {
				for j := i + 1; j < len(writerNames); j++ {
					a, b := writerScopes[writerNames[i]], writerScopes[writerNames[j]]
					if !writesDisjoint(a, b) {
						add(v.fail("disjoint_writers_overlap",
							fmt.Sprintf("writer tasks %q and %q declare overlapping writes scopes %v vs %v",
								writerNames[i], writerNames[j], a, b),
							"parallelism.allowDisjointWriters"))
					}
				}
			}
		}
	}

	// 7. flow-level total budget mandatory and >= sum of task budgets.
	if def.Budget.MaxTotalTokens < 1 {
		add(v.fail("flow_budget_required", "flow budget.maxTotalTokens is required and must be positive", "budget.maxTotalTokens"))
	} else if totalTaskTokens > def.Budget.MaxTotalTokens {
		add(v.fail("flow_budget_exceeded",
			fmt.Sprintf("flow budget.maxTotalTokens %d is below the sum of task budgets %d", def.Budget.MaxTotalTokens, totalTaskTokens),
			"budget.maxTotalTokens"))
	}

	// 8. no secret / credential / absolute path / standing approval.
	scanSecrets(add, def)

	// 9. task-node keys the lenient parser dropped. Parsing stays lenient so the
	// read path (editor load, revision load, forward compatibility) keeps working;
	// publish is where a typo must fail instead of silently disabling a clause.
	validateUnknownTaskFields(add, def)

	// 10. acceptance clause syntax and targets.
	validateAcceptance(add, def)

	result.Valid = len(result.Diagnostics) == 0
	if result.Valid {
		digest, err := ConfigDigest(def)
		if err == nil {
			result.ConfigDigest = digest
		}
	}
	return result
}

// Acceptance limits enforced at publish time (see the csv-columns leaf).
const (
	maxAcceptanceColumns    = 64
	maxAcceptanceColumnSize = 128
)

func (v *Validator) validateGoalReferences(ctx context.Context, add func(ValidationDiagnostic), def *domain.FlowDefinition,
	taskName string, task domain.FlowTask) {
	depends := make(map[string]bool)
	for _, dep := range task.Depends {
		depends[dep] = true
	}
	for _, ref := range parseGoalReferences(task.Goal) {
		field := "tasks." + taskName + ".goal"
		switch ref.Kind {
		case refInput:
			if _, ok := def.Inputs[ref.Name]; !ok {
				add(v.fail("goal_input_unknown", fmt.Sprintf("task %q references unknown input %q", taskName, ref.Name), field))
			}
		case refTask:
			if !depends[ref.Task] {
				add(v.fail("goal_task_out_of_scope",
					fmt.Sprintf("task %q may only reference outputs of its depends tasks, not %q", taskName, ref.Task), field))
			}
		case refFlowVars:
			// flow.vars.y is allowed; the value is provided at run time.
		case refForbidden:
			add(v.fail("goal_reference_forbidden", fmt.Sprintf("task %q uses forbidden goal reference {%s}", taskName, ref.Raw), field))
		case refMalformed:
			add(v.fail("goal_reference_malformed", fmt.Sprintf("task %q has malformed goal reference {%s}", taskName, ref.Raw), field))
		}
	}
}

func validateTopology(add func(ValidationDiagnostic), def *domain.FlowDefinition) {
	// Graph without convergence back-edges must be acyclic; every convergence
	// rule must bind an actual back-edge (to must reach from in the DAG).
	convergence := make(map[[2]string]int) // [from,to] -> maxRounds
	for _, rule := range def.Convergence {
		key := [2]string{rule.From, rule.To}
		convergence[key] = rule.MaxRounds
		if rule.MaxRounds < 1 || (maxRoundsLimit > 0 && rule.MaxRounds > maxRoundsLimit) {
			add(ValidationDiagnostic{Code: "convergence_rounds_invalid",
				Message: fmt.Sprintf("convergence %s->%s maxRounds must be between 1 and %d", rule.From, rule.To, maxRoundsLimit),
				Field:   "convergence"})
		}
		if _, ok := def.Tasks[rule.From]; !ok {
			add(ValidationDiagnostic{Code: "convergence_from_unknown",
				Message: fmt.Sprintf("convergence from task %q does not exist", rule.From), Field: "convergence"})
		}
		if _, ok := def.Tasks[rule.To]; !ok {
			add(ValidationDiagnostic{Code: "convergence_to_unknown",
				Message: fmt.Sprintf("convergence to task %q does not exist", rule.To), Field: "convergence"})
		}
	}
	// Reachability and acyclicity check.
	children := make(map[string][]string, len(def.Tasks))
	indegree := make(map[string]int, len(def.Tasks))
	for name := range def.Tasks {
		indegree[name] = 0
	}
	for name, task := range def.Tasks {
		for _, dep := range task.Depends {
			if _, ok := def.Tasks[dep]; !ok {
				continue
			}
			if _, isBackEdge := convergence[[2]string{name, dep}]; isBackEdge {
				continue // declared back-edge: removed for the acyclicity check
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
	processed := 0
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		processed++
		for _, child := range children[name] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	if processed != len(def.Tasks) {
		add(ValidationDiagnostic{Code: "dag_cycle",
			Message: "flow task graph contains a cycle outside the declared convergence back-edges", Field: "tasks"})
	}
	// Every convergence rule must form an actual loop: `to` must reach `from`
	// through depends edges (so the back-edge closes a real cycle).
	reachable := reachabilityMatrix(def.Tasks, convergence)
	for _, rule := range def.Convergence {
		if !reachable[rule.To][rule.From] {
			add(ValidationDiagnostic{Code: "convergence_not_a_loop",
				Message: fmt.Sprintf("convergence %s->%s does not form an actual loop: %s cannot reach %s through depends edges",
					rule.From, rule.To, rule.To, rule.From),
				Field: "convergence"})
		}
	}
}

const maxRoundsLimit = 100

// validateBranchRouting enforces the Phase 2 check-gate branch contract
// (v2 §5.2 next): keys are pass/fail only; targets exist, are downstream of
// the check, and differ; one task is never claimed by two different checks.
func validateBranchRouting(add func(ValidationDiagnostic), def *domain.FlowDefinition) {
	reach := reachabilityMatrix(def.Tasks, map[[2]string]int{})
	claimedBy := make(map[string]string)
	for name, task := range def.Tasks {
		if task.Type != domain.FlowTaskCheck {
			continue
		}
		passTarget := strings.TrimSpace(task.Next["pass"])
		failTarget := strings.TrimSpace(task.Next["fail"])
		if _, ok := task.Next["pass"]; ok {
			if passTarget == "" {
				add(ValidationDiagnostic{Code: "next_target_unknown",
					Message: fmt.Sprintf("check %q next.pass references an unknown task", name), Field: "tasks." + name + ".next"})
			} else if _, exists := def.Tasks[passTarget]; !exists {
				add(ValidationDiagnostic{Code: "next_target_unknown",
					Message: fmt.Sprintf("check %q next.pass references unknown task %q", name, passTarget), Field: "tasks." + name + ".next"})
			} else {
				if !reach[name][passTarget] {
					add(ValidationDiagnostic{Code: "next_target_not_downstream",
						Message: fmt.Sprintf("check %q next.pass target %q is not downstream of the check", name, passTarget),
						Field:   "tasks." + name + ".next"})
				}
				if other, dup := claimedBy[passTarget]; dup && other != name {
					add(ValidationDiagnostic{Code: "next_target_conflict",
						Message: fmt.Sprintf("task %q is claimed by both check %q and check %q", passTarget, other, name),
						Field:   "tasks." + name + ".next"})
				} else {
					claimedBy[passTarget] = name
				}
			}
		}
		if _, ok := task.Next["fail"]; ok {
			if failTarget == "" {
				add(ValidationDiagnostic{Code: "next_target_unknown",
					Message: fmt.Sprintf("check %q next.fail references an unknown task", name), Field: "tasks." + name + ".next"})
			} else if _, exists := def.Tasks[failTarget]; !exists {
				add(ValidationDiagnostic{Code: "next_target_unknown",
					Message: fmt.Sprintf("check %q next.fail references unknown task %q", name, failTarget), Field: "tasks." + name + ".next"})
			} else {
				if !reach[name][failTarget] {
					add(ValidationDiagnostic{Code: "next_target_not_downstream",
						Message: fmt.Sprintf("check %q next.fail target %q is not downstream of the check", name, failTarget),
						Field:   "tasks." + name + ".next"})
				}
				if other, dup := claimedBy[failTarget]; dup && other != name {
					add(ValidationDiagnostic{Code: "next_target_conflict",
						Message: fmt.Sprintf("task %q is claimed by both check %q and check %q", failTarget, other, name),
						Field:   "tasks." + name + ".next"})
				} else {
					claimedBy[failTarget] = name
				}
			}
		}
		if passTarget != "" && passTarget == failTarget {
			add(ValidationDiagnostic{Code: "next_target_same",
				Message: fmt.Sprintf("check %q next.pass and next.fail must differ", name), Field: "tasks." + name + ".next"})
		}
	}
}

// branchCondition is the set of check-gate decisions a task requires to be
// reached (gate handle -> "pass"/"fail"). conflict marks a condition that
// demands both verdicts on one gate: such a task is unreachable, so it can
// never be unlocked together with anything.
type branchCondition struct {
	decisions map[string]string
	conflict  bool
}

func newBranchCondition() *branchCondition {
	return &branchCondition{decisions: map[string]string{}}
}

// mergeCondition unions other into cond, flagging a conflict when both demand
// different decisions on the same gate.
func mergeCondition(cond, other *branchCondition) *branchCondition {
	if other == nil {
		return cond
	}
	if other.conflict {
		cond.conflict = true
	}
	for gate, decision := range other.decisions {
		if existing, ok := cond.decisions[gate]; ok && existing != decision {
			cond.conflict = true
			continue
		}
		cond.decisions[gate] = decision
	}
	return cond
}

// extendCondition returns a copy of cond that additionally requires the given
// decision on gate.
func extendCondition(cond *branchCondition, gate, decision string) *branchCondition {
	out := newBranchCondition()
	mergeCondition(out, cond)
	if existing, ok := out.decisions[gate]; ok && existing != decision {
		out.conflict = true
	}
	out.decisions[gate] = decision
	return out
}

// exclusiveWith reports whether two conditions conflict on at least one gate.
// An unreachable (conflicting) condition can never be unlocked, so it is
// exclusive with anything.
func exclusiveWith(a, b *branchCondition) bool {
	if a == nil || b == nil {
		return false
	}
	if a.conflict || b.conflict {
		return true
	}
	for gate, decision := range a.decisions {
		if other, ok := b.decisions[gate]; ok && other != decision {
			return true
		}
	}
	return false
}

// sharedGateDecision returns the first gate (in sorted order) both conditions
// route through with the same decision.
func sharedGateDecision(a, b *branchCondition) (string, string, bool) {
	if a == nil || b == nil {
		return "", "", false
	}
	gates := make([]string, 0, len(a.decisions))
	for gate := range a.decisions {
		gates = append(gates, gate)
	}
	sort.Strings(gates)
	for _, gate := range gates {
		if other, ok := b.decisions[gate]; ok && other == a.decisions[gate] {
			return gate, a.decisions[gate], true
		}
	}
	return "", "", false
}

// branchClaim is one check-gate route into a task handle.
type branchClaim struct {
	gate     string
	decision string
}

// branchConditions computes every task's check-gate condition in dependency
// order. ok is false when the graph has no DAG order (dag_cycle owns that
// diagnostic). A check task's pass/fail targets extend the gate's condition
// with the verdict that selects them; every other task merges its depends.
func branchConditions(def *domain.FlowDefinition) (map[string]*branchCondition, bool) {
	backEdges := make(map[[2]string]bool, len(def.Convergence))
	for _, rule := range def.Convergence {
		backEdges[[2]string{rule.From, rule.To}] = true
	}
	tasks := make(map[string]domain.FlowTask, len(def.Tasks))
	for name, task := range def.Tasks {
		depends := make([]string, 0, len(task.Depends))
		for _, dep := range task.Depends {
			if backEdges[[2]string{name, dep}] {
				continue
			}
			if _, ok := def.Tasks[dep]; !ok {
				continue
			}
			depends = append(depends, dep)
		}
		task.Depends = depends
		tasks[name] = task
	}
	order, err := TopologicalOrder(tasks)
	if err != nil {
		return nil, false
	}
	// Route claims are collected in sorted task order so the message is stable.
	gateNames := make([]string, 0, len(def.Tasks))
	for name := range def.Tasks {
		gateNames = append(gateNames, name)
	}
	sort.Strings(gateNames)
	claims := make(map[string][]branchClaim)
	for _, name := range gateNames {
		task := def.Tasks[name]
		if task.Type != domain.FlowTaskCheck {
			continue
		}
		for _, decision := range []string{"pass", "fail"} {
			target := strings.TrimSpace(task.Next[decision])
			if target == "" || target == name {
				continue
			}
			if _, ok := tasks[target]; !ok {
				continue
			}
			claims[target] = append(claims[target], branchClaim{gate: name, decision: decision})
		}
	}
	conditions := make(map[string]*branchCondition, len(order))
	for _, name := range order {
		cond := newBranchCondition()
		for _, dep := range tasks[name].Depends {
			mergeCondition(cond, conditions[dep])
		}
		for _, claim := range claims[name] {
			mergeCondition(cond, extendCondition(conditions[claim.gate], claim.gate, claim.decision))
		}
		conditions[name] = cond
	}
	return conditions, true
}

// validateTerminalExclusivity enforces the multi-terminal contract: a flow may
// declare any number of terminals as long as every pair is mutually exclusive,
// i.e. at least one check gate demands pass for one and fail for the other. The
// canonical branch shape (one terminal per gate branch) stays valid because the
// unselected branch stays pending forever. Pairs that could be unlocked
// together are reported once, naming both handles.
func validateTerminalExclusivity(add func(ValidationDiagnostic), def *domain.FlowDefinition) {
	var terminals []string
	for name, task := range def.Tasks {
		if task.Terminal != nil {
			terminals = append(terminals, name)
		}
	}
	if len(terminals) < 2 {
		return
	}
	sort.Strings(terminals)
	conditions, ok := branchConditions(def)
	if !ok {
		return // dag_cycle already reported; no conditions to compare
	}
	var problems []string
	for i := 0; i < len(terminals); i++ {
		for j := i + 1; j < len(terminals); j++ {
			a, b := terminals[i], terminals[j]
			if exclusiveWith(conditions[a], conditions[b]) {
				continue
			}
			message := fmt.Sprintf("terminal tasks %q and %q are not mutually exclusive", a, b)
			if gate, decision, ok := sharedGateDecision(conditions[a], conditions[b]); ok {
				message += fmt.Sprintf(" (both require check gate %q to %s)", gate, decision)
			}
			problems = append(problems, message+": no check gate separates them, so both can be unlocked at once")
		}
	}
	if len(problems) == 0 {
		return
	}
	add(ValidationDiagnostic{Code: "terminal_not_exclusive", Message: strings.Join(problems, "; "), Field: "tasks"})
}

// reachabilityMatrix computes transitive reachability over the DAG (without
// convergence back-edges).
func reachabilityMatrix(tasks map[string]domain.FlowTask, backEdges map[[2]string]int) map[string]map[string]bool {
	reach := make(map[string]map[string]bool, len(tasks))
	for name := range tasks {
		reach[name] = map[string]bool{name: true}
	}
	for name, task := range tasks {
		for _, dep := range task.Depends {
			if _, isBackEdge := backEdges[[2]string{name, dep}]; isBackEdge {
				continue
			}
			if _, ok := tasks[dep]; !ok {
				continue
			}
			reach[dep][name] = true
		}
	}
	// Floyd-like closure (small graphs; deterministic).
	for k := range tasks {
		for i := range tasks {
			if reach[i][k] {
				for j := range tasks {
					if reach[k][j] {
						reach[i][j] = true
					}
				}
			}
		}
	}
	return reach
}

// HeroGraphAdmission is the admission context a Graph Run imposes on one Hero
// task. Every Graph task child is delegated by its host on the single lane, so
// CallerKind and Strategy are fixed; the task budget is optional (0 means the
// task declared no budget.tokens, leaving the Hero ceiling in force).
//
// The delegation limits are not per task, they are per Hero object id, so a
// caller that only knows one task cannot answer them: SameHeroTaskInvocations is
// the invocation total the flow's tasks resolving to this Hero object consume,
// and FanOutMin is this task's own fan_out.min. Pass them explicitly.
//
// It is the input to the shared admission gate: the publish validator and the
// Graph Run freeze build it from the same facts, so publish and Run start
// cannot drift.
type HeroGraphAdmission struct {
	Hero *domain.HeroDefinition
	// HeroRef is the Hero reference as the task wrote it (global/checker,
	// local/checker, checker@v000001). Diagnostics carry it because the HTTP
	// layer flattens them into one message and drops Field, so a Hero-side
	// problem has to name the Hero file the author has to open.
	HeroRef          string
	TaskBudgetTokens int64
	// SameHeroTaskInvocations is how many invocations the tasks resolving to
	// this Hero object consume, max(1, fan_out.min) each. Zero means "one
	// task", the single-task baseline the freeze path uses.
	SameHeroTaskInvocations int64
	// FanOutMin is this task's fan_out.min; 0 means the task does not fan out.
	FanOutMin  int
	CallerKind string
	Strategy   string
}

// sameHeroTaskInvocations reads the invocation fact, treating "not supplied" as
// the single-task baseline instead of as unlimited.
func (in HeroGraphAdmission) sameHeroTaskInvocations() int64 {
	if in.SameHeroTaskInvocations < 1 {
		return 1
	}
	return in.SameHeroTaskInvocations
}

// heroLabel names the Hero in a diagnostic message. The HTTP layer flattens
// diagnostics and drops Field, so a message that only says "delegation.admission"
// leaves the user unable to find the file it is talking about.
func heroLabel(in HeroGraphAdmission) string {
	if ref := strings.TrimSpace(in.HeroRef); ref != "" {
		return "Hero " + ref
	}
	return "Hero"
}

// HeroAdmissibilityDiagnostics reports why a Hero cannot be admitted by a Graph
// Run: its delegation admission is not auto_within_budget, its context policy
// does not allow task_only, its delegation does not allow the caller or strategy
// the Graph uses, its budget ceiling or delegation limits are unusable, its
// invocation or concurrency limit cannot cover the flow, or the task budget
// exceeds the ceiling. The publish validator reports these per task; the freeze
// path calls the same function so both surfaces reject the same Heroes.
func HeroAdmissibilityDiagnostics(in HeroGraphAdmission) []ValidationDiagnostic {
	def := in.Hero
	if def == nil {
		return nil
	}
	hero := heroLabel(in)
	var diagnostics []ValidationDiagnostic
	delegation := def.DelegationPolicy
	if admission := delegation.Admission; admission != domain.DelegationAutoWithinBudget {
		diagnostics = append(diagnostics, ValidationDiagnostic{
			Code: "hero_admission_incompatible",
			Message: fmt.Sprintf("%s delegation.admission is %q; a Graph Run only admits %q",
				hero, admission, domain.DelegationAutoWithinBudget),
		})
	}
	taskOnly := false
	for _, mode := range def.ContextPolicy.AllowedModes {
		if mode == domain.HeroContextTask {
			taskOnly = true
			break
		}
	}
	if !taskOnly {
		diagnostics = append(diagnostics, ValidationDiagnostic{
			Code: "hero_context_incompatible",
			Message: fmt.Sprintf("%s contextPolicy.allowedModes %v does not include %q; a Graph Run child requires %q context",
				hero, def.ContextPolicy.AllowedModes, domain.HeroContextTask, domain.HeroContextTask),
		})
	}
	if in.CallerKind != "" && !containsString(delegation.AllowedCallerKinds, in.CallerKind) {
		diagnostics = append(diagnostics, ValidationDiagnostic{
			Code: "hero_caller_kind_incompatible",
			Message: fmt.Sprintf("%s delegation.allowedCallerKinds %v does not allow caller kind %q; a Graph Run task is always delegated by %q",
				hero, delegation.AllowedCallerKinds, in.CallerKind, in.CallerKind),
		})
	}
	if in.Strategy != "" && !containsString(delegation.AllowedStrategies, in.Strategy) {
		diagnostics = append(diagnostics, ValidationDiagnostic{
			Code: "hero_strategy_incompatible",
			Message: fmt.Sprintf("%s delegation.allowedStrategies %v does not allow strategy %q; a Graph Run task child always runs as %q",
				hero, delegation.AllowedStrategies, in.Strategy, in.Strategy),
		})
	}
	ceiling := delegation.BudgetCeiling
	// A Graph Run child submits its ceiling as the delegation request, so every
	// field must clear the request minimum the store enforces.
	ceilings := []struct {
		name string
		got  int64
		min  int64
	}{
		{"maxModelCalls", int64(ceiling.MaxModelCalls), 1},
		{"maxToolCalls", int64(ceiling.MaxToolCalls), 1},
		{"maxTotalTokens", ceiling.MaxTotalTokens, 1},
		{"maxOutputTokens", ceiling.MaxOutputTokens, 1},
		{"maxWallTimeMs", ceiling.MaxWallTimeMS, 1},
		{"maxCostUsdMicros", ceiling.MaxCostUSDMicros, 0},
	}
	for _, field := range ceilings {
		if field.got < field.min {
			diagnostics = append(diagnostics, ValidationDiagnostic{
				Code: "hero_ceiling_invalid",
				Message: fmt.Sprintf("%s delegation.budgetCeiling.%s is %d; a Graph Run child requires at least %d",
					hero, field.name, field.got, field.min),
			})
		}
	}
	// Delegation limits below 1 are unwritable through the Hero editor but a
	// pack-installed or hand-edited Graph-local Hero bypasses those minimums, and
	// the store rejects every child once the limit is 0.
	limits := []struct {
		name string
		got  int
	}{
		{"maxInvocationsPerParentRun", delegation.MaxInvocationsPerParentRun},
		{"maxConcurrentInstances", delegation.MaxConcurrentInstances},
	}
	for _, limit := range limits {
		if limit.got < 1 {
			diagnostics = append(diagnostics, ValidationDiagnostic{
				Code: "hero_limit_invalid",
				Message: fmt.Sprintf("%s delegation.%s is %d; a Graph Run child requires at least 1",
					hero, limit.name, limit.got),
			})
		}
	}
	// Two runtime failures are statically guaranteed and reported here: every
	// task referencing the Hero consumes one invocation (a fan-out task consumes
	// fan_out.min), and a fan-out task opens fan_out.min children at once. Nothing
	// else is modelled: whether two dependent tasks become co-ready inside one Run
	// is a scheduling question, not a publish-time fact, so the gate does not try
	// to predict it.
	invocations := in.sameHeroTaskInvocations()
	if delegation.MaxInvocationsPerParentRun >= 1 && invocations > int64(delegation.MaxInvocationsPerParentRun) {
		diagnostics = append(diagnostics, ValidationDiagnostic{
			Code: "hero_invocation_limit_exceeded",
			Message: fmt.Sprintf("%s consumes %d invocations (every task counts max(1, fan_out.min)); delegation.maxInvocationsPerParentRun is %d",
				hero, invocations, delegation.MaxInvocationsPerParentRun),
		})
	}
	if delegation.MaxConcurrentInstances >= 1 && in.FanOutMin > delegation.MaxConcurrentInstances {
		diagnostics = append(diagnostics, ValidationDiagnostic{
			Code: "hero_concurrency_limit_exceeded",
			Message: fmt.Sprintf("%s fan_out.min %d would open that many concurrent instances; delegation.maxConcurrentInstances is %d",
				hero, in.FanOutMin, delegation.MaxConcurrentInstances),
		})
	}
	if in.TaskBudgetTokens > 0 && in.TaskBudgetTokens > ceiling.MaxTotalTokens {
		diagnostics = append(diagnostics, ValidationDiagnostic{
			Code: "hero_budget_exceeds_ceiling",
			Message: fmt.Sprintf("%s delegation.budgetCeiling.maxTotalTokens %d is below the task budget.tokens %d",
				hero, ceiling.MaxTotalTokens, in.TaskBudgetTokens),
		})
	}
	return diagnostics
}

// heroAdmissionFacts is one resolved Hero task as the admission gate sees it.
// The gate also needs flow-level totals, so the task loop collects facts and the
// gate runs once every task has resolved.
type heroAdmissionFacts struct {
	taskName         string
	heroRef          string
	heroObjectID     string
	definition       domain.HeroDefinition
	taskBudgetTokens int64
	fanOutMin        int
}

// heroObjectID is the delegation bucket the store counts both limits on: the
// frozen Hero version id up to "@", so checker@v1 and checker@v2 share one
// bucket. The file publish resolvers return a definition without a version id,
// so it falls back to the reference string. The scope prefix is not part of a
// frozen version id (freeze stores checker@v000001, not global/checker@...), so
// it is stripped to keep global/checker and checker in one bucket.
func heroObjectID(versionID, heroRef string) string {
	key := strings.TrimSpace(versionID)
	if key == "" {
		key = strings.TrimSpace(heroRef)
	}
	scope, _, _ := strings.Cut(key, "@")
	return strings.TrimPrefix(strings.TrimSpace(scope), "global/")
}

// validateHeroAdmission runs the shared admission gate once per Hero task.
//
// The delegation limits are counted per Hero object id across the flow: every
// task referencing the Hero consumes max(1, fan_out.min) invocations, and a
// fan-out task needs fan_out.min concurrent instances. Those totals are a
// publish-only fact - graphrun freeze validates one task at a time and passes the
// single-task baseline instead (see HeroAdmissibilityDiagnostics).
func validateHeroAdmission(facts []heroAdmissionFacts, add func(ValidationDiagnostic)) {
	if len(facts) == 0 {
		return
	}
	invocations := make(map[string]int64, len(facts))
	for _, fact := range facts {
		consumed := int64(1)
		if fact.fanOutMin > 1 {
			consumed = int64(fact.fanOutMin)
		}
		invocations[fact.heroObjectID] += consumed
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].taskName < facts[j].taskName })
	for _, fact := range facts {
		field := "tasks." + fact.taskName
		for _, diagnostic := range HeroAdmissibilityDiagnostics(HeroGraphAdmission{
			Hero:                    &fact.definition,
			HeroRef:                 fact.heroRef,
			TaskBudgetTokens:        fact.taskBudgetTokens,
			SameHeroTaskInvocations: invocations[fact.heroObjectID],
			FanOutMin:               fact.fanOutMin,
			CallerKind:              "host",
			Strategy:                string(domain.DelegationStrategySingle),
		}) {
			// A budget above the ceiling is fixed in the Graph task, not in the
			// Hero file; every other code names a Hero-side problem.
			diagnosticField := field + ".hero"
			if diagnostic.Code == "hero_budget_exceeds_ceiling" {
				diagnosticField = field + ".budget.tokens"
			}
			add(ValidationDiagnostic{
				Code:    diagnostic.Code,
				Message: fmt.Sprintf("task %q: %s", fact.taskName, diagnostic.Message),
				Field:   diagnosticField,
			})
		}
	}
}

func heroAllToolsReadOnly(ctx context.Context, resolver Resolver, heroDef domain.HeroDefinition) bool {
	if heroDef.Authority != domain.HeroAuthorityReadOnly {
		return false
	}
	for _, tool := range heroDef.AllowedTools {
		if !resolver.ToolReadOnly(ctx, tool) {
			return false
		}
	}
	return true
}

// ValidateWritesScope checks a single writes-scope glob: relative path, no
// traversal, no leading slash, and only glob metacharacters we understand.
//
// It is exported because the Graph authoring format declares the same field and
// publishes through a different validator; both surfaces must reject the same
// scopes rather than each inventing its own rules.
func ValidateWritesScope(scope string) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return errors.New("empty scope")
	}
	if strings.HasPrefix(scope, "/") {
		return errors.New("must be relative to /workspace (no leading slash)")
	}
	if strings.HasPrefix(scope, "..") {
		return errors.New("must not escape the workspace (no .. segments)")
	}
	for _, seg := range strings.Split(scope, "/") {
		if seg == ".." {
			return errors.New("must not escape the workspace (no .. segments)")
		}
		if seg != "" && strings.ContainsAny(seg, "\\:\";|&`${}()") {
			return fmt.Errorf("segment %q contains unsupported characters", seg)
		}
	}
	return nil
}

// literalPrefix returns everything up to the first glob metacharacter, or the
// whole pattern if none. Two scopes are conservatively disjoint only when
// neither literal prefix is a prefix of the other: if either prefix extends
// the other they may match a common path, so they are treated as overlapping.
func literalPrefix(pattern string) string {
	for i, r := range pattern {
		if r == '*' || r == '?' || r == '[' {
			return pattern[:i]
		}
	}
	return pattern
}

// writesDisjoint reports whether two writes-scope sets cannot match any common
// workspace path. Empty set means "whole workspace" and overlaps everything.
// This is a conservative static approximation: overlapping literals at any
// prefix point disqualify parallelism (scheduling-only; no runtime scope
// enforcement today).
func writesDisjoint(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for _, pa := range a {
		for _, pb := range b {
			pa, pb = strings.TrimSpace(pa), strings.TrimSpace(pb)
			if pa == "" || pb == "" {
				return false
			}
			prefA, prefB := literalPrefix(pa), literalPrefix(pb)
			if strings.HasPrefix(prefA, prefB) || strings.HasPrefix(prefB, prefA) {
				return false
			}
		}
	}
	return true
}

// --- Goal reference parsing ---

type refKind int

const (
	refInput refKind = iota
	refTask
	refFlowVars
	refForbidden
	refMalformed
)

type goalRef struct {
	Kind refKind
	Raw  string
	Name string
	Task string
}

var goalRefPattern = regexp.MustCompile(`\{([^{}]+)\}`)

// parseGoalReferences extracts every {…} token from a goal template. The
// allowed vocabulary: {inputs.x}, {task.X.output(.field)}, {flow.vars.y}.
// Anything else is classified forbidden or malformed.
func parseGoalReferences(goal string) []goalRef {
	var refs []goalRef
	for _, match := range goalRefPattern.FindAllStringSubmatch(goal, -1) {
		raw := match[1]
		ref := goalRef{Raw: raw, Kind: refMalformed}
		switch {
		case strings.HasPrefix(raw, "inputs."):
			ref.Kind = refInput
			ref.Name = strings.TrimPrefix(raw, "inputs.")
		case strings.HasPrefix(raw, "task."):
			rest := strings.TrimPrefix(raw, "task.")
			task, _, _ := strings.Cut(rest, ".")
			ref.Kind = refTask
			ref.Task = strings.TrimSpace(task)
		case raw == "flow.vars" || strings.HasPrefix(raw, "flow.vars."):
			ref.Kind = refFlowVars
		case strings.HasPrefix(raw, "prev."):
			ref.Kind = refForbidden
		default:
			ref.Kind = refForbidden
		}
		refs = append(refs, ref)
	}
	return refs
}

// --- Secret / path scan ---

var (
	secretKeyPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|passwd|authorization|bearer[ =]|token[ =])`)
	standingApproval = regexp.MustCompile(`(?i)standing[ _-]?approval|always[ _-]?approve|auto[ _-]?approve[ _-]?everything`)
	absolutePath     = regexp.MustCompile(`(^|[[:space:]=]["']?)(/[/A-Za-z0-9_.-]+|~[/A-Za-z0-9_.-]+)`)
)

func scanSecrets(add func(ValidationDiagnostic), def *domain.FlowDefinition) {
	scan := func(field, text string) {
		if text == "" {
			return
		}
		if secretKeyPattern.MatchString(text) {
			add(ValidationDiagnostic{Code: "secret_present",
				Message: "flow definition must not contain secret-like content (keys, tokens, passwords)", Field: field})
		}
		if standingApproval.MatchString(text) {
			add(ValidationDiagnostic{Code: "standing_approval_present",
				Message: "flow definition must not contain standing-approval language", Field: field})
		}
		if absolutePath.MatchString(text) {
			add(ValidationDiagnostic{Code: "absolute_path_present",
				Message: "flow definition must not contain workspace absolute paths or home references", Field: field})
		}
	}
	scan("description", def.Description)
	for name, task := range def.Tasks {
		field := "tasks." + name
		scan(field+".goal", task.Goal)
		scan(field+".command", task.Command)
	}
}
