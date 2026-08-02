// This file defines the prompt-level hook gate the API server consults before
// creating a new run. The concrete implementation lives in cmd/ennoworker so
// the api package does not depend on the hooks engine or trust store.
package api

import (
	"context"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
)

// PromptHookOutcome is the result of evaluating UserPromptSubmit hooks for a
// new turn submission.
type PromptHookOutcome struct {
	// Blocked is true when a hook decided to block the submission.
	Blocked bool
	// Reason is the blocking reason (only meaningful when Blocked).
	Reason string
	// AdditionalContext is context to inject into the run (never blocked).
	AdditionalContext string
	// Error reports an infrastructure failure evaluating hooks. Callers
	// should fail-open (allow the submission) but log the error.
	Error error
}

// PromptHookGate evaluates UserPromptSubmit hooks before a new run is created.
// It is a no-op (returns a zero outcome) when no hooks are configured for the
// session's workspace.
type PromptHookGate interface {
	// CheckPrompt runs before SubmitTurn. sessionID is used to resolve the
	// workspace and its trust-gated hooks; prompt is the user's text.
	CheckPrompt(ctx context.Context, sessionID, prompt string, parts []domain.ContentBlock) PromptHookOutcome
}
