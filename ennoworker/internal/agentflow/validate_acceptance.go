package agentflow

// Acceptance authoring validation: what a task may declare, and which of those
// declarations this Worker can actually decide. Split out of validate.go so the
// generic publish checks and this vocabulary stay separately readable.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// validateUnknownTaskFields reports every task-node YAML key that the parser
// dropped because the task struct does not know it. A silently dropped key can
// disable a whole clause (a misspelled `acceptance:`), so it fails publish.
func validateUnknownTaskFields(add func(ValidationDiagnostic), def *domain.FlowDefinition) {
	names := make([]string, 0, len(def.UnknownTaskFields))
	for name := range def.UnknownTaskFields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fields := def.UnknownTaskFields[name]
		if len(fields) == 0 {
			continue
		}
		add(ValidationDiagnostic{Code: "flow_task_unknown_field",
			Message: fmt.Sprintf("task %q has unknown field(s): %s", name, strings.Join(fields, ", ")),
			Field:   "tasks." + name})
	}
}

// heroAcceptanceLimit is one Hero task's acceptance admission: the Hero
// reference and the leaf kinds that Hero declared in acceptanceKinds. An empty
// kinds list is the kernel's whole vocabulary, not "nothing allowed".
type heroAcceptanceLimit struct {
	heroRef string
	kinds   []string
}

// validateAcceptance validates every task's acceptance clause. Acceptance is
// only supported on ordinary hero tasks: check/terminal tasks have no artifact
// of their own, and a fan_out parent has no single verdict (parent aggregation
// is a later design).
//
// limits carries the resolved Hero of every task that declared acceptance; a
// Hero with a non-empty acceptanceKinds restricts the leaves its tasks may
// declare.
func validateAcceptance(add func(ValidationDiagnostic), def *domain.FlowDefinition,
	limits map[string]heroAcceptanceLimit) {
	names := make([]string, 0, len(def.Tasks))
	for name := range def.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		task := def.Tasks[name]
		if task.Acceptance == nil {
			continue
		}
		field := "tasks." + name
		switch {
		case task.Terminal != nil:
			add(ValidationDiagnostic{Code: "acceptance_not_supported",
				Message: fmt.Sprintf("task %q is a terminal task and cannot declare acceptance", name),
				Field:   field + ".acceptance"})
			continue
		case task.Type == domain.FlowTaskCheck:
			add(ValidationDiagnostic{Code: "acceptance_not_supported",
				Message: fmt.Sprintf("task %q is a check task and cannot declare acceptance", name),
				Field:   field + ".acceptance"})
			continue
		case task.FanOut != nil:
			add(ValidationDiagnostic{Code: "acceptance_fan_out_unsupported",
				Message: fmt.Sprintf("task %q is a fan_out task and cannot declare acceptance (fan-out parent verdicts are not supported)", name),
				Field:   field + ".acceptance"})
			continue
		}
		if len(task.Acceptance) == 0 {
			add(ValidationDiagnostic{Code: "acceptance_empty",
				Message: fmt.Sprintf("task %q declares an empty acceptance list", name),
				Field:   field + ".acceptance"})
			continue
		}
		for i, spec := range task.Acceptance {
			entryField := fmt.Sprintf("%s.acceptance[%d]", field, i)
			validateAcceptanceEntry(add, name, i, entryField, spec)
			validateHeroAcceptanceKind(add, name, i, entryField, spec, limits[name])
		}
	}
}

// validateHeroAcceptanceKind refuses a leaf the producing Hero does not accept.
// It only speaks about kinds the kernel knows: an unknown check already fails as
// acceptance_check_unknown, and reporting it twice would not help the author.
// An empty acceptanceKinds list means "the whole vocabulary" and never fires,
// which is what keeps every Hero written before the field existed working.
func validateHeroAcceptanceKind(add func(ValidationDiagnostic), taskName string, index int,
	entryField string, spec domain.FlowAcceptance, limit heroAcceptanceLimit) {
	if len(limit.kinds) == 0 || !knownAcceptanceCheck(spec.Check) {
		return
	}
	for _, kind := range limit.kinds {
		if kind == spec.Check {
			return
		}
	}
	add(ValidationDiagnostic{Code: "hero_acceptance_kind_unsupported",
		Message: fmt.Sprintf("task %q acceptance[%d] check %q is not accepted by Hero %q (it accepts: %s)",
			taskName, index, spec.Check, limit.heroRef, strings.Join(limit.kinds, ", ")),
		Field: entryField + ".check"})
}

