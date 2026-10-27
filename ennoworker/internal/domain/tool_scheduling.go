package domain

// readOnlySchedulingTools is the single, conservative allowlist used by every
// scheduling decision that treats a tool as "cannot mutate the workspace":
// Agent Flow fan_out planning, writer-scope accounting, publish-time flow
// validation, and Graph Run freeze classification.
//
// Membership is explicit and exhaustive on purpose. Anything absent — bash,
// exec, write, edit, publish_artifact, web_fetch, http, delegate_tasks,
// submit_result, every MCP (mcp__*) tool, and every unknown/hallucinated tool —
// is treated as mutating, so it fails closed and forfeits fan-out parallelism.
//
// MCP tools are deliberately excluded: a remote server can mutate state through
// a call that carries no local hint in its name. If a future capability model
// ever declares a specific MCP tool read-only, it must be added here explicitly
// rather than inferred from a name substring.
var readOnlySchedulingTools = map[string]struct{}{
	"read":                     {},
	"ls":                       {},
	"grep":                     {},
	"find":                     {},
	"git_readonly":             {},
	"search_compacted_history": {},
	"todo":                     {},
	"ask_user":                 {},
}

// IsReadOnlySchedulingTool reports whether tool is known to be incapable of
// mutating the workspace, and therefore safe to schedule in parallel with other
// readers. Unknown tools and all MCP tools return false.
func IsReadOnlySchedulingTool(tool string) bool {
	_, ok := readOnlySchedulingTools[tool]
	return ok
}

// RoleDefinitionIsReadOnly reports whether a frozen Role definition is fully
// read-only: read_only authority plus an allowlist made entirely of read-only
// scheduling tools. It is the shared classification behind fan_out safety,
// writer-scope accounting, flow publish validation, and Graph Run freezes, so
// the same Role can never receive different scheduling classes per entry path.
func RoleDefinitionIsReadOnly(roleDef RoleDefinition) bool {
	if roleDef.Authority != RoleAuthorityReadOnly {
		return false
	}
	for _, tool := range roleDef.AllowedTools {
		if !IsReadOnlySchedulingTool(tool) {
			return false
		}
	}
	return true
}
