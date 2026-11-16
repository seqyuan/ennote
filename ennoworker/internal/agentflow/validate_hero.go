package agentflow

// The Hero admission gate: what a Graph Run requires of the Hero it is handed,
// and why a given Hero cannot satisfy it. Split out of validate.go because it is
// the one rule set shared by Graph publish, pack install and the Run freeze.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

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
	// A read-only Hero can never call publish_artifact (it is a mutation tool), so
	// a report contract that REQUIRES an artifact is unsatisfiable by this Hero's
	// own Run: every Run would do its work and then fail
	// report_contract_violation. The global-Hero publish path refuses this too;
	// this is the same rule where a Graph publishes and freezes a Hero, including
	// a graph-local one that never goes through that endpoint.
	if def.Authority == domain.HeroAuthorityReadOnly && def.ReportContract != nil && def.ReportContract.RequireArtifacts {
		diagnostics = append(diagnostics, ValidationDiagnostic{
			Code: "hero_require_artifacts_unusable",
			Message: fmt.Sprintf("%s requires an artifact (reportContract.requireArtifacts) but its authority is read_only, "+
				"which forbids publish_artifact: every Run would fail report_contract_violation", hero),
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