func validateAcceptanceEntry(add func(ValidationDiagnostic), taskName string, index int, entryField string, spec domain.FlowAcceptance) {
	if !knownAcceptanceCheck(spec.Check) {
		add(ValidationDiagnostic{Code: "acceptance_check_unknown",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q is unknown (supported: %s)",
				taskName, index, spec.Check, strings.Join(KnownChecks, ", ")),
			Field: entryField + ".check"})
		return
	}
	switch spec.Check {
	case acceptanceCheckArtifactPublished:
		validateArtifactAcceptanceEntry(add, taskName, index, entryField, spec)
		return
	case acceptanceCheckTestsPassed:
		validateCommandAcceptanceEntry(add, taskName, index, entryField, spec)
		return
	}
	// A file leaf names one workspace path. A path, a published artifact and a
	// recorded command are three different evidences, so the other operands are
	// mutually exclusive.
	if strings.TrimSpace(spec.Artifact) != "" {
		add(ValidationDiagnostic{Code: "acceptance_artifact_forbidden",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q takes a file, not an artifact (only artifact-published declares an artifact)",
				taskName, index, spec.Check),
			Field: entryField + ".artifact"})
	}
	if strings.TrimSpace(spec.Command) != "" {
		add(ValidationDiagnostic{Code: "acceptance_command_forbidden",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q does not take a command (only tests-passed declares one)",
				taskName, index, spec.Check),
			Field: entryField + ".command"})
	}
	if strings.TrimSpace(spec.File) == "" {
		add(ValidationDiagnostic{Code: "acceptance_file_required",
			Message: fmt.Sprintf("task %q acceptance[%d] requires a file", taskName, index),
			Field:   entryField + ".file"})
	} else if !validAcceptancePath(spec.File) {
		add(ValidationDiagnostic{Code: "acceptance_path_absolute",
			Message: fmt.Sprintf("task %q acceptance[%d] file %q must be workspace-relative and must not contain a .. segment",
				taskName, index, spec.File),
			Field: entryField + ".file"})
	}
	if spec.Check != acceptanceCheckCSVColumns {
		if len(spec.Columns) > 0 {
			add(ValidationDiagnostic{Code: "acceptance_columns_forbidden",
				Message: fmt.Sprintf("task %q acceptance[%d] check %q does not take columns", taskName, index, spec.Check),
				Field:   entryField + ".columns"})
		}
		return
	}
	if len(spec.Columns) == 0 {
		add(ValidationDiagnostic{Code: "acceptance_columns_required",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q requires columns", taskName, index, spec.Check),
			Field:   entryField + ".columns"})
		return
	}
	if reason := invalidAcceptanceColumns(spec.Columns); reason != "" {
		add(ValidationDiagnostic{Code: "acceptance_columns_invalid",
			Message: fmt.Sprintf("task %q acceptance[%d] columns are invalid: %s", taskName, index, reason),
			Field:   entryField + ".columns"})
	}
}

