package domain

import "encoding/json"

// ProjectToolPolicyHook is the frozen JSON shape of one project-declared tool
// policy hook (design 一 Stage 2b). It is pure data: the agent package compiles
// it into an executable chain listener. Kinds:
//
//	deny    — append a denial when the matcher hits (fail-closed direction)
//	rewrite — rewrite Arguments on an allowed decision (never overrides a denial)
//	project — post-execution content projection (redact patterns)
type ProjectToolPolicyHook struct {
	Kind           string          `json:"kind"`
	Code           string          `json:"code,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	When           ProjectHookWhen `json:"when,omitempty"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	RedactPatterns []string        `json:"redactPatterns,omitempty"`
}

// ProjectHookWhen is the matcher for one project hook.
type ProjectHookWhen struct {
	ToolName        string `json:"toolName,omitempty"`
	CommandContains string `json:"commandContains,omitempty"`
}
