package domain

import "encoding/json"

// StandingApprovalScope is the canonical, secret-free representation of what a
// standing rule authorizes. Each tool defines its own scope kind and
// versioned canonicalization rules.
type StandingApprovalScope struct {
	Kind         string // stable machine type, e.g. "origin"
	ScopeVersion int    // canonicalization/matching semantics version (>= 1)
	Key          string // canonical, secret-free authorization key
	Display      string // bounded, redacted, safe for API/UI (≤ 200 chars)
}

// StandingScopeRef is a lightweight reference used for matching against
// active standing rules.
type StandingScopeRef struct {
	ToolName     string
	Kind         string
	ScopeVersion int
	Key          string
}

// StandingApprovalScopeResolver resolves a tool call into a standing scope
// candidate. Only tools that explicitly implement the provider interface
// return ok=true.
type StandingApprovalScopeResolver interface {
	ResolveStandingApprovalScope(toolName string, arguments json.RawMessage) (StandingApprovalScope, bool, error)
}

// StandingApprovalScopeProvider is implemented by tools that support standing
// approval. The returned scope must be secret-free and independently verifiable.
type StandingApprovalScopeProvider interface {
	StandingApprovalScope(arguments json.RawMessage) (StandingApprovalScope, error)
}
