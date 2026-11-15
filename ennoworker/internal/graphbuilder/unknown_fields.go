package graphbuilder

// Refusing to rewrite a draft that holds keys the parsed definition lost.
//
// ParseDefinition stays lenient (a revision written by a newer Worker must still
// load), and the publish validator rejects unknown task keys. Between those two
// gates there is a third path that silently disables a clause anyway: the save
// path rewrites graph.yaml from the parsed definition, which deletes the keys it
// dropped, and the following publish then succeeds *without* the clause. A
// typo such as `acceptence:` therefore has to stop before the rewrite, not after
// it, or the file is normalised behind the user's back and there is nothing left
// to fix.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// UnknownTaskKeysError refuses a Graph draft save: the graph.yaml on disk
// carries task-node keys this Worker does not recognise, and rewriting the
// document from the parsed definition would drop them.
type UnknownTaskKeysError struct {
	// Fields maps task id -> the unknown keys the parser recorded (sorted).
	Fields map[string][]string
}

// Error names every task and key, then says what to do about it: the only
// correct fix is editing graph.yaml directly, because every UI save path
// round-trips through the parsed definition.
func (e *UnknownTaskKeysError) Error() string {
	tasks := make([]string, 0, len(e.Fields))
	for task := range e.Fields {
		tasks = append(tasks, task)
	}
	sort.Strings(tasks)
	parts := make([]string, 0, len(tasks))
	for _, task := range tasks {
		parts = append(parts, fmt.Sprintf("task %q: %s", task, strings.Join(e.Fields[task], ", ")))
	}
	return "graph.yaml carries task keys this Worker does not recognise (" + strings.Join(parts, "; ") +
		"). Nothing was written: saving from this view would delete them. " +
		"Edit graph.yaml directly to remove or correct them, then save again."
}

// UnknownTaskKeys returns an *UnknownTaskKeysError when the definition was
// parsed from a draft that carries unknown task keys, and nil otherwise. It is
// the guard both save paths (the editor update and this package's apply) run
// inside the read-modify-write so the file is never rewritten first.
func UnknownTaskKeys(definition *domain.FlowDefinition) error {
	if definition == nil || len(definition.UnknownTaskFields) == 0 {
		return nil
	}
	fields := make(map[string][]string, len(definition.UnknownTaskFields))
	for task, keys := range definition.UnknownTaskFields {
		if len(keys) == 0 {
			continue
		}
		fields[task] = append([]string(nil), keys...)
	}
	if len(fields) == 0 {
		return nil
	}
	return &UnknownTaskKeysError{Fields: fields}
}
