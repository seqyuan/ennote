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

// validateAcceptance validates every task's acceptance clause. Acceptance is
// only supported on ordinary hero tasks: check/terminal tasks have no artifact
// of their own, and a fan_out parent has no single verdict (parent aggregation
// is a later design).
func validateAcceptance(add func(ValidationDiagnostic), def *domain.FlowDefinition) {
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
		}
	}
}

func validateAcceptanceEntry(add func(ValidationDiagnostic), taskName string, index int, entryField string, spec domain.FlowAcceptance) {
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
	if !knownAcceptanceCheck(spec.Check) {
		add(ValidationDiagnostic{Code: "acceptance_check_unknown",
			Message: fmt.Sprintf("task %q acceptance[%d] check %q is unknown (supported: %s); tests-passed is not supported yet",
				taskName, index, spec.Check, strings.Join(KnownChecks, ", ")),
			Field: entryField + ".check"})
		return
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

// validAcceptancePath rejects absolute paths and any .. segment. Paths are
// workspace-relative; this slice deliberately allows any workspace subtree
// (not only outputs/), because some flows write under work/.
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
