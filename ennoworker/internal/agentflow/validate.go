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

// RoleInfo is the publish-time resolution of one role@version reference.
type RoleInfo struct {
	VersionID  string
	Definition domain.RoleDefinition
}

// Resolver resolves external references at publish time. It is the seam the
// store wires with live SQL/catalog lookups; tests use a stub.
type Resolver interface {
	ResolveRole(ctx context.Context, roleRef string) (*RoleInfo, error)
	KnownSkill(ctx context.Context, name string) bool
	ToolReadOnly(ctx context.Context, tool string) bool
}

// Validator runs the full §5.3 publish validation (9 checks, fail loud).
type Validator struct {
	Resolver Resolver
	// CheckAllowlist is the sandbox executable allowlist for check tasks.
	CheckAllowlist []string
	// MaxFanOut bounds fan_out.max.
	MaxFanOut int
	// MaxRounds bounds convergence max_rounds.
	MaxRounds int
}

func (v *Validator) fail(code, message, field string) ValidationDiagnostic {
	return ValidationDiagnostic{Code: code, Message: message, Field: field}
}

func (v *Validator) allowedCheckExec(command string) bool {
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
// nil fails closed (role/skill/read-only checks cannot run).
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

	// 3. role@version / skill resolvability; 6. fan_out read-only; 7. budget.
	totalTaskTokens := int64(0)
	checkAllowlistMissing := false
	// writerScopes collects tasks whose Role can mutate (writer class); used by
	// the allow_disjoint_writers validation.
	writerScopes := map[string][]string{}
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
		case domain.FlowTaskRole:
			if strings.TrimSpace(task.Role) == "" {
				add(v.fail("role_required", fmt.Sprintf("task %q requires a role", name), field+".role"))
				continue
			}
			if strings.HasSuffix(strings.TrimSpace(task.Role), "@latest") {
				add(v.fail("role_version_required",
					fmt.Sprintf("task %q role %q must pin an exact version (handle@version, no latest)", name, task.Role),
					field+".role"))
				continue
			}
			// A bare handle is a flow-local reference (resolves flow > shared
			// catalog at freeze time); the resolver enforces that the handle
			// actually resolves, failing loud otherwise.
			info, err := v.Resolver.ResolveRole(ctx, task.Role)
			if err != nil {
				add(v.fail("role_not_found", fmt.Sprintf("task %q: %v", name, err), field+".role"))
				continue
			}
			roleDef := info.Definition
			readOnly := roleAllToolsReadOnly(ctx, v.Resolver, roleDef)
			if task.FanOut != nil {
				if task.FanOut.Min < 1 || task.FanOut.Max < task.FanOut.Min ||
					(v.MaxFanOut > 0 && task.FanOut.Max > v.MaxFanOut) {
					add(v.fail("fan_out_range_invalid",
						fmt.Sprintf("task %q fan_out range %d..%d is invalid", name, task.FanOut.Min, task.FanOut.Max),
						field+".fanOut"))
				}
				if !readOnly {
					add(v.fail("fan_out_not_read_only",
						fmt.Sprintf("task %q fan_out requires the Role allowlist to be fully read-only", name),
						field+".fanOut"))
				}
			}
			// Writer class: any mutation-capable task is a candidate for the
		// disjoint-writes parallel lane; declared scopes are validated here
		// regardless of the flow flag (catches typos early).
			if !readOnly {
				for _, w := range task.Writes {
					if err := validateWritesGlob(w); err != nil {
						add(v.fail("writes_glob_invalid",
							fmt.Sprintf("task %q writes scope %q: %v", name, w, err),
							field+".writes"))
					}
				}
				writerScopes[name] = task.Writes
			}
			effectiveTokens := roleDef.DelegationPolicy.BudgetCeiling.MaxTotalTokens
			if task.Budget != nil && task.Budget.Tokens > 0 {
				effectiveTokens = task.Budget.Tokens
				totalTaskTokens += effectiveTokens
			}
			// Tasks without an explicit budget contribute 0 to the publish-time
			// sum: their effective ceiling is the Role's, which the runtime flow
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

	// 4. DAG acyclicity + convergence back-edge binding.
	validateTopology(add, def)
	validateBranchRouting(add, def)

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

	result.Valid = len(result.Diagnostics) == 0
	if result.Valid {
		digest, err := ConfigDigest(def)
		if err == nil {
			result.ConfigDigest = digest
		}
	}
	return result
}

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

func roleAllToolsReadOnly(ctx context.Context, resolver Resolver, roleDef domain.RoleDefinition) bool {
	if roleDef.Authority != domain.RoleAuthorityReadOnly {
		return false
	}
	for _, tool := range roleDef.AllowedTools {
		if !resolver.ToolReadOnly(ctx, tool) {
			return false
		}
	}
	return true
}

// validateWritesGlob checks a single writes-scope glob: relative path, no
// traversal, no leading slash, and only glob metacharacters we understand.
func validateWritesGlob(scope string) error {
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
