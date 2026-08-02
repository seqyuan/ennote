// This file defines the process contract between ennote and a user hook
// command: what ennote writes to the hook's stdin (HookInput), what ennote
// reads back from stdout (HookOutput), and the internal merged decision the
// dispatcher produces (Decision). It also defines exit-code semantics.
//
// The wire contract follows Claude Code: exit 0 = allow, exit 2 = block (with
// stderr as the reason), any other non-zero = execution failure. On exit 0 a
// well-formed JSON stdout is parsed as HookOutput; a non-JSON stdout is a no-op.
package hooks

import (
	"encoding/json"
	"strings"
)

// HookInput is the JSON payload ennote writes to a hook command's stdin. It
// carries only observable, non-secret fields — never API keys or credentials.
type HookInput struct {
	DeliveryID    string          `json:"delivery_id"`
	EventType     string          `json:"event_type"`
	RunID         string          `json:"run_id"`
	SessionID     string          `json:"session_id,omitempty"`
	WorkspaceID   string          `json:"workspace_id,omitempty"`
	WorkspaceRoot string          `json:"workspace_root,omitempty"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse  json.RawMessage `json:"tool_response,omitempty"`
	IsError       bool            `json:"is_error,omitempty"`
	Prompt        string          `json:"prompt,omitempty"`
	StopReason    string          `json:"stop_reason,omitempty"`
	Iteration     int             `json:"iteration,omitempty"`
	Source        string          `json:"source,omitempty"`
	Trigger       string          `json:"trigger,omitempty"`
	Status        string          `json:"status,omitempty"`
	ErrorCode     string          `json:"error_code,omitempty"`
	Message       string          `json:"message,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	RiskHint      string          `json:"risk_hint,omitempty"`
	ToolCallID    string          `json:"tool_call_id,omitempty"`
}

// HookOutput is the optional JSON a hook may print to stdout to influence the
// agent. A non-JSON stdout on exit 0 is treated as an empty HookOutput (no
// operation). Decision "block" is equivalent to exiting with code 2.
type HookOutput struct {
	Decision          string          `json:"decision,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	AdditionalContext string          `json:"additionalContext,omitempty"`
	Continue          *bool           `json:"continue,omitempty"`
	UpdatedInput      json.RawMessage `json:"updatedInput,omitempty"`
}

// Blocks reports whether this output requests a block.
func (o HookOutput) Blocks() bool {
	if strings.EqualFold(o.Decision, "block") {
		return true
	}
	if o.Continue != nil && !*o.Continue {
		return true
	}
	return false
}

// Decision is the dispatcher's merged result after running all matched
// hooks for one event. Block is set if any hook blocked; Reason accumulates
// the blocking reasons; AdditionalContext accumulates injected context in
// order; UpdatedInput holds the last-provided rewrite (last writer wins).
type Decision struct {
	Block             bool
	Reason            string
	AdditionalContext string
	UpdatedInput      json.RawMessage
}

// ParseOutput parses a hook's stdout into a HookOutput. On exit 0 a non-JSON
// body is a no-op (returns the zero value, ok=false). Empty/whitespace stdout
// is also a no-op. A valid JSON object is parsed and ok is true.
func ParseOutput(stdout []byte) (HookOutput, bool) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return HookOutput{}, false
	}
	var out HookOutput
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return HookOutput{}, false
	}
	return out, true
}