// validateArtifactAcceptanceEntry validates artifact-published: it is the one
// leaf whose operand is a published artifact NAME, so it requires `artifact` and
// refuses the file-leaf operands. It never validates a path: there is no path.
func validateArtifactAcceptanceEntry(add func(ValidationDiagnostic), taskName string, index int, entryField string, spec domain.FlowAcceptance) {
	if strings.TrimSpace(spec.Artifact) == "" {
		add(ValidationDiagnostic{Code: "acceptance_artifact_required",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q requires the artifact field (the name of the artifact the task must have published)",
				taskName, index, spec.Check),
			Field: entryField + ".artifact"})
	}
	if strings.TrimSpace(spec.File) != "" {
		add(ValidationDiagnostic{Code: "acceptance_file_forbidden",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q takes an artifact name, not a file path (artifact-published reads the published artifact, never the workspace)",
				taskName, index, spec.Check),
			Field: entryField + ".file"})
	}
	if len(spec.Columns) > 0 {
		add(ValidationDiagnostic{Code: "acceptance_columns_forbidden",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q does not take columns", taskName, index, spec.Check),
			Field:   entryField + ".columns"})
	}
	if strings.TrimSpace(spec.Command) != "" {
		add(ValidationDiagnostic{Code: "acceptance_command_forbidden",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q does not take a command (only tests-passed declares one)",
				taskName, index, spec.Check),
			Field: entryField + ".command"})
	}
}

// validateCommandAcceptanceEntry validates tests-passed: the only leaf whose
// operand is a command the child must have executed. The command is matched
// token for token against one recorded execution, so a compound command - two
// invocations glued together - is refused here. Refusing at publish is what
// keeps `pytest a.py || true` from being satisfiable by a run that never passed;
// the operator is refused even inside a quoted argument, because the recorded
// command is re-split into tokens the same way.
func validateCommandAcceptanceEntry(add func(ValidationDiagnostic), taskName string, index int, entryField string, spec domain.FlowAcceptance) {
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		add(ValidationDiagnostic{Code: "acceptance_command_required",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q requires the command the task must have executed",
				taskName, index, spec.Check),
			Field: entryField + ".command"})
	} else if token := compoundCommandToken(command); token != "" {
		add(ValidationDiagnostic{Code: "acceptance_command_compound",
			Message: fmt.Sprintf("task %q acceptance[%d] command %q contains shell control syntax (%q); tests-passed matches one recorded invocation token for token, so a compound command cannot be anchored to a single execution. This includes an operator inside a quoted argument. Declare the plain invocation instead",
				taskName, index, command, token),
			Field: entryField + ".command"})
	}
	if strings.TrimSpace(spec.File) != "" {
		add(ValidationDiagnostic{Code: "acceptance_file_forbidden",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q reads a recorded execution, not a file path",
				taskName, index, spec.Check),
			Field: entryField + ".file"})
	}
	if strings.TrimSpace(spec.Artifact) != "" {
		add(ValidationDiagnostic{Code: "acceptance_artifact_forbidden",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q reads a recorded execution, not a published artifact",
				taskName, index, spec.Check),
			Field: entryField + ".artifact"})
	}
	if len(spec.Columns) > 0 {
		add(ValidationDiagnostic{Code: "acceptance_columns_forbidden",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q does not take columns", taskName, index, spec.Check),
			Field:   entryField + ".columns"})
	}
}

// validAcceptancePath rejects absolute paths and any .. segment. Paths are
// workspace-relative; an artifact name is not a path and never reaches this
// function. This slice deliberately allows any workspace subtree (not only
// outputs/), because some flows write under work/.
func validAcceptancePath(file string) bool {
	path := strings.TrimSpace(file)
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return false
	}
	if len(path) >= 2 && path[1] == ':' { // C:... style
		return false
	}
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return false
		}
	}
	return true
}

// invalidAcceptanceColumns returns a human-readable reason for the first
// problem, or "" when the column list is acceptable.
func invalidAcceptanceColumns(columns []string) string {
	if len(columns) > maxAcceptanceColumns {
		return fmt.Sprintf("at most %d columns are allowed, got %d", maxAcceptanceColumns, len(columns))
	}
	seen := make(map[string]bool, len(columns))
	for i, column := range columns {
		if strings.TrimSpace(column) == "" {
			return fmt.Sprintf("column %d is blank", i+1)
		}
		if len(column) > maxAcceptanceColumnSize {
			return fmt.Sprintf("column %d is longer than %d bytes", i+1, maxAcceptanceColumnSize)
		}
		if seen[column] {
			return fmt.Sprintf("column %q is declared twice", column)
		}
		seen[column] = true
	}
	return ""
}
