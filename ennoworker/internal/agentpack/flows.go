package agentpack

import (
	"strings"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
)

// Flow task types mirrored from the Agent Flow contract. They are re-declared
// here so the pack validator reads the same vocabulary the runtime uses.
const (
	flowTaskRole  = domain.FlowTaskRole
	flowTaskCheck = domain.FlowTaskCheck
)

// agentflowParse parses a full Agent Flow definition (format: flow).
func agentflowParse(data []byte) (*domain.FlowDefinition, error) {
	definition, err := agentflow.ParseDefinition(data)
	if err != nil {
		return nil, err
	}
	// TopologicalOrder reports unknown dependencies and cycles. The full
	// agentflow.Validator is intentionally NOT run: it needs a database-backed
	// role/skill resolver, which a pure inspect must not depend on.
	if _, err := agentflow.TopologicalOrder(definition.Tasks); err != nil {
		return nil, err
	}
	return definition, nil
}

// agentflowDigest returns the version digest of a parsed definition with the
// same sha256: prefix the other source digests use.
func agentflowDigest(definition *domain.FlowDefinition) (string, error) {
	digest, err := agentflow.ConfigDigest(definition)
	if err != nil {
		return "", err
	}
	return "sha256:" + digest, nil
}

// definitionEntryTask returns the entry task (no depends). Multiple entry tasks
// are a publish-time failure, not an inspect failure: S1 reports the
// lexicographically first one and leaves the verdict to the publish validator.
func definitionEntryTask(definition *domain.FlowDefinition) string {
	tasks := make(map[string]domain.FlowTask, len(definition.Tasks))
	for name, task := range definition.Tasks {
		tasks[name] = task
	}
	return firstIndependentTask(tasks)
}

func graphEntryTask(document *graphsource.Document) string {
	if document == nil {
		return ""
	}
	entry := ""
	for name, dependencies := range document.Graph {
		if len(dependencies) != 0 {
			continue
		}
		if entry == "" || name < entry {
			entry = name
		}
	}
	return entry
}

func firstIndependentTask(tasks map[string]domain.FlowTask) string {
	entry := ""
	for name, task := range tasks {
		if len(task.Depends) != 0 {
			continue
		}
		if entry == "" || name < entry {
			entry = name
		}
	}
	return entry
}

// splitScopedRef splits a local/<handle> or global/<handle> reference and drops
// an optional @version suffix. Bare handles report ok=false and pass through
// unchecked: a bare handle is the Agent Flow spelling of a flow-local role that
// resolves flow > shared catalog at freeze time.
func splitScopedRef(raw string) (scope, name string, ok bool) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", "", false
	}
	if index := strings.LastIndexByte(ref, '@'); index >= 0 {
		ref = ref[:index]
	}
	scope, name, found := strings.Cut(ref, "/")
	if !found || (scope != "local" && scope != "global") {
		return "", ref, false
	}
	if name == "" {
		return "", ref, false
	}
	return scope, name, true
}

func (b *builder) checkScopedRoleRef(field, ref string) error {
	scope, handle, scoped := splitScopedRef(ref)
	if !scoped {
		// A bare handle is the Agent Flow spelling that resolves flow-local
		// first and then the shared catalog. When the pack declares that handle,
		// treat the reference as local so a flow-scoped Role is not reported as
		// unreferenced just because the task omitted the local/ prefix.
		if handle != "" && b.roleSet[handle] {
			b.flowRoleRefs[handle] = true
			return nil
		}
		b.warn(WarningFlowRefsGlobalRole, SeverityWarning,
			"task role "+strings.TrimSpace(ref)+" is not declared in the pack; it will resolve from the Worker-global catalog")
		return nil
	}
	if scope == "global" {
		b.warn(WarningFlowRefsGlobalRole, SeverityWarning,
			"task role "+strings.TrimSpace(ref)+" points at a Worker-global Role; a portable pack should vendor it as local/")
		return nil
	}
	if !b.roleSet[handle] {
		return fieldError(CodePackRefUnknown, field,
			"role reference %q does not match a declared pack role", strings.TrimSpace(ref))
	}
	b.flowRoleRefs[handle] = true
	return nil
}

func (b *builder) checkScopedSkillRef(field, ref string) error {
	scope, id, scoped := splitScopedRef(ref)
	if !scoped {
		if id != "" && b.skillIDs[id] {
			return nil
		}
		b.warn(WarningFlowRefsGlobalSkill, SeverityWarning,
			"task skill "+strings.TrimSpace(ref)+" is not vendored in the pack; it will resolve from the Worker-global catalog")
		return nil
	}
	if scope == "global" {
		b.warn(WarningFlowRefsGlobalSkill, SeverityWarning,
			"task skill "+strings.TrimSpace(ref)+" points at a Worker-global Skill; a portable pack should vendor it")
		return nil
	}
	if !b.skillIDs[id] {
		return fieldError(CodePackRefUnknown, field,
			"skill reference %q does not match a vendored pack skill", strings.TrimSpace(ref))
	}
	return nil
}
