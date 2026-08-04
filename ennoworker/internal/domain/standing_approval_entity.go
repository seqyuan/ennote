package domain

import "time"

// StandingApproval is a persisted, user-granted rule that auto-approves
// future tool calls matching the rule's scope within one session.
type StandingApproval struct {
	ID                    string     `json:"id"`
	SessionID             string     `json:"sessionId"`
	ToolName              string     `json:"toolName"`
	ScopeKind             string     `json:"scopeKind"`
	ScopeVersion          int        `json:"scopeVersion"`
	ScopeKey              string     `json:"scopeKey"`
	ScopeDisplay          string     `json:"scopeDisplay"`
	RiskClass             RiskClass  `json:"riskClass"`
	CreatedAt             time.Time  `json:"createdAt"`
	CreatedByRunID        string     `json:"-"`
	CreatedByApprovalID   string     `json:"-"`
	RevokedAt             *time.Time `json:"revokedAt,omitempty"`
	RevokeClientRequestID string     `json:"-"`
}

// StandingGrantCandidate is a server-authoritative scope candidate persisted
// at approval suspension time. It is the input to the Decide transaction.
type StandingGrantCandidate struct {
	CallIndex    int       `json:"callIndex"`
	ToolCallID   string    `json:"toolCallId"`
	ToolName     string    `json:"toolName"`
	ScopeKind    string    `json:"scopeKind"`
	ScopeVersion int       `json:"scopeVersion"`
	ScopeKey     string    `json:"scopeKey"`
	ScopeDisplay string    `json:"scopeDisplay"`
	RiskClass    RiskClass `json:"riskClass"`
}

// StandingGrantRequest is the subset of a grant choice the client sends back.
// Only the call index is trusted; the rest is validated against persisted candidates.
type StandingGrantRequest struct {
	CallIndex int `json:"callIndex"`
}

// StandingGrantResult records which rule was created for a given call.
type StandingGrantResult struct {
	CallIndex int    `json:"callIndex"`
	RuleID    string `json:"ruleId"`
}

// StandingAuthorizationSnapshot is a frozen record of which calls in a
// suspended batch were covered by standing rules. It lives in the
// ResumeState and is replayed (not re-queried) on resume.
type StandingAuthorizationSnapshot struct {
	CallIndex    int    `json:"callIndex"`
	ToolCallID   string `json:"toolCallId"`
	ToolName     string `json:"toolName"`
	ScopeKind    string `json:"scopeKind"`
	ScopeVersion int    `json:"scopeVersion"`
	ScopeKey     string `json:"scopeKey"`
	RuleID       string `json:"ruleId"`
}
